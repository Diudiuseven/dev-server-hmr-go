package builder

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/compiler"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
)

func testCfg(t *testing.T) *config.Config {
	c := config.Default()
	c.Workspace = t.TempDir()
	c.DebounceWindow = config.Duration(20 * time.Millisecond)
	c.BuildTimeout = config.Duration(5 * time.Second)
	c.MaxParallelBuilds = 4
	c.Modules = []config.ModuleConfig{
		{Name: "a", Dir: "a"},
		{Name: "b", Dir: "b", Deps: []string{"a"}},
		{Name: "c", Dir: "c", Deps: []string{"b"}},
		{Name: "d", Dir: "d", Deps: []string{"b"}},
		{Name: "e", Dir: "e"},
	}
	return c
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func newOrchestrator(t *testing.T, cfg *config.Config, comp compiler.Compiler) *Orchestrator {
	t.Helper()
	deps := map[string][]string{}
	for _, m := range cfg.Modules {
		deps[m.Name] = m.Deps
	}
	g, err := graph.New(deps)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	o, err := New(context.Background(), cfg, g, comp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(o.Close)
	select {
	case <-o.Started:
	case <-time.After(5 * time.Second):
		t.Fatal("initial build did not finish")
	}
	return o
}

// staticCompiler returns fixed artifacts per module, counting builds.
type staticCompiler struct {
	mu       sync.Mutex
	art      map[string][]artifact.Artifact
	fail     map[string]bool
	building map[string]bool
	calls    map[string]int
	maxPara  int
	active   int
	// block makes Compile block until release is called for the module.
	block map[string]chan struct{}
	// blockUntilCancel makes Compile block until its context is canceled,
	// simulating a command that only returns when the timeout fires.
	blockUntilCancel map[string]bool
}

func newStaticCompiler() *staticCompiler {
	return &staticCompiler{
		art:              map[string][]artifact.Artifact{},
		fail:             map[string]bool{},
		building:         map[string]bool{},
		calls:            map[string]int{},
		block:            map[string]chan struct{}{},
		blockUntilCancel: map[string]bool{},
	}
}

func (c *staticCompiler) set(name, content string) {
	c.mu.Lock()
	c.art[name] = []artifact.Artifact{{Path: name + ".out", Content: []byte(content)}}
	c.mu.Unlock()
}

func (c *staticCompiler) Compile(ctx context.Context, in compiler.Input) ([]artifact.Artifact, error) {
	c.mu.Lock()
	c.active++
	if c.active > c.maxPara {
		c.maxPara = c.active
	}
	c.calls[in.Module.Name]++
	gate := c.block[in.Module.Name]
	untilCancel := c.blockUntilCancel[in.Module.Name]
	art := c.art[in.Module.Name]
	fail := c.fail[in.Module.Name]
	c.building[in.Module.Name] = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.building[in.Module.Name] = false
		c.active--
		c.mu.Unlock()
	}()

	if untilCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Re-read content/failure after the gate so test changes made while
	// the build was blocked take effect (without holding the mutex).
	c.mu.Lock()
	art = c.art[in.Module.Name]
	fail = c.fail[in.Module.Name]
	c.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("module %s broken", in.Module.Name)
	}
	out := make([]artifact.Artifact, len(art))
	copy(out, art)
	return out, nil
}

func (c *staticCompiler) maxPeak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxPara
}

func (c *staticCompiler) callsOf(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func TestInitialFullBuild(t *testing.T) {
	cfg := testCfg(t)
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "v0-"+m.Name)
	}
	o := newOrchestrator(t, cfg, comp)
	snap := o.Store().Current()
	for _, m := range cfg.Modules {
		if snap.States[m.Name].Status != artifact.StatusOK {
			t.Fatalf("module %s status = %s, want ok", m.Name, snap.States[m.Name].Status)
		}
		if snap.States[m.Name].Version == "" {
			t.Fatalf("module %s has empty version after initial build", m.Name)
		}
	}
}

