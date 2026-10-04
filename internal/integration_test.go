package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/compiler"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/server"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/watcher"
)

// stateEvent is the JSON payload pushed over SSE and served at /api/state.
type stateEvent struct {
	Epoch   uint64           `json:"epoch"`
	Modules []artifact.State `json:"modules"`
}

// countingCompiler wraps the real compiler and records per-module builds.
type countingCompiler struct {
	inner compiler.Compiler
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingCompiler) Compile(ctx context.Context, in compiler.Input) ([]artifact.Artifact, error) {
	arts, err := c.inner.Compile(ctx, in)
	c.mu.Lock()
	c.calls[in.Module.Name]++
	c.mu.Unlock()
	return arts, err
}

func (c *countingCompiler) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func setupWorkspace(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// app depends on lib; extra is unrelated.
	write("lib/index.js", "LIB_V1")
	write("app/index.js", "APP_V1")
	write("extra/index.js", "EXTRA")

	cfg := config.Default()
	cfg.Workspace = root
	cfg.Address = ":0"
	cfg.DebounceWindow = config.Duration(30 * time.Millisecond)
	cfg.WatchPollInterval = config.Duration(15 * time.Millisecond)
	cfg.BuildTimeout = config.Duration(10 * time.Second)
	cfg.MaxParallelBuilds = 4
	cfg.Modules = []config.ModuleConfig{
		{Name: "lib", Dir: "lib", Include: []string{"**/*.js"}},
		{Name: "app", Dir: "app", Deps: []string{"lib"}, Include: []string{"**/*.js"}},
		{Name: "extra", Dir: "extra", Include: []string{"**/*.js"}},
	}
	return cfg
}

type system struct {
	o    *builder.Orchestrator
	h    *hub.Hub
	srv  *httptest.Server
	comp *countingCompiler
}

func startSystem(t *testing.T, cfg *config.Config) *system {
	t.Helper()
	deps := map[string][]string{}
	for _, m := range cfg.Modules {
		deps[m.Name] = m.Deps
	}
	g, err := graph.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	comp := &countingCompiler{inner: compiler.NewCommandCompiler(), calls: map[string]int{}}
	h := hub.New()
	o, err := builder.New(ctx, cfg, g, comp, builder.WithPublishCallback(h.Publish))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	<-o.Started

	w := watcher.New(watchDirs(cfg), cfg.WatchPollInterval.Std())
	changes := w.Run(ctx)
	go func() {
		for c := range changes {
			o.NotifyChangedPaths([]string{c.Path})
		}
	}()

	ts := httptest.NewServer(server.New(cfg, o.Store(), h).Handler())
	t.Cleanup(func() {
		ts.Close()
		o.Close()
		cancel()
	})
	return &system{o: o, h: h, srv: ts, comp: comp}
}

func watchDirs(cfg *config.Config) []string {
	var dirs []string
	for _, m := range cfg.Modules {
		dirs = append(dirs, filepath.Join(cfg.Workspace, filepath.FromSlash(m.Dir)))
	}
	return dirs
}

