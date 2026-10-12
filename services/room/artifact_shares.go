package room

import (
	"context"
	"errors"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"log/slog"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/secrettoken"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Public share links (docs/prd/2026-10-09-artifact-shares-and-site-previews.md,
// "Contracts"): a room member mints an expiring, revocable link to one
// artifact (a pinned version, or 0 = always the latest). The token
// "bsh_<shareId>_<secret>" is shown once; only the secret's hash is stored.
// The gateway resolves a token through the signed internal route below and
// serves the bytes; every inactive case answers the same 404 there.

// shareTokenFormat is the share-link token family.
var shareTokenFormat = secrettoken.Format{Prefix: "bsh_", IDLen: 32}

const (
	defaultShareTTLHours = 168      // 7 days
	maxShareTTLHours     = 2160     // 90 days
	hourMs               = 3600_000 // one hour in ms
	// shareListWindowMs keeps revoked/expired links listed for 30 days.
	shareListWindowMs = 30 * 24 * hourMs
)

// errPublicSharesDisabled is the contract's refusal text.
const errPublicSharesDisabled = "public share links are disabled for this organization"

// shareResolverSubject is the only identity the internal resolve route
// answers: the gateway's own service subject (the svcauth convention).
var shareResolverSubject = svcauth.ServiceSubject("gateway")

func (s *Service) registerShareRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/rooms/{roomId}/artifacts/{artifactId}/shares", s.handleCreateShare)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}/shares", s.handleListShares)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/artifacts/{artifactId}/shares/{shareId}", s.handleRevokeShare)
	// Org-admin list: reachable only through /v1/tenant/*, which the gateway
	// gates on manage_org before signing the request.
	mux.HandleFunc("GET /v1/tenant/shares", s.handleTenantShares)
	// Internal, gateway-only: token → link, artifact and version.
	mux.Handle("POST /internal/v1/shares/resolve", svcauth.RequireSigned(svcauth.KeyFromEnv(), http.HandlerFunc(s.handleResolveShare)))
}

func viewShare(sh store.ArtifactShare, nowMs int64) wire.ShareView {
	return wire.ShareView{
		ShareID: sh.ShareID, ArtifactID: sh.ArtifactID, Version: sh.Version, CreatedBy: sh.CreatedBy,
		CreatedAtMs: sh.CreatedAtMs, ExpiresAtMs: sh.ExpiresAtMs, RevokedAtMs: sh.RevokedAtMs, RevokedBy: sh.RevokedBy,
		ViewCount: sh.ViewCount, LastViewedAtMs: sh.LastViewedAtMs, Active: sh.Active(nowMs),
	}
}

// tenantShareView is one row of the org-admin list.
type tenantShareView struct {
	wire.ShareView
	RoomID        string `json:"roomId"`
	ArtifactTitle string `json:"artifactTitle"`
}

// tenantPolicy reads the context tenant's policy switches. A tenant with no
// registry row has set no policy, so it gets the defaults; a read failure is
// an error (callers fail closed).
func (s *Service) tenantPolicy(ctx context.Context) (store.Tenant, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return store.Tenant{}, err
	}
	t, err := s.Tenants.GetTenant(ctx, tid)
	if errors.Is(err, store.ErrTenantNotFound) {
		return store.Tenant{TenantID: tid}, nil
	}
	return t, err
}

// sharesReady checks the share routes' dependencies.
func (s *Service) sharesReady(w http.ResponseWriter) bool {
	if s.Artifacts == nil || s.Tenants == nil {
		obs.ServerError(w, "artifact shares not configured", nil)
		writeErr(w, http.StatusServiceUnavailable, "artifact shares not configured")
		return false
	}
	return true
}

// shareArtifact is the member gate of the create/list routes: the caller is
// a room member and the artifact lives in the path's room.
func (s *Service) shareArtifact(w http.ResponseWriter, r *http.Request) (store.Artifact, bool) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return store.Artifact{}, false
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), r.PathValue("artifactId"))
	if err != nil || art.RoomID != roomID {
		writeErr(w, http.StatusNotFound, "artifact not found")
		return store.Artifact{}, false
	}
	return art, true
}

