package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
)

// buildResult records the outcome of one module within a pass.
type buildResult struct {
	module string
	// planned identifies the job this result belongs to.
	planned *job
	v       *state.Version
	err     error
}

// job is everything prepared before a build command is dispatched.
type job struct {
	mod          string
	root         string
	sourceHash   string
	depArtifacts map[string][]byte
	ownHash      string
}

type specialErr struct{ msg string }

func (e *specialErr) Error() string { return e.msg }

var (
	errCancelled = &specialErr{"build cancelled"}
	errStale     = &specialErr{"source changed during build"}
	errUnchanged = &specialErr{"content unchanged"}
	errBlocked   = &specialErr{"blocked by failed dependency"}
	errDiscarded = &specialErr{"upstream result discarded"}
)

// Run is the engine scheduler loop, stopped by cancelling ctx.
//
// It alternates between waiting for a merged change batch and running build
// passes. After EVERY pass it enters a quiet-period: it waits for the full
// coalesce window to elapse with no pending change before going idle. Thus
// an edit that lands while (or just after) a pass runs is merged and built
// in a follow-up pass; the last change can never be lost.
func (e *Engine) Run(ctx context.Context) error {
	go e.coals.Run(ctx)

	if err := e.runPassCtx(ctx, e.g.Nodes()); err != nil {
		return err
	}

	for {
		batch := e.waitBlocked(ctx) // block until next change
		if batch == nil {
			return ctx.Err()
		}
		for {
			if err := e.runPassCtx(ctx, batch); err != nil {
				return err
			}
			batch = e.nextBatch(ctx) // quiet-period + dirty check
			if batch == nil {
				return ctx.Err()
			}
			if len(batch) == 0 {
				break // genuinely idle; return to blocking wait
			}
		}
	}
}

// waitBlocked blocks until a non-empty coalesced batch is available.
func (e *Engine) waitBlocked(ctx context.Context) []string {
	for {
		select {
		case <-ctx.Done():
			return nil
		case b := <-e.coals.Out():
			if len(b) > 0 {
				return b
			}
		}
	}
}

// nextBatch determines the work to run after a finished pass. It collects
// coalesced notifications for a full quiet window and THEN performs a
// content-based re-hash of built modules. A module whose sources differ
// from what was built (e.g. an edit that landed mid-pass whose signal was
// already consumed) is marked dirty and, with its whole dependent closure,
// returned for another pass. An empty result means the system is idle.
func (e *Engine) nextBatch(ctx context.Context) []string {
	window := e.cfg.Watch.Debounce.Duration
	if window <= 0 {
		window = 50 * time.Millisecond
	}
	set := map[string]struct{}{}

	// Phase 1: collect notifications until the window elapses with none.
	timer := time.NewTimer(window)
	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case b := <-e.coals.Out():
			for _, m := range b {
				set[m] = struct{}{}
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(window)
		case <-timer.C:
			goto rehash
		}
	}
rehash:
	// Phase 2: content-based safety net. Re-hash every module built so far;
	// anything that no longer matches its published source is rebuilt.
	e.recheckDirty()
	for _, m := range e.takeDirty() {
		for _, n := range e.g.Affected([]string{m}) {
			set[n] = struct{}{}
		}
	}
	if len(set) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	return out
}

// runPassCtx runs one pass with clean shutdown semantics.
func (e *Engine) runPassCtx(ctx context.Context, changed []string) error {
	done := make(chan struct{})
	go func() {
		e.runPass(ctx, changed)
		close(done)
	}()
	select {
	case <-ctx.Done():
		<-done
		return ctx.Err()
	case <-done:
		return nil
	}
}

