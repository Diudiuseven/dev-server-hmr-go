// Package server exposes status, artifact and hot-reload event endpoints.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/engine"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/hub"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/store"
)

// Server holds HTTP handlers.
type Server struct {
	cfg config.Config
	st  *state.Store
	art *store.ArtifactStore
	hub *hub.Hub
	eng *engine.Engine
}

// New constructs the HTTP server dependencies.
func New(cfg config.Config, st *state.Store, art *store.ArtifactStore, h *hub.Hub, e *engine.Engine) *Server {
	return &Server{cfg: cfg, st: st, art: art, hub: h, eng: e}
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /modules/{name}", s.handleModule)
	mux.HandleFunc("GET /artifacts/{name}/{version}", s.handleArtifact)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /rebuild", s.handleRebuild)
	return logging(mux)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
