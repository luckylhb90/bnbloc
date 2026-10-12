package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// presigningDisk is a disk blob store that also presigns (a stand-in for
// S3 with direct downloads on).
type presigningDisk struct{ *blobstore.DiskStore }

func (p presigningDisk) PresignGet(_ context.Context, key string, _ time.Duration, opts blobstore.PresignOptions) (string, error) {
	return "https://s3.example.com/bucket/" + key + "?X-Amz-Signature=sig&cd=" + url.QueryEscape(opts.ContentDisposition), nil
}

// siteEnv is a gateway in front of a real realtime blob store and a fake
// room service that serves artifact versions and share resolutions.
type siteEnv struct {
	t           *testing.T
	svc         *Service
	gw          *httptest.Server
	blobs       *blobstore.DiskStore
	rt          *realtimehttp.Service
	mu          sync.Mutex
	members     map[string]bool                  // "room/user"
	versions    map[string]store.ArtifactVersion // "room/artifact/version"
	shares      map[string]map[string]any        // token -> resolution
	views       map[string]int
	resolveUser string
	versionGets atomic.Int32
}

func newSiteEnv(t *testing.T) *siteEnv {
	t.Helper()
	e := &siteEnv{t: t, members: map[string]bool{}, versions: map[string]store.ArtifactVersion{}, shares: map[string]map[string]any{}, views: map[string]int{}}
	disk, err := blobstore.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.blobs = disk
	e.rt = &realtimehttp.Service{Blobs: disk}
	rtSrv := httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), e.rt.Handler()))
	t.Cleanup(rtSrv.Close)

	room := http.NewServeMux()
	room.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		e.versionGets.Add(1)
		e.mu.Lock()
		defer e.mu.Unlock()
		if !e.members[r.PathValue("roomId")+"/"+r.Header.Get(svcauth.HeaderUser)] {
			http.Error(w, "not a member", http.StatusForbidden)
			return
		}
		v, ok := e.versions[r.PathValue("roomId")+"/"+r.PathValue("artifactId")+"/"+r.PathValue("version")]
		if !ok {
			http.Error(w, "version not found", http.StatusNotFound)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"version": v})
	})
	room.HandleFunc("POST /internal/v1/shares/resolve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token     string `json:"token"`
			CountView bool   `json:"countView"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.resolveUser = r.Header.Get(svcauth.HeaderUser)
		res, ok := e.shares[body.Token]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if body.CountView {
			e.views[body.Token]++
		}
		httpx.WriteJSON(w, http.StatusOK, res)
	})
	roomSrv := httptest.NewServer(room)
	t.Cleanup(roomSrv.Close)

	e.svc = newGateway(t, roomSrv.URL)
	e.svc.Backends.Realtime = rtSrv.URL
	e.svc.Rooms = func(subject, _ string) []string {
		e.mu.Lock()
		defer e.mu.Unlock()
		var out []string
		for k := range e.members {
			if room, user, _ := strings.Cut(k, "/"); user == subject {
				out = append(out, room)
			}
		}
		return out
	}
	e.gw = httptest.NewServer(e.svc.Handler())
	t.Cleanup(e.gw.Close)
	return e
}

func (e *siteEnv) session(subject string) string {
	return e.svc.Signer.Issue(auth.Claims{Subject: subject, Tenant: "default", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

// putSite stores a site snapshot of files as room/artifact version 1.
func (e *siteEnv) putSite(roomID, artifactID string, files map[string]string) store.ArtifactVersion {
	e.t.Helper()
	m := map[string][]byte{}
	for k, v := range files {
		m[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(m, tarbundle.SiteLimits)
	if err != nil {
		e.t.Fatal(err)
	}
	key, size, sha, err := e.blobs.Put(context.Background(), bytes.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	v := store.ArtifactVersion{ArtifactID: artifactID, Version: 1, Storage: store.ArtifactStorageBlob, BlobKey: key,
		ContentHash: sha, SizeBytes: size, MimeType: tarbundle.SiteMIME, CreatedAtMs: 1700000000000}
	e.mu.Lock()
	e.versions[roomID+"/"+artifactID+"/1"] = v
	e.mu.Unlock()
	return v
}

func (e *siteEnv) join(roomID, user string) {
	e.mu.Lock()
	e.members[roomID+"/"+user] = true
	e.mu.Unlock()
}

func (e *siteEnv) leave(roomID, user string) {
	e.mu.Lock()
	delete(e.members, roomID+"/"+user)
	e.mu.Unlock()
}

func (e *siteEnv) do(method, path, bearer string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.gw.URL+path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

var noRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *siteEnv) ticket(subject, roomID, artifactID string) (int, string) {
	e.t.Helper()
	resp, body := e.do(http.MethodPost, "/v1/rooms/"+roomID+"/artifacts/"+artifactID+"/versions/1/site-ticket", e.session(subject))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, body
	}
	var out struct {
		SiteURL     string `json:"siteUrl"`
		ExpiresAtMs int64  `json:"expiresAtMs"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || !strings.HasPrefix(out.SiteURL, "/v1/sites/") || !strings.HasSuffix(out.SiteURL, "/") {
		e.t.Fatalf("ticket body %s", body)
	}
	if exp := time.UnixMilli(out.ExpiresAtMs); exp.Before(time.Now().Add(siteTicketTTL-time.Minute)) || exp.After(time.Now().Add(siteTicketTTL+time.Minute)) {
		e.t.Fatalf("expiresAtMs = %v", exp)
	}
	return resp.StatusCode, out.SiteURL
}

