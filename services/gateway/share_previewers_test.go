package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Previewers and covers on a share link
// (docs/prd/2026-10-27-shared-file-viewers.md): the in-process stack of the
// share tests, with the component pipeline switched on.

// componentAdmin turns the gateway's component pipeline on (unsigned installs,
// the install store, a secret box) and returns a platform admin's session.
func (e *appGW) componentAdmin() string {
	e.t.Helper()
	e.gw.PlatformSettings = store.NewMemoryPlatformSettings()
	e.gw.AllowExtUnsigned = true
	e.gw.SecretBox = testSecretBox(e.t)
	e.gw.ComponentInstalls = store.NewMemoryComponentInstall()
	// The pipeline reads the unsigned switch when it is built, and the test
	// gateway built it already.
	e.gw.ExtPipe = NewCorePipeline(e.gw, e.gw.ExtAudit)
	return e.gw.Signer.Issue(auth.Claims{Subject: "admin", Tenant: e.tenant, Role: string(rbac.RoleSuperAdmin), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

// installPreviewerAt installs a previewer claiming the given extensions, with
// a rollout, through the platform's own install route.
func (e *appGW) installPreviewerAt(admin, name, rollout string, extensions ...string) {
	e.t.Helper()
	bundle := uploadComponentArtifact(e.t, e.srv.URL, admin, previewerBundleBytes(e.t, testPreviewerFiles))
	body := previewerPackageBodyWith(e.t, name, "1.0.0", bundle, func(p map[string]any) {
		p["match"] = map[string]any{"extensions": extensions}
	})
	if raw, status := postComponentWithRollout(e.t, e.srv.URL, admin, body, "", rollout); status != http.StatusOK {
		e.t.Fatalf("install %s = %d %s", name, status, raw)
	}
}

// sharedFile is a file artifact of the room with one inline version.
func (e *appGW) sharedFile(fileName, mimeType string) string {
	e.t.Helper()
	id := ids.New("art_")
	if _, err := e.b.Artifacts.PutArtifact(e.ctx(), store.Artifact{ArtifactID: id, RoomID: e.roomID, Kind: store.ArtifactKindFile, Title: fileName, FileName: fileName, CreatedBy: "maya"}); err != nil {
		e.t.Fatal(err)
	}
	e.publishVersion(id, mimeType, "content", 0)
	return id
}

func (e *appGW) publishVersion(id, mimeType, content string, base int) {
	e.t.Helper()
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), id, store.ArtifactVersion{Storage: store.ArtifactStorageInline, Content: content,
		MimeType: mimeType, SizeBytes: int64(len(content)), AuthorUserID: "maya", CreatedAtMs: 1}, base); err != nil {
		e.t.Fatal(err)
	}
}

// coverFor stores bytes as a version's cover of the given media type.
func (e *appGW) coverFor(id string, version int, mimeType string, image []byte) {
	e.t.Helper()
	key, _, _, err := e.blobs.Put(e.ctx(), bytes.NewReader(image))
	if err != nil {
		e.t.Fatal(err)
	}
	if _, _, err := e.b.Artifacts.SetArtifactThumbnail(e.ctx(), id, version, store.ArtifactThumbnailWrite{BlobKey: key, MimeType: mimeType, Width: 4, Height: 4,
		Rank: store.ArtifactThumbnailRank(store.ArtifactThumbnailPreviewer)}); err != nil {
		e.t.Fatal(err)
	}
}

// pinnedShare is an anyone link to one version.
func (e *appGW) pinnedShare(id string, version int) string {
	e.t.Helper()
	r := e.roomCall("maya", http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+id+"/shares", wire.ShareCreateRequest{Access: wire.ShareAccessAnyone, Version: version})
	var out struct {
		Token string `json:"token"`
	}
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &out) != nil || out.Token == "" {
		e.t.Fatalf("pinned share = %d %s", r.status, r.body)
	}
	return out.Token
}

// shareList is what the previewers route answered, entries by name.
type shareList struct {
	status  int
	raw     []byte
	entries []map[string]any
}

func (e *appGW) sharePreviewers(cred string, headers ...string) shareList {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/public/shares/"+cred+"/previewers", nil, headers...)
	out := shareList{status: r.status, raw: r.body}
	var body struct {
		Components []map[string]any `json:"components"`
	}
	if r.status == http.StatusOK {
		if err := json.Unmarshal(r.body, &body); err != nil || body.Components == nil {
			e.t.Fatalf("previewers answer %s (%v)", r.body, err)
		}
		out.entries = body.Components
	}
	return out
}

