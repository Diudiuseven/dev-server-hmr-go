package builder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Spec describes how to build one module.
type Spec struct {
	Name     string
	Root     string
	Command  string // optional; empty means "no command", artifact is synthetic
	Args     []string
	Artifact string // optional output file relative to Root; stdout otherwise
}

// CommandBuilder runs each module's build command. It is safe for
// concurrent use; commands run in independent working directories.
type CommandBuilder struct {
	specs map[string]Spec
}

// NewCommandBuilder constructs a builder from per-module specs.
func NewCommandBuilder(specs []Spec) *CommandBuilder {
	m := make(map[string]Spec, len(specs))
	for _, s := range specs {
		m[s.Name] = s
	}
	return &CommandBuilder{specs: m}
}

// Spec returns the registered spec for a module.
func (b *CommandBuilder) Spec(name string) (Spec, bool) {
	s, ok := b.specs[name]
	return s, ok
}

// Build implements Builder.
func (b *CommandBuilder) Build(ctx context.Context, in Input) (*Result, error) {
	spec, ok := b.specs[in.Module]
	if !ok {
		return nil, fmt.Errorf("builder: unknown module %q", in.Module)
	}
	var artifact []byte
	if spec.Command != "" {
		cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
		cmd.Dir = spec.Root
		cmd.Env = append(os.Environ(),
			"HMR_MODULE="+spec.Name,
			"HMR_ARTIFACT_PATH="+in.ArtifactPath,
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("build %s failed: %w: %s", spec.Name, err, stderr.String())
		}
		if spec.Artifact != "" {
			data, err := os.ReadFile(filepath.Join(spec.Root, spec.Artifact))
			if err != nil {
				return nil, fmt.Errorf("build %s: read artifact: %w", spec.Name, err)
			}
			artifact = data
		} else {
			artifact = stdout.Bytes()
		}
	} else {
		// No command: emit a synthetic artifact derived from the inputs so
		// the pipeline remains content-addressed and testable.
		artifact = []byte("module=" + spec.Name + ";source=" + in.SourceHash + "\n")
	}
	return &Result{
		Module:     in.Module,
		Artifact:   artifact,
		SourceHash: in.SourceHash,
		VersionID:  ArtifactID(artifact),
		BuiltAt:    nowTime(),
	}, nil
}