const viteIndex = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <link rel="icon" type="image/svg+xml" href="/vite.svg" />
    <title>Demo</title>
    <script type="module" crossorigin src="/assets/index-AbC123.js"></script>
    <link rel="stylesheet" href="/assets/index-x.css">
  </head>
  <body><div id="root"></div></body>
</html>`

var testSiteFiles = map[string]string{
	"index.html":             viteIndex,
	"assets/index-AbC123.js": `import "./chunk.js"; fetch("/api/data")`,
	"assets/index-x.css":     `body{background:url(/assets/bg.png)}`,
	"docs/index.html":        `<a href="/">home</a>`,
}

func assertSiteHeaders(t *testing.T, resp *http.Response, contentType string) {
	t.Helper()
	want := map[string]string{
		"Content-Type":                contentType,
		"Content-Security-Policy":     "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; frame-ancestors *",
		"X-Content-Type-Options":      "nosniff",
		"Referrer-Policy":             "no-referrer",
		"Access-Control-Allow-Origin": "*",
		"Cache-Control":               "private, max-age=300",
	}
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestMemberSite_ticket_serves_rewritten_files_with_site_headers(t *testing.T) {
	e := newSiteEnv(t)
	e.join("room-1", "alice")
	e.putSite("room-1", "art_site", testSiteFiles)

	status, siteURL := e.ticket("alice", "room-1", "art_site")
	if status != http.StatusOK {
		t.Fatalf("ticket status %d: %s", status, siteURL)
	}

	resp, body := e.do(http.MethodGet, siteURL, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status %d: %s", resp.StatusCode, body)
	}
	assertSiteHeaders(t, resp, "text/html; charset=utf-8")
	for _, want := range []string{
		`src="` + siteURL + `assets/index-AbC123.js"`,
		`href="` + siteURL + `assets/index-x.css"`,
		`href="` + siteURL + `vite.svg"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("index missing %s:\n%s", want, body)
		}
	}

	resp, body = e.do(http.MethodGet, siteURL+"assets/index-AbC123.js", "")
	if resp.StatusCode != http.StatusOK || body != testSiteFiles["assets/index-AbC123.js"] {
		t.Fatalf("js status %d body %q (runtime JS is never rewritten)", resp.StatusCode, body)
	}
	assertSiteHeaders(t, resp, "text/javascript; charset=utf-8")

	resp, body = e.do(http.MethodGet, siteURL+"assets/index-x.css", "")
	if resp.StatusCode != http.StatusOK || body != `body{background:url(`+siteURL+`assets/bg.png)}` {
		t.Fatalf("css status %d body %q", resp.StatusCode, body)
	}

	// SPA fallback, directory index, directory redirect, misses.
	if resp, body = e.do(http.MethodGet, siteURL+"dashboard/users", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, "<title>Demo</title>") {
		t.Fatalf("spa fallback %d", resp.StatusCode)
	}
	if resp, body = e.do(http.MethodGet, siteURL+"docs/", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `href="`+siteURL+`"`) {
		t.Fatalf("dir index %d %s", resp.StatusCode, body)
	}
	if resp, _ = e.do(http.MethodGet, siteURL+"docs", ""); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != siteURL+"docs/" {
		t.Fatalf("dir redirect %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ = e.do(http.MethodGet, siteURL+"assets/missing.png", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset %d", resp.StatusCode)
	}
	if resp, _ = e.do(http.MethodGet, strings.TrimSuffix(siteURL, "/"), ""); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != siteURL {
		t.Fatalf("base redirect %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestMemberSite_rechecks_room_read_after_the_auth_cache(t *testing.T) {
	e := newSiteEnv(t)
	e.join("room-1", "alice")
	e.putSite("room-1", "art_site", testSiteFiles)
	_, siteURL := e.ticket("alice", "room-1", "art_site")
	before := e.versionGets.Load()
	for i := 0; i < 3; i++ {
		if resp, _ := e.do(http.MethodGet, siteURL, ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	if got := e.versionGets.Load(); got != before {
		t.Fatalf("cached authorization re-fetched the version %d times", got-before)
	}

	// Removed from the room: once the authorization expires, 404.
	e.leave("room-1", "alice")
	e.svc.siteAuth.mu.Lock()
	e.svc.siteAuth.now = func() time.Time { return time.Now().Add(siteAuthTTL + time.Second) }
	e.svc.siteAuth.mu.Unlock()
	if resp, _ := e.do(http.MethodGet, siteURL, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after leave status %d", resp.StatusCode)
	}
}

func TestMemberSite_refuses_non_members_non_sites_and_foreign_tickets(t *testing.T) {
	e := newSiteEnv(t)
	e.join("room-1", "alice")
	e.putSite("room-1", "art_site", testSiteFiles)
	e.mu.Lock()
	e.versions["room-1/art_doc/1"] = store.ArtifactVersion{ArtifactID: "art_doc", Version: 1, Storage: store.ArtifactStorageInline, Content: "# hi", MimeType: "text/markdown"}
	e.mu.Unlock()

	if status, _ := e.ticket("bob", "room-1", "art_site"); status != http.StatusForbidden {
		t.Fatalf("non-member ticket status %d", status)
	}
	if status, _ := e.ticket("alice", "room-1", "art_doc"); status != http.StatusNotFound {
		t.Fatalf("non-site ticket status %d", status)
	}
	if resp, _ := e.do(http.MethodPost, "/v1/rooms/room-1/artifacts/art_site/versions/1/site-ticket", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous ticket status %d", resp.StatusCode)
	}
	// A session bearer or another ticket kind is not a site ticket.
	previewer := e.svc.Signer.Issue(auth.Claims{Subject: "alice", ExpiresAtUnix: time.Now().Add(time.Hour).Unix(), Kind: auth.KindPreviewerFrame, ID: "room-1/art_site/1"})
	for _, bad := range []string{e.session("alice"), previewer, "garbage"} {
		if resp, _ := e.do(http.MethodGet, "/v1/sites/"+url.PathEscape(bad)+"/", ""); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("ticket %q status %d", bad, resp.StatusCode)
		}
	}
}

func TestSiteFile_rules(t *testing.T) {
	raw, err := tarbundle.PackFiles(map[string][]byte{
		"index.html": []byte("root"), "about/index.html": []byte("about"), "app.js": []byte("js"),
	}, tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tarbundle.Parse(raw, "", tarbundle.SiteLimits)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, file, redirect string }{
		{"", "index.html", ""},
		{"app.js", "app.js", ""},
		{"about/", "about/index.html", ""},
		{"about", "", "about/"},
		{"settings/profile", "index.html", ""},
		{"nope/", "index.html", ""},
		{"missing.css", "", ""},
		{"a b/c", "index.html", ""},
		{"../etc/passwd.txt", "", ""},
	}
	for _, c := range cases {
		file, redirect := siteFile(b, c.in)
		if file != c.file || redirect != c.redirect {
			t.Errorf("siteFile(%q) = (%q,%q), want (%q,%q)", c.in, file, redirect, c.file, c.redirect)
		}
	}
}
