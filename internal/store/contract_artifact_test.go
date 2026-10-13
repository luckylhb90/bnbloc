package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// ArtifactStore, ArtifactIndex and ArtifactShareStore contract: versions
// under compare-and-swap, typed blob versions and thumbnails, the list
// filters and paging, anchors, title search and share links behave the same
// on both backends.

func artifactIDs(list []Artifact) []string {
	out := []string{}
	for _, a := range list {
		out = append(out, a.ArtifactID)
	}
	return out
}

func putArtifact(t *testing.T, ctx context.Context, s ArtifactStore, a Artifact) Artifact {
	t.Helper()
	got, err := s.PutArtifact(ctx, a)
	if err != nil {
		t.Fatalf("put %s: %v", a.ArtifactID, err)
	}
	return got
}

// Two publishers racing on one base: exactly one wins, the loser is told
// the artifact moved on, and history is immutable. Version lists carry
// metadata only, newest first, paged with the shared size rule.
func TestContractArtifactStoreVersions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-art-"))
		s := b.Artifacts
		room, id := uniq("room-"), uniq("art-")
		a := putArtifact(t, ctx, s, Artifact{ArtifactID: id, RoomID: room, Kind: ArtifactKindCode, Title: "svc", CreatedBy: "maya", CreatedAtMs: 5})
		if a.CurrentVersion != 0 || a.UpdatedAtMs != 5 || a.TenantID != tenant.FromContext(ctx) {
			t.Fatalf("put = %+v", a)
		}
		if got := must(s.ListArtifactVersions(ctx, id, 10))(t); got == nil || len(got) != 0 {
			t.Fatalf("versions of a fresh artifact = %#v, want []", got)
		}
		_, v1, err := s.PublishArtifactVersion(ctx, id, ArtifactVersion{Content: "one", AuthorUserID: "maya", Summary: "s", CreatedAtMs: 10}, 0)
		if err != nil || v1.Version != 1 || v1.ParentVersion != 0 || v1.ContentHash != HashArtifactContent("one") || v1.SizeBytes != 3 {
			t.Fatalf("v1 = %+v, %v", v1, err)
		}
		a2, v2, err := s.PublishArtifactVersion(ctx, id, ArtifactVersion{Content: "two-a", AuthorUserID: "jack", CreatedAtMs: 20}, 1)
		if err != nil || v2.Version != 2 || v2.ParentVersion != 1 || a2.CurrentVersion != 2 || a2.UpdatedAtMs != 20 {
			t.Fatalf("v2 = %+v %+v, %v", a2, v2, err)
		}
		cur, _, err := s.PublishArtifactVersion(ctx, id, ArtifactVersion{Content: "two-b"}, 1)
		if !errors.Is(err, ErrArtifactVersionConflict) || cur.CurrentVersion != 2 {
			t.Fatalf("stale publish = %+v, %v; want the conflict and the current artifact", cur, err)
		}
		if got, err := s.GetArtifactVersion(ctx, id, 2); err != nil || got.Content != "two-a" || got.AuthorUserID != "jack" {
			t.Fatalf("v2 = %+v, %v: the loser's write landed", got, err)
		}
		if _, err := s.GetArtifactVersion(ctx, id, 9); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("missing version = %v", err)
		}
		if _, _, err := s.PublishArtifactVersion(ctx, uniq("nope-"), ArtifactVersion{Content: "x"}, 0); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("publish to a missing artifact = %v", err)
		}
		list := must(s.ListArtifactVersions(ctx, id, 10))(t)
		if len(list) != 2 || list[0].Version != 2 || list[1].Version != 1 {
			t.Fatalf("versions = %+v, want newest first", list)
		}
		for _, v := range list {
			if v.Content != "" || v.ContentHash == "" {
				t.Fatalf("version list entry = %+v; want metadata without content", v)
			}
		}

		// Another tenant sees none of it.
		octx := tenantCtx(uniq("t-art-other-"))
		if _, err := s.GetArtifact(octx, id); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("cross-tenant get = %v", err)
		}
		if _, err := s.GetArtifactVersion(octx, id, 1); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("cross-tenant version = %v", err)
		}
		if got := must(s.ListArtifactVersions(octx, id, 10))(t); got == nil || len(got) != 0 {
			t.Fatalf("cross-tenant versions = %#v, want []", got)
		}
		if _, _, err := s.PublishArtifactVersion(octx, id, ArtifactVersion{Content: "x"}, 2); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("cross-tenant publish = %v", err)
		}

		// Page sizes.
		many := uniq("art-many-")
		putArtifact(t, ctx, s, Artifact{ArtifactID: many, RoomID: room, Kind: ArtifactKindMarkdown, Title: "many", CreatedBy: "maya"})
		for v := 0; v < MaxArtifactVersionPage+2; v++ {
			if _, _, err := s.PublishArtifactVersion(ctx, many, ArtifactVersion{Content: "x", AuthorUserID: "maya", CreatedAtMs: int64(v + 1)}, v); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct{ limit, want int }{{0, DefaultArtifactVersionPage}, {3, 3}, {MaxArtifactVersionPage * 2, MaxArtifactVersionPage}} {
			got := must(s.ListArtifactVersions(ctx, many, c.limit))(t)
			if len(got) != c.want || got[0].Version != MaxArtifactVersionPage+2 {
				t.Fatalf("ListArtifactVersions(limit %d) = %d versions, want %d newest first", c.limit, len(got), c.want)
			}
		}
	})
}

