package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Links anyone signed in can open (docs/prd/2026-10-13-share-link-sign-in.md,
// "Amendment").

// sessionIn is a session of subject in organization tid with role, an
// organization they need not belong to: with auto-provisioning off, a
// sign-in lands a person who belongs to no organization as a viewer of one.
func (e *appGW) sessionIn(subject, tid, role string) string {
	return e.gw.Signer.Issue(auth.Claims{Subject: subject, Tenant: tid, Role: role, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

// mintTicketIn asks for a session's share ticket of the link tok.
func (e *appGW) mintTicketIn(session, tok string) resp {
	e.t.Helper()
	return e.call(http.MethodPost, "/v1/share-tickets", wire.ShareTicketRequest{Token: tok}, "Authorization: Bearer "+session)
}

// ticketIn is a session's share ticket of the link tok (fails the test
// when refused).
func (e *appGW) ticketIn(session, tok string) string {
	e.t.Helper()
	r := e.mintTicketIn(session, tok)
	var out wire.ShareTicket
	if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil || out.Ticket == "" {
		e.t.Fatalf("share ticket = %d %s", r.status, r.body)
	}
	return out.Ticket
}

// A signed_in link refuses an anonymous caller on every route, exactly
// like an org link (the share page then sends them to sign in), whatever
// session or cookie the request carries; no view is counted.
func TestSignedInLinkRefusesAnonymousOnEveryRoute(t *testing.T) {
	e := newAppGW(t)
	app := e.liveSite(e.roomID)
	tok, id := e.shareWith(app, wire.ShareAccessSignedIn)
	if sh := e.shareRow(id); sh.Access != wire.ShareAccessSignedIn {
		t.Fatalf("access = %q", sh.Access)
	}
	if _, _, err := e.b.Artifacts.PublishArtifactVersion(e.ctx(), e.doc, store.ArtifactVersion{Storage: store.ArtifactStorageInline, Content: "# Plan",
		MimeType: "text/markdown", SizeBytes: 6, AuthorUserID: "maya", CreatedAtMs: 1}, 0); err != nil {
		t.Fatal(err)
	}
	docTok, _ := e.shareWith(e.doc, wire.ShareAccessSignedIn)
	base := "/v1/public/shares/" + tok
	session := "Authorization: Bearer " + e.sessionIn("olaf", "away", "member")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, base},
		{http.MethodGet, base + "/content"},
		{http.MethodGet, base + "/content?download=1"},
		{http.MethodGet, "/v1/public/shares/" + docTok},
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
	if r := e.call(http.MethodPost, "/v1/share-tickets", wire.ShareTicketRequest{Token: tok}); r.status != http.StatusUnauthorized {
		t.Fatalf("a ticket without a session = %d %s", r.status, r.body)
	}
	if sh := e.shareRow(id); sh.ViewCount != 0 {
		t.Fatalf("a refused caller counted %d views", sh.ViewCount)
	}
}

// Anyone signed in opens a signed_in link: a member of another
// organization, and a person who belongs to no organization at all (a
// sign-in that landed them as a viewer of one without a membership). In
// the app each is a participant (their own name and documents), never an
// owner; their ticket opens that link only, and no room route, member
// site or session takes it. Switched to org, the outsiders' tickets are
// refused at once with the org-only answer.
func TestSignedInLinkOpensForAnyoneSignedIn(t *testing.T) {
	e := newAppGW(t)
	app := e.liveSite(e.roomID)
	tok, id := e.shareWith(app, wire.ShareAccessSignedIn)
	for _, tid := range []string{e.tenant, "away", "default"} {
		if _, member, err := e.b.Tenant.GetMember(context.Background(), tid, "zed"); err != nil || member {
			t.Fatalf("zed must belong to no organization (%s: %v %v)", tid, member, err)
		}
	}

	// jack, a room member and so an owner, writes a private and a public
	// document through his own site ticket.
	jack := e.ticketBase("jack", app)
	if r := e.call(http.MethodPost, jack+"~app/c/votes", map[string]any{"doc": map[string]any{"choice": "B"}}); r.status != http.StatusCreated {
		t.Fatalf("jack's vote = %d %s", r.status, r.body)
	}
	r := e.call(http.MethodPost, jack+"~app/c/options", map[string]any{"doc": map[string]any{"name": "A"}, "public": true})
	if r.status != http.StatusCreated {
		t.Fatalf("jack's option = %d %s", r.status, r.body)
	}
	option := r.json()["_id"].(string)

	tickets := map[string]string{}
	for who, session := range map[string]string{
		"olaf": e.sessionIn("olaf", "away", "member"),
		"zed":  e.sessionIn("zed", "default", "viewer"),
	} {
		ticket := e.ticketIn(session, tok)
		tickets[who] = ticket
		base := "/v1/public/shares/" + ticket
		if r := e.call(http.MethodGet, base, nil); r.status != http.StatusOK || r.json()["siteUrl"] != base+"/site/" {
			t.Fatalf("%s's metadata = %d %s", who, r.status, r.body)
		}
		site := base + "/site/"
		if r := e.call(http.MethodGet, site, nil); r.status != http.StatusOK || !strings.Contains(string(r.body), "<p>app</p>") {
			t.Fatalf("%s's site file = %d %s", who, r.status, r.body)
		}
		me := e.call(http.MethodGet, site+"~app/me", nil)
		if me.status != http.StatusOK || me.json()["name"] != who || me.json()["id"] == "" {
			t.Fatalf("%s's me = %d %s", who, me.status, me.body)
		}
		self := me.json()["id"].(string)
		if r := e.call(http.MethodPut, site+"~app/c/votes/"+self, map[string]any{"doc": map[string]any{"choice": "A"}}, "If-None-Match: *"); r.status != http.StatusCreated || r.json()["_by"] != self {
			t.Fatalf("%s's vote = %d %s", who, r.status, r.body)
		}
		// A participant: their own vote only, the public option without
		// its author, no change to jack's document, no owners' routes.
		if r := e.call(http.MethodGet, site+"~app/c/votes", nil); r.status != http.StatusOK || len(r.docs()) != 1 || r.docs()[0]["_id"] != self {
			t.Fatalf("%s's find = %d %s (their own vote only)", who, r.status, r.body)
		}
		if r := e.call(http.MethodGet, site+"~app/c/options/"+option, nil); r.status != http.StatusOK || r.json()["_author"] != nil {
			t.Fatalf("%s reads the public option = %d %s", who, r.status, r.body)
		}
		for _, m := range []string{http.MethodDelete, http.MethodPatch} {
			if r := e.call(m, site+"~app/c/options/"+option, map[string]any{"merge": map[string]any{"name": "X"}}); r.status != http.StatusForbidden {
				t.Fatalf("%s's %s of jack's document = %d %s", who, m, r.status, r.body)
			}
		}
		if r := e.call(http.MethodGet, site+"~app/stats", nil); r.status != http.StatusNotFound {
			t.Fatalf("%s's stats under a link = %d %s", who, r.status, r.body)
		}
		if r := e.call(http.MethodPost, base+"/visitor", nil); r.status != http.StatusBadRequest {
			t.Fatalf("a visitor token for %s's ticket = %d %s", who, r.status, r.body)
		}
		// The ticket is no member site ticket and no session.
		if r := e.call(http.MethodGet, "/v1/sites/"+ticket+"/~app/me", nil); r.status != http.StatusNotFound {
			t.Fatalf("%s's ticket as a member site ticket = %d %s", who, r.status, r.body)
		}
		for _, path := range []string{"/v1/rooms", "/v1/rooms/" + e.roomID + "/apps/" + app + "/c/votes"} {
			if r := e.call(http.MethodGet, path, nil, "Authorization: Bearer "+ticket); r.status != http.StatusUnauthorized {
				t.Fatalf("%s's ticket as a session on %s = %d %s", who, path, r.status, r.body)
			}
		}
	}
	if sh := e.shareRow(id); sh.ViewCount != 2 {
		t.Fatalf("views = %d, want 2", sh.ViewCount)
	}
	// The owner sees both outsiders' votes with their real authors.
	if r := e.call(http.MethodGet, jack+"~app/c/votes", nil); r.status != http.StatusOK || len(r.docs()) != 3 ||
		!strings.Contains(string(r.body), "olaf") || !strings.Contains(string(r.body), "zed") {
		t.Fatalf("the owner's find = %d %s", r.status, r.body)
	}

	// The ticket opens its own link only: another link of the same room,
	// for the organization, refuses olaf with the org-only answer, and a
	// ticket naming another organization opens nothing.
	orgTok, _ := e.shareWith(app, "")
	if r := e.mintTicketIn(e.sessionIn("olaf", "away", "member"), orgTok); r.status != http.StatusForbidden || refusalCode(r) != wire.ShareOrgOnly {
		t.Fatalf("olaf's ticket for an org link = %d %s", r.status, r.body)
	}
	other := e.gw.Signer.Issue(auth.Claims{Subject: "olaf", Tenant: "away", Kind: auth.KindShareTicket, ID: id, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	if r := e.call(http.MethodGet, "/v1/public/shares/"+other, nil); r.status != http.StatusNotFound {
		t.Fatalf("a ticket naming another organization = %d %s", r.status, r.body)
	}

	// Switched to org: the outsiders' tickets are refused at once; a
	// member of the organization opens it.
	if r := e.roomCall("maya", http.MethodPatch, "/v1/rooms/"+e.roomID+"/artifacts/"+app+"/shares/"+id, wire.ShareAccessUpdate{Access: wire.ShareAccessOrg}); r.status != http.StatusOK {
		t.Fatalf("switch to org = %d %s", r.status, r.body)
	}
	for who, ticket := range tickets {
		for _, path := range []string{"/v1/public/shares/" + ticket, "/v1/public/shares/" + ticket + "/site/~app/me"} {
			if r := e.call(http.MethodGet, path, nil); r.status != http.StatusForbidden || refusalCode(r) != wire.ShareOrgOnly {
				t.Fatalf("%s's %s after the switch = %d %s", who, path, r.status, r.body)
			}
		}
	}
	if r := e.call(http.MethodGet, "/v1/public/shares/"+e.ticket("nora", tok), nil); r.status != http.StatusOK {
		t.Fatalf("a member after the switch = %d %s", r.status, r.body)
	}
}

// The organization's outside-links switch turns signed_in and anyone links
// off at once (an open ticket's and token's routes, the ticket mint and
// the visitor mint answer 404) and leaves org links working; back on, both
// work again. The anonymous switch alone leaves signed_in working.
func TestOutsideLinksSwitchStopsSignedInAndAnyone(t *testing.T) {
	e := newAppGW(t)
	signedTok, _ := e.shareWith(e.appA, wire.ShareAccessSignedIn)
	anyTok, _ := e.share(e.appA)
	orgTok, _ := e.shareWith(e.appA, "")
	olaf := e.sessionIn("olaf", "away", "member")
	ticket := e.ticketIn(olaf, signedTok)
	vis := visitorHeader(e.visitor(anyTok, ""))
	orgTicket := e.ticket("nora", orgTok)
	setPolicy := func(u store.TenantPolicyUpdate) {
		t.Helper()
		if _, err := e.b.Tenant.SetTenantPolicy(e.ctx(), e.tenant, u); err != nil {
			t.Fatal(err)
		}
	}
	status := func(ok bool) int {
		if ok {
			return http.StatusOK
		}
		return http.StatusNotFound
	}
	works := func(when string, signedIn, anyone bool) {
		t.Helper()
		for _, c := range []struct {
			method, path string
			headers      []string
			want         int
		}{
			{http.MethodGet, "/v1/public/shares/" + ticket, nil, status(signedIn)},
			{http.MethodGet, shareBase(ticket) + "~app/me", nil, status(signedIn)},
			{http.MethodGet, "/v1/public/shares/" + anyTok, nil, status(anyone)},
			{http.MethodGet, shareBase(anyTok) + "~app/me", []string{vis}, status(anyone)},
			{http.MethodPost, "/v1/public/shares/" + anyTok + "/visitor", nil, status(anyone)},
			{http.MethodGet, "/v1/public/shares/" + orgTicket, nil, http.StatusOK},
		} {
			if r := e.call(c.method, c.path, nil, c.headers...); r.status != c.want {
				t.Fatalf("%s: %s %s = %d %s, want %d", when, c.method, c.path, r.status, r.body, c.want)
			}
		}
		if r := e.mintTicketIn(olaf, signedTok); r.status != status(signedIn) {
			t.Fatalf("%s: the ticket mint = %d %s", when, r.status, r.body)
		}
	}
	on, off := true, false
	works("allowed", true, true)
	setPolicy(store.TenantPolicyUpdate{OutsideLinksDisabled: &on})
	works("outside links off", false, false)
	setPolicy(store.TenantPolicyUpdate{OutsideLinksDisabled: &off, AnonymousLinksDisabled: &on})
	works("anonymous links off", true, false)
	setPolicy(store.TenantPolicyUpdate{AnonymousLinksDisabled: &off})
	works("allowed again", true, true)
}
