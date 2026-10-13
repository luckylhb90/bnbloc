package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// siteFileReadMaxBytes bounds the one file handleSiteFileRead answers.
const siteFileReadMaxBytes = 4 << 20

// handleSiteFileRead answers one file of a site version, raw, on the main
// origin:
// GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/files/{path...}
//
// It is how a room tool (read_artifact) reads a site deliverable with the
// bridge's session. A site URL may be on the sites origin, which never sees
// a session, so an agent never reads through a site ticket. The caller must
// reach the room; with ?job= the bridge running that job reads as the job's
// requester (jobGrant, the content routes' rule). The room service's own
// read check then decides as that member (memberSiteVersion): a non-member,
// an artifact of another room or a version that is not a site is a 404.
// path names a bundle file exactly (no directory index, no SPA fallback, no
// rewrite); a file over siteFileReadMaxBytes is a 413. The bytes come as a
// sandboxed attachment, never rendered on the main origin.
func (s *Service) handleSiteFileRead(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	scope, roomID, ok := siteVersionPath(w, r)
	if !ok {
		return
	}
	file := r.PathValue("path")
	if !core.ValidBundlePath(file) {
		httpx.Error(w, "path must name a file of the site, e.g. index.html", http.StatusBadRequest)
		return
	}
	claims, ok := s.roomClaims(caller, roomID)
	if !ok {
		httpx.Error(w, "room scope not authorized", http.StatusForbidden)
		return
	}
	if job := strings.TrimSpace(r.URL.Query().Get("job")); job != "" {
		actor, err := s.jobGrant(r.Context(), caller, claims, roomID, job)
		if err != nil {
			writeGrantErr(w, err)
			return
		}
		claims = actor.Claims
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
	data, ok := b.File(file)
	if !ok {
		httpx.Error(w, "the site has no file "+file, http.StatusNotFound)
		return
	}
	if len(data) > siteFileReadMaxBytes {
		httpx.Error(w, fmt.Sprintf("%s is %d bytes, more than a read answers (%d): ask the person to open the site", file, len(data), siteFileReadMaxBytes),
			http.StatusRequestEntityTooLarge)
		return
	}
	h := w.Header()
	h.Set("Content-Type", tarbundle.ContentType(file))
	h.Set("Content-Disposition", httpx.ContentDisposition("attachment", path.Base(file)))
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