func (l shareList) names() []string {
	var out []string
	for _, c := range l.entries {
		out = append(out, c["name"].(string))
	}
	return out
}

func entryKeys(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

var pngBytes = []byte("\x89PNG\r\n\x1a\nfake-cover")

// An anyone link answers with no session: the previewers that claim the file,
// in the minimal shape, with a frame the UNCHANGED frame route serves to an
// anonymous browser; a file nothing claims gets an empty list; no view counted.
func TestSharePreviewers_anonymousRecipientGetsTheCreatorsViewer(t *testing.T) {
	e := newAppGW(t)
	admin := e.componentAdmin()
	e.installPreviewerAt(admin, "sprites", "auto", ".atlas.json")
	file := e.sharedFile("ship.atlas.json", "application/json")
	tok, id := e.shareWith(file, wire.ShareAccessAnyone)

	got := e.sharePreviewers(tok)
	if got.status != http.StatusOK || len(got.entries) != 1 || got.entries[0]["name"] != "sprites" {
		t.Fatalf("list = %d %s", got.status, got.raw)
	}
	entry := got.entries[0]
	if keys := entryKeys(entry); len(keys) != 6 || !keys["kind"] || !keys["name"] || !keys["version"] || !keys["description"] || !keys["installed"] || !keys["previewer"] {
		t.Fatalf("entry keys = %v, want kind, name, version, description, installed, previewer only", keys)
	}
	if entry["kind"] != "previewer" || entry["installed"] != true || entry["version"] != "1.0.0" {
		t.Fatalf("entry = %v", entry)
	}
	block := entry["previewer"].(map[string]any)
	allowed := map[string]bool{"entry": true, "match": true, "rank": true, "capabilities": true, "toolbar": true, "draft": true, "bundleUrl": true, "bundleSha256": true, "frameUrl": true, "frameExpiresAtMs": true}
	for k := range block {
		if !allowed[k] {
			t.Errorf("previewer block leaks %q", k)
		}
	}
	if block["bundleUrl"] != "" || block["bundleSha256"] == "" {
		t.Fatalf("bundleUrl %q bundleSha256 %q: the desktop's address stays out, the hash the parser needs stays in", block["bundleUrl"], block["bundleSha256"])
	}
	if !bytes.Contains(got.raw, []byte(`"components"`)) || bytes.Contains(got.raw, []byte("Example Co")) {
		t.Fatalf("the publisher leaked: %s", got.raw)
	}
	for _, k := range []string{"permissions", "settings", "artifacts", "rollout", "managed", "memberInstalls", "endpoint", "sidecar", "hooks", "publisher"} {
		if _, ok := entry[k]; ok {
			t.Errorf("entry carries %q", k)
		}
	}

	// The frame base works for a browser with no session, through the route
	// the member catalog already uses.
	frame := block["frameUrl"].(string)
	if !strings.HasPrefix(frame, "/v1/previewers/sprites/1.0.0/") {
		t.Fatalf("frameUrl = %q", frame)
	}
	r := e.call(http.MethodGet, frame+block["entry"].(string), nil)
	if r.status != http.StatusOK || string(r.body) != testPreviewerFiles["index.html"] || !strings.Contains(r.header.Get("Content-Security-Policy"), "connect-src 'none'") {
		t.Fatalf("anonymous frame entry = %d %q csp %q", r.status, r.body, r.header.Get("Content-Security-Policy"))
	}

	// A file nothing claims: an empty list, not an error.
	other, _ := e.shareWith(e.sharedFile("notes.md", "text/markdown"), wire.ShareAccessAnyone)
	if l := e.sharePreviewers(other); l.status != http.StatusOK || len(l.entries) != 0 || !bytes.Contains(l.raw, []byte(`"components":[]`)) {
		t.Fatalf("unclaimed file = %d %s", l.status, l.raw)
	}
	if sh := e.shareRow(id); sh.ViewCount != 0 {
		t.Fatalf("the previewers route counted %d views", sh.ViewCount)
	}
	if resp := e.call(http.MethodGet, "/v1/public/shares/"+tok+"/previewers", nil); resp.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control %q", resp.header.Get("Cache-Control"))
	}
}

