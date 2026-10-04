package builder

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/compiler"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/version"
)

// Orchestrator owns the incremental build loop:
// it coalesces bursts of change notifications within a debounce window,
// rebuilds only affected modules in dependency order, survives build
// failures of individual modules and guarantees that a change arriving
// during a build is not lost (it triggers exactly one follow-up run).
type Orchestrator struct {
	cfg  *config.Config
	g    *graph.Graph
	comp compiler.Compiler
	st   *store.Store

	modules map[string]config.ModuleConfig

	mu      sync.Mutex
	dirty   map[string]bool
	wake    chan struct{} // signaled (non-blocking) when dirty becomes true
	stop    chan struct{}
	stopped chan struct{}

	// Started is closed once the initial full build cycle has committed.
	Started chan struct{}

	onPublish func(*artifact.Snapshot)
}

// Option customizes an Orchestrator at construction time.
type Option func(*Orchestrator)

// WithPublishCallback installs a callback invoked with every committed
// snapshot. It must be set at construction time so it is safe to read
// from the build loop without further synchronization.
func WithPublishCallback(fn func(*artifact.Snapshot)) Option {
	return func(o *Orchestrator) { o.onPublish = fn }
}

// New creates an Orchestrator and schedules the initial full build on
// its background loop (so notifications arriving during that first build
// are coalesced like any other). Wait on Started to block until the
// initial from-scratch build has committed. Every later incremental
// cycle converges to the same result for the same inputs.
func New(ctx context.Context, cfg *config.Config, g *graph.Graph, comp compiler.Compiler, opts ...Option) (*Orchestrator, error) {
	if comp == nil {
		comp = compiler.NewCommandCompiler()
	}
	o := &Orchestrator{
		cfg:     cfg,
		g:       g,
		comp:    comp,
		st:      store.New(g.Names()),
		modules: make(map[string]config.ModuleConfig, len(cfg.Modules)),
		dirty:   map[string]bool{},
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
		Started: make(chan struct{}),
	}
	for _, m := range cfg.Modules {
		o.modules[m.Name] = m
		o.dirty[m.Name] = true // initial full build
	}
	for _, opt := range opts {
		opt(o)
	}
	go o.loop()
	return o, nil
}

// Store returns the underlying artifact store.
func (o *Orchestrator) Store() *store.Store { return o.st }

// Notify records that modules changed. Notifications are merged within
// the debounce window; notifications arriving during a run trigger
// exactly one follow-up run, so no change is ever lost.
func (o *Orchestrator) Notify(modules []string) {
	if len(modules) == 0 {
		return
	}
	o.mu.Lock()
	for _, m := range modules {
		if _, ok := o.modules[m]; ok {
			o.dirty[m] = true
		}
	}
	// Non-blocking signal; a pending signal is enough to wake the loop.
	select {
	case o.wake <- struct{}{}:
	default:
	}
	o.mu.Unlock()
}

// NotifyChangedPaths maps changed file paths to owning modules by
// comparing each path against the module source directories.
func (o *Orchestrator) NotifyChangedPaths(paths []string) {
	var modules []string
	for _, p := range paths {
		name := o.moduleForPath(p)
		if name != "" {
			modules = append(modules, name)
		}
	}
	o.Notify(modules)
}

// moduleForPath returns the longest-prefix matching module for an
// absolute or workspace-relative path, or "" when none matches.
func (o *Orchestrator) moduleForPath(p string) string {
	abs := filepath.FromSlash(p)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(o.cfg.Workspace, abs)
	}
	abs = filepath.Clean(abs)
	best, bestLen := "", 0
	for _, m := range o.cfg.Modules {
		modDir := filepath.FromSlash(m.Dir)
		if !filepath.IsAbs(modDir) {
			modDir = filepath.Join(o.cfg.Workspace, modDir)
		}
		modDir = filepath.Clean(modDir)
		rel, err := filepath.Rel(modDir, abs)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			if best == "" || len(modDir) > bestLen {
				best, bestLen = m.Name, len(modDir)
			}
		}
	}
	return best
}

// Close stops the background loop. In-flight builds are not interrupted
// by Close itself; cancel the context passed to New to interrupt them.
func (o *Orchestrator) Close() {
	select {
	case <-o.stop:
	default:
		close(o.stop)
	}
	<-o.stopped
}

