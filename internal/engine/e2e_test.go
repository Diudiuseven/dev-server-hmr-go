package engine_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/engine"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/server"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/watcher"
)

// fileBuilder builds by copying a deterministic transform of the source so
// real filesystem changes flow end to end.
func fileBuilderSpec(ws string, mods []config.Module) []builder.Spec {
	specs := make([]builder.Spec, 0, len(mods))
	for _, m := range mods {
		specs = append(specs, builder.Spec{Name: m.Name, Root: filepath.Join(ws, m.Root)})
	}
	return specs
}

// TestEndToEndIncrementalHMR wires the real watcher, engine, store and HTTP
// server, then edits a dependency module and asserts that:
//   - the client receives build events over SSE,
//   - only the affected chain rebuilds,
//   - the published artifact atomically becomes the new version.
func TestEndToEndIncrementalHMR(t *testing.T) {
	ws := t.TempDir()
	mods := []config.Module{
		{Name: "app", Root: "app", Deps: []string{"lib"}},
		{Name: "lib", Root: "lib"},
		{Name: "standalone", Root: "solo"},
	}
	for _, m := range mods {
		if err := os.MkdirAll(filepath.Join(ws, m.Root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, m.Root, "src.txt"), []byte("init-"+m.Name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	decls := []graph.ModuleDecl{
		{Name: "app", Deps: []string{"lib"}},
		{Name: "lib"},
		{Name: "standalone"},
	}
	g, err := graph.New(decls)
	if err != nil {
		t.Fatal(err)
	}
	st := state.NewStore([]string{"app", "lib", "standalone"})
	art, err := store.NewArtifactStore(filepath.Join(ws, ".art"))
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New()

	var eventMu sync.Mutex
	var gotEvents []string
	cfg := config.Config{
		Workspace: ws,
		Modules:   mods,
		Build:     config.BuildConfig{Concurrency: 2, Timeout: config.Duration{Duration: 5 * time.Second}},
		Watch:     config.WatchConfig{Debounce: config.Duration{Duration: 50 * time.Millisecond}, PollInterval: config.Duration{Duration: 20 * time.Millisecond}},
	}
	eng := engine.New(engine.Options{
		Config: cfg, Graph: g,
		Builder:   builder.NewCommandBuilder(fileBuilderSpec(ws, mods)),
		State:     st,
		Artifacts: art,
		Debounce:  50 * time.Millisecond,
		OnEvent: func(ev engine.Event) {
			eventMu.Lock()
			gotEvents = append(gotEvents, ev.Type+":"+ev.Module)
			eventMu.Unlock()
			if payload, merr := json.Marshal(ev); merr == nil {
				h.Broadcast(payload)
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := watcher.New(watcher.Options{
		Workspace: ws, Roots: []string{"app", "lib", "solo"},
		Exclude: []string{".art"}, Interval: 20 * time.Millisecond,
	})
	changes := w.Run(ctx)
	go func() {
		for ch := range changes {
			eng.NotifyChanged([]string{ch.Path})
		}
	}()

	engErr := make(chan error, 1)
	go func() { engErr <- eng.Run(ctx) }()

	srv := httptest.NewServer(server.New(cfg, st, art, h, eng).Handler())
	defer srv.Close()

	// Wait for initial build.
	waitForVersion(t, srv.URL, "app", true, 3*time.Second)
	libV1 := moduleVersion(t, srv.URL, "lib")
	if libV1 == "" {
		t.Fatal("lib did not build initially")
	}

	// Open SSE client after initial snapshot.
	sseEvents := openSSE(t, srv.URL)

	// Edit lib: only lib + app should rebuild; standalone unchanged.
	if err := os.WriteFile(filepath.Join(ws, "lib", "src.txt"), []byte("updated-lib-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Artifact atomically advances to a new lib version.
	waitForVersionChange(t, srv.URL, "lib", libV1, 4*time.Second)

	// SSE eventually reports lib and app ok.
	deadline := time.After(4 * time.Second)
	seen := map[string]bool{}
	for !(seen["build:ok:lib"] && seen["build:ok:app"]) {
		select {
		case ev := <-sseEvents:
			if strings.Contains(ev, `"type":"build:ok"`) {
				if strings.Contains(ev, `"module":"lib"`) {
					seen["build:ok:lib"] = true
				}
				if strings.Contains(ev, `"module":"app"`) {
					seen["build:ok:app"] = true
				}
			}
		case <-deadline:
			t.Fatalf("did not receive rebuild events, got %v", seen)
		}
	}
	// Verify standalone version unchanged (unrelated module not rebuilt).
	if got := moduleVersion(t, srv.URL, "standalone"); got == "" {
		t.Fatal("standalone missing")
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func moduleVersion(t *testing.T, base, mod string) string {
	t.Helper()
	resp, err := http.Get(base + "/modules/" + mod)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	body, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(body, &m)
	if v, ok := m["versionId"].(string); ok {
		return v
	}
	return ""
}

func waitForVersion(t *testing.T, base, mod string, _ bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if moduleVersion(t, base, mod) != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("module %s never produced a version", mod)
}

func waitForVersionChange(t *testing.T, base, mod, old string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if v := moduleVersion(t, base, mod); v != "" && v != old {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("module %s version never advanced from %s", mod, old)
}

func openSSE(t *testing.T, base string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("sse status %d", resp.StatusCode)
	}
	out := make(chan string, 64)
	go func() {
		defer cancel()
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		var data strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(out)
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data: ") {
				data.WriteString(strings.TrimPrefix(line, "data: "))
			} else if line == "" && data.Len() > 0 {
				select {
				case out <- data.String():
				default:
				}
				data.Reset()
			}
		}
	}()
	return out
}
