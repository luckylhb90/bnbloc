package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// testArtifactFiles exercises the binary-capable artifact model against one
// ArtifactStore: typed blob versions, the artifact's MIME tracking, source
// links, thumbnail ranking, list filters + keyset paging, and file-name
// search. Ids are suffixed so the MySQL run never collides.
func testArtifactFiles(t *testing.T, s ArtifactStore, ctx context.Context, sfx string) {
	t.Helper()
	room := "room-files-" + sfx
	md, img := "md_"+sfx, "img_"+sfx
	for _, a := range []Artifact{
		{ArtifactID: md, RoomID: room, Kind: ArtifactKindMarkdown, Title: "Spec", FileName: "spec.md", CreatedBy: "alice", CreatedAtMs: 1000, UpdatedAtMs: 1000},
		{ArtifactID: img, RoomID: room, Kind: ArtifactKindImage, Title: "assets/chart.png", FileName: "chart.png", CreatedBy: "bob", CreatedAtMs: 1001, UpdatedAtMs: 1001},
	} {
		if _, err := s.PutArtifact(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.PublishArtifactVersion(ctx, md, ArtifactVersion{Content: "# s", MimeType: "text/markdown", AuthorUserID: "alice", CreatedAtMs: 2000}, 0); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	a, v, err := s.PublishArtifactVersion(ctx, img, ArtifactVersion{
		Storage: ArtifactStorageBlob, BlobKey: "sha256/" + hash, ContentHash: hash, SizeBytes: 1234, MimeType: "image/png",
		SourceJobID: "job-1", SourceSeq: 77, AuthorUserID: "bob", CreatedAtMs: 3000,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.MimeType != "image/png" || v.Storage != ArtifactStorageBlob || v.ContentHash != hash || v.SizeBytes != 1234 {
		t.Fatalf("blob publish = %+v %+v", a, v)
	}
	got, err := s.GetArtifactVersion(ctx, img, 1)
	if err != nil || got.BlobKey != "sha256/"+hash || got.MimeType != "image/png" || got.SourceJobID != "job-1" || got.SourceSeq != 77 || got.Content != "" || got.Thumbnail != nil {
		t.Fatalf("blob version = %+v %v", got, err)
	}
	inline, _ := s.GetArtifactVersion(ctx, md, 1)
	if inline.Storage != ArtifactStorageInline || inline.SizeBytes != 3 || inline.ContentHash != HashArtifactContent("# s") {
		t.Fatalf("inline version = %+v", inline)
	}
	// Contradictory storage fields are refused.
	if _, _, err := s.PublishArtifactVersion(ctx, img, ArtifactVersion{Storage: ArtifactStorageBlob, Content: "x"}, 1); err != ErrInvalidArtifactVersion {
		t.Fatalf("blob without key = %v", err)
	}

	// Thumbnails: first wins, a higher rank replaces, a lower one does not.
	tw := ArtifactThumbnailWrite{BlobKey: "sha256/" + strings.Repeat("01", 32), MimeType: "image/png", Width: 64, Height: 48, Rank: ArtifactThumbnailRank(ArtifactThumbnailBuiltin)}
	if v, ok, err := s.SetArtifactThumbnail(ctx, img, 1, tw); err != nil || !ok || v.Thumbnail == nil || v.Thumbnail.Width != 64 {
		t.Fatalf("first thumbnail = %+v %v %v", v, ok, err)
	}
	if _, ok, _ := s.SetArtifactThumbnail(ctx, img, 1, tw); ok {
		t.Fatal("same-rank thumbnail replaced the first")
	}
	tw.Rank, tw.Width = ArtifactThumbnailRank(ArtifactThumbnailPreviewer), 100
	if v, ok, _ := s.SetArtifactThumbnail(ctx, img, 1, tw); !ok || v.Thumbnail.Width != 100 || v.ThumbnailRank != 2 {
		t.Fatalf("previewer thumbnail = %+v %v", v, ok)
	}
	tw.Rank = 1
	if _, ok, _ := s.SetArtifactThumbnail(ctx, img, 1, tw); ok {
		t.Fatal("a lower-rank thumbnail replaced a higher one")
	}
	if _, _, err := s.SetArtifactThumbnail(ctx, img, 9, tw); err != ErrArtifactNotFound {
		t.Fatalf("missing version thumbnail = %v", err)
	}
	if meta := s.ListArtifactVersions(ctx, img, 10); len(meta) != 1 || meta[0].Thumbnail == nil {
		t.Fatalf("version list lost the thumbnail: %+v", meta)
	}

	list := func(q ArtifactQuery) []string {
		q.RoomID = room
		arts, err := s.ListArtifacts(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, a := range arts {
			out = append(out, a.ArtifactID)
		}
		return out
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	eq("all", list(ArtifactQuery{}), img, md)
	eq("kind", list(ArtifactQuery{Kind: ArtifactKindImage}), img)
	eq("mime prefix", list(ArtifactQuery{MimeType: "image/*"}), img)
	eq("mime exact", list(ArtifactQuery{MimeType: "text/markdown"}), md)
	eq("author", list(ArtifactQuery{Author: "alice"}), md)
	eq("sourceSeq", list(ArtifactQuery{SourceSeq: 77}), img)
	eq("sourceSeq miss", list(ArtifactQuery{SourceSeq: 78}))
	eq("text title", list(ArtifactQuery{Text: "SPEC"}), md)
	eq("text file name", list(ArtifactQuery{Text: "chart.png"}), img)
	eq("page 1", list(ArtifactQuery{Limit: 1}), img)
	eq("page 2", list(ArtifactQuery{Limit: 1, BeforeUpdatedMs: 3000, BeforeID: img}), md)

	found, err := s.SearchArtifacts(ctx, []string{room}, "chart.png", 10)
	if err != nil || len(found) != 1 || found[0].ArtifactID != img || found[0].FileName != "chart.png" {
		t.Fatalf("file-name search = %+v %v", found, err)
	}
}

func TestMemoryArtifactFiles(t *testing.T) {
	testArtifactFiles(t, NewMemoryArtifacts(), actx(), "mem")
}

func TestMySQLArtifactFiles(t *testing.T) {
	m, closeDB := mysqlArtifacts(t)
	defer closeDB()
	sfx := itoaBase(time.Now().UnixNano())
	ctx := tenant.NewContext(context.Background(), "t-files-"+sfx)
	testArtifactFiles(t, m, ctx, sfx)
}

func TestArtifactFileTyping(t *testing.T) {
	cases := []struct {
		name, mime, kind string
		head             []byte
	}{
		{"docs/spec.md", "text/markdown", ArtifactKindMarkdown, nil},
		{"index.html", "text/html", ArtifactKindHTML, nil},
		{"main.go", "text/x-go", ArtifactKindCode, nil},
		{"Makefile", "text/plain", ArtifactKindCode, nil},
		{"chart.png", "image/png", ArtifactKindImage, nil},
		{"logo.svg", "image/svg+xml", ArtifactKindImage, nil},
		{"report.pdf", "application/pdf", ArtifactKindFile, nil},
		{"blob", "image/png", ArtifactKindImage, []byte("\x89PNG\r\n\x1a\n\x00\x00")},
		{"blob2", "application/octet-stream", ArtifactKindFile, []byte{0, 1, 2, 3}},
	}
	for _, c := range cases {
		m := ArtifactMIME(c.name, c.head)
		if m != c.mime {
			t.Errorf("ArtifactMIME(%q) = %q, want %q", c.name, m, c.mime)
		}
		if k := ArtifactKindForFile(c.name, m); k != c.kind {
			t.Errorf("ArtifactKindForFile(%q) = %q, want %q", c.name, k, c.kind)
		}
	}
	if !IsInlineSafeImage("image/png; x=1") || IsInlineSafeImage("image/svg+xml") {
		t.Fatal("inline-safe image set wrong")
	}
}
