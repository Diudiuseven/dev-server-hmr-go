package engine

import (
	"context"
	"os"

	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
)

// fakeBuilder simulates builds: the artifact echoes module + source hash;
// failing modules can be toggled at runtime.
type fakeBuilder struct {
	mu      sync.Mutex
	failing map[string]bool
	calls   map[string]int
	slow    time.Duration
}

func newFakeBuilder() *fakeBuilder {
	return &fakeBuilder{failing: map[string]bool{}, calls: map[string]int{}}
}

func (f *fakeBuilder) setFailing(mod string, v bool) {
	f.mu.Lock()
	f.failing[mod] = v
	f.mu.Unlock()
}

func (f *fakeBuilder) callCount(mod string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[mod]
}

func (f *fakeBuilder) Build(ctx context.Context, in builder.Input) (*builder.Result, error) {
	f.mu.Lock()
	f.calls[in.Module]++
	fail := f.failing[in.Module]
	slow := f.slow
	f.mu.Unlock()
	if slow > 0 {
		select {
		case <-time.After(slow):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, &fakeErr{in.Module}
	}
	data := []byte("artifact:" + in.Module + ":" + in.SourceHash)
	return &builder.Result{
		Module:     in.Module,
		Artifact:   data,
		SourceHash: in.SourceHash,
		VersionID:  builder.ArtifactID(data),
		BuiltAt:    time.Now(),
	}, nil
}

type fakeErr struct{ mod string }

func (e *fakeErr) Error() string { return "fake build failure in " + e.mod }

type harness struct {
	ws      string
	eng     *Engine
	st      *state.Store
	b       *fakeBuilder
	events  []Event
	evMu    sync.Mutex
	engDone chan struct{}
	cancel  context.CancelFunc
}

func newHarness(t *testing.T, modules []config.Module, debounce time.Duration, conc int) *harness {
	t.Helper()
	ws := t.TempDir()
	initWorkspace(t, ws, modules)
	return startHarness(t, ws, ".artifacts", modules, debounce, conc)
}

// newHarnessIn builds an engine over an existing workspace (same source
// files), letting tests compare incremental output with a clean build.
func newHarnessIn(t *testing.T, ws, artSub string, modules []config.Module, debounce time.Duration, conc int) *harness {
	t.Helper()
	return startHarness(t, ws, artSub, modules, debounce, conc)
}

func initWorkspace(t *testing.T, ws string, modules []config.Module) {
	t.Helper()
	for _, m := range modules {
		if err := os.MkdirAll(filepath.Join(ws, m.Root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, m.Root, "src.txt"), []byte("v0-"+m.Name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func startHarness(t *testing.T, ws, artSub string, modules []config.Module, debounce time.Duration, conc int) *harness {
	decls := make([]graph.ModuleDecl, 0, len(modules))
	for _, m := range modules {
		decls = append(decls, graph.ModuleDecl{Name: m.Name, Deps: m.Deps})
	}
	g, err := graph.New(decls)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(modules))
	for i, m := range modules {
		names[i] = m.Name
	}
	st := state.NewStore(names)
	art, err := store.NewArtifactStore(filepath.Join(ws, artSub))
	if err != nil {
		t.Fatal(err)
	}
	fb := newFakeBuilder()
	h := &harness{ws: ws, st: st, b: fb, engDone: make(chan struct{})}
	cfg := config.Config{
		Workspace: ws,
		Modules:   modules,
		Build:     config.BuildConfig{Concurrency: conc, Timeout: config.Duration{Duration: 5 * time.Second}},
	}
	opts := Options{
		Config:    cfg,
		Graph:     g,
		Builder:   fb,
		State:     st,
		Artifacts: art,
		Debounce:  debounce,
		OnEvent: func(ev Event) {
			h.evMu.Lock()
			h.events = append(h.events, ev)
			h.evMu.Unlock()
		},
	}
	h.eng = New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { _ = h.eng.Run(ctx); close(h.engDone) }()
	return h
}

func (h *harness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case <-h.engDone:
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not stop")
	}
}

func (h *harness) eventsOf(kind string) []Event {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	var out []Event
	for _, e := range h.events {
		if e.Type == kind {
			out = append(out, e)
		}
	}
	return out
}

func (h *harness) waitStatus(t *testing.T, mod string, want state.Status, within time.Duration) state.Snapshot {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		snap := h.st.Snapshot(mod)
		if snap.Status == want && !snap.Building {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap := h.st.Snapshot(mod)
	t.Fatalf("module %s status = %s building=%v, want %s", mod, snap.Status, snap.Building, want)
	return snap
}

func (h *harness) write(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.ws, root, "src.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) waitIdleEvent(t *testing.T, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		h.evMu.Lock()
		n := 0
		for _, e := range h.events {
			if e.Type == EventIdle {
				n++
			}
		}
		h.evMu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("no idle event")
}

func (h *harness) idleEvents() int {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	n := 0
	for _, e := range h.events {
		if e.Type == EventIdle {
			n++
		}
	}
	return n
}

func (h *harness) waitNextIdle(t *testing.T, within time.Duration) {
	t.Helper()
	from := h.idleEvents()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if h.idleEvents() > from {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("no next idle event")
}

func (h *harness) waitQuiet(t *testing.T, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !h.st.AnyBuilding() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("still building")
}

func init() { runtime.GOMAXPROCS(runtime.NumCPU()) }