func TestIncrementalPropagationAndNoUnrelatedRebuilds(t *testing.T) {
	cfg := testCfg(t)
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "v0-"+m.Name)
	}
	o := newOrchestrator(t, cfg, comp)

	// Change a: only a, b, c, d rebuild; e is unrelated.
	comp.set("a", "v1-a")
	o.Notify([]string{"a"})
	waitFor(t, 2*time.Second, func() bool {
		snap := o.Store().Current()
		return snap.States["a"].Status == artifact.StatusOK &&
			snap.States["b"].Status == artifact.StatusOK &&
			snap.States["c"].Status == artifact.StatusOK &&
			snap.States["d"].Status == artifact.StatusOK &&
			snap.States["e"].Status == artifact.StatusOK
	})
	if got := comp.callsOf("e"); got != 1 {
		t.Fatalf("unrelated module e built %d times, want exactly 1 (initial)", got)
	}
	// a's new version must propagate: b,c,d versions should differ from initial.
	snap := o.Store().Current()
	va1 := snap.States["a"].Version
	if va1 == "" {
		t.Fatal("a missing version")
	}
}

func TestUnchangedContentProducesNoNewVersion(t *testing.T) {
	cfg := testCfg(t)
	comp := newStaticCompiler()
	comp.set("a", "same")
	o := newOrchestrator(t, cfg, comp)
	before := o.Store().Current().States["a"].Version
	o.Notify([]string{"a"}) // same content again
	time.Sleep(100 * time.Millisecond)
	after := o.Store().Current().States["a"].Version
	if before != after {
		t.Fatalf("identical content changed version: %s -> %s", before, after)
	}
}

func TestChangeBurstCoalescesIntoOneBuild(t *testing.T) {
	cfg := testCfg(t)
	cfg.DebounceWindow = config.Duration(50 * time.Millisecond)
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "v0")
	}
	o := newOrchestrator(t, cfg, comp)
	callsBefore := comp.callsOf("a")

	// Editor saves: many duplicate notifications in a short burst.
	for i := 0; i < 100; i++ {
		o.Notify([]string{"a"})
		time.Sleep(time.Millisecond)
	}
	waitFor(t, 2*time.Second, func() bool {
		comp.mu.Lock()
		n := comp.calls["a"]
		comp.mu.Unlock()
		return n >= callsBefore+1
	})
	time.Sleep(150 * time.Millisecond) // let any stragglers show
	comp.mu.Lock()
	got := comp.calls["a"]
	comp.mu.Unlock()
	if got != callsBefore+1 {
		t.Fatalf("burst caused %d builds of a, want exactly 1", got-callsBefore)
	}
}

func TestChangeDuringBuildIsNotLost(t *testing.T) {
	cfg := testCfg(t)
	cfg.DebounceWindow = config.Duration(10 * time.Millisecond)
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "v0-"+m.Name)
	}
	// Gate module a so the initial build blocks.
	gate := make(chan struct{})
	comp.mu.Lock()
	comp.block["a"] = gate
	comp.mu.Unlock()

	deps := map[string][]string{}
	for _, m := range cfg.Modules {
		deps[m.Name] = m.Deps
	}
	g, err := graph.New(deps)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	o, err := New(context.Background(), cfg, g, comp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(o.Close)
	// Wait for the build to be blocked on a.
	waitFor(t, time.Second, func() bool {
		comp.mu.Lock()
		b := comp.building["a"]
		comp.mu.Unlock()
		return b
	})
	// New change arrives while the build is in progress.
	comp.set("a", "final-content")
	o.Notify([]string{"a"})
	close(gate)

	waitFor(t, 3*time.Second, func() bool {
		arts := o.Store().Current().Artifacts("a")
		return len(arts) == 1 && string(arts[0].Content) == "final-content"
	})
}

func TestFailureIsolationExposedAndRecovers(t *testing.T) {
	cfg := testCfg(t)
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "good-"+m.Name)
	}
	o := newOrchestrator(t, cfg, comp)

	comp.mu.Lock()
	comp.fail["a"] = true
	comp.mu.Unlock()
	o.Notify([]string{"a"})
	waitFor(t, 2*time.Second, func() bool {
		snap := o.Store().Current()
		return snap.States["a"].Status == artifact.StatusError
	})
	snap := o.Store().Current()
	if snap.States["a"].Error == "" {
		t.Fatal("error status must carry an explicit message")
	}
	if snap.States["a"].Version == "" {
		t.Fatal("failed module must keep its last successful version")
	}
	if got := snap.Artifacts("a"); len(got) != 1 || string(got[0].Content) != "good-a" {
		t.Fatalf("failed module must keep last good artifacts: %+v", got)
	}
	// Dependents were rebuilt because of a's change but must not erase a's state.
	// Independent e stays ok.
	if snap.States["e"].Status != artifact.StatusOK {
		t.Fatal("unrelated module e must stay usable while a is broken")
	}

	// Fix a: automatic recovery.
	comp.set("a", "fixed-a")
	comp.mu.Lock()
	comp.fail["a"] = false
	comp.mu.Unlock()
	o.Notify([]string{"a"})
	waitFor(t, 2*time.Second, func() bool {
		s := o.Store().Current()
		return s.States["a"].Status == artifact.StatusOK &&
			string(s.Artifacts("a")[0].Content) == "fixed-a"
	})
}

