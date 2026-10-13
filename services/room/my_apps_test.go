package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

// countingBlobs counts reads of realtime's blob store.
type countingBlobs struct {
	realtimehttp.BlobStore
	gets *atomic.Int64
}

func (c countingBlobs) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	c.gets.Add(1)
	return c.BlobStore.Get(ctx, key)
}

func siteBundle(t *testing.T, index string) []byte {
	t.Helper()
	raw, err := tarbundle.PackFiles(map[string][]byte{"index.html": []byte(index), "app.css": []byte("main{}")}, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Whether a site loads the app SDK is decided once per version, while its
// bytes are stored: listings, details and a member's apps read the stored
// answer and never the bundle. A later version that drops the SDK is no
// longer an app (the current version's answer counts).
func TestSiteAppSDKDetectedOncePerVersion(t *testing.T) {
	e := filesRoom(t)
	poll := e.createArtifact("alice", map[string]any{"kind": "site", "title": "Friday poll"})
	page := e.createArtifact("alice", map[string]any{"kind": "site", "title": "Landing page"})
	sdk := siteBundle(t, `<!doctype html><script src="~app/sdk.js"></script><main></main>`)
	plain := siteBundle(t, `<!doctype html><p>hello</p>`)
	for id, bundle := range map[string][]byte{poll.ArtifactID: sdk, page.ArtifactID: plain} {
		if code, rep := e.postFile("alice", id, map[string]string{"baseVersion": "0"}, "site.tar", tarbundle.SiteMIME, bundle); code != http.StatusOK {
			t.Fatalf("publish %s = %d %s", id, code, rep.Error)
		}
	}
	gets := &atomic.Int64{}
	e.rt.Blobs = countingBlobs{e.rt.Blobs, gets}
	isApp := func() map[string]bool {
		t.Helper()
		resp := e.get("/v1/rooms/files/artifacts", "bob")
		defer resp.Body.Close()
		var list wire.ArtifactList
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, a := range list.Artifacts {
			out[a.ArtifactID] = a.IsApp
		}
		return out
	}
	for range 3 {
		if got := isApp(); !got[poll.ArtifactID] || got[page.ArtifactID] {
			t.Fatalf("isApp = %v, want the poll only", got)
		}
	}
	resp := e.get("/v1/rooms/files/artifacts?app=1", "bob")
	var apps wire.ArtifactList
	_ = json.NewDecoder(resp.Body).Decode(&apps)
	resp.Body.Close()
	if len(apps.Artifacts) != 1 || apps.Artifacts[0].ArtifactID != poll.ArtifactID {
		t.Fatalf("the Apps filter = %+v", apps.Artifacts)
	}
	resp = e.get("/v1/my/apps", "alice")
	var mine wire.MyAppList
	_ = json.NewDecoder(resp.Body).Decode(&mine)
	resp.Body.Close()
	if len(mine.Apps) != 1 || mine.Apps[0].AppID != poll.ArtifactID || mine.Apps[0].RoomName != "Files" {
		t.Fatalf("my apps = %+v", mine)
	}
	if n := gets.Load(); n != 0 {
		t.Fatalf("listing read %d bundles back: the detection is the version's, stored once", n)
	}
	// A new version without the SDK: the poll is no app any more.
	if code, rep := e.postFile("alice", poll.ArtifactID, map[string]string{"baseVersion": "1"}, "site.tar", tarbundle.SiteMIME, plain); code != http.StatusOK {
		t.Fatalf("publish v2 = %d %s", code, rep.Error)
	}
	if got := isApp(); got[poll.ArtifactID] {
		t.Fatalf("isApp after a version without the SDK = %v", got)
	}
}

// myAppsAs lists user's apps in tenantID through the signed route.
func (e *appEnv) myAppsAs(user, tenantID, query string) (int, wire.MyAppList) {
	e.t.Helper()
	k := svcauth.Key(appTestKey)
	req, err := k.NewRequest(context.Background(), http.MethodGet, e.srv.URL+"/v1/my/apps"+query, nil, user, tenantID)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out wire.MyAppList
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// publishSite publishes a site version in ctx's tenant (loadsSDK: its
// bundle loads the app SDK) and returns the artifact id.
func (e *appEnv) publishSite(ctx context.Context, room, title string, loadsSDK bool, job string, atMs int64) string {
	e.t.Helper()
	a, err := e.b.Artifacts.PutArtifact(ctx, store.Artifact{ArtifactID: ids.New("art_"), RoomID: room, Kind: store.ArtifactKindSite, Title: title, CreatedBy: "maya", CreatedAtMs: atMs})
	if err != nil {
		e.t.Fatal(err)
	}
	sha := digest.Of([]byte(a.ArtifactID))
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(ctx, a.ArtifactID, store.ArtifactVersion{Storage: store.ArtifactStorageBlob, BlobKey: digest.Key(sha),
		ContentHash: sha, MimeType: tarbundle.SiteMIME, SizeBytes: 10, AuthorUserID: "maya", SourceJobID: job, CreatedAtMs: atMs, LoadsAppSDK: loadsSDK}, 0); err != nil {
		e.t.Fatal(err)
	}
	return a.ArtifactID
}

// GET /v1/my/apps lists the apps of the rooms where the caller is an owner
// (a member who may chat), in the signed tenant only, newest-updated first
// and paged; each with its room, producer, documents and working links.
func TestMyAppsListsOwnedRoomsInTheTenant(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		e.svc.Runs = b.AgentRuns()
		ctx := e.ctx()
		job := ids.New("job-")
		if _, err := b.BridgeJobs.PutAgentJob(ctx, store.AgentJob{JobID: job, RoomID: e.room, MessageSeq: 1, RequesterUserID: "maya",
			OwnerUserID: "maya", Core: "acme", Provider: "codex", Status: store.AgentJobReplied, CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		// e.room: maya owner, jack member, vic visitor. The SDK poll (an
		// agent's), the data app (e.app, a person's: it holds a document),
		// a plain site and a document, which are no apps.
		poll := e.publishSite(ctx, e.room, "Friday poll", true, job, 300)
		e.insert(maya, "votes", map[string]any{"v": 1}, false)
		e.publishSite(ctx, e.room, "Landing page", false, "", 400)
		e.newArtifact(e.room, store.ArtifactKindMarkdown)
		now := e.svc.Now()
		for _, sh := range []store.ArtifactShare{
			{ShareID: uniqShare(), Access: wire.ShareAccessOrg, ExpiresAtMs: now + 3_600_000},
			{ShareID: uniqShare(), Access: wire.ShareAccessAnyone, ExpiresAtMs: now - 1},
		} {
			sh.TokenHash, sh.RoomID, sh.ArtifactID, sh.CreatedBy, sh.CreatedAtMs = digest.Of([]byte(sh.ShareID)), e.room, poll, "maya", 1
			if _, err := b.Artifacts.CreateArtifactShare(ctx, sh); err != nil {
				t.Fatal(err)
			}
		}
		// A room of jack's where maya is a visitor: its app is jack's, not
		// maya's. A room of maya's in another organization: never listed in
		// this one.
		visiting := ids.New("room-")
		if err := b.Room.CreateRoomWithOwner(ctx, store.Room{RoomID: visiting, Name: "jack's", CreatedBy: "jack", CreatedAtMs: 1},
			store.Membership{RoomID: visiting, UserID: "jack", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if err := b.Room.AddMember(ctx, store.Membership{RoomID: visiting, UserID: "maya", Role: store.RoleVisitor, JoinedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		jacks := e.publishSite(ctx, visiting, "Jack's survey", true, "", 500)
		away := ids.New("t-away-")
		awayCtx := tenant.NewContext(context.Background(), away)
		awayRoom := ids.New("room-")
		if err := b.Room.CreateRoomWithOwner(awayCtx, store.Room{RoomID: awayRoom, Name: "away", CreatedBy: "maya", CreatedAtMs: 1},
			store.Membership{RoomID: awayRoom, UserID: "maya", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		awayApp := e.publishSite(awayCtx, awayRoom, "Away poll", true, "", 600)

		code, all := e.myAppsAs("maya", e.tenant, "")
		if code != http.StatusOK || len(all.Apps) != 2 || all.NextCursor != "" {
			t.Fatalf("maya's apps = %d %+v", code, all)
		}
		byID := map[string]wire.MyApp{}
		for _, a := range all.Apps {
			byID[a.AppID] = a
		}
		p, d := byID[poll], byID[e.app]
		if p.Name != "Friday poll" || p.RoomID != e.room || p.RoomName != "r" || p.Version != 1 ||
			p.Producer != (wire.AppProducer{Owner: "maya", Provider: "codex", Core: "acme"}) || p.Documents != 0 ||
			len(p.Links) != 1 || p.Links[0].Access != wire.ShareAccessOrg {
			t.Fatalf("the poll = %+v", p)
		}
		if d.AppID != e.app || d.Documents != 1 || len(d.Links) != 0 {
			t.Fatalf("the data app = %+v", d)
		}
		// Paged: one a page, the cursor carrying on to the last.
		code, first := e.myAppsAs("maya", e.tenant, "?limit=1")
		if code != http.StatusOK || len(first.Apps) != 1 || first.NextCursor == "" {
			t.Fatalf("page 1 = %d %+v", code, first)
		}
		code, second := e.myAppsAs("maya", e.tenant, "?limit=1&cursor="+url.QueryEscape(first.NextCursor))
		if code != http.StatusOK || len(second.Apps) != 1 || second.Apps[0].AppID == first.Apps[0].AppID {
			t.Fatalf("page 2 = %d %+v", code, second)
		}
		if _, last := e.myAppsAs("maya", e.tenant, "?limit=1&cursor="+url.QueryEscape(second.NextCursor)); len(last.Apps) != 0 {
			t.Fatalf("page 3 = %+v", last)
		}
		// Jack owns his room's app (and is a member of maya's room); vic, a
		// visitor, owns none; maya's other organization lists its own.
		if _, j := e.myAppsAs("jack", e.tenant, ""); len(j.Apps) != 3 || !containsApp(j, jacks) {
			t.Fatalf("jack's apps = %+v", j)
		}
		if _, v := e.myAppsAs("vic", e.tenant, ""); len(v.Apps) != 0 {
			t.Fatalf("a visitor's apps = %+v", v)
		}
		if _, a := e.myAppsAs("maya", away, ""); len(a.Apps) != 1 || a.Apps[0].AppID != awayApp {
			t.Fatalf("maya's apps in her other organization = %+v", a)
		}
		if code, _ := e.myAppsAs("maya", e.tenant, "?limit=999"); code != http.StatusBadRequest {
			t.Fatalf("an oversized page = %d", code)
		}
	})
}

// uniqShare is a fresh share id (share ids are at most 32 characters).
func uniqShare() string { return digest.Of([]byte(ids.New("sh")))[:24] }

func containsApp(l wire.MyAppList, id string) bool {
	for _, a := range l.Apps {
		if a.AppID == id {
			return true
		}
	}
	return false
}
