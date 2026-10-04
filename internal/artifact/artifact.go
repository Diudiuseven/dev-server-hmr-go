package artifact

import "time"

// Status values of a module build.
const (
	// StatusBuilding means a build is currently in progress.
	StatusBuilding = "building"
	// StatusOK means the latest build succeeded.
	StatusOK = "ok"
	// StatusError means the latest build failed.
	StatusError = "error"
)

// Artifact is one output file of a successful module build.
type Artifact struct {
	// Path is the artifact path relative to the module, slash separated.
	Path string
	// Content is the file content.
	Content []byte
}

// State is the externally visible state of one module.
type State struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Snapshot is one immutable point-in-time view of all modules.
// It is replaced atomically; readers never see a half-written view.
type Snapshot struct {
	// Epoch is incremented by every committed build cycle.
	Epoch uint64 `json:"epoch"`
	// States holds the per-module states, keyed by module name.
	States map[string]State `json:"states"`
	// artifacts is keyed by module name and only accessible via accessors.
	artifacts map[string][]Artifact
}

// NewSnapshot creates an immutable snapshot. The maps are copied; the
// caller must not mutate the artifact byte slices afterwards.
func NewSnapshot(epoch uint64, states map[string]State, arts map[string][]Artifact) *Snapshot {
	sc := make(map[string]State, len(states))
	for k, v := range states {
		sc[k] = v
	}
	var ac map[string][]Artifact
	if arts != nil {
		ac = make(map[string][]Artifact, len(arts))
		for k, v := range arts {
			cp := make([]Artifact, len(v))
			copy(cp, v)
			ac[k] = cp
		}
	}
	return &Snapshot{Epoch: epoch, States: sc, artifacts: ac}
}

// ModuleStates returns the states of the named modules in the given
// order; missing names are skipped.
func (s *Snapshot) ModuleStates(names []string) []State {
	out := make([]State, 0, len(names))
	for _, name := range names {
		if st, ok := s.States[name]; ok {
			out = append(out, st)
		}
	}
	return out
}

// Artifacts returns a copy of the artifact slice of a module.
// A failed or never-built module returns nil.
func (s *Snapshot) Artifacts(module string) []Artifact {
	arts := s.artifacts[module]
	if len(arts) == 0 {
		return nil
	}
	out := make([]Artifact, len(arts))
	copy(out, arts)
	return out
}
