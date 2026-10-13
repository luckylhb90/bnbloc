package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Share links require sign-in to the room's organization by default
// (docs/prd/2026-10-13-share-link-sign-in.md).

// liveSite is a site artifact of room whose version 1 is a real snapshot
// in the blob store (its files can be served).
func (e *appGW) liveSite(room string) string {
	e.t.Helper()
	raw, err := tarbundle.PackFiles(map[string][]byte{"index.html": []byte("<!doctype html><p>app</p>")}, tarbundle.SiteLimits)
	if err != nil {
		e.t.Fatal(err)
	}
	key, size, sha, err := e.blobs.Put(context.Background(), bytes.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	id := e.artifact(room, store.ArtifactKindSite)
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), id, store.ArtifactVersion{Storage: store.ArtifactStorageBlob, BlobKey: key,
		ContentHash: sha, MimeType: tarbundle.SiteMIME, SizeBytes: size, AuthorUserID: "maya", CreatedAtMs: 1}, 0); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// mintTicket asks for subject's share ticket of the link tok.
func (e *appGW) mintTicket(subject, tok string, headers ...string) resp {
	e.t.Helper()
	return e.call(http.MethodPost, "/v1/share-tickets", wire.ShareTicketRequest{Token: tok}, append(headers, "Authorization: Bearer "+e.session(subject))...)
}

// ticket is subject's share ticket of the link tok (fails the test when
// refused).
func (e *appGW) ticket(subject, tok string) string {
	e.t.Helper()
	r := e.mintTicket(subject, tok)
	var out wire.ShareTicket
	if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil || out.Ticket == "" || out.ExpiresAtMs <= time.Now().UnixMilli() {
		e.t.Fatalf("share ticket for %s = %d %s", subject, r.status, r.body)
	}
	return out.Ticket
}

func refusalCode(r resp) string { c, _ := r.json()["code"].(string); return c }

func (e *appGW) shareRow(id string) store.ArtifactShare {
	e.t.Helper()
	sh, err := e.b.Artifacts.GetArtifactShare(e.ctx(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return sh
}

// A link is "org" unless its creator chose otherwise, and an org link
// refuses an anonymous caller on every route — metadata, content, the
// site's files, its app API and the visitor mint — whatever session or
// cookie the request carries (the share routes take none). No view is
// counted.
func TestOrgLinkRefusesAnonymousOnEveryRoute(t *testing.T) {
	e := newAppGW(t)
	app := e.liveSite(e.roomID)
	tok, id := e.shareWith(app, "")
	if sh := e.shareRow(id); sh.Access != wire.ShareAccessOrg {
		t.Fatalf("default access = %q, want org", sh.Access)
	}
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), e.doc, store.ArtifactVersion{Storage: store.ArtifactStorageInline, Content: "# Plan",
		MimeType: "text/markdown", SizeBytes: 6, AuthorUserID: "maya", CreatedAtMs: 1}, 0); err != nil {
		t.Fatal(err)
	}
	docTok, _ := e.shareWith(e.doc, "")
	base := "/v1/public/shares/" + tok
	session := "Authorization: Bearer " + e.session("jack")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, base},
		{http.MethodGet, base + "/content"},
		{http.MethodGet, base + "/content?download=1"},
		{http.MethodGet, "/v1/public/shares/" + docTok + "/content"},
		{http.MethodGet, base + "/site/"},
		{http.MethodGet, base + "/site/index.html"},
		{http.MethodGet, base + "/site/~app/me"},
		{http.MethodGet, base + "/site/~app/c/votes"},
		{http.MethodPost, base + "/site/~app/c/votes"},
		{http.MethodGet, base + "/site/~app/events"},
		{http.MethodPost, base + "/visitor"},
	} {
		for _, headers := range [][]string{nil, {session, "Cookie: bnbloc_session=x"}, {visitorHeader("garbage")}} {
			r := e.call(c.method, c.path, map[string]any{"doc": map[string]any{"v": 1}}, headers...)
			if r.status != http.StatusUnauthorized || refusalCode(r) != wire.ShareSignInRequired {
				t.Errorf("%s %s %v = %d %s, want 401 %s", c.method, c.path, headers, r.status, r.body, wire.ShareSignInRequired)
			}
		}
	}
	if sh := e.shareRow(id); sh.ViewCount != 0 {
		t.Fatalf("a refused caller counted %d views", sh.ViewCount)
	}
}