// runPass executes one incremental build pass.
//
// Preparation (hashing sources, resolving dependency versions, dedup) runs
// serially on this goroutine the moment a module becomes ready; only the
// build command itself runs on worker goroutines. Consequently a module
// cannot be built from one snapshot while a newer one is observed by a
// dependent within the same pass, eliminating cascading staleness.
func (e *Engine) runPass(parent context.Context, changed []string) {
	order := e.g.Affected(changed)
	if len(order) == 0 {
		return
	}
	inSet := make(map[string]bool, len(order))
	for _, m := range order {
		inSet[m] = true
	}
	depsOf := map[string][]string{}
	for _, m := range order {
		for _, d := range e.directDeps(m) {
			if inSet[d] {
				depsOf[m] = append(depsOf[m], d)
			}
		}
	}

	completed := map[string]*state.Version{} // successful versions this pass
	failedSet := map[string]bool{}           // failed or blocked
	staleSet := map[string]bool{}            // discarded (concurrent edit)
	remaining := map[string]int{}
	for _, m := range order {
		remaining[m] = len(depsOf[m])
	}

	sem := make(chan struct{}, e.concurrency())
	results := make(chan buildResult, len(order))
	var wg sync.WaitGroup
	jobs := map[string]*job{}
	var jobsMu sync.Mutex

	ready := make(chan string, len(order))
	for _, m := range order {
		if remaining[m] == 0 {
			ready <- m
		}
	}

	doneCount := 0
	for doneCount < len(order) {
		select {
		case <-parent.Done():
			wg.Wait()
			return
		case mod := <-ready:
			// Synchronous preparation using the current consistent view.
			j, skipErr := e.prepare(parent, mod, completed)
			if skipErr != nil {
				results <- buildResult{module: mod, err: skipErr}
				break
			}
			jobsMu.Lock()
			jobs[mod] = j
			jobsMu.Unlock()
			wg.Add(1)
			sem <- struct{}{}
			go func(j *job) {
				defer wg.Done()
				defer func() { <-sem }()
				e.execute(parent, j, results)
			}(j)
		case r := <-results:
			doneCount++
			switch {
			case errors.Is(r.err, errUnchanged):
				// identical content already published: adopt current version
				if snap := e.st.Snapshot(r.module); snap.Current != nil {
					completed[r.module] = snap.Current
				}
			case errors.Is(r.err, errCancelled):
				// shutdown; do nothing
			case errors.Is(r.err, errStale):
				staleSet[r.module] = true
			case errors.Is(r.err, errBlocked):
				failedSet[r.module] = true
			case errors.Is(r.err, errDiscarded):
				staleSet[r.module] = true
			case r.err != nil:
				failedSet[r.module] = true
			case r.v != nil:
				completed[r.module] = r.v
			}

			// Release dependents.
			for _, dep := range e.dependentsIn(r.module, inSet, depsOf) {
				remaining[dep]--
				if remaining[dep] <= 0 {
					remaining[dep] = 0
					blocked, staleDep := false, false
					for _, d := range depsOf[dep] {
						if failedSet[d] {
							blocked = true
						}
						if staleSet[d] {
							staleDep = true
						}
					}
					switch {
					case blocked:
						results <- buildResult{module: dep, err: errBlocked}
					case staleDep:
						results <- buildResult{module: dep, err: errDiscarded}
					default:
						ready <- dep
					}
				}
			}
		}
	}
	wg.Wait()

	// Any module discarded due to a concurrent edit, together with every
	// transitive dependent, must be rebuilt in a follow-up pass. Re-signal
	// the full affected closure so nothing silently stays on an old version.
	for m, isStale := range staleSet {
		if isStale {
			for _, n := range e.g.Affected([]string{m}) {
				e.coals.Signal(n)
			}
		}
	}

	if !e.st.AnyBuilding() {
		e.emit(Event{Type: EventIdle})
	}
}

