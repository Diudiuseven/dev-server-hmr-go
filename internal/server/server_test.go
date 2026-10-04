package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
)

func testServer() (*Server, *store.Store, *hub.Hub) {
	cfg := &config.Config{
		Workspace: "/ws",
		Address:   ":0",
		Modules: []config.ModuleConfig{
			{Name: "a", Dir: "a"},
			{Name: "b", Dir: "b", Deps: []string{"a"}},
		},
	}
	st := store.New([]string{"a", "b"})
	h := hub.New()
	return New(cfg, st, h), st, h
}

// publishCurrent mirrors what the orchestrator does after every commit.
func publishCurrent(st *store.Store, h *hub.Hub) { h.Publish(st.Current()) }

func TestStateEndpoint(t *testing.T) {
	srv, st, _ := testServer()
	st.Commit(map[string]store.Result{
		"a": {Version: "va", Artifacts: []artifact.Artifact{{Path: "a.js", Content: []byte("A")}}},
		"b": {Version: "vb", Artifacts: []artifact.Artifact{{Path: "b.js", Content: []byte("B")}}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got stateJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Modules) != 2 || got.Modules[0].Name != "a" || got.Modules[0].Version != "va" {
		t.Fatalf("unexpected state payload: %+v", got)
	}
}

func TestArtifactEndpointAndStatusHeaders(t *testing.T) {
	srv, st, _ := testServer()
	st.Commit(map[string]store.Result{
		"a": {Version: "va", Artifacts: []artifact.Artifact{{Path: "a.js", Content: []byte("hello")}}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifacts/a/a.js", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Module-Version") != "va" {
		t.Fatal("missing version header")
	}
	if rec.Header().Get("X-Module-Status") != artifact.StatusOK {
		t.Fatal("missing ok status header")
	}
	// Conditional GET with matching ETag must be a cache hit.
	etag := rec.Header().Get("ETag")
	req2 := httptest.NewRequest(http.MethodGet, "/artifacts/a/a.js", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", rec2.Code)
	}
}

func TestArtifactTraversalRejected(t *testing.T) {
	srv, _, _ := testServer()
	// ServeMux normalizes traversal attempts with a redirect before the
	// handler runs; both redirects and direct 404s are acceptable denies.
	for _, p := range []string{"/artifacts/a/../../etc/passwd"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s -> %d, want deny", p, rec.Code)
		}
	}
	// Encoded traversal / unknown module reaches the handler as 404.
	for _, p := range []string{"/artifacts/a/..%2f..%2fetc/passwd", "/artifacts/unknown/x"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s -> %d, want 404", p, rec.Code)
		}
	}
}

func TestSSEReceivesCurrentAndUpdates(t *testing.T) {
	srv, st, h := testServer()
	st.Commit(map[string]store.Result{
		"a": {Version: "v1", Artifacts: []artifact.Artifact{{Path: "a.js", Content: []byte("1")}}},
		"b": {Version: "v1"},
	})
	publishCurrent(st, h)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)

	// First event = current snapshot (new client gets current state).
	if !readSSE(t, reader, func(event, data string) bool {
		if event != "update" {
			return false
		}
		var s stateJSON
		if json.Unmarshal([]byte(data), &s) != nil {
			return false
		}
		return s.Modules[0].Version == "v1"
	}) {
		t.Fatal("did not receive current snapshot as first event")
	}

	// Publish an update; it must be pushed without the client refreshing.
	snap := st.Commit(map[string]store.Result{
		"a": {Version: "v2", Artifacts: []artifact.Artifact{{Path: "a.js", Content: []byte("2")}}},
	})
	h.Publish(snap)
	if !readSSE(t, reader, func(event, data string) bool {
		return strings.Contains(data, `"v2"`)
	}) {
		t.Fatal("did not receive pushed update")
	}
}

// readSSE reads SSE frames until predicate(event,data) is true or timeout.
func readSSE(t *testing.T, r *bufio.Reader, ok func(event, data string) bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var event, data []string
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			return false
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			ev := strings.Join(event, ";")
			d := strings.Join(data, "\n")
			event, data = nil, nil
			if ev == "" && d == "" {
				continue // comment/keep-alive frame (e.g. initial ": connected")
			}
			if ok(ev, d) {
				return true
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			event = append(event, strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
	return false
}

func TestFailedModuleHeaders(t *testing.T) {
	srv, st, _ := testServer()
	st.Commit(map[string]store.Result{
		"a": {Version: "good", Artifacts: []artifact.Artifact{{Path: "a.js", Content: []byte("g")}}},
	})
	st.Commit(map[string]store.Result{"a": {Error: "compile error"}})

	// Existing last-good artifact is still served.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/artifacts/a/a.js", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("last good artifact must remain available, got %d", rec.Code)
	}
	if rec.Header().Get("X-Module-Status") != artifact.StatusError {
		t.Fatal("artifact response must expose error status")
	}
	if rec.Header().Get("X-Module-Error") != "compile error" {
		t.Fatal("artifact response must expose error message")
	}
}