// Only a signed-in member of the link's organization gets a share ticket:
// a member of another organization is told whose link it is, a request
// without a session is refused, and a dead or garbage token is a 404.
func TestShareTicketForTheOrganizationOnly(t *testing.T) {
	e := newAppGW(t)
	tok, _ := e.shareWith(e.appA, "")
	r := e.mintTicket("olaf", tok)
	if r.status != http.StatusForbidden || refusalCode(r) != wire.ShareOrgOnly || r.json()["error"] != "This link is only for members of Home Org" {
		t.Fatalf("another organization's member = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/share-tickets", wire.ShareTicketRequest{Token: tok}); r.status != http.StatusUnauthorized {
		t.Fatalf("no session = %d %s", r.status, r.body)
	}
	for _, bad := range []string{"bsh_nope_x", "garbage", ""} {
		if r := e.mintTicket("nora", bad); r.status != http.StatusNotFound {
			t.Fatalf("token %q = %d %s", bad, r.status, r.body)
		}
	}
	// A member of the organization who later leaves it loses the link at
	// once, ticket or not.
	ticket := e.ticket("nora", tok)
	if r := e.call(http.MethodGet, "/v1/public/shares/"+ticket+"/site/~app/me", nil); r.status != http.StatusOK {
		t.Fatalf("nora's app call = %d %s", r.status, r.body)
	}
	if err := e.b.Tenant.RemoveMember(context.Background(), e.tenant, "nora"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/public/shares/" + ticket, "/v1/public/shares/" + ticket + "/site/~app/me"} {
		if r := e.call(http.MethodGet, path, nil); r.status != http.StatusForbidden || refusalCode(r) != wire.ShareOrgOnly {
			t.Fatalf("%s after leaving = %d %s", path, r.status, r.body)
		}
	}
	if r := e.mintTicket("nora", tok); r.status != http.StatusForbidden {
		t.Fatalf("a new ticket after leaving = %d %s", r.status, r.body)
	}
}

// A member of the organization who is not in the room opens an org link
// with a share ticket in the token's place: the metadata (one view), the
// site's files and the app, as a participant — their own pseudonym and
// display name, their own private documents, never an owner's rights.
func TestOrgMemberOutsideTheRoomIsAParticipant(t *testing.T) {
	e := newAppGW(t)
	app := e.liveSite(e.roomID)
	tok, id := e.shareWith(app, "")
	ticket := e.ticket("nora", tok)
	base := "/v1/public/shares/" + ticket

	r := e.call(http.MethodGet, base, nil)
	if r.status != http.StatusOK || r.json()["siteUrl"] != base+"/site/" {
		t.Fatalf("metadata = %d %s", r.status, r.body)
	}
	if sh := e.shareRow(id); sh.ViewCount != 1 {
		t.Fatalf("views = %d, want 1", sh.ViewCount)
	}
	if r := e.call(http.MethodGet, base+"/site/", nil); r.status != http.StatusOK || !strings.Contains(string(r.body), "<p>app</p>") {
		t.Fatalf("site file = %d %s", r.status, r.body)
	}
	site := base + "/site/"
	me := e.call(http.MethodGet, site+"~app/me", nil)
	if me.status != http.StatusOK || me.json()["name"] != "nora" || me.json()["id"] == "" {
		t.Fatalf("me = %d %s", me.status, me.body)
	}
	noraID := me.json()["id"].(string)

	// jack, a room member and so an owner, writes a private and a public
	// document through his own site ticket.
	jack := e.ticketBase("jack", app)
	if r := e.call(http.MethodPost, jack+"~app/c/votes", map[string]any{"doc": map[string]any{"choice": "B"}}); r.status != http.StatusCreated {
		t.Fatalf("jack's vote = %d %s", r.status, r.body)
	}
	r = e.call(http.MethodPost, jack+"~app/c/options", map[string]any{"doc": map[string]any{"name": "A"}, "public": true})
	if r.status != http.StatusCreated {
		t.Fatalf("jack's option = %d %s", r.status, r.body)
	}
	option := r.json()["_id"].(string)

	// nora: her own vote, nobody else's private one, the public option
	// without its author, and no change to a document she did not write.
	if r := e.call(http.MethodPut, site+"~app/c/votes/"+noraID, map[string]any{"doc": map[string]any{"choice": "A"}}, "If-None-Match: *"); r.status != http.StatusCreated || r.json()["_by"] != noraID {
		t.Fatalf("nora's vote = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, site+"~app/c/votes", nil); r.status != http.StatusOK || len(r.docs()) != 1 || r.docs()[0]["_id"] != noraID {
		t.Fatalf("nora's find = %d %s (her own vote only)", r.status, r.body)
	}
	if r := e.call(http.MethodGet, site+"~app/c/options/"+option, nil); r.status != http.StatusOK || r.json()["_author"] != nil {
		t.Fatalf("the public option = %d %s", r.status, r.body)
	}
	for _, m := range []string{http.MethodDelete, http.MethodPatch} {
		if r := e.call(m, site+"~app/c/options/"+option, map[string]any{"merge": map[string]any{"name": "X"}}); r.status != http.StatusForbidden {
			t.Fatalf("%s of jack's document = %d %s", m, r.status, r.body)
		}
	}
	// The owners' routes do not exist under a link.
	if r := e.call(http.MethodGet, site+"~app/stats", nil); r.status != http.StatusNotFound {
		t.Fatalf("stats under a link = %d %s", r.status, r.body)
	}
	// The owner sees nora's vote with its real author.
	r = e.call(http.MethodGet, jack+"~app/c/votes", nil)
	authors := []any{}
	for _, d := range r.docs() {
		authors = append(authors, d["_author"])
	}
	if r.status != http.StatusOK || len(r.docs()) != 2 || !strings.Contains(string(r.body), "nora") {
		t.Fatalf("the owner's find = %d %s (authors %v)", r.status, r.body, authors)
	}
	// No visitor token for a member: the ticket is the participant.
	if r := e.call(http.MethodPost, base+"/visitor", nil); r.status != http.StatusBadRequest {
		t.Fatalf("a visitor token for a ticket = %d %s", r.status, r.body)
	}
}

// A share ticket opens its own link and nothing else: it is no member
// site ticket, no session and no visitor token; a forged or tampered one,
// or one naming another organization, opens nothing.
func TestShareTicketBoundToItsShare(t *testing.T) {
	e := newAppGW(t)
	tok, id := e.shareWith(e.appA, "")
	ticket := e.ticket("nora", tok)
	if r := e.call(http.MethodGet, "/v1/public/shares/"+ticket, nil); r.status != http.StatusOK || r.json()["artifact"].(map[string]any)["kind"] != store.ArtifactKindSite {
		t.Fatalf("its own link = %d %s", r.status, r.body)
	}
	// Another link of the room: the ticket names one share id, signed.
	_, docID := e.shareWith(e.doc, "")
	tampered := []byte(ticket)
	tampered[len(tampered)/3] ^= 1
	forged := e.gw.Signer.Issue(auth.Claims{Subject: "nora", Tenant: "away", Kind: auth.KindShareTicket, ID: id, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	asSite := e.gw.Signer.Issue(auth.Claims{Subject: "nora", Tenant: e.tenant, Kind: auth.KindSiteFrame, ID: docID, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	for name, cred := range map[string]string{"tampered": string(tampered), "another organization": forged, "a site ticket": asSite} {
		if r := e.call(http.MethodGet, "/v1/public/shares/"+cred, nil); r.status != http.StatusNotFound {
			t.Fatalf("%s = %d %s", name, r.status, r.body)
		}
	}
	// Not a member site ticket, not a session, not a visitor token.
	if r := e.call(http.MethodGet, "/v1/sites/"+ticket+"/~app/me", nil); r.status != http.StatusNotFound {
		t.Fatalf("as a member site ticket = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/apps/"+e.appA+"/c/votes", nil, "Authorization: Bearer "+ticket); r.status != http.StatusUnauthorized {
		t.Fatalf("as a session on the room entry = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, "/v1/rooms", nil, "Authorization: Bearer "+ticket); r.status != http.StatusUnauthorized {
		t.Fatalf("as a session on the proxy = %d %s", r.status, r.body)
	}
	anyone, _ := e.share(e.appA)
	if r := e.call(http.MethodGet, shareBase(anyone)+"~app/me", nil, visitorHeader(ticket)); r.status != http.StatusUnauthorized || refusalCode(r) != wire.AppVisitorInvalid {
		t.Fatalf("as a visitor token = %d %s", r.status, r.body)
	}
	// Revoking the link ends the ticket.
	if r := e.roomCall("maya", http.MethodDelete, "/v1/rooms/"+e.roomID+"/artifacts/"+e.appA+"/shares/"+id, nil); r.status != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", r.status, r.body)
	}
	for _, path := range []string{"/v1/public/shares/" + ticket, "/v1/public/shares/" + ticket + "/site/~app/me"} {
		if r := e.call(http.MethodGet, path, nil); r.status != http.StatusNotFound {
			t.Fatalf("%s after the revoke = %d %s", path, r.status, r.body)
		}
	}
}

// With a sites origin, an org link's site and app are served there under
// the share ticket, and nowhere a session opens them: the sites origin
// mints no ticket and serves no share metadata, and a session or cookie on
// the link's own token opens nothing.
func TestSitesOriginNeverAcceptsASession(t *testing.T) {
	e := newAppGW(t)
	e.withSites()
	app := e.liveSite(e.roomID)
	tok, _ := e.shareWith(app, "")
	if r := e.mintTicket("nora", tok, sitesHost); r.status != http.StatusNotFound {
		t.Fatalf("ticket mint on the sites origin = %d %s", r.status, r.body)
	}
	r := e.mintTicket("nora", tok, mainHost)
	var tk wire.ShareTicket
	if r.status != http.StatusOK || json.Unmarshal(r.body, &tk) != nil {
		t.Fatalf("ticket = %d %s", r.status, r.body)
	}
	r = e.call(http.MethodGet, "/v1/public/shares/"+tk.Ticket, nil, mainHost)
	siteURL, _ := r.json()["siteUrl"].(string)
	site := sitePathOf(t, siteURL)
	for _, path := range []string{site, site + "~app/me"} {
		if r := e.call(http.MethodGet, path, nil, sitesHost); r.status != http.StatusOK {
			t.Fatalf("%s on the sites origin = %d %s", path, r.status, r.body)
		}
	}
	if r := e.call(http.MethodGet, "/v1/public/shares/"+tk.Ticket, nil, sitesHost); r.status != http.StatusNotFound {
		t.Fatalf("share metadata on the sites origin = %d %s", r.status, r.body)
	}
	session := "Authorization: Bearer " + e.session("jack")
	for _, path := range []string{"/v1/public/shares/" + tok + "/site/", "/v1/public/shares/" + tok + "/site/~app/me"} {
		r := e.call(http.MethodGet, path, nil, sitesHost, session, "Cookie: bnbloc_session="+e.session("jack"))
		if r.status != http.StatusUnauthorized || refusalCode(r) != wire.ShareSignInRequired {
			t.Fatalf("%s with a session on the sites origin = %d %s", path, r.status, r.body)
		}
	}
}

// An "anyone" link needs no sign-in until its creator switches it to
// "org": then the visitor's app calls, the visitor mint and the metadata
// refuse at once, while a signed-in member's ticket works; switched back,
// the visitor's token works again. While the organization disables
// anonymous links, an "anyone" link is dead to everyone.
func TestSwitchingAnyoneToOrgStopsAnonymousAccess(t *testing.T) {
	e := newAppGW(t)
	tok, id := e.share(e.appA)
	vis := visitorHeader(e.visitor(tok, ""))
	pub := shareBase(tok)
	if r := e.call(http.MethodGet, pub+"~app/me", nil, vis); r.status != http.StatusOK {
		t.Fatalf("anyone = %d %s", r.status, r.body)
	}
	setAccess := func(access string) {
		t.Helper()
		if r := e.roomCall("maya", http.MethodPatch, "/v1/rooms/"+e.roomID+"/artifacts/"+e.appA+"/shares/"+id, wire.ShareAccessUpdate{Access: access}); r.status != http.StatusOK {
			t.Fatalf("set %s = %d %s", access, r.status, r.body)
		}
	}
	setAccess(wire.ShareAccessOrg)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, pub + "~app/me"},
		{http.MethodPost, pub + "~app/c/votes"},
		{http.MethodPost, "/v1/public/shares/" + tok + "/visitor"},
		{http.MethodGet, "/v1/public/shares/" + tok},
	} {
		if r := e.call(c.method, c.path, map[string]any{"doc": map[string]any{}}, vis); r.status != http.StatusUnauthorized || refusalCode(r) != wire.ShareSignInRequired {
			t.Fatalf("%s %s after the switch = %d %s", c.method, c.path, r.status, r.body)
		}
	}
	ticket := e.ticket("jack", tok)
	if r := e.call(http.MethodGet, "/v1/public/shares/"+ticket+"/site/~app/me", nil); r.status != http.StatusOK || r.json()["name"] != "jack" {
		t.Fatalf("a member's ticket = %d %s", r.status, r.body)
	}
	setAccess(wire.ShareAccessAnyone)
	if r := e.call(http.MethodGet, pub+"~app/me", nil, vis); r.status != http.StatusOK {
		t.Fatalf("anyone again = %d %s", r.status, r.body)
	}
	// Anonymous links disabled: the anyone link is dead, an org link lives.
	on := true
	if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, store.TenantPolicyUpdate{AnonymousLinksDisabled: &on}); err != nil {
		t.Fatal(err)
	}
	if r := e.call(http.MethodGet, pub+"~app/me", nil, vis); r.status != http.StatusNotFound {
		t.Fatalf("anyone while disabled = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, "/v1/public/shares/"+ticket, nil); r.status != http.StatusNotFound {
		t.Fatalf("a ticket of an anyone link while disabled = %d %s", r.status, r.body)
	}
	orgTok, _ := e.shareWith(e.appA, "")
	if r := e.call(http.MethodGet, "/v1/public/shares/"+e.ticket("nora", orgTok), nil); r.status != http.StatusOK {
		t.Fatalf("an org link while disabled = %d %s", r.status, r.body)
	}
}

// TestShareAccessSingleOwner: whether a caller may open a link is decided
// in share_access.go alone (no other gateway source compares a link's
// access), and the organization's switch is read only by the room
// service's anonymousLinkAllowed (beyond the store and the settings
// routes that persist and serve it).
func TestShareAccessSingleOwner(t *testing.T) {
	access := regexp.MustCompile(`wire\.ShareAccess(Org|Anyone)|\.Share\.Access\b`)
	disabled := regexp.MustCompile(`\.AnonymousLinksDisabled\b`)
	switchReads := 0
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "cmd"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "services/gateway/") && rel != "services/gateway/share_access.go" && access.Match(src) {
			t.Errorf("%s decides on a share link's access; shareAccess (share_access.go) is the one owner", rel)
		}
		if n := len(disabled.FindAll(src, -1)); n > 0 {
			switch {
			case strings.HasPrefix(rel, "services/shareagent/"):
			case rel == "services/room/artifact_shares.go":
				switchReads += n
			default:
				t.Errorf("%s reads the organization's anonymous links switch; anonymousLinkAllowed is its one reader", rel)
			}
		}
	})
	if switchReads != 1 {
		t.Errorf("services/room/artifact_shares.go reads the switch %d times; only anonymousLinkAllowed may", switchReads)
	}
}