// handleCreateShare mints a link: {version?: 0 = latest, expiresInHours?:
// 1..2160, default 168} → 201 {share, token}. The token is never shown again.
func (s *Service) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) {
		return
	}
	art, ok := s.shareArtifact(w, r)
	if !ok {
		return
	}
	var body struct {
		Version        int `json:"version"`
		ExpiresInHours int `json:"expiresInHours"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Version < 0 {
		writeErr(w, http.StatusBadRequest, "version must be 0 (latest) or an existing version")
		return
	}
	if body.ExpiresInHours == 0 {
		body.ExpiresInHours = defaultShareTTLHours
	}
	if body.ExpiresInHours < 1 || body.ExpiresInHours > maxShareTTLHours {
		writeErr(w, http.StatusBadRequest, "expiresInHours must be 1..2160")
		return
	}
	if body.Version > 0 {
		if _, err := s.Artifacts.GetArtifactVersion(r.Context(), art.ArtifactID, body.Version); err != nil {
			writeErr(w, http.StatusBadRequest, "version does not exist")
			return
		}
	}
	policy, err := s.tenantPolicy(r.Context())
	if err != nil {
		obs.ServerError(w, "read tenant policy failed", err)
		writeErr(w, http.StatusInternalServerError, "read tenant policy failed")
		return
	}
	if policy.PublicSharesDisabled {
		writeErr(w, http.StatusForbidden, errPublicSharesDisabled)
		return
	}
	id, token, hash, err := shareTokenFormat.New()
	if err != nil {
		obs.ServerError(w, "mint share token failed", err)
		writeErr(w, http.StatusInternalServerError, "create share failed")
		return
	}
	now := s.Now()
	sh, err := s.Artifacts.CreateArtifactShare(r.Context(), store.ArtifactShare{
		ShareID: id, TokenHash: hash, RoomID: art.RoomID, ArtifactID: art.ArtifactID, Version: body.Version,
		CreatedBy: caller(r), CreatedAtMs: now, ExpiresAtMs: now + int64(body.ExpiresInHours)*hourMs,
	})
	if err != nil {
		obs.ServerError(w, "create share failed", err)
		writeErr(w, http.StatusInternalServerError, "create share failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"share": viewShare(sh, now), "token": token})
}

// handleListShares lists an artifact's links, newest first; revoked and
// expired ones stay listed for 30 days.
func (s *Service) handleListShares(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) {
		return
	}
	art, ok := s.shareArtifact(w, r)
	if !ok {
		return
	}
	now := s.Now()
	list, err := s.Artifacts.ListArtifactShares(r.Context(), art.RoomID, art.ArtifactID, now-shareListWindowMs)
	if err != nil {
		obs.ServerError(w, "list shares failed", err)
		writeErr(w, http.StatusInternalServerError, "list shares failed")
		return
	}
	out := make([]wire.ShareView, 0, len(list))
	for _, sh := range list {
		out = append(out, viewShare(sh, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

// handleRevokeShare revokes a link (204, idempotent). Allowed for the link's
// creator, the room's owner/admins, and an org admin of the room's tenant —
// who, for this route only, need not be a room member (the gateway lets
// manage_org through its room scope here; the org role is re-checked below).
func (s *Service) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) {
		return
	}
	ctx := r.Context()
	roomID, user := r.PathValue("roomId"), caller(r)
	role, member := s.Store.MemberRole(ctx, roomID, user)
	orgAdmin, err := s.isOrgAdmin(ctx, user)
	if err != nil {
		obs.ServerError(w, "read org role failed", err)
		writeErr(w, http.StatusInternalServerError, "read org role failed")
		return
	}
	if !member && !orgAdmin {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	sh, err := s.Artifacts.GetArtifactShare(ctx, r.PathValue("shareId"))
	if errors.Is(err, store.ErrArtifactShareNotFound) || (err == nil && (sh.RoomID != roomID || sh.ArtifactID != r.PathValue("artifactId"))) {
		writeErr(w, http.StatusNotFound, "share not found")
		return
	}
	if err != nil {
		obs.ServerError(w, "read share failed", err)
		writeErr(w, http.StatusInternalServerError, "read share failed")
		return
	}
	if !orgAdmin && sh.CreatedBy != user && !(member && role.CanInvite()) {
		writeErr(w, http.StatusForbidden, "only the link's creator, a room owner or admin, or an org admin may revoke it")
		return
	}
	if _, err := s.Artifacts.RevokeArtifactShare(ctx, sh.ShareID, user, s.Now()); err != nil {
		obs.ServerError(w, "revoke share failed", err)
		writeErr(w, http.StatusInternalServerError, "revoke share failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isOrgAdmin reports whether user is an org admin of the context tenant
// (for a room route, the room's tenant).
func (s *Service) isOrgAdmin(ctx context.Context, user string) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	m, ok, err := s.Tenants.GetMember(ctx, tid, user)
	if err != nil {
		return false, err
	}
	return ok && m.Role == store.OrgRoleAdmin, nil
}

// handleTenantShares is the org-admin list of the tenant's links
// (?active=1: only links that currently work), newest first.
func (s *Service) handleTenantShares(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) {
		return
	}
	if caller(r) == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	now := s.Now()
	q := store.TenantShareQuery{EndedAfterMs: now - shareListWindowMs}
	if v := strings.TrimSpace(r.URL.Query().Get("active")); v == "1" || v == "true" {
		q.ActiveAtMs = now
	}
	list, err := s.Artifacts.ListTenantArtifactShares(r.Context(), q)
	if err != nil {
		obs.ServerError(w, "list tenant shares failed", err)
		writeErr(w, http.StatusInternalServerError, "list tenant shares failed")
		return
	}
	out := make([]tenantShareView, 0, len(list))
	for _, sh := range list {
		out = append(out, tenantShareView{ShareView: viewShare(sh.ArtifactShare, now), RoomID: sh.RoomID, ArtifactTitle: sh.ArtifactTitle})
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

// handleResolveShare is POST /internal/v1/shares/resolve {token, countView}
// for the gateway's public share routes. An active link answers its tenant,
// room, artifact and version (the latest one, resolved now, for a link that
// follows the latest); every other case — unknown id, wrong secret, expired,
// revoked, public links disabled, artifact or room gone — is the same 404,
// so a token holder learns nothing about a dead link.
func (s *Service) handleResolveShare(w http.ResponseWriter, r *http.Request) {
	if caller(r) != shareResolverSubject {
		writeErr(w, http.StatusForbidden, "share resolution is gateway-only")
		return
	}
	if !s.sharesReady(w) || !s.profilesReady(w) {
		return
	}
	var body struct {
		Token     string `json:"token"`
		CountView bool   `json:"countView"`
	}
	if !decode(w, r, &body) {
		return
	}
	res, ok, err := s.resolveShare(r.Context(), body.Token, body.CountView)
	if err != nil {
		obs.ServerError(w, "resolve share failed", err)
		writeErr(w, http.StatusInternalServerError, "resolve share failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "share not found")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// resolveShare implements the resolve route: ok=false for every inactive
// case; err only for a store failure.
func (s *Service) resolveShare(ctx context.Context, token string, countView bool) (wire.ShareResolution, bool, error) {
	id, secret, ok := shareTokenFormat.Parse(token)
	if !ok {
		return wire.ShareResolution{}, false, nil
	}
	sh, err := s.Artifacts.ResolveArtifactShare(ctx, id)
	if errors.Is(err, store.ErrArtifactShareNotFound) {
		return wire.ShareResolution{}, false, nil
	}
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	now := s.Now()
	if !secrettoken.Verify(secret, sh.TokenHash) || !sh.Active(now) {
		return wire.ShareResolution{}, false, nil
	}
	// From here on the link's own tenant scopes every read.
	ctx = tenant.NewContext(ctx, sh.TenantID)
	policy, err := s.tenantPolicy(ctx)
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	if policy.PublicSharesDisabled {
		return wire.ShareResolution{}, false, nil
	}
	if _, err := s.Store.GetRoom(ctx, sh.RoomID); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			return wire.ShareResolution{}, false, nil
		}
		return wire.ShareResolution{}, false, err
	}
	art, err := s.Artifacts.GetArtifact(ctx, sh.ArtifactID)
	if errors.Is(err, store.ErrArtifactNotFound) || (err == nil && art.RoomID != sh.RoomID) {
		return wire.ShareResolution{}, false, nil
	}
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	version := sh.Version
	if version == 0 {
		version = art.CurrentVersion
	}
	if version <= 0 {
		return wire.ShareResolution{}, false, nil
	}
	v, err := s.Artifacts.GetArtifactVersion(ctx, art.ArtifactID, version)
	if errors.Is(err, store.ErrArtifactNotFound) {
		return wire.ShareResolution{}, false, nil
	}
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	if countView {
		if err := s.Artifacts.CountArtifactShareView(ctx, sh.ShareID, now); err != nil {
			// A lost view count must not break the viewer's page.
			slog.Warn("bnbloc room: share view count failed", "event", "bnbloc.share.view_count_failed", "share", sh.ShareID, "error", err)
		} else {
			sh.ViewCount++
			sh.LastViewedAtMs = now
		}
	}
	mimeType := v.MimeType
	if mimeType == "" && v.Storage != store.ArtifactStorageBlob {
		mimeType = store.ArtifactInlineMIME(art)
	}
	sharedBy, err := s.displayNames(ctx, []string{sh.CreatedBy})
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	out := wire.ShareResolution{
		TenantID: sh.TenantID,
		RoomID:   sh.RoomID,
		Share:    viewShare(sh, now),
		Artifact: wire.ShareArtifact{ArtifactID: art.ArtifactID, Kind: art.Kind, Title: art.Title, FileName: art.FileName},
		Version: wire.ShareVersion{
			Version: v.Version, Storage: v.Storage, MimeType: mimeType, SizeBytes: v.SizeBytes,
			ContentHash: v.ContentHash, Summary: v.Summary, CreatedAtMs: v.CreatedAtMs,
		},
		SharedBy: wire.ShareUser{UserID: sh.CreatedBy, DisplayName: sharedBy[0].DisplayName},
	}
	if v.Storage == store.ArtifactStorageBlob {
		out.Version.BlobKey = v.BlobKey
	} else {
		out.Version.Content = v.Content
	}
	return out, true, nil
}
