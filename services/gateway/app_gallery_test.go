package gateway

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The app gallery's routes (docs/prd/2026-10-13-app-gallery.md §6–§8) on a
// gateway in front of the real room service (appGW): a site published as
// an app component, its cover, favorites and "Use this app".

// galleryGW is appGW with the content components on (the backends' store),
// the room service's blob store (a copy uploads a site version) and the
// organization's settings.
func galleryGW(t *testing.T) *appGW {
	t.Helper()
	e := newAppGW(t)
	e.gw.limiter.get(1000, 1000, nil)
	e.gw.ContentComponents = e.b.ContentComponents
	e.gw.PlatformSettings = store.NewMemoryPlatformSettings()
	e.gw.TenantSettings = e.b.Tenant.GetTenant
	e.room.Blobs = realtime.NewHandoffClient(e.gw.Backends.Realtime, "room")
	return e
}

func coverPNG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 20))); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// pollSite is a real site deliverable of the room: a page, its script, a
// cover and a README.
func (e *appGW) pollSite() string {
	return e.realSite(e.roomID, map[string]string{
		"index.html": `<script src="~app/sdk.js"></script><script src="poll.js"></script>`,
		"poll.js":    "app.collection('votes')",
		"cover.png":  coverPNG(e.t),
		"README.md":  "# Lunch poll\n",
	})
}

// as is subject's session header, in their own organization (olaf's is
// "away").
func (e *appGW) as(subject string) string {
	tid := e.tenant
	if subject == "olaf" {
		tid = "away"
	}
	return "Authorization: Bearer " + e.gw.Signer.Issue(auth.Claims{Subject: subject, Tenant: tid, Role: "member", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

// publishApp publishes a site as subject's app (201 expected).
func (e *appGW) publishApp(subject, site string, in wire.AppPublishRequest) wire.ContentComponent {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+site+"/publish-app", in, e.as(subject))
	var d wire.ContentComponentDetail
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &d) != nil {
		e.t.Fatalf("publish-app as %s = %d %s", subject, r.status, r.body)
	}
	return d.Component
}

// appDocs counts an app's documents as an owner reads them (its stats).
func (e *appGW) appDocs(subject, app string) float64 {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/apps/"+app+"/stats", nil, e.as(subject))
	n, ok := r.json()["documents"].(float64)
	if r.status != http.StatusOK || !ok {
		e.t.Fatalf("stats of %s = %d %s", app, r.status, r.body)
	}
	return n
}

// A room owner publishes a site's current version as an app of theirs:
// its files, cover and README, never its data. Someone who may not post in
// the room cannot; a second publish is the app's next version.
func TestPublishAppFromSite(t *testing.T) {
	e := galleryGW(t)
	site := e.pollSite()
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/apps/"+site+"/c/votes", map[string]any{"doc": map[string]any{"ballot": "secret-ballot-B"}}, e.as("maya")); r.status != http.StatusCreated {
		t.Fatalf("a vote = %d %s", r.status, r.body)
	}

	// Who may not post in the room publishes none of its apps.
	if err := e.b.Room.AddMember(e.ctx(), store.Membership{RoomID: e.roomID, UserID: "nora", Role: store.RoleVisitor, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"nora", "olaf"} {
		if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+site+"/publish-app", wire.AppPublishRequest{Name: "lunch-poll"}, e.as(u)); r.status != http.StatusForbidden {
			t.Fatalf("publish by %s = %d %s", u, r.status, r.body)
		}
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+site+"/publish-app", wire.AppPublishRequest{Name: "lunch-poll", Category: "games"}, e.as("maya")); r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "category") {
		t.Fatalf("a bad category = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+e.doc+"/publish-app", wire.AppPublishRequest{Name: "doc"}, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("a document as an app = %d %s", r.status, r.body)
	}

	app := e.publishApp("jack", site, wire.AppPublishRequest{Name: "lunch-poll", Description: "Vote on lunch", Category: wire.AppCategoryLife, Tags: []string{"poll"}})
	if app.Kind != "app" || app.Scope != "personal" || !app.Mine || app.Category != wire.AppCategoryLife || app.DisplayName != "app" || app.LatestVersion != "1.0.0" ||
		len(app.Permissions) != 0 || app.NeedsSandbox || app.CoverURL == "" {
		t.Fatalf("the app = %+v", app)
	}
	// Its files are the site's and the manifest; the votes stay with the site.
	r := e.call(http.MethodGet, "/v1/content-components/"+app.ID+"/versions/1.0.0", nil, e.as("jack"))
	var v wire.ContentVersionDetail
	if r.status != http.StatusOK || json.Unmarshal(r.body, &v) != nil {
		t.Fatalf("version = %d %s", r.status, r.body)
	}
	var files []string
	for _, f := range v.Files {
		files = append(files, f.Path)
		if strings.Contains(f.Text, "secret-ballot") {
			t.Fatalf("the app's %s holds the site's data", f.Path)
		}
	}
	sort.Strings(files)
	if strings.Join(files, ",") != "README.md,component.json,cover.png,index.html,poll.js" {
		t.Fatalf("the app's files = %v", files)
	}
	if n := e.appDocs("maya", site); n != 1 {
		t.Fatalf("the site's own data = %v documents", n)
	}

	// The cover: the image, as its type, never sniffed.
	r = e.call(http.MethodGet, app.CoverURL, nil, e.as("jack"))
	if r.status != http.StatusOK || r.header.Get("Content-Type") != "image/png" || r.header.Get("X-Content-Type-Options") != "nosniff" || r.body[1] != 'P' {
		t.Fatalf("cover = %d %v", r.status, r.header)
	}
	if r := e.call(http.MethodGet, app.CoverURL, nil, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("someone else's personal app's cover = %d", r.status)
	}

	// A new version of the same app; someone else's app is not theirs to
	// version.
	// A new version keeps what the request leaves out.
	again := e.publishApp("jack", site, wire.AppPublishRequest{ComponentID: app.ID})
	if again.ID != app.ID || again.LatestVersion != "1.0.1" || again.Category != wire.AppCategoryLife || again.Description != "Vote on lunch" {
		t.Fatalf("the next version = %+v", again)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+site+"/publish-app", wire.AppPublishRequest{ComponentID: app.ID}, e.as("maya")); r.status != http.StatusNotFound {
		t.Fatalf("a version of someone else's app = %d %s", r.status, r.body)
	}
}

