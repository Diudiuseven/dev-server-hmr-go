package state

import (
	"errors"
	"testing"
	"time"
)

func TestDedupeIdenticalContent(t *testing.T) {
	s := NewStore([]string{"m"})
	now := time.Now()
	if !s.BeginBuild("m", "h1", now) {
		t.Fatal("first build must proceed")
	}
	// same hash while in flight is skipped
	if s.BeginBuild("m", "h1", now) {
		t.Fatal("identical in-flight build must be skipped")
	}
	s.CommitBuild("m", &Version{Module: "m", VersionID: "v1", SourceHash: "h1"}, nil)
	// after publish, same hash is skipped (no new version for same content)
	if s.BeginBuild("m", "h1", now) {
		t.Fatal("identical published content must not rebuild")
	}
	if !s.BeginBuild("m", "h2", now) {
		t.Fatal("changed content must trigger rebuild")
	}
}

func TestFailureRetainsLastGood(t *testing.T) {
	s := NewStore([]string{"m"})
	s.BeginBuild("m", "h1", time.Now())
	s.CommitBuild("m", &Version{Module: "m", VersionID: "v1", SourceHash: "h1"}, nil)

	s.BeginBuild("m", "h2", time.Now())
	s.CommitBuild("m", nil, errors.New("boom"))

	snap := s.Snapshot("m")
	if snap.Status != StatusFailed || snap.Error != "boom" {
		t.Fatalf("expected failed/boom, got %+v", snap)
	}
	if snap.VersionID != "v1" || snap.Current == nil {
		t.Fatalf("last good version must be retained, got %+v", snap)
	}
}

func TestRecoveryClearsFailure(t *testing.T) {
	s := NewStore([]string{"m"})
	s.BeginBuild("m", "h1", time.Now())
	s.CommitBuild("m", &Version{VersionID: "v1", SourceHash: "h1"}, nil)
	s.BeginBuild("m", "h2", time.Now())
	s.CommitBuild("m", nil, errors.New("x"))
	s.BeginBuild("m", "h2", time.Now())
	s.CommitBuild("m", &Version{VersionID: "v2", SourceHash: "h2"}, nil)
	snap := s.Snapshot("m")
	if snap.Status != StatusOK || snap.VersionID != "v2" || snap.Error != "" {
		t.Fatalf("expected recovered v2, got %+v", snap)
	}
}
