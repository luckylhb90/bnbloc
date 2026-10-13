package gateway

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// An app's cover (docs/prd/2026-10-13-app-gallery.md §4, §9): the image a
// card shows, as tarbundle.SiteCover reads it (the one rule, shared with
// the app component validator). It is served as an image only, with
// nosniff and a sandbox CSP. Without one (404) or with one refused (413,
// 415) the web draws the generated cover (web/src/lib/appCover.ts).

// handleSiteCover answers a site version's cover:
// GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/cover.
// The caller must reach the room and the room service's read check then
// decides as them (memberSiteVersion, as a site ticket): a non-member, an
// artifact of another room or a version that is not a site is a 404, like a
// site without a cover.
func (s *Service) handleSiteCover(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	scope, roomID, ok := siteVersionPath(w, r)
	if !ok {
		return
	}
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		httpx.Error(w, "room scope not authorized", http.StatusForbidden)
		return
	}
	sv, err := s.memberSiteVersion(r.Context(), claims.Subject, claims.Tenant, scope)
	if errors.Is(err, errSiteNotFound) {
		httpx.Error(w, "site version not found", http.StatusNotFound)
		return
	}
	if err != nil {
		obs.ServerError(w, "load site version failed", err)
		httpx.Error(w, "load site version failed", http.StatusBadGateway)
		return
	}
	b, err := s.siteBundle(r.Context(), sv)
	if err != nil {
		obs.ServerError(w, "load site bundle failed", err)
		httpx.Error(w, "load site bundle failed", http.StatusBadGateway)
		return
	}
	cover, found, err := tarbundle.SiteCover(b)
	switch {
	case !found:
		httpx.Error(w, "the site has no cover", http.StatusNotFound)
		return
	case errors.Is(err, tarbundle.ErrCoverTooLarge):
		httpx.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		httpx.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		return
	}
	writeCover(w, r, cover)
}

// writeCover answers a cover as an image only: its checked type, nosniff,
// a sandbox CSP (a site version's and an app version's alike).
func writeCover(w http.ResponseWriter, r *http.Request, cover tarbundle.Cover) {
	h := w.Header()
	h.Set("Content-Type", cover.MimeType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A version never changes: its cover is cached by the member's browser.
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Length", strconv.Itoa(len(cover.Data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(cover.Data)
}