// The agent publishes an app with the room tools' save_component: kind app
// and the site's artifact id, as the job's requester.
func TestAgentPublishesAppThroughRoomTools(t *testing.T) {
	e := galleryGW(t)
	site := e.pollSite()
	e.runJob("job-1", e.roomID)
	r := e.roomToolAs(e.roomID, "job-1", "save_component", map[string]any{"artifactId": site, "name": "lunch-poll", "category": wire.AppCategoryLife})
	if r.Err != "" {
		t.Fatalf("save_component = %+v", r)
	}
	out, _ := r.Value.(map[string]any)
	if out["kind"] != "app" || out["name"] != "lunch-poll" || out["version"] != "1.0.0" {
		t.Fatalf("save_component = %v", out)
	}
	comps, err := e.b.ContentComponents.ListContentComponents(e.ctx(), store.ContentComponentQuery{Owner: "maya", Kind: "bnbloc.app.package/v1"})
	if err != nil || len(comps) != 1 || comps[0].Scope != store.ContentScopePersonal || comps[0].TenantID != e.tenant {
		t.Fatalf("maya's apps = %+v, %v (saved for the job's requester)", comps, err)
	}
	if r := e.roomToolAs(e.roomID, "job-1", "save_component", map[string]any{"artifactId": site, "id": comps[0].ID}); r.Err != "" || r.Value.(map[string]any)["version"] != "1.0.1" {
		t.Fatalf("a new version = %+v", r)
	}
	if r := e.roomToolAs(e.roomID, "job-1", "publish_component", map[string]any{"id": comps[0].ID, "scope": "org"}); r.Err != "" {
		t.Fatalf("publish_component = %+v", r)
	}
}

