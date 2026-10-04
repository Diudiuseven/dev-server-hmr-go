package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHashDirStable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world"), 0o644)
	h1, err := HashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// future mtime must not change the hash
	old := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(dir, "a.txt"), old, old)
	h2, _ := HashDir(dir)
	if h1 != h2 {
		t.Fatal("content hash must not depend on mtime")
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hell0"), 0o644)
	h3, _ := HashDir(dir)
	if h3 == h1 {
		t.Fatal("content change must change hash")
	}
}

func TestCombinedHashReactsToDeps(t *testing.T) {
	h1 := CombinedHash("src", map[string]string{"d": "v1"})
	h2 := CombinedHash("src", map[string]string{"d": "v2"})
	h3 := CombinedHash("src", map[string]string{"d": "v1"})
	if h1 == h2 {
		t.Fatal("dep version change must change hash")
	}
	if h1 != h3 {
		t.Fatal("identical inputs must hash identically")
	}
}

func TestCommandBuilderStdout(t *testing.T) {
	b := NewCommandBuilder([]Spec{{
		Name: "m", Root: t.TempDir(),
		Command: "sh", Args: []string{"-c", "echo out:$HMR_MODULE"},
	}})
	res, err := b.Build(context.Background(), Input{Module: "m", SourceHash: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Artifact) != "out:m\n" {
		t.Fatalf("artifact = %q", res.Artifact)
	}
	if res.VersionID != ArtifactID(res.Artifact) {
		t.Fatal("version id must be artifact content address")
	}
}

func TestCommandBuilderFailure(t *testing.T) {
	b := NewCommandBuilder([]Spec{{
		Name: "m", Root: t.TempDir(),
		Command: "sh", Args: []string{"-c", "echo details 1>&2; exit 7"},
	}})
	_, err := b.Build(context.Background(), Input{Module: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) && err.Error() == "" {
		t.Fatal("error must carry message")
	}
}

func TestSyntheticWhenNoCommand(t *testing.T) {
	b := NewCommandBuilder([]Spec{{Name: "m", Root: t.TempDir()}})
	r1, _ := b.Build(context.Background(), Input{Module: "m", SourceHash: "same"})
	r2, _ := b.Build(context.Background(), Input{Module: "m", SourceHash: "same"})
	if r1.VersionID != r2.VersionID {
		t.Fatal("identical source must produce identical version")
	}
}
