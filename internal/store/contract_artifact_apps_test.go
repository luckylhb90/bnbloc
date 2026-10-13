package store

import (
	"context"
	"slices"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/digest"
)

// Which deliverables are apps (artifact_apps.go): a site whose current
// version loads the app SDK, or whose data space exists, on every read,
// the Apps filter and ListApps alike, on both backends.

func publishContractSite(t *testing.T, ctx context.Context, s ArtifactStore, room, id string, base int, loadsSDK bool, atMs int64) {
	t.Helper()
	if base == 0 {
		putArtifact(t, ctx, s, Artifact{ArtifactID: id, RoomID: room, Kind: ArtifactKindSite, Title: id, CreatedBy: "maya", CreatedAtMs: atMs})
	}
	sha := digest.Of([]byte(id + string(rune('a'+base))))
	if _, _, err := s.PublishArtifactVersion(ctx, id, ArtifactVersion{Storage: ArtifactStorageBlob, BlobKey: digest.Key(sha), ContentHash: sha,
		MimeType: "application/vnd.bnbloc.site+tar", SizeBytes: 1, AuthorUserID: "maya", CreatedAtMs: atMs, LoadsAppSDK: loadsSDK}, base); err != nil {
		t.Fatalf("publish %s: %v", id, err)
	}
}

func TestContractArtifactStoreAppRule(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-apps-"))
		s := b.Artifacts
		room := newContractRoom(t, ctx, b, "maya")
		sdk, data, plain, doc := uniq("art-sdk-"), uniq("art-data-"), uniq("art-plain-"), uniq("art-doc-")
		publishContractSite(t, ctx, s, room, sdk, 0, true, 10)
		publishContractSite(t, ctx, s, room, data, 0, false, 20)
		publishContractSite(t, ctx, s, room, plain, 0, false, 30)
		putArtifact(t, ctx, s, Artifact{ArtifactID: doc, RoomID: room, Kind: ArtifactKindMarkdown, Title: "notes", CreatedBy: "maya", CreatedAtMs: 40})
		if v, err := s.GetArtifactVersion(ctx, sdk, 1); err != nil || !v.LoadsAppSDK {
			t.Fatalf("the SDK version = %+v, %v", v, err)
		}
		// data's data space exists once its key is stored.
		if _, err := b.AppDocs.EnsureAppKey(ctx, AppKey{AppID: data, RoomID: room, WrappedKey: "wrapped", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		apps := func() []string {
			t.Helper()
			list := must(s.ListArtifacts(ctx, ArtifactQuery{RoomID: room, Apps: true}))(t)
			for _, id := range []string{sdk, data, plain, doc} {
				a := must(s.GetArtifact(ctx, id))(t)
				if a.IsApp() != slices.Contains(artifactIDs(list), id) || a.View().IsApp != a.IsApp() {
					t.Fatalf("%s: get says IsApp=%v, the Apps filter %v", id, a.IsApp(), artifactIDs(list))
				}
			}
			return artifactIDs(list)
		}
		if got := apps(); !slices.Equal(got, []string{data, sdk}) {
			t.Fatalf("apps = %v, want [%s %s]", got, data, sdk)
		}
		all := must(s.ListArtifacts(ctx, ArtifactQuery{RoomID: room}))(t)
		for _, a := range all {
			if a.IsApp() != (a.ArtifactID == sdk || a.ArtifactID == data) {
				t.Fatalf("list: %s IsApp=%v", a.ArtifactID, a.IsApp())
			}
		}
		found := must(s.SearchArtifacts(ctx, []string{room}, "art-sdk-", 10))(t)
		if len(found) != 1 || !found[0].IsApp() {
			t.Fatalf("search = %+v", found)
		}
		// The data is deleted; the SDK version is followed by one without it.
		if _, err := b.AppDocs.WipeApp(ctx, AppScope{AppID: data, RoomID: room}); err != nil {
			t.Fatal(err)
		}
		publishContractSite(t, ctx, s, room, sdk, 1, false, 50)
		if got := apps(); len(got) != 0 {
			t.Fatalf("apps after the data went and the SDK left = %v", got)
		}
	})
}

// ListApps pages the apps of the named rooms only, in the ctx tenant,
// newest-updated first.
func TestContractArtifactStoreListApps(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-listapps-"))
		s := b.Artifacts
		r1, r2, r3 := newContractRoom(t, ctx, b, "maya"), newContractRoom(t, ctx, b, "maya"), newContractRoom(t, ctx, b, "jack")
		a1, a2, a3, a4, plain := uniq("art-a1-"), uniq("art-a2-"), uniq("art-a3-"), uniq("art-a4-"), uniq("art-p-")
		publishContractSite(t, ctx, s, r1, a1, 0, true, 10)
		publishContractSite(t, ctx, s, r1, a2, 0, true, 30)
		publishContractSite(t, ctx, s, r2, a3, 0, true, 20)
		publishContractSite(t, ctx, s, r3, a4, 0, true, 40)
		publishContractSite(t, ctx, s, r1, plain, 0, false, 50)
		other := tenantCtx(uniq("t-listapps-other-"))
		if got := must(s.ListApps(other, AppListQuery{RoomIDs: []string{r1, r2}}))(t); len(got) != 0 {
			t.Fatalf("another tenant's listing = %v", artifactIDs(got))
		}
		if got := must(s.ListApps(ctx, AppListQuery{}))(t); got == nil || len(got) != 0 {
			t.Fatalf("no rooms = %#v", got)
		}
		var pages []string
		q := AppListQuery{RoomIDs: []string{r2, r1}, Limit: 2}
		for {
			page := must(s.ListApps(ctx, q))(t)
			pages = append(pages, artifactIDs(page)...)
			if len(page) < q.PageSize() {
				break
			}
			last := page[len(page)-1]
			q.BeforeUpdatedMs, q.BeforeID = last.UpdatedAtMs, last.ArtifactID
		}
		if !slices.Equal(pages, []string{a2, a3, a1}) {
			t.Fatalf("pages = %v, want [%s %s %s]", pages, a2, a3, a1)
		}
	})
}