// "Use this app": a copy of a visible app becomes a new site in a room the
// caller owns, or in a new room, with a data space of its own that starts
// empty; the copy counts as a use. An invisible app and a room the caller
// does not own are refused.
func TestUseApp(t *testing.T) {
	e := galleryGW(t)
	site := e.pollSite()
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/apps/"+site+"/c/votes", map[string]any{"doc": map[string]any{"ballot": "B"}}, e.as("maya")); r.status != http.StatusCreated {
		t.Fatalf("a vote = %d %s", r.status, r.body)
	}
	app := e.publishApp("maya", site, wire.AppPublishRequest{Name: "lunch-poll", DisplayName: "Lunch poll"})

	// maya's personal app is hers alone.
	if r := e.call(http.MethodPost, "/v1/content-components/"+app.ID+"/use-app", wire.AppUseRequest{RoomID: e.roomID}, e.as("jack")); r.status != http.StatusNotFound {
		t.Fatalf("someone else's personal app = %d %s", r.status, r.body)
	}
	// Her organization publishes without approval: it is everyone's there.
	none := wire.PublishApprovalNone
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{PublishApproval: &none}); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+app.ID+"/publish", wire.ContentPublishInput{Scope: "org"}, e.as("maya")); r.status != http.StatusCreated {
		t.Fatalf("publish = %d %s", r.status, r.body)
	}
	org := e.libraryApp("jack", "lunch-poll")
	if org == nil || org.Scope != "org" {
		t.Fatalf("jack's gallery has no org app: %+v", org)
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/use-app", wire.AppUseRequest{RoomID: e.roomID}, e.as("olaf")); r.status != http.StatusNotFound {
		t.Fatalf("another organization's app = %d %s", r.status, r.body)
	}

	// A room jack does not own: he is a visitor there.
	other := e.newRoom("maya")
	if err := e.b.Room.AddMember(e.ctx(), store.Membership{RoomID: other, UserID: "jack", Role: store.RoleVisitor, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	for _, room := range []string{other, e.newRoom("maya")} {
		if r := e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/use-app", wire.AppUseRequest{RoomID: room}, e.as("jack")); r.status != http.StatusForbidden {
			t.Fatalf("a room jack does not own = %d %s", r.status, r.body)
		}
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/use-app", wire.AppUseRequest{}, e.as("jack")); r.status != http.StatusBadRequest {
		t.Fatalf("no room = %d %s", r.status, r.body)
	}

	// Into his own room: a new site, its own empty data space.
	r := e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/use-app", wire.AppUseRequest{RoomID: e.roomID}, e.as("jack"))
	var used wire.AppUseReply
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &used) != nil {
		t.Fatalf("use-app = %d %s", r.status, r.body)
	}
	if used.RoomID != e.roomID || used.ArtifactID == "" || used.ArtifactID == site || used.Version != 1 || used.From != "lunch-poll@1.0.0" || used.CreatedRoom {
		t.Fatalf("use-app reply = %+v", used)
	}
	if n := e.appDocs("jack", used.ArtifactID); n != 0 {
		t.Fatalf("the copy's data space holds %v documents", n)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/apps/"+used.ArtifactID+"/c/votes", map[string]any{"doc": map[string]any{"ballot": "A"}}, e.as("jack")); r.status != http.StatusCreated {
		t.Fatalf("a vote in the copy = %d %s", r.status, r.body)
	}
	if a, b := e.appDocs("maya", site), e.appDocs("jack", used.ArtifactID); a != 1 || b != 1 {
		t.Fatalf("documents: original %v, copy %v (each its own)", a, b)
	}
	rv := e.roomCall("jack", http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+used.ArtifactID+"/versions/1", nil)
	var ver wire.ArtifactVersionReply
	if rv.status != http.StatusOK || json.Unmarshal(rv.body, &ver) != nil || ver.Version.Summary != "from lunch-poll@1.0.0" || ver.Version.AuthorUserID != "jack" {
		t.Fatalf("the copy's version = %d %s", rv.status, rv.body)
	}
	if f := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+used.ArtifactID+"/versions/1/files/poll.js", nil, e.as("jack")); f.status != http.StatusOK || string(f.body) != "app.collection('votes')" {
		t.Fatalf("the copy's script = %d %s", f.status, f.body)
	}
	if f := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/artifacts/"+used.ArtifactID+"/versions/1/files/component.json", nil, e.as("jack")); f.status != http.StatusNotFound {
		t.Fatalf("the copy carries the template's manifest = %d", f.status)
	}

	// Into a new room of his.
	r = e.call(http.MethodPost, "/v1/content-components/"+org.ID+"/use-app", wire.AppUseRequest{NewRoom: &wire.AppNewRoom{}}, e.as("jack"))
	var fresh wire.AppUseReply
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &fresh) != nil || !fresh.CreatedRoom || fresh.RoomID == e.roomID {
		t.Fatalf("use-app in a new room = %d %s", r.status, r.body)
	}
	if role, ok, err := e.b.Room.MemberRole(e.ctx(), fresh.RoomID, "jack"); err != nil || !ok || role != store.RoleOwner {
		t.Fatalf("jack in the new room = %v %v %v", role, ok, err)
	}
	room, err := e.b.Room.GetRoom(e.ctx(), fresh.RoomID)
	if err != nil || room.Name != "Lunch poll" {
		t.Fatalf("the new room = %+v, %v", room, err)
	}

	// Two copies: two uses in the organization.
	if got := e.libraryApp("jack", "lunch-poll"); got == nil || got.Uses != 2 {
		t.Fatalf("uses = %+v", got)
	}
}