// Typed blob versions move the artifact's MIME type, carry their source
// links, and keep the best-ranked thumbnail.
func TestContractArtifactStoreBlobsAndThumbnails(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-blob-"))
		s := b.Artifacts
		room, md, img := uniq("room-"), uniq("md-"), uniq("img-")
		putArtifact(t, ctx, s, Artifact{ArtifactID: md, RoomID: room, Kind: ArtifactKindMarkdown, Title: "Spec", FileName: "spec.md", CreatedBy: "maya", CreatedAtMs: 1000})
		putArtifact(t, ctx, s, Artifact{ArtifactID: img, RoomID: room, Kind: ArtifactKindImage, Title: "chart", FileName: "chart.png", CreatedBy: "jack", CreatedAtMs: 1001})
		if a, _, err := s.PublishArtifactVersion(ctx, md, ArtifactVersion{Content: "# s", MimeType: "text/markdown", AuthorUserID: "maya", CreatedAtMs: 2000}, 0); err != nil || a.MimeType != "text/markdown" {
			t.Fatalf("typed inline publish = %+v, %v", a, err)
		}
		if a, _, err := s.PublishArtifactVersion(ctx, md, ArtifactVersion{Content: "# t", AuthorUserID: "maya", CreatedAtMs: 2001}, 1); err != nil || a.MimeType != "text/markdown" {
			t.Fatalf("untyped publish = %+v, %v; want the MIME type kept", a, err)
		}
		hash := strings.Repeat("ab", 32)
		a, v, err := s.PublishArtifactVersion(ctx, img, ArtifactVersion{
			Storage: ArtifactStorageBlob, BlobKey: "sha256/" + hash, ContentHash: hash, SizeBytes: 1234, MimeType: "image/png",
			SourceJobID: "job-1", SourceSeq: 77, AuthorUserID: "jack", CreatedAtMs: 3000,
		}, 0)
		if err != nil || a.MimeType != "image/png" || v.Storage != ArtifactStorageBlob || v.ContentHash != hash || v.SizeBytes != 1234 {
			t.Fatalf("blob publish = %+v %+v %v", a, v, err)
		}
		got, err := s.GetArtifactVersion(ctx, img, 1)
		if err != nil || got.BlobKey != "sha256/"+hash || got.MimeType != "image/png" || got.SourceJobID != "job-1" || got.SourceSeq != 77 || got.Content != "" || got.Thumbnail != nil {
			t.Fatalf("blob version = %+v %v", got, err)
		}
		for name, bad := range map[string]ArtifactVersion{
			"blob with content":  {Storage: ArtifactStorageBlob, BlobKey: "k", ContentHash: hash, Content: "x"},
			"blob without key":   {Storage: ArtifactStorageBlob, ContentHash: hash},
			"inline with a key":  {Content: "x", BlobKey: "k"},
			"unknown storage":    {Storage: "tape"},
			"blob with bad hash": {Storage: ArtifactStorageBlob, BlobKey: "k", ContentHash: "short"},
		} {
			if _, _, err := s.PublishArtifactVersion(ctx, img, bad, 1); !errors.Is(err, ErrInvalidArtifactVersion) {
				t.Fatalf("%s = %v, want ErrInvalidArtifactVersion", name, err)
			}
		}

		// Thumbnails: the first wins, a higher rank replaces, an equal or
		// lower one does not.
		tw := ArtifactThumbnailWrite{BlobKey: "sha256/" + strings.Repeat("01", 32), MimeType: "image/png", Width: 64, Height: 48, Rank: ArtifactThumbnailRank(ArtifactThumbnailBuiltin)}
		if v, ok, err := s.SetArtifactThumbnail(ctx, img, 1, tw); err != nil || !ok || v.Thumbnail == nil || v.Thumbnail.Width != 64 {
			t.Fatalf("first thumbnail = %+v %v %v", v, ok, err)
		}
		if _, ok, _ := s.SetArtifactThumbnail(ctx, img, 1, tw); ok {
			t.Fatal("a same-rank thumbnail replaced the first")
		}
		tw.Rank, tw.Width = ArtifactThumbnailRank(ArtifactThumbnailPreviewer), 100
		if v, ok, _ := s.SetArtifactThumbnail(ctx, img, 1, tw); !ok || v.Thumbnail.Width != 100 || v.ThumbnailRank != 2 {
			t.Fatalf("previewer thumbnail = %+v %v", v, ok)
		}
		tw.Rank = 1
		if v, ok, _ := s.SetArtifactThumbnail(ctx, img, 1, tw); ok || v.Thumbnail.Width != 100 {
			t.Fatalf("a lower-rank thumbnail = %+v %v; want the stored one kept", v, ok)
		}
		if _, _, err := s.SetArtifactThumbnail(ctx, img, 9, tw); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("thumbnail of a missing version = %v", err)
		}
		if _, _, err := s.SetArtifactThumbnail(tenantCtx(uniq("t-blob-other-")), img, 1, tw); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("cross-tenant thumbnail = %v", err)
		}
		if meta := must(s.ListArtifactVersions(ctx, img, 10))(t); len(meta) != 1 || meta[0].Thumbnail == nil || meta[0].Thumbnail.Width != 100 {
			t.Fatalf("version list lost the thumbnail: %+v", meta)
		}
	})
}

