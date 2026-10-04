// Package state tracks the current version and status of every module.
package state

import (
	"sync"
	"time"
)

// Status is the build status of a module.
type Status int

const (
	// StatusUnknown before the first build.
	StatusUnknown Status = iota
	// StatusBuilding while a build is in progress.
	StatusBuilding
	// StatusOK after a successful build.
	StatusOK
	// StatusFailed after a failed build; the previous good version is retained.
	StatusFailed
)

// String renders a status for API responses.
func (s Status) String() string {
	switch s {
	case StatusBuilding:
		return "building"
	case StatusOK:
		return "ok"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Version is one immutable, content-addressed build result.
type Version struct {
	Module     string
	VersionID  string // content hash; identical content -> identical id
	Artifact   []byte
	BuiltAt    time.Time
	SourceHash string
}

// Snapshot is a point-in-time view of one module.
type Snapshot struct {
	Module    string   `json:"module"`
	Status    Status   `json:"status"`
	Building  bool     `json:"building"`
	Current   *Version `json:"-"`
	VersionID string   `json:"versionId,omitempty"`
	Error     string   `json:"error,omitempty"`
	FailedAt  int64    `json:"failedAt,omitempty"`
	ChangedAt int64    `json:"changedAt,omitempty"`
}

type moduleState struct {
	status    Status
	current   *Version // last successful version; nil until first success
	building  bool
	errMsg    string
	failedAt  time.Time
	changedAt time.Time
	// inflightSource is the source hash currently being built, used to
	// collapse duplicate builds of identical content.
	inflightSource string
}

// Store is the concurrency-safe registry of per-module state.
type Store struct {
	mu    sync.RWMutex
	mods  map[string]*moduleState
	order []string
}

// NewStore creates an empty Store for the given modules.
func NewStore(modules []string) *Store {
	st := &Store{mods: make(map[string]*moduleState, len(modules))}
	for _, m := range modules {
		if _, ok := st.mods[m]; ok {
			continue
		}
		st.mods[m] = &moduleState{}
		st.order = append(st.order, m)
	}
	return st
}

// Snapshot returns a point-in-time view of one module.
func (s *Store) Snapshot(name string) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ms, ok := s.mods[name]
	if !ok {
		return Snapshot{Module: name, Status: StatusUnknown}
	}
	return ms.snapshot(name)
}

// All returns snapshots in module declaration order.
func (s *Store) All() []Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Snapshot, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.mods[name].snapshot(name))
	}
	return out
}

func (ms *moduleState) snapshot(name string) Snapshot {
	snap := Snapshot{
		Module:   name,
		Status:   ms.status,
		Building: ms.building,
		Current:  ms.current,
		Error:    ms.errMsg,
	}
	if !ms.failedAt.IsZero() {
		snap.FailedAt = ms.failedAt.UnixNano()
	}
	if !ms.changedAt.IsZero() {
		snap.ChangedAt = ms.changedAt.UnixNano()
	}
	if ms.current != nil {
		snap.VersionID = ms.current.VersionID
	}
	return snap
}

// BeginBuild marks a module as building. It returns true if this source
// hash should actually be built: false means an identical build is either
// in flight or already published, so the caller must skip it.
func (s *Store) BeginBuild(name, sourceHash string, changedAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms, ok := s.mods[name]
	if !ok {
		return false
	}
	ms.changedAt = changedAt
	if sourceHash != "" {
		if ms.current != nil && ms.current.SourceHash == sourceHash {
			return false // identical content already published
		}
		if ms.building && ms.inflightSource == sourceHash {
			return false // identical build already in flight
		}
	}
	ms.building = true
	ms.status = StatusBuilding
	ms.inflightSource = sourceHash
	return true
}

// CommitBuild publishes a successful build. On failure it records the
// error while keeping the previous good version.
func (s *Store) CommitBuild(name string, v *Version, buildErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms, ok := s.mods[name]
	if !ok {
		return
	}
	ms.building = false
	ms.inflightSource = ""
	if buildErr != nil {
		ms.status = StatusFailed
		ms.errMsg = buildErr.Error()
		ms.failedAt = time.Now()
		return
	}
	ms.status = StatusOK
	ms.errMsg = ""
	ms.failedAt = time.Time{}
	if v != nil {
		ms.current = v
	}
}

// IsBuilding reports whether a build is currently running for the module.
func (s *Store) IsBuilding(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mods[name].building
}

// AnyBuilding reports whether any module build is in progress.
func (s *Store) AnyBuilding() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ms := range s.mods {
		if ms.building {
			return true
		}
	}
	return false
}