// prepare hashes sources, resolves dependency artifacts and decides whether
// a build is needed. It runs on the scheduler goroutine. A non-nil error is
// a benign skip sentinel (unchanged/blocked/discarded).
func (e *Engine) prepare(parent context.Context, mod string, completed map[string]*state.Version) (*job, error) {
	modCfg, ok := e.cfg.ModuleByName(mod)
	if !ok {
		return nil, fmt.Errorf("unknown module %q", mod)
	}
	root := joinPath(e.cfg.Workspace, modCfg.Root)
	ownHash, err := builder.HashDir(root)
	if err != nil {
		e.st.CommitBuild(mod, nil, err)
		e.emit(Event{Type: EventBuildFailed, Module: mod, Error: err.Error()})
		return nil, err
	}
	depIDs := map[string]string{}
	depArtifacts := map[string][]byte{}
	for _, d := range modCfg.Deps {
		if v, ok := completed[d]; ok && v != nil {
			depIDs[d] = v.VersionID
			depArtifacts[d] = v.Artifact
			continue
		}
		snap := e.st.Snapshot(d)
		if snap.Current != nil {
			depIDs[d] = snap.Current.VersionID
			if data, gerr := e.art.Get(storeKey(d, snap.Current.VersionID)); gerr == nil {
				depArtifacts[d] = data
			}
		}
	}
	sourceHash := builder.CombinedHash(ownHash, depIDs)
	if !e.st.BeginBuild(mod, sourceHash, time.Now()) {
		return nil, errUnchanged
	}
	e.emit(Event{Type: EventBuildStart, Module: mod})
	return &job{mod: mod, root: root, sourceHash: sourceHash, depArtifacts: depArtifacts, ownHash: ownHash}, nil
}

// execute runs the actual build command for a prepared job, then validates
// that sources did not change while it ran.
func (e *Engine) execute(parent context.Context, j *job, out chan<- buildResult) {
	ctx, cancel := context.WithCancel(parent)
	if d := e.cfg.Build.Timeout.Duration; d > 0 {
		ctx, cancel = context.WithTimeout(parent, d)
	}
	defer cancel()

	res, berr := e.b.Build(ctx, builder.Input{
		Module:       j.mod,
		Root:         j.root,
		SourceHash:   j.sourceHash,
		DepArtifacts: j.depArtifacts,
	})
	if berr != nil {
		if parent.Err() != nil {
			e.st.CommitBuild(j.mod, nil, nil)
			out <- buildResult{module: j.mod, planned: j, err: errCancelled}
			return
		}
		e.publishFailure(j.mod, berr, out)
		return
	}
	// Re-hash after the command: if sources changed while building, the
	// published artifact is transient. Mark the module dirty so the quiet
	// loop rebuilds it (and dependents) until the hash is stable, even if
	// the change notification was already consumed by this pass.
	currentHash, herr := builder.HashDir(j.root)
	if herr != nil {
		e.publishFailure(j.mod, herr, out)
		return
	}
	// Record the hash we actually built from (pre-build snapshot), NOT the
	// post-build hash: recheck compares current disk content against the
	// inputs of the artifact we published.
	e.noteBuiltHash(j.mod, j.ownHash)
	if currentHash != j.ownHash {
		e.markDirty(j.mod)
	}
	if _, err := e.art.Put(storeKey(j.mod, res.VersionID), res.Artifact); err != nil {
		e.publishFailure(j.mod, err, out)
		return
	}
	v := &state.Version{
		Module:     j.mod,
		VersionID:  res.VersionID,
		Artifact:   res.Artifact,
		BuiltAt:    res.BuiltAt,
		SourceHash: j.sourceHash,
	}
	e.st.CommitBuild(j.mod, v, nil)
	e.emit(Event{Type: EventBuildOK, Module: j.mod, VersionID: res.VersionID})
	out <- buildResult{module: j.mod, planned: j, v: v}
}

func (e *Engine) concurrency() int {
	if n := e.cfg.Build.Concurrency; n > 0 {
		return n
	}
	return 1
}

func (e *Engine) directDeps(module string) []string {
	for _, m := range e.cfg.Modules {
		if m.Name == module {
			return m.Deps
		}
	}
	return nil
}

// dependentsIn returns modules inside the pass that directly depend on mod.
func (e *Engine) dependentsIn(mod string, inSet map[string]bool, depsOf map[string][]string) []string {
	var out []string
	for candidate, deps := range depsOf {
		for _, d := range deps {
			if d == mod {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}

func errBlockedHasher(mod string, err error) error {
	return fmt.Errorf("hash %s: %w", mod, err)
}

// publishFailure records the error (last-good retained) and emits an event.
func (e *Engine) publishFailure(mod string, err error, out chan<- buildResult) {
	e.st.CommitBuild(mod, nil, err)
	e.emit(Event{Type: EventBuildFailed, Module: mod, Error: err.Error()})
	if out != nil {
		out <- buildResult{module: mod, err: err}
	}
}
