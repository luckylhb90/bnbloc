package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Site snapshots (PRD 2026-10-09 "Public share links and live web-app
// previews"): a `site` artifact version is a tar bundle in realtime's blob
// store; the gateway serves its files to a sandboxed frame. Members reach a
// version through a short-lived ticket (POST …/site-ticket, then
// GET /v1/sites/{ticket}/{path...}); outsiders through a public share link
// (public_shares.go). Both routes serve through serveSite, the one
// implementation of the site file rules.

const (
	// siteTicketTTL bounds a member site ticket. A running app keeps the URL
	// it was loaded from (lazy-loaded files use it), so the ticket outlives
	// a working session; it names one version, and membership is re-checked
	// at most every siteAuthTTL, so a long life grants nothing new.
	siteTicketTTL = 12 * time.Hour
	// siteAuthTTL bounds reuse of a positive room-level read check for one
	// subject and site version.
	siteAuthTTL = 30 * time.Second
	// publicSiteResolveTTL bounds reuse of a share resolution by the public
	// site file route (a page load fetches many files); a revoked link stops
	// serving site files within it.
	publicSiteResolveTTL = 10 * time.Second
	// siteCacheMaxEntries caps each site authorization cache.
	siteCacheMaxEntries = 10000
)

// siteCSP is the policy of every served site file: an opaque origin even
// when opened top-level (no gateway cookies or storage, no same-origin
// reach into the app), scripts/forms/popups/modals/downloads allowed, network
// allowed so apps can use public APIs and CDNs, framable anywhere.
const siteCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; frame-ancestors *"

// errSiteNotFound is every "no such readable site" outcome: unknown, not a
// member, not a site version. Callers answer 404.
var errSiteNotFound = errors.New("site not found")

// siteVersion is an authorized site snapshot: what loading its bundle needs.
type siteVersion struct {
	ContentHash string
	BlobKey     string
}

// siteVersionOf returns the site snapshot of a version, ok=false when the
// version is not one (inline, another type, or malformed metadata).
func siteVersionOf(storage, mimeType, blobKey, contentHash string) (siteVersion, bool) {
	if storage != store.ArtifactStorageBlob || store.MediaType(mimeType) != tarbundle.SiteMIME ||
		blobKey == "" || !isSHA256Hex(contentHash) {
		return siteVersion{}, false
	}
	return siteVersion{ContentHash: contentHash, BlobKey: blobKey}, true
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// siteTicketScope is a ticket's ID: the one version it reads.
func siteTicketScope(roomID, artifactID string, version int) string {
	return roomID + "/" + artifactID + "/" + strconv.Itoa(version)
}

func parseSiteTicketScope(id string) (roomID, artifactID string, version int, ok bool) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", 0, false
	}
	v, err := strconv.Atoi(parts[2])
	if err != nil || v <= 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], v, true
}

// handleSiteTicket mints a member's site ticket:
// POST /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/site-ticket
// → {siteUrl: "/v1/sites/<ticket>/", expiresAtMs}. The caller must be a room
// member (the edge room scope, then the room service's own read check while
// the version is fetched) and the version a site snapshot.
func (s *Service) handleSiteTicket(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	roomID, artifactID := r.PathValue("roomId"), r.PathValue("artifactId")
	version, err := strconv.Atoi(r.PathValue("version"))
	if roomctx.ValidateID(roomID) != nil || artifactID == "" || strings.Contains(artifactID, "/") || err != nil || version <= 0 {
		http.Error(w, "invalid site version path", http.StatusBadRequest)
		return
	}
	if !claims.HasRoom(roomID) && !s.subjectInRoom(claims.Subject, roomID, claims.Tenant) {
		http.Error(w, "room scope not authorized", http.StatusForbidden)
		return
	}
	tenantID := claims.Tenant
	if s.RoomTenant != nil {
		if rt := s.RoomTenant(roomID); rt != "" {
			tenantID = rt
		}
	}
	scope := siteTicketScope(roomID, artifactID, version)
	sv, err := s.memberSiteVersion(r.Context(), claims.Subject, tenantID, scope)
	if errors.Is(err, errSiteNotFound) {
		http.Error(w, "site version not found", http.StatusNotFound)
		return
	}
	if err != nil {
		obs.ServerError(w, "load site version failed", err)
		http.Error(w, "load site version failed", http.StatusBadGateway)
		return
	}
	s.cacheSiteAuth(claims.Subject, scope, sv)
	expires := s.nowTime().Add(siteTicketTTL)
	ticket := s.Signer.Issue(auth.Claims{
		Subject:       claims.Subject,
		Tenant:        tenantID,
		Role:          claims.Role,
		ExpiresAtUnix: expires.Unix(),
		Kind:          auth.KindSiteFrame,
		ID:            scope,
	})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"siteUrl":     memberSiteBase(ticket),
		"expiresAtMs": expires.UnixMilli(),
	})
}

