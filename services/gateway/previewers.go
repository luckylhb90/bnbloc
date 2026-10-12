package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/wire"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/previewerruntime"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Previewer components (bnbloc.previewer.package/v1) in the gateway: the
// install-time bundle probe, the member-catalog block, and the browser frame
// route that serves bundle files to a sandboxed opaque-origin iframe. See
// docs/architecture/previewer-components.md.

// previewerFrameTTL bounds a frame ticket: the catalog mints a fresh one on
// every read, and the web host re-reads the catalog before it expires.
const previewerFrameTTL = time.Hour

// previewerCatalogInfo is the member-catalog block of an installed previewer
// (componentInfo.Previewer).
type previewerCatalogInfo struct {
	pkgmeta.Previewer
	// BundleURL is the gateway artifact URL of the archive (the desktop
	// downloads it and checks BundleSHA256).
	BundleURL    string `json:"bundleUrl"`
	BundleSHA256 string `json:"bundleSha256"`
	// FrameURL is the gateway-relative browser frame base (ticket included);
	// the host appends Entry.
	FrameURL         string `json:"frameUrl"`
	FrameExpiresAtMs int64  `json:"frameExpiresAtMs"`
}

func (s *Service) previewerInstaller() extpipe.KindInstaller {
	return previewerruntime.Installer(s.probePreviewerBundle)
}

// probePreviewerBundle proves at install that the pinned bundle exists in the
// artifact store, matches its sha256, is a valid archive, and holds the
// entry document.
func (s *Service) probePreviewerBundle(ctx context.Context, _ extpipe.Record, cfg extpipe.PackageConfig) error {
	art, ok := previewerruntime.Bundle(cfg)
	if !ok {
		return fmt.Errorf("previewer requires artifact %q", pkgmeta.PreviewerBundleArtifact)
	}
	b, err := s.fetchPreviewerBundle(ctx, art)
	if err != nil {
		return fmt.Errorf("artifacts.%s: %w", pkgmeta.PreviewerBundleArtifact, err)
	}
	return b.RequireEntry(cfg.Previewer.Entry)
}

// previewerCatalog builds an installed previewer's catalog block, minting a
// frame ticket bound to the caller and this exact version.
func (s *Service) previewerCatalog(claims auth.Claims, rec extpipe.Record, cfg extpipe.PackageConfig) *previewerCatalogInfo {
	art, ok := previewerruntime.Bundle(cfg)
	if !ok {
		return nil
	}
	expires := s.nowTime().Add(previewerFrameTTL)
	ticket := s.Signer.Issue(auth.Claims{
		Subject:       claims.Subject,
		Tenant:        claims.Tenant,
		Role:          claims.Role,
		ExpiresAtUnix: expires.Unix(),
		Kind:          auth.KindPreviewerFrame,
		ID:            previewerTicketScope(rec.Name, rec.Version),
	})
	return &previewerCatalogInfo{
		Previewer:        *cfg.Previewer,
		BundleURL:        art.URL,
		BundleSHA256:     art.SHA256,
		FrameURL:         previewerFrameBase(rec.Name, rec.Version) + url.PathEscape(ticket) + "/",
		FrameExpiresAtMs: expires.UnixMilli(),
	}
}

func previewerTicketScope(name, version string) string { return name + "@" + version }

func previewerFrameBase(name, version string) string {
	return "/v1/previewers/" + url.PathEscape(name) + "/" + url.PathEscape(version) + "/"
}