// The list query, one filter at a time, then ordering, keyset paging and
// page sizes.
func TestContractArtifactStoreList(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-list-")
		ctx := tenantCtx(tid)
		s := b.Artifacts
		room, other := uniq("room-"), uniq("room-")
		md, img, svg, pct, tieA, tieB := "md-"+uniq(""), "img-"+uniq(""), "svg-"+uniq(""), "pct-"+uniq(""), "ta-"+uniq(""), "tb-"+uniq("")
		for _, a := range []Artifact{
			{ArtifactID: md, RoomID: room, ChannelID: "ch1", Kind: ArtifactKindMarkdown, Title: "Spec", FileName: "spec.md", CreatedBy: "maya", CreatedAtMs: 10},
			{ArtifactID: img, RoomID: room, Kind: ArtifactKindImage, Title: "Chart 2010", FileName: "chart.png", CreatedBy: "jack", CreatedAtMs: 20},
			{ArtifactID: svg, RoomID: room, Kind: ArtifactKindImage, Title: "Logo doneXv", FileName: "logo.svg", CreatedBy: "jack", CreatedAtMs: 30},
			{ArtifactID: pct, RoomID: room, Kind: ArtifactKindFile, Title: "100% done_v2", CreatedBy: "maya", CreatedAtMs: 40},
			{ArtifactID: tieA, RoomID: room, Kind: ArtifactKindFile, Title: "tie", CreatedBy: "maya", CreatedAtMs: 5},
			{ArtifactID: tieB, RoomID: room, Kind: ArtifactKindFile, Title: "tie", CreatedBy: "maya", CreatedAtMs: 5},
			{ArtifactID: uniq("elsewhere-"), RoomID: other, Kind: ArtifactKindMarkdown, Title: "Spec", CreatedBy: "maya", CreatedAtMs: 50},
		} {
			putArtifact(t, ctx, s, a)
		}
		publish := func(id string, v ArtifactVersion) {
			t.Helper()
			cur, err := s.GetArtifact(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.PublishArtifactVersion(ctx, id, v, cur.CurrentVersion); err != nil {
				t.Fatal(err)
			}
		}
		publish(md, ArtifactVersion{Content: "# s", MimeType: "text/markdown", CreatedAtMs: 11, SourceSeq: 7})
		publish(img, ArtifactVersion{Content: "png", MimeType: "image/png", CreatedAtMs: 21, SourceSeq: 9})
		publish(svg, ArtifactVersion{Content: "svg", MimeType: "image/svg+xml", CreatedAtMs: 31, SourceSeq: 9})
		putArtifact(t, tenantCtx(uniq("t-list-other-")), s, Artifact{ArtifactID: uniq("foreign-"), RoomID: room, Kind: ArtifactKindMarkdown, Title: "Spec", CreatedBy: "maya", CreatedAtMs: 60})

		list := func(q ArtifactQuery) []string {
			t.Helper()
			q.RoomID = room
			return artifactIDs(must(s.ListArtifacts(ctx, q))(t))
		}
		newest := []string{pct, svg, img, md, maxKey(tieA, tieB), minKey(tieA, tieB)}
		for _, c := range []struct {
			name string
			q    ArtifactQuery
			want []string
		}{
			{"all (newest updated first, then id; other rooms and tenants excluded)", ArtifactQuery{}, newest},
			{"channel", ArtifactQuery{ChannelID: "ch1"}, []string{md}},
			{"kind", ArtifactQuery{Kind: ArtifactKindImage}, []string{svg, img}},
			{"mime prefix", ArtifactQuery{MimeType: "image/*"}, []string{svg, img}},
			{"mime exact", ArtifactQuery{MimeType: "image/png"}, []string{img}},
			{"author", ArtifactQuery{Author: "jack"}, []string{svg, img}},
			{"author is exact", ArtifactQuery{Author: "Jack"}, []string{}},
			{"source seq", ArtifactQuery{SourceSeq: 9}, []string{svg, img}},
			{"source seq miss", ArtifactQuery{SourceSeq: 8}, []string{}},
			{"text in title, any case", ArtifactQuery{Text: "SPEC"}, []string{md}},
			{"text in file name", ArtifactQuery{Text: "chart.png"}, []string{img}},
			{"text % is literal", ArtifactQuery{Text: "0%"}, []string{pct}},
			{"text _ is literal", ArtifactQuery{Text: "e_v"}, []string{pct}},
			{"filters combine", ArtifactQuery{Kind: ArtifactKindImage, Author: "jack", Text: "logo"}, []string{svg}},
			{"page", ArtifactQuery{Limit: 2}, newest[:2]},
		} {
			if got := list(c.q); !slices.Equal(got, c.want) {
				t.Fatalf("%s: %v, want %v", c.name, got, c.want)
			}
		}
		// Keyset paging walks the whole list exactly once.
		var walked []string
		q := ArtifactQuery{Limit: 2}
		for i := 0; i < 10; i++ {
			q.RoomID = room
			page := must(s.ListArtifacts(ctx, q))(t)
			if len(page) == 0 {
				break
			}
			walked = append(walked, artifactIDs(page)...)
			last := page[len(page)-1]
			q.BeforeUpdatedMs, q.BeforeID = last.UpdatedAtMs, last.ArtifactID
		}
		if !slices.Equal(walked, newest) {
			t.Fatalf("keyset walk = %v, want %v", walked, newest)
		}
		if got := must(s.ListArtifacts(ctx, ArtifactQuery{RoomID: uniq("nope-")}))(t); got == nil || len(got) != 0 {
			t.Fatalf("list of an empty room = %#v, want []", got)
		}

		// Page sizes.
		bulk := uniq("room-bulk-")
		for i := 0; i < MaxArtifactListPage+3; i++ {
			putArtifact(t, ctx, s, Artifact{ArtifactID: uniq("bulk-"), RoomID: bulk, Kind: ArtifactKindFile, Title: "b", CreatedBy: "maya", CreatedAtMs: int64(i + 1)})
		}
		for _, c := range []struct{ limit, want int }{{0, DefaultArtifactListPage}, {-1, DefaultArtifactListPage}, {7, 7}, {MaxArtifactListPage * 3, MaxArtifactListPage}} {
			if got := must(s.ListArtifacts(ctx, ArtifactQuery{RoomID: bulk, Limit: c.limit}))(t); len(got) != c.want {
				t.Fatalf("ListArtifacts(limit %d) = %d, want %d", c.limit, len(got), c.want)
			}
		}
	})
}