func memberSiteBase(ticket string) string { return "/v1/sites/" + url.PathEscape(ticket) + "/" }

// handleMemberSite serves one file of a member's site version:
// GET /v1/sites/{ticket}/{path...}. The ticket (an iframe src cannot carry
// the bearer) names the subject and the version; the room-level read is
// re-checked whenever no positive check for this subject is cached (≤
// siteAuthTTL). Anything unreadable is a 404.
func (s *Service) handleMemberSite(w http.ResponseWriter, r *http.Request) {
	ticket := r.PathValue("ticket")
	claims, err := s.Signer.VerifyKind(ticket, auth.KindSiteFrame)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, _, _, ok := parseSiteTicketScope(claims.ID); !ok {
		http.NotFound(w, r)
		return
	}
	s.bundleCaches()
	key := claims.Subject + "\x00" + claims.ID
	sv, ok := s.siteAuth.get(key)
	if !ok {
		sv, err = s.memberSiteVersion(r.Context(), claims.Subject, claims.Tenant, claims.ID)
		if errors.Is(err, errSiteNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			obs.ServerError(w, "load site version failed", err)
			http.Error(w, "load site version failed", http.StatusBadGateway)
			return
		}
		s.cacheSiteAuth(claims.Subject, claims.ID, sv)
	}
	s.serveSite(w, r, sv, memberSiteBase(ticket))
}

func (s *Service) cacheSiteAuth(subject, scope string, sv siteVersion) {
	s.bundleCaches()
	s.siteAuth.put(subject+"\x00"+scope, sv)
}

