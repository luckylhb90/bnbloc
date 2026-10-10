// Package webui serves the bnbloc collaboration web client as an embedded SPA.
// It is served same-origin by bnbloc-gateway, so the browser talks to /v1/* on
// the same host with no CORS. The client source lives in web/ (React + Vite +
// Tailwind); `make web` builds it into static/ for local work, and the gateway
// image builds it in its own Node stage (Dockerfile, web-full). The built
// assets are NOT committed — static/ is gitignored.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var files embed.FS

// Handler returns an http.Handler serving the embedded web client. Unknown
// paths fall back to index.html so the SPA owns client-side routing.
//
// Cache headers are a correctness contract, not an optimization: every
// asset filename carries a content hash (Vite), so /assets/* can be cached
// forever. index.html CANNOT: it names the current assets, and an embedded
// webview (Tauri/WKWebView) will happily serve a stale copy from its disk
// cache across app updates — the client then runs yesterday's UI against
// today's gateway, which looked exactly like "the client is broken but the
// browser is fine". index.html (and the SPA fallback that serves it) is
// sent with no-store so every load picks up the current asset set.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isIndex := r.URL.Path == "/" || r.URL.Path == "/index.html"
		if _, statErr := fs.Stat(sub, trimLeadingSlash(r.URL.Path)); statErr != nil && r.URL.Path != "/" {
			// SPA fallback: serve index.html for unknown client routes.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			isIndex = true
		}
		if isIndex {
			w.Header().Set("Cache-Control", "no-store")
		} else if r.URL.Path == ServiceWorkerPath {
			// The browser push service worker (N4): served from the web
			// root so its default scope is the whole app, and revalidated on
			// every update check so a deploy's worker replaces the old one.
			w.Header().Set("Cache-Control", "no-cache")
		} else if r.URL.Path == ManifestPath {
			// The installable web app manifest (R14). Go's MIME table has no
			// .webmanifest entry, so the type is set here; revalidated so a
			// name/icon change reaches installed apps.
			w.Header().Set("Content-Type", "application/manifest+json")
			w.Header().Set("Cache-Control", "no-cache")
		} else if r.URL.Path == ArtifactRunnerPath {
			// The app frames the artifact runner (same origin): say so
			// explicitly. CSP frame-ancestors supersedes X-Frame-Options, so an
			// edge that adds "X-Frame-Options: DENY" to every response cannot
			// break previews, while other origins still cannot frame it.
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
			w.Header().Set("Cache-Control", "no-cache")
		} else if len(r.URL.Path) > 8 && r.URL.Path[:8] == "/assets/" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ServiceWorkerPath is the app's one service worker (web/public): browser
// push (N4) and the installed app's offline shell (R14).
const ServiceWorkerPath = "/push-sw.js"

// ArtifactRunnerPath is the sandboxed artifact runner page the app frames.
const ArtifactRunnerPath = "/artifact-runner.html"

// ManifestPath is the installable web app manifest (web/public).
const ManifestPath = "/manifest.webmanifest"

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
