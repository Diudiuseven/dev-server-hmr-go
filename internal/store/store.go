package store

import (
	"sync"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
)

// Store keeps the current Snapshot and replaces it atomically.
// Every read gets either the previous complete snapshot or the new
// complete snapshot, never a partially updated one.
type Store struct {
	mu  sync.RWMutex
	cur *artifact.Snapshot
}

// Result is the outcome of building one module.
type Result struct {
	// Version is the content-addressed version of the successful build.
	Version string
	// Artifacts are the produced outputs, ignored when Error != "".
	Artifacts []artifact.Artifact
	// Error is the empty string on success.
	Error string
}

// New creates a Store for the given modules, all in StatusBuilding
// until the first build completes.
func New(names []string) *Store {
	states := make(map[string]artifact.State, len(names))
	now := time.Now().UTC()
	for _, name := range names {
		states[name] = artifact.State{Name: name, Status: artifact.StatusBuilding, UpdatedAt: now}
	}
	return &Store{cur: artifact.NewSnapshot(0, states, nil)}
}

// Current returns the current immutable snapshot. The returned pointer
// stays valid forever; a later Commit replaces it instead of mutating.
func (s *Store) Current() *artifact.Snapshot {
	s.mu.RLock()
	cur := s.cur
	s.mu.RUnlock()
	return cur
}

// Commit applies the results of one build cycle atomically and returns
// the new snapshot.
//
// Modules absent from results keep their previous state. A failed module
// keeps serving its previous successful artifacts/version; only status
// and error message change.
func (s *Store) Commit(results map[string]Result) *artifact.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev := s.cur
	now := time.Now().UTC()
	states := make(map[string]artifact.State, len(prev.States))
	arts := make(map[string][]artifact.Artifact, len(prev.States))
	// Carry every previous state/artifact forward first.
	for name, st := range prev.States {
		states[name] = st
	}

	// Snapshot is constructed with copied maps; rebuild the artifact map
	// from the previous snapshot via its accessor so we do not depend on
	// private fields across types.
	for _, st := range prev.States {
		if got := prev.Artifacts(st.Name); got != nil {
			arts[st.Name] = got
		}
	}

	for name, res := range results {
		old, ok := states[name]
		if !ok {
			// Unknown module in the result set: ignore rather than corrupt.
			continue
		}
		if res.Error != "" {
			// Build failed: retain last successful version and artifacts,
			// expose the failure explicitly.
			old.Status = artifact.StatusError
			old.Error = res.Error
			old.UpdatedAt = now
			states[name] = old
			continue
		}
		// Build succeeded.
		old.Status = artifact.StatusOK
		old.Error = ""
		if old.Version != res.Version {
			old.Version = res.Version
			old.UpdatedAt = now
		}
		states[name] = old
		if len(res.Artifacts) > 0 {
			cp := make([]artifact.Artifact, len(res.Artifacts))
			copy(cp, res.Artifacts)
			arts[name] = cp
		} else {
			arts[name] = nil
		}
	}

	s.cur = artifact.NewSnapshot(prev.Epoch+1, states, arts)
	return s.cur
}

// MarkBuilding flips the given modules to StatusBuilding (preserving
// their last known version and artifacts) and returns the new snapshot.
func (s *Store) MarkBuilding(modules []string) *artifact.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev := s.cur
	states := make(map[string]artifact.State, len(prev.States))
	for name, st := range prev.States {
		states[name] = st
	}
	now := time.Now().UTC()
	for _, name := range modules {
		if st, ok := states[name]; ok {
			st.Status = artifact.StatusBuilding
			st.UpdatedAt = now
			states[name] = st
		}
	}
	// Artifacts are unchanged; NewSnapshot copies what the previous
	// snapshot exposes.
	arts := map[string][]artifact.Artifact{}
	for name := range prev.States {
		if got := prev.Artifacts(name); got != nil {
			arts[name] = got
		}
	}
	s.cur = artifact.NewSnapshot(prev.Epoch+1, states, arts)
	return s.cur
}