// handlePreviewerFile serves one file of an installed previewer's verified
// bundle to a browser frame: GET /v1/previewers/{name}/{version}/{ticket}/{path...}.
// The ticket (not a bearer: an iframe src cannot carry one) must be valid
// for this name@version, and the rollout + tenant gate is re-checked on
// every request. Anything else is a 404, indistinguishable from a missing
// file.
func (s *Service) handlePreviewerFile(w http.ResponseWriter, r *http.Request) {
	name, version, file := r.PathValue("name"), r.PathValue("version"), r.PathValue("path")
	if !core.ValidBundlePath(file) {
		http.NotFound(w, r)
		return
	}
	claims, err := s.Signer.VerifyKind(r.PathValue("ticket"), auth.KindPreviewerFrame)
	if err != nil || claims.ID != previewerTicketScope(name, version) {
		http.NotFound(w, r)
		return
	}
	cfg, ok, err := s.visiblePreviewer(r.Context(), claims, name, version)
	if err != nil {
		obs.ServerError(w, "load previewer failed", err)
		httpx.Error(w, "load previewer failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	art, _ := previewerruntime.Bundle(cfg)
	b, err := s.fetchPreviewerBundle(r.Context(), art)
	if err != nil {
		obs.ServerError(w, "load previewer bundle failed", err)
		httpx.Error(w, "load previewer bundle failed", http.StatusBadGateway)
		return
	}
	data, ok := b.File(file)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", tarbundle.ContentType(file))
	h.Set("Content-Security-Policy", previewerCSP(publicBaseURL(r)+previewerFrameBase(name, version)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// Module scripts and fonts load in CORS mode from the frame's opaque
	// origin ("null"); the bytes are already readable by whoever holds the
	// ticket, so a wildcard discloses nothing more.
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Length", fmt.Sprint(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// previewerCSP is the policy of every served bundle file: scripts, styles
// and fonts only from the bundle itself, images/media only from blob:/data:
// (the file bytes the host transfers), no network at all, and framing only by
// the app. bundleBase names the bundle path explicitly besides 'self'
// because engines differ on what 'self' means in an opaque-origin document.
func previewerCSP(bundleBase string) string {
	return "default-src 'none'; script-src 'self' " + bundleBase +
		"; style-src 'self' " + bundleBase + " 'unsafe-inline'; img-src blob: data:; media-src blob: data:; font-src 'self' " +
		bundleBase + " data:; connect-src 'none'; frame-ancestors 'self'"
}

// visiblePreviewer resolves an installed previewer the claims may use, at
// exactly the requested version.
func (s *Service) visiblePreviewer(ctx context.Context, claims auth.Claims, name, version string) (extpipe.PackageConfig, bool, error) {
	view, err := s.loadMemberComponents(ctx, claims)
	if err != nil {
		return extpipe.PackageConfig{}, false, err
	}
	rec, ok := view.record(extreg.KindPreviewer, name)
	if !ok || rec.Version != version || !view.visibility(rec).Installed {
		return extpipe.PackageConfig{}, false, nil
	}
	cfg, err := previewerruntime.ValidateRecord(rec)
	if err != nil {
		return extpipe.PackageConfig{}, false, err
	}
	return cfg, true, nil
}

// fetchPreviewerBundle returns the verified, parsed bundle pinned by art,
// from the cache or the artifact store (via realtime).
func (s *Service) fetchPreviewerBundle(ctx context.Context, art pkgmeta.Artifact) (*tarbundle.Bundle, error) {
	previewers, _ := s.bundleCaches()
	return previewers.LoadPinned(art.SHA256, tarbundle.PreviewerLimits, func() ([]byte, error) {
		key := artifactURLKey(art.URL)
		if key == "" {
			return nil, fmt.Errorf("bundle URL is not a gateway artifact")
		}
		return s.readArtifactBlob(ctx, key, tarbundle.PreviewerLimits.MaxArchiveBytes())
	})
}

// readArtifactBlob reads one blob from realtime's store on the gateway's own
// behalf, refusing more than limit bytes.
func (s *Service) readArtifactBlob(ctx context.Context, key string, limit int64) ([]byte, error) {
	if strings.TrimSpace(s.Backends.Realtime) == "" {
		return nil, fmt.Errorf("artifact store not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rc, _, err := s.blobs().OpenBlob(ctx, key)
	if err != nil {
		return nil, blobOpenErr(err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("artifact read failed")
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("artifact exceeds %d bytes", limit)
	}
	return raw, nil
}

// Bundle cache bounds (file bytes kept in memory): previewer bundles, and
// site snapshots (PRD 2026-10-09 "Site file rules": 256 MiB).
const (
	previewerCacheBytes = 64 << 20
	siteCacheBytes      = 256 << 20
)

// bundleCaches returns the shared verified-bundle caches (previewers,
// sites), built on first use.
func (s *Service) bundleCaches() (previewers, sites *tarbundle.Cache) {
	s.bundleCachesOnce.Do(func() {
		s.previewerBundles = tarbundle.NewCache(previewerCacheBytes)
		s.siteBundles = tarbundle.NewCache(siteCacheBytes)
		s.siteAuth = newBoundedCache[siteVersion](siteAuthTTL, siteCacheMaxEntries)
		s.publicSites = newBoundedCache[wire.ShareResolution](publicSiteResolveTTL, siteCacheMaxEntries)
	})
	return s.previewerBundles, s.siteBundles
}
