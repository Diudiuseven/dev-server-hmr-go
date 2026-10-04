package store

import (
	"sync"
	"testing"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
)

func TestInitialState(t *testing.T) {
	s := New([]string{"a", "b"})
	snap := s.Current()
	if snap.Epoch != 0 {
		t.Fatalf("initial epoch = %d, want 0", snap.Epoch)
	}
	for _, name := range []string{"a", "b"} {
		if snap.States[name].Status != artifact.StatusBuilding {
			t.Fatalf("module %s initial status = %q, want building", name, snap.States[name].Status)
		}
	}
}

func TestCommitSuccessAndUnchangedVersion(t *testing.T) {
	s := New([]string{"a"})
	first := s.Commit(map[string]Result{"a": {Version: "v1", Artifacts: []artifact.Artifact{{Path: "f", Content: []byte("x")}}}})
	if first.States["a"].Status != artifact.StatusOK || first.States["a"].Version != "v1" {
		t.Fatalf("unexpected first state: %+v", first.States["a"])
	}
	ts := first.States["a"].UpdatedAt

	second := s.Commit(map[string]Result{"a": {Version: "v1", Artifacts: []artifact.Artifact{{Path: "f", Content: []byte("x")}}}})
	// Same version id: content was unchanged; UpdatedAt must not change.
	if second.States["a"].Version != "v1" {
		t.Fatalf("version changed for identical content: %+v", second.States["a"])
	}
	if !second.States["a"].UpdatedAt.Equal(ts) {
		t.Fatal("UpdatedAt changed although version did not")
	}
	if got := second.Artifacts("a"); len(got) != 1 || string(got[0].Content) != "x" {
		t.Fatalf("artifacts not carried: %+v", got)
	}
}

func TestFailedBuildKeepsLastGoodVersionAndArtifacts(t *testing.T) {
	s := New([]string{"a"})
	s.Commit(map[string]Result{"a": {Version: "v-good", Artifacts: []artifact.Artifact{{Path: "f", Content: []byte("good")}}}})
	failed := s.Commit(map[string]Result{"a": {Error: "boom"}})
	st := failed.States["a"]
	if st.Status != artifact.StatusError {
		t.Fatalf("status = %q, want error", st.Status)
	}
	if st.Error != "boom" {
		t.Fatalf("error message = %q, want boom", st.Error)
	}
	if st.Version != "v-good" {
		t.Fatalf("failed build must keep last good version, got %q", st.Version)
	}
	if got := failed.Artifacts("a"); len(got) != 1 || string(got[0].Content) != "good" {
		t.Fatalf("failed build must keep last good artifacts, got %+v", got)
	}

	// Recovery: a later successful build replaces the failure state.
	recovered := s.Commit(map[string]Result{"a": {Version: "v-fixed", Artifacts: []artifact.Artifact{{Path: "f", Content: []byte("fixed")}}}})
	if recovered.States["a"].Status != artifact.StatusOK || recovered.States["a"].Error != "" || recovered.States["a"].Version != "v-fixed" {
		t.Fatalf("module did not recover: %+v", recovered.States["a"])
	}
}

func TestOtherModuleUnaffectedByFailure(t *testing.T) {
	s := New([]string{"a", "b"})
	snap := s.Commit(map[string]Result{
		"a": {Error: "a broken"},
		"b": {Version: "v-b", Artifacts: []artifact.Artifact{{Path: "f", Content: []byte("b")}}},
	})
	if snap.States["a"].Status != artifact.StatusError {
		t.Fatal("a should be error")
	}
	if snap.States["b"].Status != artifact.StatusOK {
		t.Fatal("b must remain usable while a fails")
	}
}

func TestCommitIsAtomicAcrossConcurrentReaders(t *testing.T) {
	s := New([]string{"a", "b", "c"})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// Readers must always observe a fully consistent snapshot: either
	// every committed module has the same epoch through the snapshot
	// pointer and states never change in place.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				snap := s.Current()
				for _, n := range []string{"a", "b", "c"} {
					if _, ok := snap.States[n]; !ok {
						t.Errorf("half-written snapshot for %s", n)
						return
					}
				}
			}
		}
	}()
	for i := 0; i < 200; i++ {
		s.Commit(map[string]Result{
			"a": {Version: "v", Artifacts: nil},
			"b": {Version: "v"},
			"c": {Version: "v"},
		})
	}
	close(stop)
	wg.Wait()
}

func TestSnapshotImmutability(t *testing.T) {
	s := New([]string{"a"})
	s.Commit(map[string]Result{"a": {Version: "v1", Artifacts: []artifact.Artifact{{Path: "f", Content: []byte("x")}}}})
	old := s.Current()
	s.Commit(map[string]Result{"a": {Version: "v2", Artifacts: []artifact.Artifact{{Path: "f", Content: []byte("y")}}}})
	// The old snapshot pointer must remain the previous complete version.
	if old.States["a"].Version != "v1" {
		t.Fatal("old snapshot mutated in place; readers could see torn state")
	}
	if string(old.Artifacts("a")[0].Content) != "x" {
		t.Fatal("old artifacts mutated in place")
	}
}