// Anchors are answered by message range however old the version, ordered
// by (seq, artifact), one per pair.
func TestContractArtifactStoreAnchors(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-anchor-"))
		s := b.Artifacts
		room, other := uniq("room-"), uniq("room-")
		put := func(id, room string, seqs ...int64) {
			t.Helper()
			putArtifact(t, ctx, s, Artifact{ArtifactID: id, RoomID: room, Kind: "code", Title: id, CreatedBy: "maya", CreatedAtMs: 1})
			for i, seq := range seqs {
				if _, _, err := s.PublishArtifactVersion(ctx, id, ArtifactVersion{Content: id, AuthorUserID: "maya", SourceSeq: seq, CreatedAtMs: 1}, i); err != nil {
					t.Fatal(err)
				}
			}
		}
		old, revised, unlinked, elsewhere := uniq("old-"), uniq("revised-"), uniq("unlinked-"), uniq("elsewhere-")
		put(old, room, 3)
		put(revised, room, 5, 9, 9)
		put(unlinked, room, 0)
		put(elsewhere, other, 5)
		if got := must(s.ArtifactAnchors(ctx, room, 1, 100))(t); !reflect.DeepEqual(got, []ArtifactAnchor{{3, old}, {5, revised}, {9, revised}}) {
			t.Fatalf("anchors = %+v", got)
		}
		if got := must(s.ArtifactAnchors(ctx, room, 4, 5))(t); !reflect.DeepEqual(got, []ArtifactAnchor{{5, revised}}) {
			t.Fatalf("range [4,5] = %+v", got)
		}
		if got := must(s.ArtifactAnchors(ctx, room, 10, 20))(t); got == nil || len(got) != 0 {
			t.Fatalf("empty range = %#v, want []", got)
		}
		if got := must(s.ArtifactAnchors(tenantCtx(uniq("t-anchor-other-")), room, 1, 100))(t); len(got) != 0 {
			t.Fatalf("another tenant sees %+v", got)
		}
	})
}

