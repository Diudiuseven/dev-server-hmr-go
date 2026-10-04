// Package engine orchestrates incremental builds: it consumes merged
// change events, computes affected modules, schedules dependency-ordered
// builds with bounded concurrency, retains last-good versions on failure
// and recovers automatically.
package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/coalescer"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
)

// Event kinds emitted to connected clients.
const (
	EventBuildStart  = "build:start"
	EventBuildOK     = "build:ok"
	EventBuildFailed = "build:failed"
	EventIdle        = "idle"
)

// Event is an engine lifecycle event pushed to clients.
type Event struct {
	Type      string `json:"type"`
	Module    string `json:"module,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	Error     string `json:"error,omitempty"`
	At        int64  `json:"at"`
}

// Engine orchestrates incremental builds.
type Engine struct {
	cfg   config.Config
	g     *graph.Graph
	b     builder.Builder
	st    *state.Store
	art   *store.ArtifactStore
	coals *coalescer.Coalescer[string]

	dirtyMu   sync.Mutex
	dirty     map[string]struct{} // modules whose sources changed mid-pass
	builtHash map[string]string   // module -> own source hash last built

	roots   []string          // module names in root-path length order (longest first)
	rootFor map[string]string // normalized module root -> module name

	emitMu sync.Mutex
	output func(Event)
}

// Options wires an Engine.
type Options struct {
	Config    config.Config
	Graph     *graph.Graph
	Builder   builder.Builder
	State     *state.Store
	Artifacts *store.ArtifactStore
	Debounce  time.Duration
	OnEvent   func(Event)
}

// New constructs an engine.
func New(opts Options) *Engine {
	e := &Engine{
		cfg:       opts.Config,
		g:         opts.Graph,
		b:         opts.Builder,
		st:        opts.State,
		art:       opts.Artifacts,
		coals:     coalescer.New[string](opts.Debounce),
		rootFor:   make(map[string]string),
		output:    opts.OnEvent,
		dirty:     make(map[string]struct{}),
		builtHash: make(map[string]string),
	}
	for _, m := range opts.Config.Modules {
		root := normalizeRel(m.Root)
		e.rootFor[root] = m.Name
		e.roots = append(e.roots, root)
	}
	// longest root first so nested modules map to the most specific one
	sortByLenDesc(e.roots)
	return e
}

func normalizeRel(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimPrefix(p, "./")
	if p == "." {
		return ""
	}
	return strings.TrimSuffix(p, "/")
}

func sortByLenDesc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && len(s[j]) > len(s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// moduleForPath maps a workspace-relative changed path to the module that
// owns it, returning "" for paths outside every module root.
func (e *Engine) moduleForPath(rel string) string {
	rel = normalizeRel(rel)
	for _, root := range e.roots {
		if root == "" {
			return e.rootFor[""]
		}
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return e.rootFor[root]
		}
	}
	return ""
}

// NotifyChanged records that source files changed. It never blocks the
// caller; changes arriving during a build are retained and trigger another
// build pass once the current one finishes.
func (e *Engine) NotifyChanged(paths []string) {
	mods := make(map[string]struct{})
	for _, p := range paths {
		if m := e.moduleForPath(p); m != "" {
			mods[m] = struct{}{}
		}
	}
	for m := range mods {
		e.coals.Signal(m)
	}
}

// markDirty records a module whose source was observed to change while a
// build pass was running. It guarantees a follow-up rebuild even if the
// original change notification was already consumed by that pass.
func (e *Engine) markDirty(mod string) {
	e.dirtyMu.Lock()
	e.dirty[mod] = struct{}{}
	e.dirtyMu.Unlock()
}

// noteBuiltHash records the own-source hash a module was built from.
func (e *Engine) noteBuiltHash(mod, h string) {
	e.dirtyMu.Lock()
	e.builtHash[mod] = h
	e.dirtyMu.Unlock()
}

// recheckDirty re-hashes every module we ever built and marks those whose
// current source differs from the hash they were last built with. This is
// the content-based safety net that guarantees convergence even when a
// change notification is consumed before its pass observes the file.
func (e *Engine) recheckDirty() {
	e.dirtyMu.Lock()
	snapshot := make(map[string]string, len(e.builtHash))
	for m, h := range e.builtHash {
		snapshot[m] = h
	}
	e.dirtyMu.Unlock()
	modCfg := map[string]string{}
	for _, m := range e.cfg.Modules {
		modCfg[m.Name] = m.Root
	}
	for mod, lastHash := range snapshot {
		root := joinPath(e.cfg.Workspace, modCfg[mod])
		cur, err := hashSource(root)
		if err != nil {
			continue
		}
		if cur != lastHash {
			e.markDirty(mod)
		}
	}
}

// takeDirty atomically removes and returns all dirty modules.
func (e *Engine) takeDirty() []string {
	e.dirtyMu.Lock()
	defer e.dirtyMu.Unlock()
	if len(e.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(e.dirty))
	for m := range e.dirty {
		out = append(out, m)
	}
	e.dirty = make(map[string]struct{})
	return out
}

// TriggerFullBuild enqueues a build of all modules (manual rebuild).
func (e *Engine) TriggerFullBuild() {
	for _, n := range e.g.Nodes() {
		e.coals.Signal(n)
	}
}

func (e *Engine) emit(ev Event) {
	ev.At = time.Now().UnixNano()
	e.emitMu.Lock()
	out := e.output
	e.emitMu.Unlock()
	if out != nil {
		out(ev)
	}
}
