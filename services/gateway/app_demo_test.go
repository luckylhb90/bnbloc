package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/gateway/appsdk"
)

// demo mints subject's demo of an app version: its site URL.
func (e *appGW) demo(subject, appID, version string, headers ...string) resp {
	e.t.Helper()
	return e.call(http.MethodPost, "/v1/content-components/"+appID+"/versions/"+version+"/demo", nil, append([]string{e.as(subject)}, headers...)...)
}

func demoURL(t *testing.T, r resp) string {
	t.Helper()
	var d wire.AppDemo
	if r.status != http.StatusOK || json.Unmarshal(r.body, &d) != nil || d.SiteURL == "" || d.ExpiresAtMs == 0 {
		t.Fatalf("demo = %d %s", r.status, r.body)
	}
	return d.SiteURL
}

// "Try demo" runs an app component's site in the site frame with the SDK
// in demo mode, and can never reach a data space: every app data call
// under a demo ticket is refused before anything reaches the room service
// or the change feed, whatever the app's own code sends. Only a member who
// may read the app gets a demo, and its files stop with that read.
func TestAppDemoNeverReachesAppData(t *testing.T) {
	e := galleryGW(t)
	site := e.pollSite()
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/apps/"+site+"/c/votes", map[string]any{"doc": map[string]any{"ballot": "B"}}, e.as("maya")); r.status != http.StatusCreated {
		t.Fatalf("a vote = %d %s", r.status, r.body)
	}
	app := e.publishApp("maya", site, wire.AppPublishRequest{Name: "lunch-poll"})

	// Someone who may not read the app gets no demo; a document is no app.
	if r := e.demo("jack", app.ID, "1.0.0"); r.status != http.StatusNotFound {
		t.Fatalf("a demo of someone else's personal app = %d %s", r.status, r.body)
	}
	if r := e.demo("maya", app.ID, "9.9.9"); r.status != http.StatusNotFound {
		t.Fatalf("a demo of a missing version = %d %s", r.status, r.body)
	}
	base := demoURL(t, e.demo("maya", app.ID, "1.0.0"))
	if !strings.HasPrefix(base, "/v1/sites/") || !strings.HasSuffix(base, "/") {
		t.Fatalf("demo site URL = %q", base)
	}

	// The app's site, served as a site: sandboxed, its files as they are.
	r := e.call(http.MethodGet, base, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "poll.js") || r.header.Get("Content-Security-Policy") != siteCSP {
		t.Fatalf("demo index = %d %v %s", r.status, r.header, r.body)
	}
	if r := e.call(http.MethodGet, base+"poll.js", nil); r.status != http.StatusOK || string(r.body) != "app.collection('votes')" {
		t.Fatalf("demo poll.js = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, base+"component.json", nil); r.status != http.StatusNotFound {
		t.Fatalf("the manifest is not the site's: %d", r.status)
	}

	// Its SDK is the demo SDK; a member's ticket of the site gets the live one.
	if r := e.call(http.MethodGet, base+"~app/sdk.js", nil); r.status != http.StatusOK || !bytes.Equal(r.body, appsdk.DemoJS) {
		t.Fatalf("demo sdk.js = %d (demo mode: %v)", r.status, bytes.Equal(r.body, appsdk.DemoJS))
	}
	if r := e.call(http.MethodGet, e.ticketBase("maya", site)+"~app/sdk.js", nil); r.status != http.StatusOK || !bytes.Equal(r.body, appsdk.JS) {
		t.Fatalf("a member's sdk.js = %d", r.status)
	}

	// Every app data call under the demo ticket: refused, none forwarded.
	before := e.appCalls.Load()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "c/votes"},
		{http.MethodPost, "c/votes"},
		{http.MethodGet, "c/votes/x"},
		{http.MethodPut, "c/votes/x"},
		{http.MethodPatch, "c/votes/x"},
		{http.MethodDelete, "c/votes/x"},
		{http.MethodGet, "me"},
		{http.MethodGet, "events"},
		{http.MethodGet, "stats"},
		{http.MethodGet, "export"},
		{http.MethodDelete, "data"},
	} {
		// 404: the app API has no app here; a path only the room entry
		// serves is no route of the frame (401: it needs a session).
		if r := e.call(c.method, base+"~app/"+c.path, map[string]any{"doc": map[string]any{"ballot": "A"}}); r.status != http.StatusNotFound && r.status != http.StatusUnauthorized {
			t.Errorf("%s ~app/%s under a demo ticket = %d %s", c.method, c.path, r.status, r.body)
		}
	}
	if n := e.appCalls.Load() - before; n != 0 {
		t.Fatalf("a demo reached the app data backends %d times", n)
	}
	if n := e.appDocs("maya", site); n != 1 {
		t.Fatalf("the original's data = %v documents", n)
	}

	// The demo's files stop with the read of the app.
	if r := e.call(http.MethodDelete, "/v1/content-components/"+app.ID, nil, e.as("maya")); r.status != http.StatusNoContent {
		t.Fatalf("remove the app = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodGet, base+"poll.js", nil); r.status != http.StatusNotFound {
		t.Fatalf("a removed app's demo = %d", r.status)
	}
}

// With a sites origin, a demo's URL is on it, and the main origin does not
// serve it.
func TestAppDemoOnSitesOrigin(t *testing.T) {
	e := galleryGW(t)
	app := e.publishApp("maya", e.pollSite(), wire.AppPublishRequest{Name: "lunch-poll"})
	e.withSites()
	path := sitePathOf(t, demoURL(t, e.demo("maya", app.ID, "1.0.0", mainHost)))
	if r := e.call(http.MethodGet, path+"poll.js", nil, sitesHost); r.status != http.StatusOK {
		t.Fatalf("demo on the sites origin = %d", r.status)
	}
	if r := e.call(http.MethodGet, path+"poll.js", nil, mainHost); r.status != http.StatusNotFound {
		t.Fatalf("demo on the main origin = %d", r.status)
	}
}
