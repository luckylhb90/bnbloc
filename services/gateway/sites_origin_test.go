package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

const (
	testSitesOrigin = "https://sites.bnbloc.example.com"
	sitesHost       = "Host: sites.bnbloc.example.com"
	mainHost        = "Host: bnbloc.example.com"
)

// withSites serves e's gateway again with a sites origin.
func (e *appGW) withSites() {
	e.t.Helper()
	e.gw.SitesOrigin = testSitesOrigin
	e.srv.Close()
	e.srv = httptest.NewServer(e.gw.Handler())
	e.t.Cleanup(e.srv.Close)
}

// sitePathOf is the path of a site URL the gateway handed out on the sites
// origin (refused when it is on any other).
func sitePathOf(t *testing.T, siteURL string) string {
	t.Helper()
	path, ok := strings.CutPrefix(siteURL, testSitesOrigin)
	if !ok || !strings.HasPrefix(path, "/v1/") {
		t.Fatalf("site URL %q is not on the sites origin", siteURL)
	}
	return path
}

// With a sites origin, sites, their app API and the SDK are served there
// and only there; the main origin answers their paths 404, and the sites
// origin answers every other path (the API, the web client, a session
// route) 404. The URLs the gateway hands out are on the sites origin.
func TestSitesOriginServesSitesOnly(t *testing.T) {
	e := newAppGW(t)
	e.withSites()
	maya := "Authorization: Bearer " + e.session("maya")

	// A member's site ticket: on the sites origin.
	r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/artifacts/"+e.appA+"/versions/1/site-ticket", nil, maya, mainHost)
	siteURL, _ := r.json()["siteUrl"].(string)
	if r.status != http.StatusOK {
		t.Fatalf("site ticket = %d %s", r.status, r.body)
	}
	member := sitePathOf(t, siteURL)

	// A public link's site URL: on the sites origin.
	tok, _ := e.share(e.appA)
	r = e.call(http.MethodGet, "/v1/public/shares/"+tok, nil, mainHost)
	shareURL, _ := r.json()["siteUrl"].(string)
	if r.status != http.StatusOK {
		t.Fatalf("share = %d %s", r.status, r.body)
	}
	public := sitePathOf(t, shareURL)
	vis := visitorHeader(e.visitor(tok, ""))

	// The app API and the SDK under both frame entries: on the sites
	// origin, with no-referrer; on the main origin, 404.
	for _, c := range []struct {
		path    string
		headers []string
	}{
		{member + "~app/me", nil},
		{member + "~app/sdk.js", nil},
		{member + "~app/c/votes", nil},
		{public + "~app/me", []string{vis}},
		{public + "~app/sdk.js", nil},
	} {
		on := e.call(http.MethodGet, c.path, nil, append(c.headers, sitesHost)...)
		if on.status != http.StatusOK || on.header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("GET %s on the sites origin = %d %s (referrer %q)", c.path, on.status, on.body, on.header.Get("Referrer-Policy"))
		}
		if off := e.call(http.MethodGet, c.path, nil, append(c.headers, mainHost)...); off.status != http.StatusNotFound {
			t.Errorf("GET %s on the main origin = %d, want 404", c.path, off.status)
		}
	}
	// A write and its preflight on the sites origin.
	if r := e.call(http.MethodPost, member+"~app/c/votes", map[string]any{"doc": map[string]any{"choice": "A"}}, sitesHost); r.status != http.StatusCreated {
		t.Fatalf("insert on the sites origin = %d %s", r.status, r.body)
	}
	pre := e.call(http.MethodOptions, member+"~app/c/votes", nil, sitesHost, "Origin: null", "Access-Control-Request-Method: POST")
	if pre.status != http.StatusNoContent || pre.header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight on the sites origin = %d %v", pre.status, pre.header)
	}
	if r := e.call(http.MethodOptions, member+"~app/c/votes", nil, mainHost, "Origin: null"); r.status != http.StatusNotFound {
		t.Fatalf("preflight on the main origin = %d, want 404", r.status)
	}

	// Nothing else on the sites origin: no API route (with or without a
	// session), no web client, no visitor minting, no share JSON.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/index.html"},
		{http.MethodGet, "/v1/public/client-config"},
		{http.MethodGet, "/v1/rooms"},
		{http.MethodPost, "/v1/auth/login"},
		{http.MethodPost, "/v1/auth/refresh"},
		{http.MethodGet, "/v1/rooms/" + e.roomID + "/apps/" + e.appA + "/stats"},
		{http.MethodGet, "/v1/rooms/" + e.roomID + "/apps/" + e.appA + "/export?format=json"},
		{http.MethodGet, "/v1/public/shares/" + tok},
		{http.MethodPost, "/v1/public/shares/" + tok + "/visitor"},
		{http.MethodOptions, "/v1/rooms"},
		{http.MethodGet, "/v1/sites"},
	} {
		r := e.call(c.method, c.path, nil, maya, sitesHost, "Origin: tauri://localhost")
		if r.status != http.StatusNotFound || r.header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s %s on the sites origin = %d %s, want 404", c.method, c.path, r.status, r.body)
		}
	}
	// The main origin keeps its API.
	if r := e.call(http.MethodGet, "/v1/rooms/"+e.roomID+"/apps/"+e.appA+"/stats", nil, maya, mainHost); r.status != http.StatusOK {
		t.Fatalf("stats on the main origin = %d %s", r.status, r.body)
	}
	// A client learns the sites origin from the main origin.
	var cfg wire.ClientConfig
	r = e.call(http.MethodGet, "/v1/public/client-config", nil, mainHost)
	if r.status != http.StatusOK || json.Unmarshal(r.body, &cfg) != nil || cfg.SitesOrigin != testSitesOrigin {
		t.Fatalf("client config = %d %s", r.status, r.body)
	}
}

