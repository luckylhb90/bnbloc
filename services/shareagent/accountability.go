package shareagenthttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Borrow accountability: each borrowed run is reserved before the drive
// (handleReserveBorrow: run counter + audit record) and settled here once the
// drive ends, attaching the usage the turn reported. A per-owner view projects
// their authorizations with usage and recent borrowers. The recorded actor is
// the borrower.

type recordBorrowBody struct {
	// ReservationId is the id handleReserveBorrow returned for this run.
	ReservationId string `json:"reservationId"`
	// Model is the model the core reported ("" keeps the requested model the
	// reservation recorded).
	Model string `json:"model"`
	// Cost is the turn's cost when the reporter knows it; absent means not
	// reported (the audit row keeps NULL, so usage reports never show an
	// unreported cost as zero).
	Cost *float64 `json:"cost"`
	// InputTokens / OutputTokens are the turn's token usage when the core
	// reported it; absent means not reported (NULL on the audit row).
	InputTokens  *int64 `json:"inputTokens"`
	OutputTokens *int64 `json:"outputTokens"`
}

// maxRecordedCost bounds the cost one recorded turn may charge. It is a
// sanity ceiling on a single accounting entry (not a pricing table), so a
// malformed or hostile report cannot inflate usage by an absurd amount.
const maxRecordedCost = 1_000_000

// maxRecordedTokens is the same kind of sanity ceiling for one turn's token
// counts.
const maxRecordedTokens = 1_000_000_000_000

// maxRecordedModel is the audit row's model column width (characters).
const maxRecordedModel = 128

// handleRecordBorrow settles a reserved borrowed run with the usage the turn
// reported. The run was already counted when it was reserved, so settling
// never refuses on quota: the cost is added to the grant even when it carries
// the grant past its cost allowance (the next reservation is then refused). A
// revoked authorization still settles — the run happened.
//
// Only the agent's OWNER may settle (their bridge drove the run): a non-owner
// could otherwise post usage onto another member's authorization. Answers 200
// {recorded:true}; 404 for an unknown reservation, 409 for one already settled
// or expired (RunReservationTTLms) — an expired run keeps counting, without
// usage.
func (s *Service) handleRecordBorrow(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	caller := callerSubject(r)
	if !s.requireMember(w, roomID, caller, tenant.FromContext(r.Context())) {
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	provider := providerFromTarget(r.PathValue("targetId"))
	if owner == "" || provider == "" {
		writeErr(w, http.StatusBadRequest, "owner and shared-agent:<provider> target required")
		return
	}
	if caller != owner {
		writeErr(w, http.StatusForbidden, "only the agent owner may record a borrow")
		return
	}
	var body recordBorrowBody
	if !decodeJSON(w, r, &body) {
		return
	}
	reservationID := strings.TrimSpace(body.ReservationId)
	if reservationID == "" {
		writeErr(w, http.StatusBadRequest, "reservationId required")
		return
	}
	// NaN fails every comparison, so test the admissible range positively.
	if body.Cost != nil && !(*body.Cost >= 0 && *body.Cost <= maxRecordedCost) {
		writeErr(w, http.StatusBadRequest, "cost must be between 0 and 1000000")
		return
	}
	for _, n := range []*int64{body.InputTokens, body.OutputTokens} {
		if n != nil && (*n < 0 || *n > maxRecordedTokens) {
			writeErr(w, http.StatusBadRequest, "token counts must be between 0 and 1000000000000")
			return
		}
	}
	err = s.Grants.SettleRun(r.Context(), roomAuthGrantID(r.Context(), roomID, owner, provider), reservationID, store.RunUsage{
		Model: clipModel(body.Model), Cost: body.Cost, InputTokens: body.InputTokens, OutputTokens: body.OutputTokens,
	}, s.Now())
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
	case errors.Is(err, store.ErrReservationNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"recorded": false, "error": "reservation not found"})
	case errors.Is(err, store.ErrReservationSettled):
		writeJSON(w, http.StatusConflict, map[string]any{"recorded": false, "error": "reservation already settled"})
	case errors.Is(err, store.ErrReservationExpired):
		writeJSON(w, http.StatusConflict, map[string]any{"recorded": false, "error": "reservation expired"})
	default:
		obs.ServerError(w, "record borrow failed", err)
		writeErr(w, http.StatusInternalServerError, "record borrow failed")
	}
}

