package version

import (
	"testing"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
)

func TestSameContentSameVersion(t *testing.T) {
	a := []artifact.Artifact{{Path: "out.js", Content: []byte("hello")}}
	b := []artifact.Artifact{{Path: "out.js", Content: []byte("hello")}}
	deps := map[string]string{"lib": "v1"}
	h1 := Hash("app", deps, a)
	h2 := Hash("app", deps, b)
	if h1 == "" {
		t.Fatal("empty hash")
	}
	if h1 != h2 {
		t.Fatalf("identical content produced different versions: %s vs %s", h1, h2)
	}
}

func TestDifferentContentDifferentVersion(t *testing.T) {
	base := []artifact.Artifact{{Path: "out.js", Content: []byte("hello")}}
	changed := []artifact.Artifact{{Path: "out.js", Content: []byte("world")}}
	deps := map[string]string{}
	if Hash("app", deps, base) == Hash("app", deps, changed) {
		t.Fatal("different content produced the same version")
	}
}

func TestDependencyVersionChangeChangesHash(t *testing.T) {
	arts := []artifact.Artifact{{Path: "out.js", Content: []byte("x")}}
	h1 := Hash("app", map[string]string{"lib": "v1"}, arts)
	h2 := Hash("app", map[string]string{"lib": "v2"}, arts)
	if h1 == h2 {
		t.Fatal("hash must change when a dependency version changes")
	}
}

func TestMapOrderIndependent(t *testing.T) {
	arts := []artifact.Artifact{{Path: "a.js", Content: []byte("1")}}
	d1 := map[string]string{"lib1": "v1", "lib2": "v2"}
	d2 := map[string]string{"lib2": "v2", "lib1": "v1"}
	if Hash("app", d1, arts) != Hash("app", d2, arts) {
		t.Fatal("hash must not depend on map iteration order")
	}
}

func TestArtifactOrderIndependent(t *testing.T) {
	a1 := []artifact.Artifact{
		{Path: "a", Content: []byte("1")},
		{Path: "b", Content: []byte("2")},
	}
	a2 := []artifact.Artifact{
		{Path: "b", Content: []byte("2")},
		{Path: "a", Content: []byte("1")},
	}
	if Hash("app", nil, a1) != Hash("app", nil, a2) {
		t.Fatal("hash must not depend on artifact slice order")
	}
}
