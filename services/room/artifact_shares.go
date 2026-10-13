package room

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/wire"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/secrettoken"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Share links (docs/prd/2026-10-09-artifact-shares-and-site-previews.md,
// "Contracts"; access: docs/prd/2026-10-13-share-link-sign-in.md): a room
// member mints an expiring, revocable link to one artifact (a pinned
// version, or 0 = always the latest) that the organization's signed-in
// members may open ("org", the default) or anyone holding it ("anyone").
// The token "bsh_<shareId>_<secret>" is shown once; only the secret's hash
// is stored. The gateway resolves a link through the signed internal route
// below and decides who may open it (shareAccess); every dead link answers
// the same 404 here.

// shareTokenFormat is the share-link token family.
var shareTokenFormat = secrettoken.Format{Prefix: "bsh_", IDLen: 32}

const (
	defaultShareTTLHours = 168      // 7 days
	maxShareTTLHours     = 2160     // 90 days
	hourMs               = 3600_000 // one hour in ms
	// shareListWindowMs keeps revoked/expired links listed for 30 days.
	shareListWindowMs = 30 * 24 * hourMs
)

// errAnonymousLinksDisabled refuses an "anyone" link while the
// organization disables them.
const errAnonymousLinksDisabled = "links anyone can open without signing in are disabled for this organization"

// anonymousLinkAllowed is the one reader of the organization's switch: an
// "org" link is always allowed, an "anyone" link only while the
// organization does not disable them (create, an access change, and the
// resolution of a live link all ask it).
func anonymousLinkAllowed(policy store.Tenant, access string) bool {
	return access != wire.ShareAccessAnyone || !policy.AnonymousLinksDisabled
}

func (s *Service) registerShareRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/rooms/{roomId}/artifacts/{artifactId}/shares", s.handleCreateShare)
	mux.HandleFunc("GET /v1/rooms/{roomId}/artifacts/{artifactId}/shares", s.handleListShares)
	mux.HandleFunc("PATCH /v1/rooms/{roomId}/artifacts/{artifactId}/shares/{shareId}", s.handleSetShareAccess)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/artifacts/{artifactId}/shares/{shareId}", s.handleRevokeShare)
	// Org-admin list: reachable only through /v1/tenant/*, which the gateway
	// gates on manage_org before signing the request.
	mux.HandleFunc("GET /v1/tenant/shares", s.handleTenantShares)
	// Internal, gateway-only: token or share id → link, artifact and
	// version; and one view of a link its caller may open.
	mux.Handle("POST /internal/v1/shares/resolve", svcauth.RequireService(svcauth.KeyFromEnv(), []string{"gateway"}, http.HandlerFunc(s.handleResolveShare)))
	mux.Handle("POST /internal/v1/shares/view", svcauth.RequireService(svcauth.KeyFromEnv(), []string{"gateway"}, http.HandlerFunc(s.handleShareView)))
}

func viewShare(sh store.ArtifactShare, nowMs int64) wire.ShareView {
	return wire.ShareView{
		ShareID: sh.ShareID, ArtifactID: sh.ArtifactID, Version: sh.Version, CreatedBy: sh.CreatedBy,
		CreatedAtMs: sh.CreatedAtMs, ExpiresAtMs: sh.ExpiresAtMs, RevokedAtMs: sh.RevokedAtMs, RevokedBy: sh.RevokedBy,
		ViewCount: sh.ViewCount, LastViewedAtMs: sh.LastViewedAtMs, Access: sh.Access, Active: sh.Active(nowMs),
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
		httpx.Unavailable(w, "artifact shares")
		return false
	}
	return true
}

// shareArtifact is the member gate of the create/list routes: the caller is
// a room member and the artifact lives in the path's room.
func (s *Service) shareArtifact(w http.ResponseWriter, r *http.Request) (store.Artifact, bool) {
	roomID := r.PathValue("roomId")
	isMember, err := s.Store.IsMember(r.Context(), roomID, caller(r))
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return store.Artifact{}, false
	}
	if !isMember {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return store.Artifact{}, false
	}
	art, err := s.Artifacts.GetArtifact(r.Context(), r.PathValue("artifactId"))
	if err != nil || art.RoomID != roomID {
		httpx.Error(w, "artifact not found", http.StatusNotFound)
		return store.Artifact{}, false
	}
	return art, true
}