// Without a sites origin, sites stay on the gateway's own origin under
// relative URLs, and the client config names none.
func TestNoSitesOriginServesSitesInPlace(t *testing.T) {
	e := newAppGW(t)
	base := e.ticketBase("jack", e.appA)
	if r := e.call(http.MethodGet, base+"~app/me", nil); r.status != http.StatusOK {
		t.Fatalf("me = %d %s", r.status, r.body)
	}
	tok, _ := e.share(e.appA)
	if got, _ := e.call(http.MethodGet, "/v1/public/shares/"+tok, nil).json()["siteUrl"].(string); got != shareBase(tok) {
		t.Fatalf("share siteUrl = %q, want %q", got, shareBase(tok))
	}
	if r := e.call(http.MethodGet, "/v1/public/client-config", nil); r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != "{}" {
		t.Fatalf("client config = %d %s", r.status, r.body)
	}
}

// BNBLOC_SITES_ORIGIN is a bare https origin on another host than the
// public URL; anything else refuses to start.
func TestParseSitesOrigin(t *testing.T) {
	const public = "https://bnbloc.example.com"
	for raw, want := range map[string]string{
		"":                                     "",
		"  ":                                   "",
		"https://sites.bnbloc.example.com":     "https://sites.bnbloc.example.com",
		"https://Sites.BNBLOC.example.com:443": "https://sites.bnbloc.example.com",
		"https://bnbloc-usercontent.example:8443": "https://bnbloc-usercontent.example:8443",
	} {
		got, err := ParseSitesOrigin(raw, public)
		if err != nil || got != want {
			t.Errorf("ParseSitesOrigin(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"http://sites.bnbloc.example.com",
		"sites.bnbloc.example.com",
		"https://sites.bnbloc.example.com/",
		"https://sites.bnbloc.example.com/apps",
		"https://sites.bnbloc.example.com?x=1",
		"https://sites.bnbloc.example.com#f",
		"https://user@sites.bnbloc.example.com",
		"https://",
		"https://bnbloc.example.com",
		"https://BNBLOC.example.com:8443",
		"https://%zz",
	} {
		if got, err := ParseSitesOrigin(raw, public); err == nil {
			t.Errorf("ParseSitesOrigin(%q) = %q, want an error", raw, got)
		}
	}
}

// sitePath is the one answer to "is this a site path".
func TestSitePath(t *testing.T) {
	for p, want := range map[string]bool{
		"/v1/sites/T":                         true,
		"/v1/sites/T/":                        true,
		"/v1/sites/T/a/b.js":                  true,
		"/v1/sites/T/~app/me":                 true,
		"/v1/public/shares/S/site":            true,
		"/v1/public/shares/S/site/~app/me":    true,
		"/v1/sites":                           false,
		"/v1/sites/":                          false,
		"/v1/public/shares/S":                 false,
		"/v1/public/shares/S/content":         false,
		"/v1/public/shares/S/visitor":         false,
		"/v1/public/shares//site":             false,
		"/v1/rooms/r/apps/a/c/votes":          false,
		"/":                                   false,
		"/x/v1/sites/T/":                      false,
		"/v1/public/shares/S/content/~app/me": false,
	} {
		if got := sitePath(p); got != want {
			t.Errorf("sitePath(%q) = %v, want %v", p, got, want)
		}
	}
}
