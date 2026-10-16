package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/sharelink"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Short share links (docs/prd/2026-10-13-short-share-links.md).

// createLink creates a link to artifact with access through the gateway,
// as maya, and answers what the share dialog gets.
func (e *appGW) createLink(artifact, access string, headers ...string) wire.ShareCreated {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+artifact+"/shares", wire.ShareCreateRequest{Access: access},
		append(headers, "Authorization: Bearer "+e.session("maya"))...)
	var out wire.ShareCreated
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &out) != nil || out.Share.ShareID == "" {
		e.t.Fatalf("create link = %d %s", r.status, r.body)
	}
	return out
}

// maxLinkLen bounds a link under a typical host ("https://" and a
// 20-character host): the product's "about 50 characters".
const maxLinkLen = 55

// The create answer carries the link, on the gateway's public origin
// (configured, else the host the member called), short.
func TestShareCreateCarriesTheShortLink(t *testing.T) {
	e := newAppGW(t)
	created := e.createLink(e.appA, "", mainHost)
	if !sharelink.Valid(created.Token) || created.URL != "http://bnbloc.example.com/s/"+created.Token {
		t.Fatalf("link %q, token %q", created.URL, created.Token)
	}
	e.gw.PublicURL = "https://links.acme-corp.io/"
	created = e.createLink(e.appA, wire.ShareAccessAnyone, mainHost)
	if created.URL != "https://links.acme-corp.io/s/"+created.Token || len(created.URL) > maxLinkLen {
		t.Fatalf("link %q (%d characters, at most %d)", created.URL, len(created.URL), maxLinkLen)
	}
	if strings.Contains(created.URL, created.Share.ShareID) {
		t.Fatalf("the link %q carries its share id", created.URL)
	}
	// A refused create passes as the room service answered it.
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+e.appA+"/shares", wire.ShareCreateRequest{Access: "everyone"},
		"Authorization: Bearer "+e.session("maya")); r.status != http.StatusBadRequest || strings.Contains(string(r.body), "url") {
		t.Fatalf("a refused create = %d %s", r.status, r.body)
	}
}

// /s/<token> sends the browser to the share page with the token, and the
// share page opens the link through the share routes, for every access
// mode: anyone at once, signed_in and org after sign-in with a ticket. With
// a sites origin the link is served on the main origin only.
func TestShortLinkOpensEveryAccessMode(t *testing.T) {
	e := newAppGW(t)
	e.withSites()
	app := e.liveSite(e.roomID)
	for _, access := range wire.ShareAccessModes {
		created := e.createLink(app, access, mainHost)
		path := strings.TrimPrefix(created.URL, "http://bnbloc.example.com")
		if path != "/s/"+created.Token {
			t.Fatalf("%s: link %q", access, created.URL)
		}
		r := e.call(http.MethodGet, path, nil, mainHost)
		page := r.header.Get("Location")
		if r.status != http.StatusFound || page != "/#/share/"+created.Token || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %s = %d %q", access, path, r.status, page)
		}
		if r := e.call(http.MethodGet, path, nil, sitesHost); r.status != http.StatusNotFound {
			t.Fatalf("%s: the link on the sites origin = %d", access, r.status)
		}
		// The share page: the token from the page's hash.
		token := strings.TrimPrefix(page, "/#/share/")
		r = e.call(http.MethodGet, "/v1/public/shares/"+token, nil, mainHost)
		if access == wire.ShareAccessAnyone {
			if r.status != http.StatusOK || r.json()["artifact"].(map[string]any)["kind"] != "site" {
				t.Fatalf("anyone: the page's metadata = %d %s", r.status, r.body)
			}
			continue
		}
		if r.status != http.StatusUnauthorized || refusalCode(r) != wire.ShareSignInRequired {
			t.Fatalf("%s: anonymous = %d %s", access, r.status, r.body)
		}
		ticket := e.ticketIn(e.sessionIn("nora", e.tenant, "member"), token)
		if r := e.call(http.MethodGet, "/v1/public/shares/"+ticket, nil, mainHost); r.status != http.StatusOK {
			t.Fatalf("%s: with a ticket = %d %s", access, r.status, r.body)
		}
	}
}

// An unknown link, or one of the old long format, fails cleanly: the
// gateway's link path answers 404 for a malformed token and sends a
// well-formed unknown one to the share page, whose metadata request then
// answers 404 "share not found" (its "no longer available" page); no
// route answers 500.
func TestUnknownOrOldShareLinkIsACleanNotFound(t *testing.T) {
	e := newAppGW(t)
	unknown, _ := sharelink.New()
	old := "bsh_" + strings.Repeat("0f", 16) + "_c2VjcmV0LXNlY3JldC1zZWNyZXQtc2VjcmV0LXNlY3JldA"
	if r := e.call(http.MethodGet, "/s/"+unknown, nil); r.status != http.StatusFound || r.header.Get("Location") != "/#/share/"+unknown {
		t.Fatalf("an unknown well-formed link = %d %q", r.status, r.header.Get("Location"))
	}
	for _, bad := range []string{old, "short", unknown + "0", strings.ToLower(unknown)[:21] + "-"} {
		if r := e.call(http.MethodGet, "/s/"+bad, nil); r.status != http.StatusNotFound {
			t.Fatalf("/s/%s = %d %s", bad, r.status, r.body)
		}
	}
	for _, tok := range []string{unknown, old} {
		for _, path := range []string{"/v1/public/shares/" + tok, "/v1/public/shares/" + tok + "/content", "/v1/public/shares/" + tok + "/site/"} {
			if r := e.call(http.MethodGet, path, nil); r.status != http.StatusNotFound || !strings.Contains(string(r.body), "share not found") {
				t.Fatalf("%s = %d %s", path, r.status, r.body)
			}
		}
		if r := e.mintTicketIn(e.sessionIn("nora", e.tenant, "member"), tok); r.status != http.StatusNotFound {
			t.Fatalf("a ticket for %s = %d %s", tok, r.status, r.body)
		}
	}
}

// A creator copies their link again: the answer is the link's URL on the
// gateway's public origin, never the token, uncached; the route belongs to the
// creator alone (another member gets the room service's 404).
func TestShareLinkCopyAgainCarriesTheURL(t *testing.T) {
	e := newAppGW(t)
	created := e.createLink(e.appA, "", mainHost)
	if !created.Share.Copyable {
		t.Fatalf("the create answer does not say the link can be copied again: %+v", created.Share)
	}
	path := "/v1/rooms/" + e.roomID + "/artifacts/" + e.appA + "/shares/" + created.Share.ShareID + "/link"
	r := e.call(http.MethodGet, path, nil, mainHost, "Authorization: Bearer "+e.session("maya"))
	var link map[string]any
	if r.status != http.StatusOK || json.Unmarshal(r.body, &link) != nil || link["url"] != created.URL || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("copy again = %d %s (cache %q), want url %q", r.status, r.body, r.header.Get("Cache-Control"), created.URL)
	}
	if _, leaked := link["token"]; leaked {
		t.Fatalf("the answer carries the bare token: %s", r.body)
	}
	if r := e.call(http.MethodGet, path, nil, mainHost, "Authorization: Bearer "+e.session("jack")); r.status != http.StatusNotFound && r.status != http.StatusForbidden {
		t.Fatalf("another member's copy = %d %s, want a refusal", r.status, r.body)
	}
}