// libraryApp is subject's Library entry of an app by name (nil when none).
func (e *appGW) libraryApp(subject, name string) *wire.ContentComponent {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/content-components?kind=app", nil, e.as(subject))
	var list struct {
		Components []wire.ContentComponent `json:"components"`
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &list) != nil {
		e.t.Fatalf("library of %s = %d %s", subject, r.status, r.body)
	}
	for i := range list.Components {
		if list.Components[i].Name == name {
			return &list.Components[i]
		}
	}
	return nil
}

// A member stars what they see; the listing carries their star and how
// many of their organization starred it. An invisible component cannot be
// starred; unstarring always works.
func TestContentFavorites(t *testing.T) {
	e := galleryGW(t)
	none := wire.PublishApprovalNone
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{PublishApproval: &none}); err != nil {
		t.Fatal(err)
	}
	app := e.publishApp("maya", e.pollSite(), wire.AppPublishRequest{Name: "lunch-poll"})
	if r := e.call(http.MethodPut, "/v1/content-components/"+app.ID+"/favorite", nil, e.as("jack")); r.status != http.StatusNotFound {
		t.Fatalf("a star on an invisible app = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+app.ID+"/publish", wire.ContentPublishInput{Scope: "org"}, e.as("maya")); r.status != http.StatusCreated {
		t.Fatalf("publish = %d %s", r.status, r.body)
	}
	org := e.libraryApp("jack", "lunch-poll")
	for _, u := range []string{"jack", "nora", "jack"} {
		if r := e.call(http.MethodPut, "/v1/content-components/"+org.ID+"/favorite", nil, e.as(u)); r.status != http.StatusNoContent {
			t.Fatalf("%s's star = %d %s", u, r.status, r.body)
		}
	}
	if got := e.libraryApp("jack", "lunch-poll"); !got.Favorite || got.FavoriteCount != 2 {
		t.Fatalf("jack's view = favorite %v, count %d", got.Favorite, got.FavoriteCount)
	}
	if got := e.libraryApp("maya", "lunch-poll"); got.Favorite {
		t.Fatalf("maya's view is starred: %+v", got)
	}
	if r := e.call(http.MethodDelete, "/v1/content-components/"+org.ID+"/favorite", nil, e.as("jack")); r.status != http.StatusNoContent {
		t.Fatalf("unstar = %d %s", r.status, r.body)
	}
	if got := e.libraryApp("nora", "lunch-poll"); !got.Favorite || got.FavoriteCount != 1 {
		t.Fatalf("after jack's unstar = favorite %v, count %d", got.Favorite, got.FavoriteCount)
	}
}
