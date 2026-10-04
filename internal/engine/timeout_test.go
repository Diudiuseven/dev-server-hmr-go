package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
)

// hangBuilder blocks until its context is done, proving timeout/cancel
// actually interrupts the build.
type hangBuilder struct {
	mu      sync.Mutex
	started chan struct{}
	once    sync.Once
}

func (b *hangBuilder) mark() { b.once.Do(func() { close(b.started) }) }

func (b *hangBuilder) Build(ctx context.Context, in builder.Input) (*builder.Result, error) {
	b.mark()
	<-ctx.Done() // must be released by the build timeout
	return nil, ctx.Err()
}

func TestBuildTimeoutFailsModuleNotServer(t *testing.T) {
	ws := t.TempDir()
	write := func(root, body string) {
		mustMkdir(ws + "/" + root)
		mustWrite(ws+"/"+root+"/src.txt", body)
	}
	write("m", "v1")
	g, err := graph.New([]graph.ModuleDecl{{Name: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	st := state.NewStore([]string{"m"})
	art, _ := store.NewArtifactStore(ws + "/art")
	hb := &hangBuilder{started: make(chan struct{})}
	cfg := config.Config{
		Workspace: ws,
		Modules:   []config.Module{{Name: "m", Root: "m"}},
		Build:     config.BuildConfig{Concurrency: 1, Timeout: config.Duration{Duration: 100 * time.Millisecond}},
	}
	eng := New(Options{Config: cfg, Graph: g, Builder: hb, State: st, Artifacts: art, Debounce: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Run(ctx) }()

	select {
	case <-hb.started:
	case <-time.After(2 * time.Second):
		t.Fatal("build never started")
	}
	// Timeout turns into a failed status, not a wedged server.
	snap := waitSnap(t, st, "m", state.StatusFailed, 2*time.Second)
	if snap.Error == "" {
		t.Fatal("timeout should surface an error")
	}

	// After fixing (builder now succeeds), an edit triggers recovery, proving
	// the engine kept scheduling after the timeout.
	good := goodBuilderAfterTimeout(ws)
	eng2swap(t, eng, good)
	write("m", "v2")
	eng.NotifyChanged([]string{"m/src.txt"})
	snap = waitSnap(t, st, "m", state.StatusOK, 3*time.Second)
	if snap.VersionID == "" {
		t.Fatal("engine did not recover after a prior timeout")
	}
}

// goodBuilderAfterTimeout is a builder that succeeds immediately.
func goodBuilderAfterTimeout(ws string) builder.Builder {
	return builder.Func(func(ctx context.Context, in builder.Input) (*builder.Result, error) {
		data := []byte("ok:" + in.SourceHash)
		return &builder.Result{Module: in.Module, Artifact: data, SourceHash: in.SourceHash,
			VersionID: builder.ArtifactID(data), BuiltAt: time.Now()}, nil
	})
}

// eng2swap replaces the engine's builder with a succeeding one to emulate
// the developer fixing the hang; this is only used by tests.
func eng2swap(t *testing.T, e *Engine, b builder.Builder) {
	t.Helper()
	e.b = b
}

func waitSnap(t *testing.T, st *state.Store, mod string, want state.Status, within time.Duration) state.Snapshot {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		s := st.Snapshot(mod)
		if s.Status == want && !s.Building {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("module %s never reached status %s", mod, want)
	return state.Snapshot{}
}

func mustMkdir(p string) {
	if err := mkdirAll(p); err != nil {
		panic(err)
	}
}
func mustWrite(p, body string) {
	if err := writeFile(p, []byte(body)); err != nil {
		panic(err)
	}
}
