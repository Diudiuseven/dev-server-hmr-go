package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/builder"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/engine"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/graph"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
)

func setup(t *testing.T) (*httptest.Server, *state.Store, *hub.Hub, *engine.Engine) {
	t.Helper()
	ws := t.TempDir()
	g, err := graph.New([]graph.ModuleDecl{{Name: "m"}, {Name: "n"}})
	if err != nil {
		t.Fatal(err)
	}
	st := state.NewStore([]string{"m", "n"})
	art, err := store.NewArtifactStore(ws + "/art")
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New()
	cfg := config.Config{Modules: []config.Module{{Name: "m", Root: "m"}, {Name: "n", Root: "n"}}}
	// Build an engine without running it; only TriggerFullBuild is exercised.
	eng := engine.New(engine.Options{
		Config: cfg, Graph: g,
		Builder: builder.Func(func(ctx context.Context, in builder.Input) (*builder.Result, error) {
			return &builder.Result{Module: in.Module, Artifact: []byte("x"), VersionID: "x", SourceHash: in.SourceHash}, nil
		}),
		State: st, Artifacts: art, Debounce: 10 * time.Millisecond,
	})
	srv := New(cfg, st, art, h, eng)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, h, eng
}

func TestHealthAndStatus(t *testing.T) {
	ts, st, _, _ := setup(t)
	st.CommitBuild("m", &state.Version{Module: "m", VersionID: "v1", SourceHash: "h1"}, nil)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("health not 200")
	}

	resp, err = http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sr statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		t.Fatal(err)
	}
	if len(sr.Modules) != 2 {
		t.Fatalf("want 2 modules, got %d", len(sr.Modules))
	}
	byName := map[string]moduleView{}
	for _, m := range sr.Modules {
		byName[m.Module] = m
	}
	if byName["m"].VersionID != "v1" || byName["m"].Status != "ok" {
		t.Fatalf("m snapshot wrong: %+v", byName["m"])
	}
	if byName["n"].Status != "unknown" {
		t.Fatalf("n should be unknown: %+v", byName["n"])
	}
}

func TestSSEGetsSnapshotThenEvents(t *testing.T) {
	ts, st, h, _ := setup(t)
	st.CommitBuild("m", &state.Version{Module: "m", VersionID: "v1", SourceHash: "h1"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("events status %d", resp.StatusCode)
	}
	t.Logf("ctype=%s", resp.Header.Get("Content-Type"))
	reader := bufio.NewReader(resp.Body)

	readEvent := func() string {
		var data strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			line = strings.TrimRight(line, "\n")
			if strings.HasPrefix(line, "data: ") {
				data.WriteString(strings.TrimPrefix(line, "data: "))
			}
			if line == "" && data.Len() > 0 {
				return data.String()
			}
			if line == "" {
				continue // heartbeat boundary
			}
		}
	}
	first := readEvent()
	if !strings.Contains(first, `"type":"snapshot"`) || !strings.Contains(first, "v1") {
		t.Fatalf("first event must be current snapshot, got %s", first)
	}

	h.Broadcast([]byte(`{"type":"build:ok","module":"n","versionId":"v9"}`))
	ev := readEvent()
	if !strings.Contains(ev, `"module":"n"`) || !strings.Contains(ev, "v9") {
		t.Fatalf("did not receive broadcast: %s", ev)
	}
}

func TestArtifactServesOnlyCurrentVersion(t *testing.T) {
	ts, st, _, _ := setup(t)
	st.CommitBuild("m", &state.Version{Module: "m", VersionID: "v1", SourceHash: "h1", Artifact: []byte("old")}, nil)
	st.CommitBuild("m", &state.Version{Module: "m", VersionID: "v2", SourceHash: "h2", Artifact: []byte("new")}, nil)

	resp, err := http.Get(ts.URL + "/artifacts/m/v2")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("current version status = %d", resp.StatusCode)
	}
	buf := make([]byte, 16)
	n, _ := resp.Body.Read(buf)
	resp.Body.Close()
	if string(buf[:n]) != "new" {
		t.Fatalf("artifact = %q", buf[:n])
	}

	// Old version is superseded -> conflict, never half-content.
	resp, err = http.Get(ts.URL + "/artifacts/m/v1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("superseded version status = %d, want 409", resp.StatusCode)
	}
}

func TestConcurrentClientsAllGetSnapshot(t *testing.T) {
	ts, st, _, _ := setup(t)
	st.CommitBuild("m", &state.Version{Module: "m", VersionID: "vZ", SourceHash: "hZ"}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(ts.URL + "/status")
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			var sr statusResponse
			if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
				t.Error(err)
				return
			}
			found := false
			for _, m := range sr.Modules {
				if m.Module == "m" && m.VersionID == "vZ" {
					found = true
				}
			}
			if !found {
				t.Error("client missed current version")
			}
		}()
	}
	wg.Wait()
	_ = fmt.Sprint
}