// handleCreateShare mints a link (wire.ShareCreateRequest: version 0 =
// latest, expiresInHours 1..2160 default 168, access default "org") → 201
// {share, token}. The token is never shown again.
func (s *Service) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) {
		return
	}
	art, ok := s.shareArtifact(w, r)
	if !ok {
		return
	}
	var body wire.ShareCreateRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	// No access is the store's default (org).
	if body.Access != "" && !wire.ValidShareAccess(body.Access) {
		httpx.Error(w, `access must be "org" or "anyone"`, http.StatusBadRequest)
		return
	}
	if body.Version < 0 {
		httpx.Error(w, "version must be 0 (latest) or an existing version", http.StatusBadRequest)
		return
	}
	if body.ExpiresInHours == 0 {
		body.ExpiresInHours = defaultShareTTLHours
	}
	if body.ExpiresInHours < 1 || body.ExpiresInHours > maxShareTTLHours {
		httpx.Error(w, "expiresInHours must be 1..2160", http.StatusBadRequest)
		return
	}
	if body.Version > 0 {
		if _, err := s.Artifacts.GetArtifactVersion(r.Context(), art.ArtifactID, body.Version); err != nil {
			httpx.Error(w, "version does not exist", http.StatusBadRequest)
			return
		}
	}
	policy, err := s.tenantPolicy(r.Context())
	if err != nil {
		obs.ServerError(w, "read tenant policy failed", err)
		httpx.Error(w, "read tenant policy failed", http.StatusInternalServerError)
		return
	}
	if !anonymousLinkAllowed(policy, body.Access) {
		httpx.Error(w, errAnonymousLinksDisabled, http.StatusForbidden)
		return
	}
	id, token, hash, err := shareTokenFormat.New()
	if err != nil {
		obs.ServerError(w, "mint share token failed", err)
		httpx.Error(w, "create share failed", http.StatusInternalServerError)
		return
	}
	now := s.Now()
	sh, err := s.Artifacts.CreateArtifactShare(r.Context(), store.ArtifactShare{
		ShareID: id, TokenHash: hash, RoomID: art.RoomID, ArtifactID: art.ArtifactID, Version: body.Version, Access: body.Access,
		CreatedBy: caller(r), CreatedAtMs: now, ExpiresAtMs: now + int64(body.ExpiresInHours)*hourMs,
	})
	if err != nil {
		obs.ServerError(w, "create share failed", err)
		httpx.Error(w, "create share failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"share": viewShare(sh, now), "token": token})
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
		httpx.Error(w, "list shares failed", http.StatusInternalServerError)
		return
	}
	out := make([]wire.ShareView, 0, len(list))
	for _, sh := range list {
		out = append(out, viewShare(sh, now))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"shares": out})
}

// managedShare is the gate of the routes that change a link (revoke, an
// access change): the path's link, which the caller may manage — its
// creator, the room's owner/admins, or an org admin of the room's tenant.
// An org admin need not be a room member to revoke: the gateway lets
// manage_org through its room scope for the revoke only
// (isArtifactShareRevoke); the org role is the one the gateway signed for
// the room's tenant. It answers the refusal itself.
func (s *Service) managedShare(w http.ResponseWriter, r *http.Request, verb string) (store.ArtifactShare, bool) {
	if !s.sharesReady(w) {
		return store.ArtifactShare{}, false
	}
	ctx := r.Context()
	roomID, user := r.PathValue("roomId"), caller(r)
	role, member, err := s.Store.MemberRole(ctx, roomID, user)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return store.ArtifactShare{}, false
	}
	orgAdmin := rbac.Can(svcauth.Role(r), rbac.PermManageOrg)
	if !member && !orgAdmin {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return store.ArtifactShare{}, false
	}
	sh, err := s.Artifacts.GetArtifactShare(ctx, r.PathValue("shareId"))
	if errors.Is(err, store.ErrArtifactShareNotFound) || (err == nil && (sh.RoomID != roomID || sh.ArtifactID != r.PathValue("artifactId"))) {
		httpx.Error(w, "share not found", http.StatusNotFound)
		return store.ArtifactShare{}, false
	}
	if err != nil {
		obs.ServerError(w, "read share failed", err)
		httpx.Error(w, "read share failed", http.StatusInternalServerError)
		return store.ArtifactShare{}, false
	}
	if !orgAdmin && sh.CreatedBy != user && !(member && role.CanInvite()) {
		httpx.Error(w, "only the link's creator, a room owner or admin, or an org admin may "+verb+" it", http.StatusForbidden)
		return store.ArtifactShare{}, false
	}
	return sh, true
}

