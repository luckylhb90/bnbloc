package store

import (
	"context"
	"testing"
)

func actx() context.Context { return bctx() }

// CAS is the core invariant: two publishers racing means exactly one wins and
// the other is told the artifact moved on, so divergent edits never interleave.
func TestArtifactVersionCAS(t *testing.T) {
	s := NewMemoryArtifacts()
	a, err := s.PutArtifact(actx(), Artifact{ArtifactID: "a1", RoomID: "r1", Kind: ArtifactKindCode, Title: "svc", CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	_, v1, err := s.PublishArtifactVersion(actx(), "a1", ArtifactVersion{Content: "one", AuthorUserID: "alice"}, 0)
	if err != nil || v1.Version != 1 {
		t.Fatalf("v1 = %+v, %v", v1, err)
	}
	// Winner publishes v2 based on v1.
	_, v2, err := s.PublishArtifactVersion(actx(), "a1", ArtifactVersion{Content: "two-a", AuthorUserID: "bob"}, 1)
	if err != nil || v2.Version != 2 || v2.ParentVersion != 1 {
		t.Fatalf("v2 = %+v, %v", v2, err)
	}
	// Loser still on base 1 is refused, and history is untouched.
	if _, _, err := s.PublishArtifactVersion(actx(), "a1", ArtifactVersion{Content: "two-b"}, 1); err != ErrArtifactVersionConflict {
		t.Fatalf("stale publish err = %v, want conflict", err)
	}
	if got, _ := s.GetArtifactVersion(actx(), "a1", 2); got.Content != "two-a" {
		t.Fatalf("v2 content = %q, want the winner's", got.Content)
	}
	_ = a
}

// Version metadata lists omit content: a 50-revision timeline must not move
// 100MB where the panel needs hashes and summaries only.
func TestArtifactVersionListOmitsContent(t *testing.T) {
	s := NewMemoryArtifacts()
	_, _ = s.PutArtifact(actx(), Artifact{ArtifactID: "a1", RoomID: "r1", Kind: ArtifactKindMarkdown, Title: "spec", CreatedBy: "alice"})
	_, _, _ = s.PublishArtifactVersion(actx(), "a1", ArtifactVersion{Content: "body", Summary: "s", AuthorUserID: "alice"}, 0)
	for _, v := range s.ListArtifactVersions(actx(), "a1", 10) {
		if v.Content != "" {
			t.Fatal("version list leaked content")
		}
		if v.ContentHash == "" || v.Version == 0 {
			t.Fatal("version list lost its metadata")
		}
	}
	// The content still serves per version.
	if got, _ := s.GetArtifactVersion(actx(), "a1", 1); got.Content != "body" {
		t.Fatalf("per-version fetch = %q", got.Content)
	}
}
