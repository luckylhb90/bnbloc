package gateway

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
)

func previewerBundleBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var testPreviewerFiles = map[string]string{
	"index.html":       `<!doctype html><script src="main.js"></script>`,
	"main.js":          `console.log("previewer")`,
	"assets/style.css": `body{margin:0}`,
}

func previewerPackageBody(t *testing.T, name, version string, bundle pkgmeta.Artifact) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"kind":        pkgmeta.KindPreviewerPackageV1,
		"contract":    pkgmeta.PreviewerContractV1,
		"runtime":     "bundle",
		"name":        name,
		"version":     version,
		"publisher":   "Example Co",
		"description": "sprite sheets",
		"artifacts":   map[string]any{"bundle": map[string]string{"url": bundle.URL, "sha256": bundle.SHA256}},
		"previewer": map[string]any{
			"entry":        "index.html",
			"match":        map[string]any{"mimeTypes": []string{"image/*"}, "extensions": []string{".atlas.json"}},
			"rank":         50,
			"capabilities": []string{"thumbnail", "toolbar"},
			"toolbar":      []map[string]string{{"id": "grid", "icon": "grid", "label": "Grid", "kind": "toggle"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func installPreviewer(t *testing.T, srv *httptest.Server, adminToken, name, version string) pkgmeta.Artifact {
	t.Helper()
	bundle := uploadComponentArtifact(t, srv.URL, adminToken, previewerBundleBytes(t, testPreviewerFiles))
	if raw, status := postComponent(t, srv.URL, adminToken, previewerPackageBody(t, name, version, bundle), ""); status != http.StatusOK {
		t.Fatalf("install previewer status=%d body=%s, want 200", status, raw)
	}
	return bundle
}

type catalogPreviewer struct {
	Entry            string   `json:"entry"`
	Rank             int      `json:"rank"`
	Capabilities     []string `json:"capabilities"`
	BundleURL        string   `json:"bundleUrl"`
	BundleSHA256     string   `json:"bundleSha256"`
	FrameURL         string   `json:"frameUrl"`
	FrameExpiresAtMs int64    `json:"frameExpiresAtMs"`
	Match            struct {
		MimeTypes  []string `json:"mimeTypes"`
		Extensions []string `json:"extensions"`
	} `json:"match"`
	Toolbar []map[string]string `json:"toolbar"`
}

func memberPreviewers(t *testing.T, baseURL, token string) map[string]map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/components?kind=previewer", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Components []map[string]any `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, c := range out.Components {
		if c["kind"] != "previewer" {
			t.Fatalf("?kind=previewer listed %v", c["kind"])
		}
		byName[c["name"].(string)] = c
	}
	return byName
}

func catalogBlock(t *testing.T, entry map[string]any) catalogPreviewer {
	t.Helper()
	raw, _ := json.Marshal(entry["previewer"])
	var p catalogPreviewer
	if err := json.Unmarshal(raw, &p); err != nil || p.FrameURL == "" {
		t.Fatalf("previewer block = %s (%v)", raw, err)
	}
	return p
}

func getFrameFile(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestPreviewerCatalog_listsInstalledPreviewerWithBundleAndFrame(t *testing.T) {
	// Given an installed previewer.
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	bundle := installPreviewer(t, srv, adminToken, "sprites", "1.0.0")

	// When the member reads the previewer catalog.
	entry := memberPreviewers(t, srv.URL, memberToken)["sprites"]

	// Then it carries the match metadata, the pinned bundle and a frame base.
	if entry == nil || entry["runtime"] != "bundle" || entry["contract"] != pkgmeta.PreviewerContractV1 || entry["installed"] != true || entry["requiresDesktop"] != nil {
		t.Fatalf("previewer entry = %+v", entry)
	}
	p := catalogBlock(t, entry)
	if p.Entry != "index.html" || p.Rank != 50 || len(p.Capabilities) != 2 || p.Match.MimeTypes[0] != "image/*" || p.Toolbar[0]["id"] != "grid" {
		t.Fatalf("previewer block = %+v", p)
	}
	if p.BundleURL != bundle.URL || p.BundleSHA256 != bundle.SHA256 || !strings.HasPrefix(p.FrameURL, "/v1/previewers/sprites/1.0.0/") || !strings.HasSuffix(p.FrameURL, "/") || p.FrameExpiresAtMs == 0 {
		t.Fatalf("bundle/frame = %+v", p)
	}
	// The desktop downloads the archive through the entitlement-gated route.
	if got := getArtifactStatus(t, srv, memberToken, bundle); got != http.StatusOK {
		t.Fatalf("member bundle download = %d, want 200", got)
	}
}

func TestPreviewerFrame_servesBundleFilesWithSandboxHeaders(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installPreviewer(t, srv, adminToken, "sprites", "1.0.0")
	base := catalogBlock(t, memberPreviewers(t, srv.URL, memberToken)["sprites"]).FrameURL

	resp, body := getFrameFile(t, srv, base+"index.html")
	if resp.StatusCode != http.StatusOK || body != testPreviewerFiles["index.html"] {
		t.Fatalf("entry = %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self' " + srv.URL + "/v1/previewers/sprites/1.0.0/", "img-src blob: data:", "media-src blob: data:", "connect-src 'none'", "frame-ancestors 'self'", "style-src 'self' "} {
		if !strings.Contains(csp, want) {
			t.Fatalf("csp %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self' "+srv.URL+"/v1/previewers/sprites/1.0.0/ 'unsafe-inline'") || strings.Contains(csp, "http: ") || strings.Contains(csp, " * ") {
		t.Fatalf("csp too broad: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("headers = %+v", resp.Header)
	}
	// The files are the bundle's (sha-pinned) under a ticketed URL: cacheable
	// until the ticket expires, never shared.
	var maxAge int
	if cc := resp.Header.Get("Cache-Control"); !strings.HasPrefix(cc, "private, max-age=") || !strings.HasSuffix(cc, ", immutable") {
		t.Fatalf("cache-control = %q", cc)
	} else if _, err := fmt.Sscanf(strings.TrimPrefix(cc, "private, max-age="), "%d", &maxAge); err != nil || maxAge <= 0 || maxAge > int(previewerFrameTTL.Seconds()) {
		t.Fatalf("max-age %d from %q", maxAge, cc)
	}

	// Relative assets resolve under the same ticketed base.
	if resp, body := getFrameFile(t, srv, base+"assets/style.css"); resp.StatusCode != http.StatusOK || body != testPreviewerFiles["assets/style.css"] || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("asset = %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	if resp, body := getFrameFile(t, srv, base+"main.js"); resp.StatusCode != http.StatusOK || body != testPreviewerFiles["main.js"] || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("script = %d %q", resp.StatusCode, body)
	}
}

func TestPreviewerFrame_refusesTraversalUnknownFilesAndBadTickets(t *testing.T) {
	svc, srv, adminToken, memberToken := rolloutTestGateway(t)
	installPreviewer(t, srv, adminToken, "sprites", "1.0.0")
	base := catalogBlock(t, memberPreviewers(t, srv.URL, memberToken)["sprites"]).FrameURL
	ticket := strings.TrimSuffix(strings.TrimPrefix(base, "/v1/previewers/sprites/1.0.0/"), "/")

	for _, path := range []string{
		base + "missing.js",
		base + "..%2findex.html",
		base + "%2e%2e/%2e%2e/etc/passwd",
		base + "assets/..%2f..%2findex.html",
		base + ".%2findex.html",
		base + "INDEX.html",
		"/v1/previewers/sprites/1.0.0/not-a-ticket/index.html",
		"/v1/previewers/sprites/2.0.0/" + ticket + "/index.html",
		"/v1/previewers/other/1.0.0/" + ticket + "/index.html",
		"/v1/previewers/sprites/1.0.0/" + frameTicket(t, svc, memberToken, "sprites@2.0.0", time.Hour) + "/index.html",
		"/v1/previewers/sprites/1.0.0/" + frameTicket(t, svc, memberToken, "sprites@1.0.0", -time.Minute) + "/index.html",
	} {
		resp, _ := getFrameFile(t, srv, path)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s served (want refusal)", path)
		}
	}
	// A plain session bearer is not a frame ticket either.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/previewers/sprites/1.0.0/"+memberToken+"/index.html", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("session token as ticket = %d, want 404", resp.StatusCode)
	}
}

func TestPreviewerFrame_reChecksTheGateOnEveryRequest(t *testing.T) {
	_, srv, adminToken, memberToken := rolloutTestGateway(t)
	installPreviewer(t, srv, adminToken, "sprites", "1.0.0")
	base := catalogBlock(t, memberPreviewers(t, srv.URL, memberToken)["sprites"]).FrameURL

	// Opt-in, not installed by the member: listed without a frame, ticket dead.
	if status := setRollout(t, srv, adminToken, "sprites", "optin"); status != http.StatusNoContent {
		t.Fatalf("set optin = %d", status)
	}
	if entry := memberPreviewers(t, srv.URL, memberToken)["sprites"]; entry == nil || entry["installed"] != false || entry["previewer"] != nil || entry["artifacts"] != nil {
		t.Fatalf("opt-in previewer before install = %+v", entry)
	}
	if resp, _ := getFrameFile(t, srv, base+"index.html"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("frame while not opted in = %d, want 404", resp.StatusCode)
	}
	if status := doJSON(t, srv, http.MethodPost, "/v1/components/sprites/install?kind=previewer", memberToken, ""); status != http.StatusNoContent {
		t.Fatalf("member opt-in = %d", status)
	}
	if resp, _ := getFrameFile(t, srv, base+"index.html"); resp.StatusCode != http.StatusOK {
		t.Fatalf("frame after opt-in = %d, want 200", resp.StatusCode)
	}

	// Out of the member's tenant: hidden and dead.
	if status := doJSON(t, srv, http.MethodPut, "/v1/platform/components/sprites/tenants?kind=previewer", adminToken, `{"enabledTenants":["elsewhere"]}`); status != http.StatusNoContent {
		t.Fatalf("set tenants = %d", status)
	}
	if entry := memberPreviewers(t, srv.URL, memberToken)["sprites"]; entry != nil {
		t.Fatalf("out-of-tenant previewer leaked: %+v", entry)
	}
	if resp, _ := getFrameFile(t, srv, base+"index.html"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("frame out of tenant = %d, want 404", resp.StatusCode)
	}

	// An upgrade retires the old version's frames.
	if status := doJSON(t, srv, http.MethodPut, "/v1/platform/components/sprites/tenants", adminToken, `{"allTenants":true}`); status != http.StatusNoContent {
		t.Fatalf("clear tenants = %d", status)
	}
	installPreviewer(t, srv, adminToken, "sprites", "1.1.0")
	if resp, _ := getFrameFile(t, srv, base+"index.html"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("old version frame = %d, want 404", resp.StatusCode)
	}
}

func TestPreviewerInstall_verifiesTheBundle(t *testing.T) {
	_, srv, adminToken, _ := rolloutTestGateway(t)
	// A bundle without the declared entry document.
	noEntry := uploadComponentArtifact(t, srv.URL, adminToken, previewerBundleBytes(t, map[string]string{"main.js": "x"}))
	if raw, status := postComponent(t, srv.URL, adminToken, previewerPackageBody(t, "no-entry", "1.0.0", noEntry), ""); status != http.StatusBadRequest || !strings.Contains(string(raw), "no entry") {
		t.Fatalf("missing entry install = %d %s, want 400", status, raw)
	}
	// Not a tar archive.
	notTar := uploadComponentArtifact(t, srv.URL, adminToken, []byte("<html>not an archive</html>"))
	if raw, status := postComponent(t, srv.URL, adminToken, previewerPackageBody(t, "not-tar", "1.0.0", notTar), ""); status != http.StatusBadRequest {
		t.Fatalf("non-archive install = %d %s, want 400", status, raw)
	}
	// A bundle pin that does not match the stored bytes.
	good := uploadComponentArtifact(t, srv.URL, adminToken, previewerBundleBytes(t, testPreviewerFiles))
	other := uploadComponentArtifact(t, srv.URL, adminToken, previewerBundleBytes(t, map[string]string{"index.html": "other"}))
	forged := pkgmeta.Artifact{URL: good.URL, SHA256: other.SHA256}
	if raw, status := postComponent(t, srv.URL, adminToken, previewerPackageBody(t, "forged", "1.0.0", forged), ""); status != http.StatusBadRequest {
		t.Fatalf("forged pin install = %d %s, want 400", status, raw)
	}
}

func TestPlatformComponents_listsPreviewerPayload(t *testing.T) {
	_, srv, adminToken, _ := rolloutTestGateway(t)
	installPreviewer(t, srv, adminToken, "sprites", "1.0.0")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/platform/components", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Components []struct {
			Kind      string             `json:"kind"`
			Runtime   string             `json:"runtime"`
			Previewer *pkgmeta.Previewer `json:"previewer"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Components {
		if c.Kind == "previewer" && c.Runtime == "bundle" && c.Previewer != nil && c.Previewer.Entry == "index.html" {
			return
		}
	}
	t.Fatalf("platform list has no previewer payload: %+v", out.Components)
}

// frameTicket mints a frame ticket for the member's session with an
// arbitrary scope and lifetime (a negative ttl is already expired).
func frameTicket(t *testing.T, svc *Service, sessionToken, scope string, ttl time.Duration) string {
	t.Helper()
	c, err := svc.Signer.Verify(sessionToken)
	if err != nil {
		t.Fatal(err)
	}
	c.Kind, c.ID, c.ExpiresAtUnix = auth.KindPreviewerFrame, scope, time.Now().Add(ttl).Unix()
	return svc.Signer.Issue(c)
}