func getState(t *testing.T, url string) stateEvent {
	t.Helper()
	resp, err := http.Get(url + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s stateEvent
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func moduleState(s stateEvent, name string) artifact.State {
	for _, m := range s.Modules {
		if m.Name == name {
			return m
		}
	}
	return artifact.State{}
}

func waitState(t *testing.T, url string, cond func(stateEvent) bool) stateEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := getState(t, url)
		if cond(s) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("state condition not met")
	return stateEvent{}
}

func TestEndToEndIncrementalHMR(t *testing.T) {
	cfg := setupWorkspace(t)
	root := cfg.Workspace
	sys := startSystem(t, cfg)
	url := sys.srv.URL

	initial := getState(t, url)
	for _, name := range []string{"lib", "app", "extra"} {
		if moduleState(initial, name).Status != artifact.StatusOK {
			t.Fatalf("%s not ok after initial build", name)
		}
	}
	libV1 := moduleState(initial, "lib").Version
	appV1 := moduleState(initial, "app").Version
	extraV1 := moduleState(initial, "extra").Version

	// New SSE client immediately receives the current snapshot.
	sseCtx, cancelSSE := context.WithCancel(context.Background())
	t.Cleanup(cancelSSE)
	events := subscribeSSE(t, sseCtx, url)
	select {
	case cur := <-events:
		if cur.Epoch != initial.Epoch {
			t.Fatalf("new SSE client got epoch %d, want %d", cur.Epoch, initial.Epoch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE client did not receive current snapshot")
	}

	// Edit lib: lib and app rebuild, extra must not be rebuilt.
	extraBuildsBefore := sys.comp.count("extra")
	if err := os.WriteFile(filepath.Join(root, "lib", "index.js"), []byte("LIB_V2"), 0o644); err != nil {
		t.Fatal(err)
	}
	final := waitState(t, url, func(s stateEvent) bool {
		return moduleState(s, "lib").Version != libV1 &&
			moduleState(s, "app").Version != appV1
	})
	if moduleState(final, "extra").Version != extraV1 {
		t.Fatal("unrelated module extra version changed")
	}
	if got := sys.comp.count("extra"); got != extraBuildsBefore {
		t.Fatalf("extra rebuilt %d times although unrelated", got-extraBuildsBefore)
	}
	// lib once for the change, app once as a dependent (one cycle each).
	if got := sys.comp.count("lib"); got != 2 {
		t.Fatalf("lib built %d times, want 2 (initial + 1 incremental)", got)
	}

	// The update must be pushed to the connected client. Multiple commits
	// may have happened (building + ok); wait for one carrying the new lib.
	deadline := time.Now().Add(3 * time.Second)
	pushedNew := false
	for time.Now().Before(deadline) {
		select {
		case pushed := <-events:
			if moduleState(pushed, "lib").Version != libV1 {
				pushedNew = true
			}
		case <-time.After(500 * time.Millisecond):
		}
		if pushedNew {
			break
		}
	}
	if !pushedNew {
		t.Fatal("no SSE update carrying the new lib version was pushed")
	}

	// Artifact endpoint serves the new content.
	resp, err := http.Get(url + "/artifacts/lib/index.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Module-Status") != artifact.StatusOK {
		t.Fatal("artifact response must expose ok status")
	}
	if got := readBody(t, resp); got != "LIB_V2" {
		t.Fatalf("artifact content = %q, want LIB_V2", got)
	}

	// Saving identical content must not create a new version.
	libV2 := moduleState(final, "lib").Version
	if err := os.WriteFile(filepath.Join(root, "lib", "index.js"), []byte("LIB_V2"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := moduleState(getState(t, url), "lib").Version; got != libV2 {
		t.Fatalf("identical content produced a new version: %s != %s", got, libV2)
	}
}

func TestEndToEndFailureAndRecovery(t *testing.T) {
	cfg := setupWorkspace(t)
	root := cfg.Workspace
	// lib fails while a sentinel file exists; deleting the sentinel is
	// the "fix", and a subsequent save of a watched source file triggers
	// the automatic recovery rebuild.
	sentinel := filepath.Join(root, "lib", "BROKEN")
	if err := os.WriteFile(sentinel, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Modules[0].Command = []string{"/bin/sh", "-c", "test ! -e BROKEN"}
	sys := startSystem(t, cfg)
	url := sys.srv.URL

	s := waitState(t, url, func(s stateEvent) bool {
		return moduleState(s, "lib").Status == artifact.StatusError
	})
	if moduleState(s, "lib").Error == "" {
		t.Fatal("failed module has no error message")
	}
	if moduleState(s, "extra").Status != artifact.StatusOK {
		t.Fatal("other modules must stay usable when lib fails")
	}

	// Fix the build (remove sentinel) and save a source file.
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "index.js"), []byte("LIB_FIXED"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := waitState(t, url, func(s stateEvent) bool {
		st := moduleState(s, "lib")
		return st.Status == artifact.StatusOK && st.Error == ""
	})
	if moduleState(s2, "lib").Version == "" {
		t.Fatal("recovered module should have a version")
	}
	_ = sys
}

func subscribeSSE(t *testing.T, ctx context.Context, url string) <-chan stateEvent {
	t.Helper()
	out := make(chan stateEvent, 8)
	go func() {
		defer close(out)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/events", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		r := bufio.NewReader(resp.Body)
		var data []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data: ") {
				data = append(data, strings.TrimPrefix(line, "data: "))
			} else if line == "" && len(data) > 0 {
				var ev stateEvent
				if json.Unmarshal([]byte(strings.Join(data, "\n")), &ev) == nil {
					select {
					case out <- ev:
					default:
					}
				}
				data = nil
			}
		}
	}()
	return out
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil && n == 0 {
		t.Fatal(err)
	}
	return string(buf[:n])
}