// An org link refuses an anonymous caller like every sibling route; a member
// of the organization, with a share ticket, gets the list; someone outside the
// organization never gets a ticket.
func TestSharePreviewers_orgLinkKeepsItsAudience(t *testing.T) {
	e := newAppGW(t)
	e.installPreviewerAt(e.componentAdmin(), "sprites", "auto", ".atlas.json")
	tok, id := e.shareWith(e.sharedFile("ship.atlas.json", "application/json"), wire.ShareAccessOrg)

	for _, path := range []string{"/previewers", "/thumbnail"} {
		if r := e.call(http.MethodGet, "/v1/public/shares/"+tok+path, nil); r.status != http.StatusUnauthorized || refusalCode(r) != wire.ShareSignInRequired {
			t.Errorf("anonymous %s = %d %s", path, r.status, r.body)
		}
	}
	ticket := e.ticket("jack", tok)
	if l := e.sharePreviewers(ticket); l.status != http.StatusOK || strings.Join(l.names(), ",") != "sprites" {
		t.Fatalf("member's list = %d %s", l.status, l.raw)
	}
	if r := e.mintTicketIn(e.sessionIn("olaf", "away", "member"), tok); r.status != http.StatusForbidden || refusalCode(r) != wire.ShareOrgOnly {
		t.Fatalf("outsider's ticket = %d %s", r.status, r.body)
	}
	if sh := e.shareRow(id); sh.ViewCount != 0 {
		t.Fatalf("counted %d views", sh.ViewCount)
	}
}

// The creator's installed state decides: an opt-in previewer the creator has
// not installed is not offered, an auto one is; installing it offers it; the
// frame route then re-checks the creator on every request.
func TestSharePreviewers_theCreatorsInstallsDecide(t *testing.T) {
	e := newAppGW(t)
	admin := e.componentAdmin()
	e.installPreviewerAt(admin, "atlas", "auto", ".atlas.json")
	e.installPreviewerAt(admin, "sprites", "optin", ".atlas.json")
	tok, _ := e.shareWith(e.sharedFile("ship.atlas.json", "application/json"), wire.ShareAccessAnyone)

	if l := e.sharePreviewers(tok); strings.Join(l.names(), ",") != "atlas" {
		t.Fatalf("before the creator opts in: %v (%s)", l.names(), l.raw)
	}
	if status := doJSON(t, e.srv, http.MethodPost, "/v1/components/sprites/install?kind=previewer", e.session("maya"), ""); status != http.StatusNoContent {
		t.Fatalf("creator opt-in = %d", status)
	}
	l := e.sharePreviewers(tok)
	if strings.Join(l.names(), ",") != "atlas,sprites" {
		t.Fatalf("after the creator opts in: %v (%s)", l.names(), l.raw)
	}
	frame := l.entries[1]["previewer"].(map[string]any)["frameUrl"].(string)
	if r := e.call(http.MethodGet, frame+"index.html", nil); r.status != http.StatusOK {
		t.Fatalf("frame while installed = %d", r.status)
	}
	// The creator takes it away: the same ticket stops working at once.
	if status := doJSON(t, e.srv, http.MethodDelete, "/v1/components/sprites/install?kind=previewer", e.session("maya"), ""); status != http.StatusNoContent {
		t.Fatalf("creator removes it = %d", status)
	}
	if r := e.call(http.MethodGet, frame+"index.html", nil); r.status != http.StatusNotFound {
		t.Fatalf("frame after the creator removed it = %d, want 404", r.status)
	}
}

// A creator who has left the organization, or an organization that is closed,
// offers nothing: the page renders as it did before.
func TestSharePreviewers_aGoneCreatorOrAClosedOrganizationOffersNothing(t *testing.T) {
	e := newAppGW(t)
	metrics := obs.NewMetrics("gateway")
	e.gw.Metrics = metrics
	e.installPreviewerAt(e.componentAdmin(), "sprites", "auto", ".atlas.json")
	tok, _ := e.shareWith(e.sharedFile("ship.atlas.json", "application/json"), wire.ShareAccessAnyone)
	if l := e.sharePreviewers(tok); len(l.entries) != 1 {
		t.Fatalf("baseline = %s", l.raw)
	}

	e.gw.TenantSuspended = func(string) bool { return true }
	if l := e.sharePreviewers(tok); l.status != http.StatusOK || len(l.entries) != 0 {
		t.Fatalf("closed organization = %d %s", l.status, l.raw)
	}
	e.gw.TenantSuspended = nil

	if err := e.b.Tenant.RemoveMember(e.ctx(), e.tenant, "maya"); err != nil {
		t.Fatal(err)
	}
	if l := e.sharePreviewers(tok); l.status != http.StatusOK || len(l.entries) != 0 {
		t.Fatalf("creator left = %d %s", l.status, l.raw)
	}

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{`outcome="listed"`, `outcome="closed"`, `outcome="creator-gone"`, "share_previewer_lists_total"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics lack %s:\n%s", want, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), tok) || strings.Contains(rec.Body.String(), "ship.atlas.json") {
		t.Fatal("a token or a file name reached the metrics")
	}
}

