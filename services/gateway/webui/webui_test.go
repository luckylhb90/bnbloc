package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The push service worker is served from the web root (default scope "/")
// as JavaScript and never cached, so a deploy's worker replaces the old one.
func TestServiceWorkerHeaders(t *testing.T) {
	if _, err := fs.Stat(files, "static"+ServiceWorkerPath); err != nil {
		t.Skip("web bundle not built into webui/static")
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ServiceWorkerPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("Content-Type = %q, want text/javascript", ct)
	}
}

// The web app manifest is served as application/manifest+json (Go's MIME
// table does not know .webmanifest) and revalidated on every load.
func TestManifestHeaders(t *testing.T) {
	if _, err := fs.Stat(files, "static"+ManifestPath); err != nil {
		t.Skip("web bundle not built into webui/static")
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ManifestPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("Content-Type = %q, want application/manifest+json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
}

// The artifact runner is framed by the app itself, so it declares
// frame-ancestors 'self' (which supersedes an edge X-Frame-Options: DENY).
func TestArtifactRunnerFrameAncestors(t *testing.T) {
	if _, err := fs.Stat(files, "static"+ArtifactRunnerPath); err != nil {
		t.Skip("web bundle not built into webui/static")
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ArtifactRunnerPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "frame-ancestors 'self'" {
		t.Fatalf("Content-Security-Policy = %q, want frame-ancestors 'self'", csp)
	}
}

// A hashed file the build does not have is a 404, never index.html: a page
// of a newer build asking an older pod for its chunk must not get HTML a
// service worker would keep as that chunk
// (docs/bugs/2026-10-28-cached-html-as-chunk.md). Client routes still get
// the page.
func TestMissingAssetIsNotThePage(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/Gone-AbCdEf12.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset: status %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<!doctype") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("missing asset answered with a page: %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("missing asset Cache-Control = %q, want no-store", cc)
	}
	if _, err := fs.Stat(files, "static/index.html"); err != nil {
		t.Skip("web bundle not built into webui/static")
	}
	rec = httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/client/route", nil))
	if rec.Code != http.StatusOK || !strings.Contains(strings.ToLower(rec.Body.String()), "<!doctype html") {
		t.Fatalf("client route: status %d, body %.60q", rec.Code, rec.Body.String())
	}
}
