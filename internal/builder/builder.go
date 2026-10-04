// Package builder defines how a single module is built and provides the
// default command-based implementation.
package builder

import (
	"context"
	"time"
)

// Input is everything a build needs.
type Input struct {
	Module string
	Root   string
	// DepArtifacts maps dependency module names to the artifact blob of the
	// dependency version being built against.
	DepArtifacts map[string][]byte
	// SourceHash is the content hash of the module's own source files.
	SourceHash string
	// ArtifactPath, when non-empty, is where the command must leave its
	// artifact (the command may also ignore it and write to stdout).
	ArtifactPath string
}

// Result is the outcome of one module build.
type Result struct {
	Module   string
	Artifact []byte
	// SourceHash identifies the source content + dependency versions that
	// were built. Identical inputs must produce an identical hash.
	SourceHash string
	// VersionID is the content address of the produced artifact.
	VersionID string
	BuiltAt   time.Time
}

// Builder executes one module build. Implementations must be safe for
// concurrent use.
type Builder interface {
	Build(ctx context.Context, in Input) (*Result, error)
}

// Func adapts a function to the Builder interface.
type Func func(ctx context.Context, in Input) (*Result, error)

// Build implements Builder.
func (f Func) Build(ctx context.Context, in Input) (*Result, error) { return f(ctx, in) }