func (o *Orchestrator) loop() {
	defer close(o.stopped)
	started := false
	// The initial cycle (the scheduled full build) runs immediately
	// without waiting for the debounce window.
	if changed := o.takeDirty(); len(changed) > 0 {
		o.buildDrain(changed)
		started = true
		close(o.Started)
	}
	for {
		select {
		case <-o.stop:
			if !started {
				close(o.Started)
			}
			return
		case <-o.wake:
		}
		// Coalesce the burst: keep resetting the quiet timer while
		// notifications keep arriving within the window.
		timer := time.NewTimer(o.cfg.DebounceWindow.Std())
	waiting:
		for {
			select {
			case <-o.stop:
				timer.Stop()
				return
			case <-o.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(o.cfg.DebounceWindow.Std())
			case <-timer.C:
				break waiting
			}
		}

		// Build cycles repeat while changes keep arriving during a run,
		// guaranteeing the last change is never lost.
		changed := o.takeDirty()
		if len(changed) == 0 {
			continue
		}
		o.buildDrain(changed)
	}
}

// buildDrain builds the affected set, then immediately re-runs for any
// changes that arrived during the build, until the dirty set is empty.
// A change arriving after the final takeDirty wakes the outer loop and
// goes through a normal debounced cycle, so no change is ever lost.
func (o *Orchestrator) buildDrain(changed []string) {
	for len(changed) > 0 {
		affected := o.g.Affected(changed)
		ctx, cancel := context.WithTimeout(context.Background(), o.cfg.BuildTimeout.Std())
		o.runCycle(ctx, affected)
		cancel()
		changed = o.takeDirty()
	}
}

func (o *Orchestrator) takeDirty() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(o.dirty))
	for m := range o.dirty {
		out = append(out, m)
	}
	o.dirty = map[string]bool{}
	sort.Strings(out)
	return out
}

// runCycle builds the given modules in dependency-first order, with
// independent modules up to MaxParallelBuilds at a time, and commits the
// whole cycle atomically. A failure in one module never aborts the rest.
func (o *Orchestrator) runCycle(parent context.Context, modules []string) {
	if len(modules) == 0 {
		return
	}
	o.publish(o.st.MarkBuilding(modules))

	// Dependency completion signaling within this cycle.
	done := make(map[string]chan struct{}, len(modules))
	for _, m := range modules {
		done[m] = make(chan struct{})
	}

	results := make(map[string]store.Result, len(modules))
	var resMu sync.Mutex
	sem := make(chan struct{}, o.cfg.MaxParallelBuilds)
	var wg sync.WaitGroup

	// Every module gets one goroutine that always closes its own done
	// channel exactly once; scheduling is controlled by dependency
	// channels and the concurrency semaphore.
	for _, name := range modules {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			defer close(done[name])
			// Wait for this module's dependencies that are in the cycle.
			for _, dep := range o.g.DepsOf(name) {
				if ch, ok := done[dep]; ok {
					select {
					case <-ch:
					case <-parent.Done():
						return
					}
				}
			}
			select {
			case sem <- struct{}{}:
			case <-parent.Done():
				return
			}
			defer func() { <-sem }()

			res := o.buildOne(parent, name)
			resMu.Lock()
			results[name] = res
			resMu.Unlock()
		}(name)
	}
	wg.Wait()

	// If the context was canceled/timed out, modules that never produced
	// a result get an explicit error and the cycle is still committed, so
	// the server never stays in "building".
	if parent.Err() != nil {
		for _, m := range modules {
			if _, ok := results[m]; !ok {
				results[m] = store.Result{Error: fmt.Sprintf("build canceled: %v", parent.Err())}
			}
		}
	}

	snap := o.st.Commit(results)
	o.publish(snap)
}

// buildOne compiles a single module and computes its version.
// Dependency versions are read from the current snapshot so they reflect
// the last successful build of each dependency.
func (o *Orchestrator) buildOne(ctx context.Context, name string) store.Result {
	snap := o.st.Current()
	depVersions := map[string]string{}
	for _, dep := range o.g.DepsOf(name) {
		depVersions[dep] = snap.States[dep].Version
	}
	in := compiler.Input{
		Module:      o.modules[name],
		Root:        o.cfg.Workspace,
		DepVersions: depVersions,
	}
	arts, err := o.comp.Compile(ctx, in)
	if err != nil {
		// Failure: previous successful artifacts/version are retained by
		// the store; the error is exposed in the module state.
		return store.Result{Error: err.Error()}
	}
	v := version.Hash(name, depVersions, arts)
	return store.Result{Version: v, Artifacts: arts}
}

func (o *Orchestrator) publish(snap *artifact.Snapshot) {
	if o.onPublish != nil {
		o.onPublish(snap)
	}
}