// Title / file-name search across the caller's rooms: literal,
// case-insensitive, newest first, paged with the shared search size rule.
func TestContractArtifactIndexSearch(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-asearch-"))
		s := b.Artifacts
		r1, r2, r3 := uniq("room-"), uniq("room-"), uniq("room-")
		runbook, plan, pct, hidden, tieA, tieB := "rb-"+uniq(""), "pl-"+uniq(""), "pc-"+uniq(""), "hd-"+uniq(""), "ta-"+uniq(""), "tb-"+uniq("")
		for _, a := range []Artifact{
			{ArtifactID: runbook, RoomID: r1, Kind: "markdown", Title: "Deploy Runbook", CreatedBy: "maya", CreatedAtMs: 30},
			{ArtifactID: plan, RoomID: r2, Kind: "file", Title: "plan", FileName: "runbook-v2.pdf", CreatedBy: "maya", CreatedAtMs: 20},
			{ArtifactID: pct, RoomID: r1, Kind: "file", Title: "50% runbook", CreatedBy: "maya", CreatedAtMs: 10},
			{ArtifactID: hidden, RoomID: r3, Kind: "file", Title: "runbook", CreatedBy: "maya", CreatedAtMs: 40},
			{ArtifactID: tieA, RoomID: r1, Kind: "file", Title: "tie", CreatedBy: "maya", CreatedAtMs: 5},
			{ArtifactID: tieB, RoomID: r1, Kind: "file", Title: "tie", CreatedBy: "maya", CreatedAtMs: 5},
		} {
			putArtifact(t, ctx, s, a)
		}
		search := func(rooms []string, text string, limit int) []string {
			t.Helper()
			got, err := s.SearchArtifacts(ctx, rooms, text, limit)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatalf("search %q answered nil, want []", text)
			}
			return artifactIDs(got)
		}
		both := []string{r1, r2}
		for _, c := range []struct {
			name  string
			rooms []string
			text  string
			limit int
			want  []string
		}{
			{"title and file name, newest first, other rooms excluded", both, "RUNBOOK", 0, []string{runbook, plan, pct}},
			{"% is literal", both, "%", 0, []string{pct}},
			{"page size", both, "runbook", 1, []string{runbook}},
			{"tie broken by id", both, "tie", 0, []string{maxKey(tieA, tieB), minKey(tieA, tieB)}},
			{"no rooms", nil, "runbook", 0, []string{}},
			{"no text", both, "  ", 0, []string{}},
		} {
			if got := search(c.rooms, c.text, c.limit); !slices.Equal(got, c.want) {
				t.Fatalf("%s: %v, want %v", c.name, got, c.want)
			}
		}
		if got, _ := s.SearchArtifacts(tenantCtx(uniq("t-asearch-other-")), both, "runbook", 0); len(got) != 0 {
			t.Fatalf("another tenant finds %v", artifactIDs(got))
		}
		bulk := uniq("room-bulk-")
		for i := 0; i < MaxSearchPage+2; i++ {
			putArtifact(t, ctx, s, Artifact{ArtifactID: uniq("bulk-"), RoomID: bulk, Kind: "file", Title: "bulk", CreatedBy: "maya", CreatedAtMs: int64(i + 1)})
		}
		for _, c := range []struct{ limit, want int }{{0, DefaultSearchPage}, {MaxSearchPage * 2, MaxSearchPage}} {
			if got := search([]string{bulk}, "bulk", c.limit); len(got) != c.want {
				t.Fatalf("SearchArtifacts(limit %d) = %d, want %d", c.limit, len(got), c.want)
			}
		}
	})
}