// handleTenantUsage returns the ctx tenant's aggregate borrow usage together
// with any configured org-level run/cost ceilings (0 = unlimited), so a UI can
// show remaining allowance. It requires no room membership: usage is org-wide
// and the tenant is already bound to the request context by svcauth.
func (s *Service) handleTenantUsage(w http.ResponseWriter, r *http.Request) {
	runs, cost := s.Grants.TenantUsage(r.Context())
	runQuota, costAllowance := s.tenantCeilings(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"runsUsed":      runs,
		"costUsed":      cost,
		"runQuota":      runQuota,
		"costAllowance": costAllowance,
	})
}

// tenantCeilings resolves the ctx tenant's org-level run/cost ceilings from the
// PERSISTED tenant registry, the only source of org quota (0 = unlimited). No
// registry, or a tenant with no registry row, has no org ceiling. There is no
// process-wide fallback: a shared value would leak one tenant's ceiling onto
// every other tenant.
func (s *Service) tenantCeilings(ctx context.Context) (runQuota int, costAllowance float64) {
	if s.Tenants == nil {
		return 0, 0
	}
	t, err := s.Tenants.GetTenant(ctx, tenant.FromContext(ctx))
	if err != nil {
		return 0, 0
	}
	return t.RunQuota, t.CostAllowance
}

type authorizationView struct {
	Provider        string   `json:"provider"`
	ExpiresAtMs     int64    `json:"expiresAtMs"`
	RunsUsed        int      `json:"runsUsed"`
	RunQuota        *int     `json:"runQuota,omitempty"`
	RecentBorrowers []string `json:"recentBorrowers"`
}

// handleOwnerAuthorizations projects the caller's live authorizations in a room
// with usage and recent borrowers, for the owner's accountability view. The
// owner is the authenticated caller; it lists only that caller's authorizations.
func (s *Service) handleOwnerAuthorizations(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	owner := callerSubject(r)
	if !s.requireMember(w, roomID, owner, tenant.FromContext(r.Context())) {
		return
	}
	// Recent borrowers per grant from the room's audit trail. AuditRecords is
	// newest-first; a borrow is an allow whose actor is not the owner (an owner's
	// own turn shares the "turn" action but has actor == owner).
	recent := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, rec := range s.Grants.AuditRecords(r.Context(), roomID, 100, 0) {
		if rec.OwnerUserID != owner || rec.Decision != "allow" || rec.ActorUserID == "" {
			continue
		}
		if strings.EqualFold(rec.ActorUserID, owner) {
			continue // the owner's own turn, not a borrow
		}
		if seen[rec.GrantID] == nil {
			seen[rec.GrantID] = map[string]bool{}
		}
		if seen[rec.GrantID][rec.ActorUserID] || len(recent[rec.GrantID]) >= 5 {
			continue
		}
		seen[rec.GrantID][rec.ActorUserID] = true
		recent[rec.GrantID] = append(recent[rec.GrantID], rec.ActorUserID)
	}
	// One view per live room authorization the caller owns in this room.
	views := []authorizationView{}
	now := s.Now()
	for _, grant := range s.Grants.RoomGrants(r.Context(), roomID) {
		if grant.OwnerUserID != owner || grant.GranteeUserID != "*" || grant.RevokedAtMs != nil {
			continue
		}
		if grant.ExpiresAtMs != nil && now >= *grant.ExpiresAtMs {
			continue
		}
		provider := providerFromTarget(grant.TargetID)
		if provider == "" {
			continue
		}
		v := authorizationView{
			Provider:        provider,
			RunsUsed:        grant.RunsUsed,
			RunQuota:        grant.RunQuota,
			RecentBorrowers: recent[grant.GrantID],
		}
		if v.RecentBorrowers == nil {
			v.RecentBorrowers = []string{}
		}
		if grant.ExpiresAtMs != nil {
			v.ExpiresAtMs = *grant.ExpiresAtMs
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"owner": owner, "authorizations": views})
}