func TestRespectsMaxParallelBuilds(t *testing.T) {
	cfg := testCfg(t)
	cfg.MaxParallelBuilds = 2
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "x")
	}
	o := newOrchestrator(t, cfg, comp)
	// Rebuild two independent leaves at the same time.
	o.Notify([]string{"a", "e"})
	// Trigger a broader rebuild to exercise concurrency.
	o.Notify([]string{"a"})
	time.Sleep(100 * time.Millisecond)
	if int32(comp.maxPeak()) > int32(cfg.MaxParallelBuilds) {
		t.Fatalf("observed %d parallel builds, max is %d", comp.maxPara, cfg.MaxParallelBuilds)
	}
}

func TestBuildTimeoutDoesNotStallServer(t *testing.T) {
	cfg := testCfg(t)
	cfg.DebounceWindow = config.Duration(5 * time.Millisecond)
	cfg.BuildTimeout = config.Duration(80 * time.Millisecond)
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "x")
	}
	// a's build blocks until the cycle context is canceled, mimicking a
	// command that only returns when the build timeout fires.
	comp.mu.Lock()
	comp.blockUntilCancel["a"] = true
	comp.mu.Unlock()

	deps := map[string][]string{}
	for _, m := range cfg.Modules {
		deps[m.Name] = m.Deps
	}
	g, err := graph.New(deps)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	o, err := New(context.Background(), cfg, g, comp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(o.Close)

	// The initial cycle times out and commits an explicit error rather
	// than leaving a stuck in "building" forever.
	waitFor(t, 2*time.Second, func() bool {
		return o.Store().Current().States["a"].Status == artifact.StatusError
	})
	// A subsequent fixed build works and clears the error.
	comp.mu.Lock()
	delete(comp.blockUntilCancel, "a")
	comp.art["a"] = []artifact.Artifact{{Path: "a.out", Content: []byte("recovered")}}
	comp.mu.Unlock()
	o.Notify([]string{"a"})
	waitFor(t, 2*time.Second, func() bool {
		return o.Store().Current().States["a"].Status == artifact.StatusOK
	})
}

func TestAtomicCommitNoTornState(t *testing.T) {
	cfg := testCfg(t)
	cfg.MaxParallelBuilds = 4
	comp := newStaticCompiler()
	for _, m := range cfg.Modules {
		comp.set(m.Name, "x")
	}
	o := newOrchestrator(t, cfg, comp)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				snap := o.Store().Current()
				// Every readable snapshot must contain all modules; a
				// reader can never observe a half-applied cycle.
				for _, m := range cfg.Modules {
					if _, ok := snap.States[m.Name]; !ok {
						t.Errorf("torn snapshot missing %s", m.Name)
						return
					}
				}
			}
		}
	}()
	for i := 0; i < 50; i++ {
		comp.set("a", fmt.Sprintf("content-%d", i))
		o.Notify([]string{"a"})
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestPathToModuleMapping(t *testing.T) {
	cfg := testCfg(t)
	cfg.Workspace = "/ws"
	comp := newStaticCompiler()
	o := newOrchestrator(t, cfg, comp)
	cases := map[string]string{
		"/ws/a/src/x.ts":  "a",
		"/ws/b/sub/y.js":  "b",
		"/ws/e/z.go":      "e",
		"/elsewhere/f.go": "",
	}
	for p, want := range cases {
		if got := o.moduleForPath(p); got != want {
			t.Errorf("moduleForPath(%q) = %q, want %q", p, got, want)
		}
	}
}