// The cover route: the stored image with sandboxing headers, a plain 404
// without one (and a public view that says so), the latest version's for a
// link that follows the latest and the pinned version's otherwise, nothing for
// a type the route would not serve, and the audience rules of /content.
func TestShareCover(t *testing.T) {
	e := newAppGW(t)
	file := e.sharedFile("ship.atlas.json", "application/json")
	latest, id := e.shareWith(file, wire.ShareAccessAnyone)
	pinned := e.pinnedShare(file, 1)
	view := func(cred string) map[string]any {
		r := e.call(http.MethodGet, "/v1/public/shares/"+cred, nil)
		if r.status != http.StatusOK {
			t.Fatalf("view = %d %s", r.status, r.body)
		}
		return r.json()
	}

	// No cover yet.
	if _, has := view(latest)["cover"]; has {
		t.Fatalf("a version with no cover says it has one: %v", view(latest))
	}
	r := e.call(http.MethodGet, "/v1/public/shares/"+latest+"/thumbnail", nil)
	if r.status != http.StatusNotFound || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("no cover = %d cache %q", r.status, r.header.Get("Cache-Control"))
	}
	// A 404 for a live link does not spend the guess budget.
	for i := 0; i < 3; i++ {
		e.call(http.MethodGet, "/v1/public/shares/"+latest+"/thumbnail", nil)
	}
	if e.gw.guessBlocked("share-ip:127.0.0.1") {
		t.Fatal("a cover miss on a live link spent the guess budget")
	}

	e.coverFor(file, 1, "image/png", pngBytes)
	if view(latest)["cover"] != true || view(pinned)["cover"] != true {
		t.Fatalf("cover flag: %v %v", view(latest), view(pinned))
	}
	r = e.call(http.MethodGet, "/v1/public/shares/"+latest+"/thumbnail", nil)
	if r.status != http.StatusOK || !bytes.Equal(r.body, pngBytes) {
		t.Fatalf("cover = %d %q", r.status, r.body)
	}
	for k, v := range map[string]string{
		"Content-Type":            "image/png",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "sandbox",
		"Cache-Control":           "no-store",
	} {
		if got := r.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	// Version 2 has its own cover, in another type: the link that follows the
	// latest serves it, the pinned link keeps version 1's.
	e.publishVersion(file, "application/json", "second", 1)
	webp := []byte("RIFF....WEBPfake")
	e.coverFor(file, 2, "image/webp", webp)
	if r := e.call(http.MethodGet, "/v1/public/shares/"+latest+"/thumbnail", nil); !bytes.Equal(r.body, webp) || r.header.Get("Content-Type") != "image/webp" {
		t.Fatalf("follows latest = %q %q", r.body, r.header.Get("Content-Type"))
	}
	if r := e.call(http.MethodGet, "/v1/public/shares/"+pinned+"/thumbnail", nil); !bytes.Equal(r.body, pngBytes) {
		t.Fatalf("pinned = %q", r.body)
	}

	// A stored type this route would not serve is no cover at all.
	e.publishVersion(file, "application/json", "third", 2)
	e.coverFor(file, 3, "image/svg+xml", []byte("<svg onload=alert(1)/>"))
	if _, has := view(latest)["cover"]; has {
		t.Fatal("an SVG counted as a cover")
	}
	if r := e.call(http.MethodGet, "/v1/public/shares/"+latest+"/thumbnail", nil); r.status != http.StatusNotFound {
		t.Fatalf("svg cover = %d", r.status)
	}

	// The key and type of a cover never reach the public view.
	if raw := e.call(http.MethodGet, "/v1/public/shares/"+pinned, nil).body; bytes.Contains(raw, []byte("thumbnailBlobKey")) || bytes.Contains(raw, []byte("sha256/")) {
		t.Fatalf("public view leaks the cover's location: %s", raw)
	}
	if sh := e.shareRow(id); sh.ViewCount == 0 {
		t.Fatal("the public view stopped counting views")
	}
}

// A dead or unknown credential is the same 404 as on /content, and spends the
// same guess budget.
func TestShareCover_deadLinksAreUnreachable(t *testing.T) {
	e := newAppGW(t)
	for _, path := range []string{"/previewers", "/thumbnail"} {
		if r := e.call(http.MethodGet, "/v1/public/shares/AAAAAAAAAAAAAAAAAAAAAA"+path, nil); r.status != http.StatusNotFound {
			t.Errorf("unknown link %s = %d", path, r.status)
		}
	}
}
