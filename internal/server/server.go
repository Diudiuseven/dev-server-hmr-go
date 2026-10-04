// Package server exposes status, artifact and hot-reload event endpoints.
package server

import (
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
}

// New constructs the HTTP server dependencies.
func New(cfg config.Config, st *state.Store, art *store.ArtifactStore, h *hub.Hub, e *engine.Engine) *Server {
	return &Server{}
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler { return http.NewServeMux() }
