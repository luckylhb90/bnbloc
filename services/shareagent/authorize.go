package shareagenthttp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Owner room authorization: an owner pre-authorizes members of a room to borrow
// one of their agents (a provider) within limits, replacing any per-use consent.
// The authorizing owner is always the authenticated caller (X-Bnbloc-User),
// never a request-body value, so a grantee can never authorize itself. The
// authorization is stored as a room-wide grant (GranteeUserID "*") under a
// deterministic id so it can be looked up by (room, owner, provider) without a
// grant id, and revoked or refreshed idempotently.

// roomAuthGrantID is the deterministic id of a room authorization for one
// owner's provider in the ctx tenant. It is a fixed-length digest so it always
// fits grant_id VARCHAR(64) whatever the id lengths (the old concatenation
// overflowed it for realistic room/user/provider ids, failing the write or
// truncating into collisions). The tenant is part of the digest because
// grant_id is the table's sole primary key. Parts are NUL-separated; none of
// them can contain NUL, so the encoding is unambiguous.
func roomAuthGrantID(ctx context.Context, roomID, owner, provider string) string {
	sum := sha256.Sum256([]byte(tenant.FromContext(ctx) + "\x00" + roomID + "\x00" + owner + "\x00" + provider))
	return "roomauth_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// providerFromTarget extracts the provider from a "shared-agent:<provider>"
// target id. It returns "" when the target is not a shared-agent target.
func providerFromTarget(targetID string) string {
	const prefix = "shared-agent:"
	if !strings.HasPrefix(targetID, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(targetID, prefix))
}

type authorizeBody struct {
	AllowedModels []string `json:"allowedModels"`
	MaxConcurrent int      `json:"maxConcurrent"`
	RunQuota      *int     `json:"runQuota"`
	// TTLMs is the authorization lifetime in ms; <=0 uses the bounded default.
	TTLMs int64 `json:"ttlMs"`
}

// handleAuthorize lets an owner authorize room-wide borrowing of their agent.
// The owner is the authenticated caller; the target's provider identifies which
// of the owner's agents may be borrowed here.
func (s *Service) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	owner := callerSubject(r)
	if !s.requireMember(w, roomID, owner, tenant.FromContext(r.Context())) {
		return
	}
	provider := providerFromTarget(r.PathValue("targetId"))
	if provider == "" {
		writeErr(w, http.StatusBadRequest, "target must be shared-agent:<provider>")
		return
	}
	var body authorizeBody
	if !decodeJSON(w, r, &body) {
		return
	}
	// The model range is exactly what the owner authorized. No default is
	// invented here: a model catalog is environment data owned by the agent's
	// core/provider, so an omitted list stays empty rather than becoming a
	// hardcoded model name that is wrong for every other provider.
	models := make([]store.AllowedModel, 0, len(body.AllowedModels))
	for _, m := range body.AllowedModels {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, store.AllowedModel{Model: m})
		}
	}
	maxConc := body.MaxConcurrent
	if maxConc < 1 {
		maxConc = 2
	}
	// Authorization TTL: bounded default 24h, capped at 7d, owner-shortenable.
	const defaultTTL = int64(24 * 60 * 60 * 1000)
	const maxTTL = int64(7 * 24 * 60 * 60 * 1000)
	ttl := body.TTLMs
	if ttl <= 0 {
		ttl = defaultTTL
	} else if ttl > maxTTL {
		ttl = maxTTL
	}
	expiresAt := s.Now() + ttl
	grantID := roomAuthGrantID(r.Context(), roomID, owner, provider)
	grant := store.Grant{
		GrantID:       grantID,
		RoomID:        roomID,
		TargetID:      "shared-agent:" + provider,
		OwnerUserID:   owner,
		GranteeUserID: "*",
		AllowedModels: models,
		Capabilities:  store.Capabilities{Consult: true, Review: true, Delegate: true, Upgrade: true},
		RunQuota:      body.RunQuota,
		MaxConcurrent: maxConc,
		CreatedAtMs:   s.Now(),
		CreatedBy:     owner,
		ExpiresAtMs:   &expiresAt,
	}
	if err := s.Grants.PutGrant(r.Context(), grant); err != nil {
		obs.ServerError(w, "authorize failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authorized":  true,
		"grantId":     grantID,
		"owner":       owner,
		"provider":    provider,
		"expiresAtMs": expiresAt,
	})
}

// handleDeauthorize revokes an owner's room authorization for a provider. Only
// the authenticated owner may revoke their own authorization.
func (s *Service) handleDeauthorize(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	owner := callerSubject(r)
	if !s.requireMember(w, roomID, owner, tenant.FromContext(r.Context())) {
		return
	}
	provider := providerFromTarget(r.PathValue("targetId"))
	if provider == "" {
		writeErr(w, http.StatusBadRequest, "target must be shared-agent:<provider>")
		return
	}
	if err := s.Grants.RevokeGrant(r.Context(), roomAuthGrantID(r.Context(), roomID, owner, provider), s.Now()); err != nil && err != store.ErrGrantNotFound {
		obs.ServerError(w, "deauthorize failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authorized": false, "owner": owner, "provider": provider})
}

// handleAuthorization reports whether a live room authorization exists for an
// owner's provider. The bridge consults this before driving a borrowed agent,
// and the borrower's client consults it before sending, so an unauthorized
// borrow is refused at the driver and surfaced in the composer. The owner is a
// query parameter (the agent's owner being borrowed), not the caller.
func (s *Service) handleAuthorization(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.requireMember(w, roomID, callerSubject(r), tenant.FromContext(r.Context())) {
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	provider := providerFromTarget(r.PathValue("targetId"))
	if owner == "" || provider == "" {
		writeErr(w, http.StatusBadRequest, "owner and shared-agent:<provider> target required")
		return
	}
	authorized, expiresAt, models := s.roomAuthorized(r.Context(), roomID, owner, provider)
	resp := map[string]any{"authorized": authorized, "owner": owner, "provider": provider}
	if authorized {
		resp["expiresAtMs"] = expiresAt
		resp["allowedModelRange"] = models
		// Additive quota view so a borrower's composer can tell a run-limited,
		// exhausted authorization apart and offer a quota request (F3).
		if grant, live := s.liveAuthorization(r.Context(), roomID, owner, provider); live && grant.RunQuota != nil {
			resp["runQuota"] = *grant.RunQuota
			resp["runsUsed"] = grant.RunsUsed
			resp["quotaExhausted"] = grant.RunsUsed >= *grant.RunQuota
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// roomAuthorized reports whether a live (present, not revoked, not expired) room
// authorization exists for the owner's provider, with its expiry and model
// range. It is the single source of truth for the pre-authorization check.
func (s *Service) roomAuthorized(ctx context.Context, roomID, owner, provider string) (bool, int64, []store.AllowedModel) {
	grant, live := s.liveAuthorization(ctx, roomID, owner, provider)
	if !live {
		return false, 0, nil
	}
	var expiresAt int64
	if grant.ExpiresAtMs != nil {
		expiresAt = *grant.ExpiresAtMs
	}
	return true, expiresAt, grant.AllowedModels
}