// Share links: create/list/revoke in the context tenant, expiry at
// now == expires, revoke without a time refused, the tenant-agnostic
// resolve, view counting, and the org-admin list.
func TestContractArtifactShareStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-share-"))
		s := b.Artifacts
		room, artID := uniq("room-"), uniq("art-")
		putArtifact(t, ctx, s, Artifact{ArtifactID: artID, RoomID: room, Kind: ArtifactKindMarkdown, Title: "Spec", CreatedBy: "maya"})
		const now = int64(1_000_000_000)
		const dayMs = int64(24 * 3600 * 1000)
		mk := func(createdAt, expiresAt int64) ArtifactShare {
			t.Helper()
			id := uniq("sh-")
			sh, err := s.CreateArtifactShare(ctx, ArtifactShare{ShareID: id, TokenHash: HashArtifactContent(id), RoomID: room, ArtifactID: artID,
				CreatedBy: "maya", CreatedAtMs: createdAt, ExpiresAtMs: expiresAt, RevokedAtMs: 5, ViewCount: 9})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if sh.RevokedAtMs != 0 || sh.ViewCount != 0 || sh.TenantID != tenant.FromContext(ctx) {
				t.Fatalf("created share = %+v; a new link is unrevoked and unviewed", sh)
			}
			return sh
		}
		live := mk(now-10, now+1000)
		expiredRecent := mk(now-20, now-1000)
		mk(now-40*dayMs, now-31*dayMs) // expired before the window
		revoked := mk(now-5, now+1000)
		edge := mk(now-30, now)

		if _, err := s.CreateArtifactShare(ctx, ArtifactShare{ShareID: live.ShareID, TokenHash: "x", RoomID: room, ArtifactID: artID, CreatedAtMs: 1, ExpiresAtMs: 2}); !errors.Is(err, ErrArtifactShareExists) {
			t.Fatalf("duplicate share id = %v, want ErrArtifactShareExists", err)
		}
		if _, err := s.CreateArtifactShare(tenantCtx(uniq("t-share-other-")), ArtifactShare{ShareID: live.ShareID, TokenHash: "x", RoomID: room, ArtifactID: artID, CreatedAtMs: 1, ExpiresAtMs: 2}); !errors.Is(err, ErrArtifactShareExists) {
			t.Fatalf("another tenant re-using a share id = %v, want ErrArtifactShareExists", err)
		}
		// Live strictly before expiry.
		if !live.Active(now) || edge.Active(now) || !edge.Active(now-1) {
			t.Fatalf("Active: live=%v edge@now=%v edge@now-1=%v", live.Active(now), edge.Active(now), edge.Active(now-1))
		}

		if _, err := s.RevokeArtifactShare(ctx, revoked.ShareID, "jack", 0); !errors.Is(err, ErrArtifactShareRevokeTime) {
			t.Fatalf("revoke at 0 = %v, want ErrArtifactShareRevokeTime", err)
		}
		if got, _ := s.GetArtifactShare(ctx, revoked.ShareID); got.RevokedAtMs != 0 || !got.Active(now) {
			t.Fatalf("a refused revoke changed the link: %+v", got)
		}
		r1, err := s.RevokeArtifactShare(ctx, revoked.ShareID, "jack", now-1)
		if err != nil || r1.RevokedAtMs != now-1 || r1.RevokedBy != "jack" || r1.Active(now) {
			t.Fatalf("revoke = %+v %v", r1, err)
		}
		r2, err := s.RevokeArtifactShare(ctx, revoked.ShareID, "bea", now)
		if err != nil || r2.RevokedAtMs != now-1 || r2.RevokedBy != "jack" {
			t.Fatalf("re-revoke must keep the first revocation: %+v %v", r2, err)
		}
		if _, err := s.RevokeArtifactShare(ctx, uniq("nope-"), "jack", now); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("revoke missing = %v", err)
		}

		window := now - 30*dayMs
		var ids []string
		for _, sh := range must(s.ListArtifactShares(ctx, room, artID, window))(t) {
			ids = append(ids, sh.ShareID)
		}
		if want := []string{revoked.ShareID, live.ShareID, expiredRecent.ShareID, edge.ShareID}; !slices.Equal(ids, want) {
			t.Fatalf("list = %v, want %v (newest first, the 31-day-old link dropped)", ids, want)
		}
		if got := must(s.ListArtifactShares(ctx, room, uniq("nope-"), 0))(t); got == nil || len(got) != 0 {
			t.Fatalf("shares of an unshared artifact = %#v, want []", got)
		}

		for i := 0; i < 2; i++ {
			if err := s.CountArtifactShareView(ctx, live.ShareID, now+int64(i)); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.GetArtifactShare(ctx, live.ShareID)
		if err != nil || got.ViewCount != 2 || got.LastViewedAtMs != now+1 || got.TokenHash != HashArtifactContent(live.ShareID) {
			t.Fatalf("after views = %+v %v", got, err)
		}
		if err := s.CountArtifactShareView(ctx, uniq("nope-"), now); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("view of a missing share = %v", err)
		}

		// Another tenant cannot see, revoke or count it; resolve finds it anywhere.
		other := tenantCtx(uniq("t-share-other-"))
		if _, err := s.GetArtifactShare(other, live.ShareID); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("cross-tenant get = %v", err)
		}
		if _, err := s.RevokeArtifactShare(other, live.ShareID, "eve", now); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("cross-tenant revoke = %v", err)
		}
		if err := s.CountArtifactShareView(other, live.ShareID, now); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("cross-tenant view = %v", err)
		}
		if l := must(s.ListArtifactShares(other, room, artID, 0))(t); len(l) != 0 {
			t.Fatalf("cross-tenant list = %+v", l)
		}
		res, err := s.ResolveArtifactShare(context.Background(), live.ShareID)
		if err != nil || res.TenantID != live.TenantID || res.ViewCount != 2 {
			t.Fatalf("resolve = %+v %v", res, err)
		}
		if _, err := s.ResolveArtifactShare(context.Background(), uniq("nope-")); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("resolve unknown = %v", err)
		}
		if _, err := s.CreateArtifactShare(context.Background(), live); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("create without tenant = %v", err)
		}

		// Access: org unless the link says otherwise, an unknown mode
		// refused, changed in the link's tenant only.
		if live.Access != wire.ShareAccessOrg || must(s.GetArtifactShare(ctx, live.ShareID))(t).Access != wire.ShareAccessOrg {
			t.Fatalf("a link created without an access = %q, want org", live.Access)
		}
		anyID := uniq("sh-")
		anyone, err := s.CreateArtifactShare(ctx, ArtifactShare{ShareID: anyID, TokenHash: HashArtifactContent(anyID), RoomID: room, ArtifactID: artID,
			Access: wire.ShareAccessAnyone, CreatedBy: "maya", CreatedAtMs: now - 50*dayMs, ExpiresAtMs: now - 40*dayMs})
		if err != nil || anyone.Access != wire.ShareAccessAnyone {
			t.Fatalf("create anyone = %+v %v", anyone, err)
		}
		badID := uniq("sh-")
		if _, err := s.CreateArtifactShare(ctx, ArtifactShare{ShareID: badID, TokenHash: HashArtifactContent(badID), RoomID: room, ArtifactID: artID, Access: "public", CreatedAtMs: 1, ExpiresAtMs: 2}); !errors.Is(err, ErrArtifactShareAccess) {
			t.Fatalf("create with an unknown access = %v", err)
		}
		if got, err := s.SetArtifactShareAccess(ctx, anyID, wire.ShareAccessOrg); err != nil || got.Access != wire.ShareAccessOrg {
			t.Fatalf("set org = %+v %v", got, err)
		}
		if got := must(s.ResolveArtifactShare(context.Background(), anyID))(t); got.Access != wire.ShareAccessOrg {
			t.Fatalf("resolved access after the change = %q", got.Access)
		}
		if _, err := s.SetArtifactShareAccess(ctx, anyID, wire.ShareAccessOrg); err != nil {
			t.Fatalf("setting the stored access again = %v", err)
		}
		for _, bad := range []string{"", "public"} {
			if _, err := s.SetArtifactShareAccess(ctx, anyID, bad); !errors.Is(err, ErrArtifactShareAccess) {
				t.Fatalf("set %q = %v", bad, err)
			}
		}
		if _, err := s.SetArtifactShareAccess(other, anyID, wire.ShareAccessAnyone); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("set from another tenant = %v", err)
		}
		if _, err := s.SetArtifactShareAccess(ctx, uniq("nope-"), wire.ShareAccessAnyone); !errors.Is(err, ErrArtifactShareNotFound) {
			t.Fatalf("set unknown = %v", err)
		}

		// Org-admin list: titles, the active filter (expiry at now is not
		// active), the window, artifacts that are gone.
		all := must(s.ListTenantArtifactShares(ctx, TenantShareQuery{EndedAfterMs: window}))(t)
		if len(all) != 4 || all[0].ArtifactTitle != "Spec" || all[0].RoomID != room {
			t.Fatalf("tenant list = %+v", all)
		}
		active := must(s.ListTenantArtifactShares(ctx, TenantShareQuery{ActiveAtMs: now, EndedAfterMs: window}))(t)
		if len(active) != 1 || active[0].ShareID != live.ShareID {
			t.Fatalf("active tenant list = %+v", active)
		}
		if l := must(s.ListTenantArtifactShares(ctx, TenantShareQuery{EndedAfterMs: window, Limit: 1}))(t); len(l) != 1 || l[0].ShareID != revoked.ShareID {
			t.Fatalf("limited tenant list = %+v", l)
		}
		if l := must(s.ListTenantArtifactShares(other, TenantShareQuery{}))(t); l == nil || len(l) != 0 {
			t.Fatalf("other tenant list = %#v, want []", l)
		}
		gone := uniq("sh-")
		if _, err := s.CreateArtifactShare(ctx, ArtifactShare{ShareID: gone, TokenHash: HashArtifactContent(gone), RoomID: room, ArtifactID: uniq("art-gone-"), CreatedBy: "maya", CreatedAtMs: now, ExpiresAtMs: now + 1000}); err != nil {
			t.Fatal(err)
		}
		if l := must(s.ListTenantArtifactShares(ctx, TenantShareQuery{ActiveAtMs: now, EndedAfterMs: window}))(t); len(l) != 1 {
			t.Fatalf("a gone artifact's link is listed: %+v", l)
		}
	})
}
