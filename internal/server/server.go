package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
)

// Server exposes the current state over HTTP:
//
//	GET /healthz                 liveness probe
//	GET /api/state               current snapshot (JSON)
//	GET /api/events              Server-Sent Events hot-update stream
//	GET /artifacts/<mod>/<path>  current artifact content
type Server struct {
	cfg *config.Config
	st  *store.Store
	h   *hub.Hub
}

// New creates a Server.
func New(cfg *config.Config, st *store.Store, h *hub.Hub) *Server {
	return &Server{cfg: cfg, st: st, h: h}
}

// stateJSON is the public projection of a snapshot.
type stateJSON struct {
	Epoch   uint64           `json:"epoch"`
	Modules []artifact.State `json:"modules"`
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/artifacts/", s.handleArtifact)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.st.Current()
	payload := stateJSON{
		Epoch:   snap.Epoch,
		Modules: snap.ModuleStates(s.moduleNames()),
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, cancel := s.h.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// Comment line establishes the stream immediately and through proxies.
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// Request canceled: just return, the server stays consistent.
			return
		case snap, open := <-ch:
			if !open {
				return
			}
			payload := stateJSON{
				Epoch:   snap.Epoch,
				Modules: snap.ModuleStates(s.moduleNames()),
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: update\ndata: %s\n\n", body); err != nil {
				return // client gone; unsubscribe cleans up
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	if strings.Contains(rest, "..") {
		// Reject traversal attempts before any path cleaning/redirect.
		http.NotFound(w, r)
		return
	}
	rest = path.Clean("/" + rest)
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" || rest == "." {
		http.NotFound(w, r)
		return
	}
	module, artifactPath, ok := strings.Cut(rest, "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	snap := s.st.Current()
	state, exists := snap.States[module]
	if !exists {
		http.NotFound(w, r)
		return
	}
	for _, art := range snap.Artifacts(module) {
		if art.Path == artifactPath {
			// ETag carries the content-addressed version plus path so
			// clients can cache and detect "content unchanged".
			if state.Version != "" {
				w.Header().Set("ETag", fmt.Sprintf("%q", state.Version+"-"+artifactPath))
			}
			// Headers reflect the *current* status even though the last
			// good artifacts remain served while a build fails.
			w.Header().Set("X-Module-Version", state.Version)
			w.Header().Set("X-Module-Status", state.Status)
			if state.Status == artifact.StatusError {
				w.Header().Set("X-Module-Error", state.Error)
			}
			http.ServeContent(w, r, artifactPath, state.UpdatedAt, strings.NewReader(string(art.Content)))
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) moduleNames() []string {
	names := make([]string, 0, len(s.cfg.Modules))
	for _, m := range s.cfg.Modules {
		names = append(names, m.Name)
	}
	return names
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Serve starts the HTTP server and blocks until it returns.
func Serve(ctx context.Context, cfg *config.Config, handler http.Handler) error {
	srv := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
