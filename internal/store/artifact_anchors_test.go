package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Regression (docs/bugs/2026-10-11-deliverable-cards-vanish-after-a-day.md):
// the feed re-anchored only recent artifacts, so a day-old reply lost its
// deliverable cards. Anchors are answered by message range, however old.
func testArtifactAnchors(t *testing.T, s ArtifactStore, ctx context.Context, sfx string) {
	t.Helper()
	room, other := "anchors-"+sfx, "anchors-other-"+sfx
	put := func(id, room string, seqs ...int64) {
		a, err := s.PutArtifact(ctx, Artifact{ArtifactID: id, RoomID: room, Kind: "code", Title: id, CreatedBy: "maya", CreatedAtMs: 1, UpdatedAtMs: 1})
		if err != nil {
			t.Fatal(err)
		}
		for i, seq := range seqs {
			if _, _, err := s.PublishArtifactVersion(ctx, a.ArtifactID, ArtifactVersion{Content: id, AuthorUserID: "maya", SourceSeq: seq, CreatedAtMs: 1}, i); err != nil {
				t.Fatal(err)
			}
		}
	}
	old, revised, unlinked, elsewhere := "old-"+sfx, "revised-"+sfx, "unlinked-"+sfx, "elsewhere-"+sfx
	put(old, room, 3)
	put(revised, room, 5, 9, 9)
	put(unlinked, room, 0)
	put(elsewhere, other, 5)

	got, err := s.ArtifactAnchors(ctx, room, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []ArtifactAnchor{{3, old}, {5, revised}, {9, revised}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("anchors = %+v, want %+v", got, want)
	}
	got, _ = s.ArtifactAnchors(ctx, room, 4, 5)
	if !reflect.DeepEqual(got, []ArtifactAnchor{{5, revised}}) {
		t.Fatalf("range [4,5] = %+v", got)
	}
	if got, _ := s.ArtifactAnchors(tenant.NewContext(context.Background(), "someone-else-"+sfx), room, 1, 100); len(got) != 0 {
		t.Fatalf("another tenant sees %+v", got)
	}
}

func TestMemoryArtifactAnchors(t *testing.T) {
	testArtifactAnchors(t, NewMemoryArtifacts(), actx(), "mem")
}

func TestMySQLArtifactAnchors(t *testing.T) {
	m, closeDB := mysqlArtifacts(t)
	defer closeDB()
	sfx := itoaBase(time.Now().UnixNano())
	testArtifactAnchors(t, m, tenant.NewContext(context.Background(), "t-anchors-"+sfx), sfx)
}