// handleSetShareAccess changes who may open a link
// (wire.ShareAccessUpdate) → 200 {share}. Switching to "anyone" is refused
// while the organization disables such links. Every open page, frame and
// change feed follows within the gateway's re-check windows.
func (s *Service) handleSetShareAccess(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.managedShare(w, r, "change")
	if !ok {
		return
	}
	var body wire.ShareAccessUpdate
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	if !wire.ValidShareAccess(body.Access) {
		httpx.Error(w, `access must be "org" or "anyone"`, http.StatusBadRequest)
		return
	}
	policy, err := s.tenantPolicy(r.Context())
	if err != nil {
		obs.ServerError(w, "read tenant policy failed", err)
		httpx.Error(w, "read tenant policy failed", http.StatusInternalServerError)
		return
	}
	if !anonymousLinkAllowed(policy, body.Access) {
		httpx.Error(w, errAnonymousLinksDisabled, http.StatusForbidden)
		return
	}
	sh, err = s.Artifacts.SetArtifactShareAccess(r.Context(), sh.ShareID, body.Access)
	if err != nil {
		obs.ServerError(w, "change share access failed", err)
		httpx.Error(w, "change share access failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"share": viewShare(sh, s.Now())})
}

// handleRevokeShare revokes a link (204, idempotent) for those who may
// manage it (managedShare).
func (s *Service) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.managedShare(w, r, "revoke")
	if !ok {
		return
	}
	if _, err := s.Artifacts.RevokeArtifactShare(r.Context(), sh.ShareID, caller(r), s.Now()); err != nil {
		obs.ServerError(w, "revoke share failed", err)
		httpx.Error(w, "revoke share failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTenantShares is the org-admin list of the tenant's links
// (?active=1: only links that currently work), newest first.
func (s *Service) handleTenantShares(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) {
		return
	}
	if caller(r) == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
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
		httpx.Error(w, "list tenant shares failed", http.StatusInternalServerError)
		return
	}
	out := make([]tenantShareView, 0, len(list))
	for _, sh := range list {
		out = append(out, tenantShareView{ShareView: viewShare(sh.ArtifactShare, now), RoomID: sh.RoomID, ArtifactTitle: sh.ArtifactTitle})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"shares": out})
}

// handleResolveShare is POST /internal/v1/shares/resolve
// (wire.ShareResolveRequest) for the gateway's share routes: a link's token,
// or the share id of a share ticket the gateway signed (the route is
// gateway-only, so the gateway vouches for it). A live link answers its
// tenant, organization name, room, artifact and version (the latest one,
// resolved now, for a link that follows the latest); every other case —
// unknown id, wrong secret, expired, revoked, an "anyone" link while the
// organization disables them, artifact or room gone — is the same 404, so
// a token holder learns nothing about a dead link. Who may open a live
// link is the gateway's shareAccess.
func (s *Service) handleResolveShare(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) || !s.profilesReady(w) {
		return
	}
	var body wire.ShareResolveRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	res, ok, err := s.resolveShare(r.Context(), body)
	if err != nil {
		obs.ServerError(w, "resolve share failed", err)
		httpx.Error(w, "resolve share failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpx.Error(w, "share not found", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleShareView is POST /internal/v1/shares/view (wire.ShareViewCount):
// one view of a link, counted by the gateway once its caller may open it
// (a refused caller counts none). A failure is logged, never the viewer's
// problem: 204 either way, 404 for an unknown link.
func (s *Service) handleShareView(w http.ResponseWriter, r *http.Request) {
	if !s.sharesReady(w) {
		return
	}
	var body wire.ShareViewCount
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	sh, err := s.Artifacts.ResolveArtifactShare(r.Context(), body.ShareID)
	if errors.Is(err, store.ErrArtifactShareNotFound) {
		httpx.Error(w, "share not found", http.StatusNotFound)
		return
	}
	if err == nil {
		err = s.Artifacts.CountArtifactShareView(tenant.NewContext(r.Context(), sh.TenantID), sh.ShareID, s.Now())
	}
	if err != nil {
		// A lost view count must not break the viewer's page.
		slog.Warn("bnbloc room: share view count failed", "event", "bnbloc.share.view_count_failed", "share", body.ShareID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveShare implements the resolve route: ok=false for every dead link;
// err only for a store failure.
func (s *Service) resolveShare(ctx context.Context, req wire.ShareResolveRequest) (wire.ShareResolution, bool, error) {
	if (req.Token == "") == (req.ShareID == "") {
		return wire.ShareResolution{}, false, nil
	}
	id, secret, byToken := shareTokenFormat.Parse(req.Token)
	if req.Token != "" && !byToken {
		return wire.ShareResolution{}, false, nil
	}
	if !byToken {
		id = req.ShareID
	}
	sh, err := s.Artifacts.ResolveArtifactShare(ctx, id)
	if errors.Is(err, store.ErrArtifactShareNotFound) {
		return wire.ShareResolution{}, false, nil
	}
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	now := s.Now()
	if (byToken && !secrettoken.Verify(secret, sh.TokenHash)) || !sh.Active(now) {
		return wire.ShareResolution{}, false, nil
	}
	// From here on the link's own tenant scopes every read.
	ctx = tenant.NewContext(ctx, sh.TenantID)
	policy, err := s.tenantPolicy(ctx)
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	if !anonymousLinkAllowed(policy, sh.Access) {
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
	mimeType := v.MimeType
	if mimeType == "" && v.Storage != store.ArtifactStorageBlob {
		mimeType = store.ArtifactInlineMIME(art)
	}
	sharedBy, err := s.displayNames(ctx, []string{sh.CreatedBy})
	if err != nil {
		return wire.ShareResolution{}, false, err
	}
	orgName := policy.Name
	if orgName == "" {
		orgName = sh.TenantID
	}
	out := wire.ShareResolution{
		TenantID: sh.TenantID,
		OrgName:  orgName,
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
