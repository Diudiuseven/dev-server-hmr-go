package compiler

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
)

// Input is everything a build of one module may need.
type Input struct {
	Module config.ModuleConfig
	// Root is the absolute workspace path.
	Root string
	// DepVersions maps dependency module name to its current version.
	DepVersions map[string]string
}

// Compiler builds a single module. Implementations must be safe for
// concurrent use on distinct modules.
type Compiler interface {
	Compile(ctx context.Context, in Input) ([]artifact.Artifact, error)
}

// Func adapts a function to the Compiler interface.
type Func func(ctx context.Context, in Input) ([]artifact.Artifact, error)

// Compile implements Compiler.
func (f Func) Compile(ctx context.Context, in Input) ([]artifact.Artifact, error) {
	return f(ctx, in)
}

// CommandCompiler runs the configured command and reads artifacts from
// the module's ArtifactDir. When no command is configured it treats the
// included source files themselves as artifacts.
type CommandCompiler struct {
	// Stdout/Stderr receive build command output when non-nil.
	Stdout *bytes.Buffer
}

// NewCommandCompiler creates the default compiler.
func NewCommandCompiler() *CommandCompiler { return &CommandCompiler{} }

// Compile implements Compiler.
func (c *CommandCompiler) Compile(ctx context.Context, in Input) ([]artifact.Artifact, error) {
	modDir := filepath.Join(in.Root, filepath.FromSlash(in.Module.Dir))
	if len(in.Module.Command) > 0 {
		if err := c.runCommand(ctx, in, modDir); err != nil {
			return nil, err
		}
		if in.Module.ArtifactDir == "" {
			return nil, nil
		}
		return readDirArtifacts(filepath.Join(modDir, filepath.FromSlash(in.Module.ArtifactDir)))
	}
	return readIncluded(modDir, in.Module.Include)
}

func (c *CommandCompiler) runCommand(ctx context.Context, in Input, modDir string) error {
	cmd := exec.CommandContext(ctx, in.Module.Command[0], in.Module.Command[1:]...)
	cmd.Dir = modDir
	env := os.Environ()
	// Expose dependency versions to the build, sorted for determinism.
	deps := make([]string, 0, len(in.DepVersions))
	for k := range in.DepVersions {
		deps = append(deps, k)
	}
	sort.Strings(deps)
	for _, dep := range deps {
		env = append(env, "DEP_VERSION_"+sanitizeEnv(dep)+"="+in.DepVersions[dep])
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("build of %q canceled: %w", in.Module.Name, ctx.Err())
		}
		return fmt.Errorf("build of %q failed: %w; output: %s", in.Module.Name, err, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	return nil
}

func sanitizeEnv(s string) string {
	return strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(s)
}

// readDirArtifacts reads every regular file below dir as artifacts,
// paths relative to dir in slash form.
func readDirArtifacts(dir string) ([]artifact.Artifact, error) {
	var arts []artifact.Artifact
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		arts = append(arts, artifact.Artifact{Path: filepath.ToSlash(rel), Content: content})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect artifacts: %w", err)
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].Path < arts[j].Path })
	return arts, nil
}

// readIncluded reads files below modDir whose slash-relative path matches
// one of the include patterns. Default pattern is "**/*" (all files).
func readIncluded(modDir string, includes []string) ([]artifact.Artifact, error) {
	if len(includes) == 0 {
		includes = []string{"**/*"}
	}
	var arts []artifact.Artifact
	err := filepath.WalkDir(modDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(modDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, pat := range includes {
			ok, merr := matchGlob(pat, rel)
			if merr != nil {
				return merr
			}
			if ok {
				content, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				arts = append(arts, artifact.Artifact{Path: rel, Content: content})
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}
	return arts, nil
}

// matchGlob supports standard path.Match patterns plus "**" segments
// matching any number of path segments.
func matchGlob(pattern, name string) (bool, error) {
	p := strings.Split(pattern, "/")
	n := strings.Split(name, "/")
	return matchSegments(p, n)
}

func matchSegments(pattern, name []string) (bool, error) {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// "**" matches zero or more segments; try every split.
			rest := pattern[1:]
			for skip := 0; skip <= len(name); skip++ {
				ok, err := matchSegments(rest, name[skip:])
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(name) == 0 {
			return false, nil
		}
		ok, err := path.Match(pattern[0], name[0])
		if err != nil {
			return false, fmt.Errorf("invalid glob %q: %w", pattern[0], err)
		}
		if !ok {
			return false, nil
		}
		pattern = pattern[1:]
		name = name[1:]
	}
	return len(name) == 0, nil
}
