package store

import (
	"context"
	"errors"
	"testing"
)

// A work version keeps the first stored cover; a strictly higher-ranked
// source replaces it (an author's AI cover over a reader's render), an
// equal or lower one never does; a version that does not exist takes none;
// deleting the component deletes its covers.
func TestContractContentVersionCoverStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s, prefix := b.ContentComponents, uniq("cover-")
		ctx := context.Background()
		work, other := prefix+"-work", prefix+"-other"
		for _, id := range []string{work, other} {
			c := ContentComponent{ID: id, Kind: "bnbloc.work.package/v1", Name: id, TenantID: prefix + "-t", Owner: "maya", Scope: ContentScopePersonal, Status: ContentStatusActive, CreatedAtMs: 1, UpdatedAtMs: 1}
			if err := s.CreateContentComponent(ctx, c, contentVersion(id, "1.0.0", uniqBlobKey(), 1)); err != nil {
				t.Fatal(err)
			}
		}
		cover := func(id, source, by string) ContentVersionCover {
			return ContentVersionCover{ComponentID: id, Version: "1.0.0", BlobKey: uniqBlobKey(), MimeType: "image/png", Rank: ContentCoverRank(source), CreatedBy: by, CreatedAtMs: 5}
		}
		if _, found, err := s.GetContentVersionCover(ctx, work, "1.0.0"); found || err != nil {
			t.Fatalf("no cover yet = %v %v", found, err)
		}
		first := cover(work, ContentCoverRender, "jack")
		if kept, landed, err := s.SetContentVersionCover(ctx, first); err != nil || !landed || kept != first {
			t.Fatalf("the first render = %+v %v %v", kept, landed, err)
		}
		if kept, landed, err := s.SetContentVersionCover(ctx, cover(work, ContentCoverRender, "olga")); err != nil || landed || kept != first {
			t.Fatalf("a second render = %+v %v %v (the first kept)", kept, landed, err)
		}
		ai := cover(work, ContentCoverAI, "maya")
		if kept, landed, err := s.SetContentVersionCover(ctx, ai); err != nil || !landed || kept != ai {
			t.Fatalf("the author's AI cover = %+v %v %v (replaces the render)", kept, landed, err)
		}
		if kept, landed, err := s.SetContentVersionCover(ctx, cover(work, ContentCoverRender, "jack")); err != nil || landed || kept != ai {
			t.Fatalf("a render over the AI cover = %+v %v %v", kept, landed, err)
		}
		if got, found, err := s.GetContentVersionCover(ctx, work, "1.0.0"); !found || err != nil || got != ai {
			t.Fatalf("the stored cover = %+v %v %v", got, found, err)
		}
		if _, _, err := s.SetContentVersionCover(ctx, ContentVersionCover{ComponentID: work, Version: "9.9.9", BlobKey: uniqBlobKey(), MimeType: "image/png", Rank: 1}); !errors.Is(err, ErrContentNotFound) {
			t.Fatalf("a cover of a missing version = %v", err)
		}
		if _, _, err := s.SetContentVersionCover(ctx, ContentVersionCover{ComponentID: prefix + "-gone", Version: "1.0.0", BlobKey: uniqBlobKey(), MimeType: "image/png", Rank: 1}); !errors.Is(err, ErrContentNotFound) {
			t.Fatalf("a cover of a missing component = %v", err)
		}
		kept := cover(other, ContentCoverRender, "jack")
		if _, landed, err := s.SetContentVersionCover(ctx, kept); err != nil || !landed {
			t.Fatalf("another work's cover = %v %v", landed, err)
		}
		if err := s.DeleteContentComponent(ctx, work); err != nil {
			t.Fatal(err)
		}
		if _, found, err := s.GetContentVersionCover(ctx, work, "1.0.0"); found || err != nil {
			t.Fatalf("a deleted work's cover = %v %v", found, err)
		}
		if got, found, err := s.GetContentVersionCover(ctx, other, "1.0.0"); !found || err != nil || got != kept {
			t.Fatalf("another work's cover after the delete = %+v %v %v", got, found, err)
		}
	})
}

func TestContentCoverRank(t *testing.T) {
	if !(ContentCoverRank(ContentCoverRender) > 0 && ContentCoverRank(ContentCoverAI) > ContentCoverRank(ContentCoverRender)) || ContentCoverRank("previewer") != 0 {
		t.Fatal("ranks: render < ai, an unknown source 0")
	}
}