// handleSiteRootRedirect sends a site base without its trailing slash to
// the base (relative URLs in index.html resolve against the directory).
func (s *Service) handleSiteRootRedirect(w http.ResponseWriter, r *http.Request) {
	target := r.URL.EscapedPath() + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

// memberSiteVersion reads a site version from the room service AS the
// subject (svcauth-signed, in tenantID), so the room's own membership and
// artifact checks decide. scope is siteTicketScope.
func (s *Service) memberSiteVersion(ctx context.Context, subject, tenantID, scope string) (siteVersion, error) {
	roomID, artifactID, version, ok := parseSiteTicketScope(scope)
	if !ok {
		return siteVersion{}, errSiteNotFound
	}
	base := strings.TrimRight(s.Backends.Room, "/")
	if base == "" {
		return siteVersion{}, fmt.Errorf("room service not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/rooms/"+url.PathEscape(roomID)+
		"/artifacts/"+url.PathEscape(artifactID)+"/versions/"+strconv.Itoa(version), nil)
	if err != nil {
		return siteVersion{}, err
	}
	if tenantID == "" {
		tenantID = tenant.Default
	}
	req.Header.Set(svcauth.HeaderUser, subject)
	req.Header.Set(svcauth.HeaderTenant, tenantID)
	svcauth.Sign(req, svcauth.KeyFromEnv(), subject)
	resp, err := backendClient.Do(req)
	if err != nil {
		return siteVersion{}, fmt.Errorf("room service unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
		return siteVersion{}, errSiteNotFound
	default:
		return siteVersion{}, fmt.Errorf("room service answered %d", resp.StatusCode)
	}
	var out struct {
		Version store.ArtifactVersion `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return siteVersion{}, fmt.Errorf("decode artifact version: %w", err)
	}
	sv, ok := siteVersionOf(out.Version.Storage, out.Version.MimeType, out.Version.BlobKey, out.Version.ContentHash)
	if !ok {
		return siteVersion{}, errSiteNotFound
	}
	return sv, nil
}

// backendClient performs the gateway's own short JSON calls to backends.
var backendClient = &http.Client{Timeout: 15 * time.Second}

// siteBundle returns the verified, parsed snapshot of sv from the cache or
// the blob store (read on the gateway's own behalf: the caller authorized).
func (s *Service) siteBundle(ctx context.Context, sv siteVersion) (*tarbundle.Bundle, error) {
	_, sites := s.bundleCaches()
	return sites.GetOrLoad(sv.ContentHash, func() (*tarbundle.Bundle, error) {
		raw, err := s.readArtifactBlob(ctx, sv.BlobKey, tarbundle.SiteLimits.MaxArchiveBytes())
		if err != nil {
			return nil, err
		}
		return tarbundle.Parse(raw, sv.ContentHash, tarbundle.SiteLimits)
	})
}

// serveSite serves the request's {path...} of a site snapshot under base
// (the site's URL path, ending in "/"): the site file rules shared by the
// member and public routes.
func (s *Service) serveSite(w http.ResponseWriter, r *http.Request, sv siteVersion, base string) {
	b, err := s.siteBundle(r.Context(), sv)
	if err != nil {
		obs.ServerError(w, "load site bundle failed", err)
		http.Error(w, "load site bundle failed", http.StatusBadGateway)
		return
	}
	file, redirect := siteFile(b, r.PathValue("path"))
	if redirect != "" {
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, base+redirect, http.StatusFound)
		return
	}
	if file == "" {
		http.NotFound(w, r)
		return
	}
	data, _ := b.File(file)
	switch strings.ToLower(path.Ext(file)) {
	case ".html", ".htm":
		data = rewriteSiteHTML(data, base)
	case ".css":
		data = rewriteSiteCSS(data, base)
	}
	h := w.Header()
	h.Set("Content-Type", tarbundle.ContentType(file))
	h.Set("Content-Security-Policy", siteCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// Module scripts and fonts load in CORS mode from the opaque origin.
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "private, max-age=300")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// siteFile maps a request path to a bundle file. "" or a directory path
// ("docs/") serves that directory's index.html; a directory without its
// slash redirects to it (redirect is the slash-terminated path relative to
// the site base); an extensionless miss serves the root index.html (SPA
// fallback); anything else is a miss (file "").
func siteFile(b *tarbundle.Bundle, p string) (file, redirect string) {
	p = strings.TrimPrefix(p, "/")
	isDir := p == "" || strings.HasSuffix(p, "/")
	clean := strings.TrimSuffix(p, "/")
	if isDir {
		index := "index.html"
		if clean != "" {
			index = clean + "/index.html"
		}
		if pkgmeta.ValidBundlePath(index) {
			if _, ok := b.File(index); ok {
				return index, ""
			}
		}
	} else if pkgmeta.ValidBundlePath(clean) {
		if _, ok := b.File(clean); ok {
			return clean, ""
		}
		if _, ok := b.File(clean + "/index.html"); ok {
			return "", clean + "/"
		}
	}
	if path.Ext(path.Base(clean)) == "" {
		if _, ok := b.File("index.html"); ok {
			return "index.html", ""
		}
	}
	return "", ""
}
