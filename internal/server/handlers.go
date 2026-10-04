package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/state"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// statusResponse is the complete current snapshot handed to clients.
type statusResponse struct {
	Modules []moduleView `json:"modules"`
}

type moduleView struct {
	Module    string `json:"module"`
	Status    string `json:"status"`
	Building  bool   `json:"building"`
	VersionID string `json:"versionId,omitempty"`
	Error     string `json:"error,omitempty"`
	FailedAt  int64  `json:"failedAt,omitempty"`
	ChangedAt int64  `json:"changedAt,omitempty"`
}

func toView(snap state.Snapshot) moduleView {
	return moduleView{
		Module:    snap.Module,
		Status:    snap.Status.String(),
		Building:  snap.Building,
		VersionID: snap.VersionID,
		Error:     snap.Error,
		FailedAt:  snap.FailedAt,
		ChangedAt: snap.ChangedAt,
	}
}

func (s *Server) snapshotPayload() []byte {
	snaps := s.st.All()
	resp := statusResponse{Modules: make([]moduleView, 0, len(snaps))}
	for _, snap := range snaps {
		resp.Modules = append(resp.Modules, toView(snap))
	}
	b, _ := json.Marshal(struct {
		Type    string       `json:"type"`
		Modules []moduleView `json:"modules"`
	}{Type: "snapshot", Modules: resp.Modules})
	return b
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snaps := s.st.All()
	resp := statusResponse{Modules: make([]moduleView, 0, len(snaps))}
	for _, snap := range snaps {
		resp.Modules = append(resp.Modules, toView(snap))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleModule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	snap := s.st.Snapshot(name)
	if snap.Module == "" || snap.Status == state.StatusUnknown {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown module"})
		return
	}
	writeJSON(w, http.StatusOK, toView(snap))
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	version := r.PathValue("version")
	snap := s.st.Snapshot(name)
	if snap.Module == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown module"})
		return
	}
	// Only serve a version that is the current published one, so readers
	// never fetch a half-replaced or superseded artifact.
	if snap.VersionID != version || snap.Current == nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":   "version not current",
			"current": snap.VersionID,
		})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Module", name)
	w.Header().Set("X-Version-Id", version)
	_, _ = w.Write(snap.Current.Artifact)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Make sure this client receives the current complete snapshot first.
	s.hub.SnapshotFor = s.snapshotPayload
	client, cancel := s.hub.Subscribe(r.Context())
	defer cancel()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-client.Events():
			if !open || !json.Valid(payload) {
				if !open {
					return
				}
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return // vanished client: defer unsubscribes; builds unaffected
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	s.eng.TriggerFullBuild()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scheduled"})
}
