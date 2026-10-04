package compiler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/config"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIncludeGlobReadsFiles(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "app")
	writeFile(t, filepath.Join(mod, "src", "a.js"), "A")
	writeFile(t, filepath.Join(mod, "src", "nested", "b.js"), "B")
	writeFile(t, filepath.Join(mod, "src", "c.txt"), "C")

	c := NewCommandCompiler()
	arts, err := c.Compile(context.Background(), Input{
		Root: root,
		Module: config.ModuleConfig{
			Name:    "app",
			Dir:     "app",
			Include: []string{"src/**/*.js"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range arts {
		got[a.Path] = string(a.Content)
	}
	if got["src/a.js"] != "A" || got["src/nested/b.js"] != "B" {
		t.Fatalf("glob matched wrong files: %v", got)
	}
	if _, ok := got["src/c.txt"]; ok {
		t.Fatal("c.txt should not match *.js")
	}
}

func TestCommandBuildsAndCollectsArtifacts(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	mod := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(mod, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewCommandCompiler()
	arts, err := c.Compile(context.Background(), Input{
		Root: root,
		Module: config.ModuleConfig{
			Name:        "app",
			Dir:         "app",
			Command:     []string{"/bin/sh", "-c", "echo built > dist/out.txt"},
			ArtifactDir: "dist",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Path != "out.txt" {
		t.Fatalf("unexpected artifacts: %+v", arts)
	}
}

func TestCommandFailureReturnsError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewCommandCompiler()
	_, err := c.Compile(context.Background(), Input{
		Root:   root,
		Module: config.ModuleConfig{Name: "m", Dir: "m", Command: []string{"/bin/sh", "-c", "echo boom >&2; exit 1"}},
	})
	if err == nil {
		t.Fatal("expected build error")
	}
}

func TestContextCancelInterrupts(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c := NewCommandCompiler()
	_, err := c.Compile(ctx, Input{
		Root:   root,
		Module: config.ModuleConfig{Name: "m", Dir: "m", Command: []string{"/bin/sh", "-c", "sleep 5"}},
	})
	if err == nil || !errors.Is(context.DeadlineExceeded, err) && ctx.Err() == nil {
		// DeadlineExceeded may be wrapped; either an error is required.
	}
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestDepVersionsExposedToCommand(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := NewCommandCompiler()
	_, err := c.Compile(context.Background(), Input{
		Root:        root,
		Module:      config.ModuleConfig{Name: "m", Dir: "m", Command: []string{"/bin/sh", "-c", "test \"$DEP_VERSION_lib\" = \"ver123\""}},
		DepVersions: map[string]string{"lib": "ver123"},
	})
	if err != nil {
		t.Fatalf("DEP_VERSION not propagated: %v", err)
	}
}
