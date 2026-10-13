package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// An app's cover (docs/prd/2026-10-13-app-gallery.md §4, §9): the image a
// card shows, cover.png, cover.webp or cover.jpg (.jpeg) at the root of the
// site's bundle, the first of these present. It is served as an image only:
// its bytes must sniff as the type its name says (an SVG, HTML or any other
// content is never a cover), at most wire.MaxAppCoverBytes, with nosniff
// and a sandbox CSP. Without one (404) or with one refused (413, 415) the
// web draws the generated cover (web/src/lib/appCover.ts).

// siteCoverFiles are the cover file names, in order, with the one media
// type each may hold.
var siteCoverFiles = []struct{ name, mimeType string }{
	{"cover.png", "image/png"},
	{"cover.webp", "image/webp"},
	{"cover.jpg", "image/jpeg"},
	{"cover.jpeg", "image/jpeg"},
}

// siteCover returns the bundle's cover and its type: found=false when the
// site has none; err when its cover is refused (errCoverTooLarge,
// errCoverNotImage).
func siteCover(b *tarbundle.Bundle) (data []byte, mimeType string, found bool, err error) {
	for _, f := range siteCoverFiles {
		data, ok := b.File(f.name)
		if !ok {
			continue
		}
		if len(data) > wire.MaxAppCoverBytes {
			return nil, "", true, errCoverTooLarge
		}
		if http.DetectContentType(data) != f.mimeType {
			return nil, "", true, errCoverNotImage
		}
		return data, f.mimeType, true, nil
	}
	return nil, "", false, nil
}

var (
	errCoverTooLarge = fmt.Errorf("the cover is larger than %d bytes", wire.MaxAppCoverBytes)
	errCoverNotImage = errors.New("the cover is not the PNG, WebP or JPEG image its name says")
)

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
	data, mimeType, found, err := siteCover(b)
	switch {
	case !found:
		httpx.Error(w, "the site has no cover", http.StatusNotFound)
		return
	case errors.Is(err, errCoverTooLarge):
		httpx.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		httpx.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mimeType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A version never changes: its cover is cached by the member's browser.
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
