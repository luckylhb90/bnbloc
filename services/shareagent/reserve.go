package shareagenthttp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Exact borrow quota (R8): the owner's bridge reserves a borrowed run BEFORE
// it drives it, and settles the reservation with the run's usage once the
// drive ends (handleRecordBorrow). The reservation is the authorization: a
// borrow the authority does not reserve is not driven, and reserved runs count
// against the quota immediately, so concurrent borrows can never exceed it.

// DenyOwnerNotAuthorized is a reservation refused because the owner holds no
// live room authorization for the agent (none, revoked, or expired).
const DenyOwnerNotAuthorized = "owner_not_authorized"

// DenyOrgQuotaExhausted is a reservation refused by the tenant's org-level
// run/cost ceiling.
const DenyOrgQuotaExhausted = "org_quota_exhausted"

type reserveBorrowBody struct {
	// Borrower is the member who borrowed the agent (the chat author). The
	// owner's bridge reports it explicitly because the driven turn is not
	// attributed to the borrower's session.
	Borrower string `json:"borrower"`
	// Model is the model the job requested ("" = none); the settle replaces
	// it with the model the core reported.
	Model string `json:"model"`
	// ChannelId is the room channel the turn is driven from; the run's audit
	// record carries it so an admin can see which topic drove a borrow.
	ChannelId string `json:"channelId"`
	// Sandboxed is the bridge's plan for the drive: true when it runs in the
	// owner's agent sandbox. A platform grant is refused (sandbox_required)
	// for a drive that would not, before anything is reserved.
	Sandboxed bool `json:"sandboxed"`
}

// handleReserveBorrow reserves one borrowed run against the owner's room
// authorization. Only the agent's OWNER may reserve (their bridge drives the
// borrow under their session): a non-owner could otherwise burn another
// member's quota or forge audit rows. The borrower must be another member of
// the room. Answers 200 {reserved:true, reservationId, expiresAtMs,
// sandboxRequired, defaultModel?} or 403 {reserved:false, denyReason,
// sandboxRequired}; every refusal is audited. sandboxRequired is true for a
// platform grant: its drive must run in the host's agent sandbox.
// defaultModel is the host's default model of a platform agent (absent when
// none): the bridge drives the turn with it when the job named no model.
func (s *Service) handleReserveBorrow(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tid := tenant.FromContext(r.Context())
	caller := callerSubject(r)
	if !s.requireMember(w, roomID, caller, tid) {
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	provider := providerFromTarget(r.PathValue("targetId"))
	if owner == "" || provider == "" {
		writeErr(w, http.StatusBadRequest, "owner and shared-agent:<provider> target required")
		return
	}
	if caller != owner {
		writeErr(w, http.StatusForbidden, "only the agent owner may reserve a borrow")
		return
	}
	var body reserveBorrowBody
	if !decodeJSON(w, r, &body) {
		return
	}
	borrower := strings.TrimSpace(body.Borrower)
	if borrower == "" || borrower == owner {
		writeErr(w, http.StatusBadRequest, "borrower must be a member other than the owner")
		return
	}
	if !s.Membership.IsMember(roomID, borrower, tid) {
		writeErr(w, http.StatusForbidden, "borrower is not a member of room")
		return
	}
	grantID := roomAuthGrantID(r.Context(), roomID, owner, provider)
	channelID := strings.TrimSpace(body.ChannelId)
	model := clipModel(body.Model)
	// A platform grant (docs/prd/2026-10-10-zero-install-onboarding.md §2)
	// is unlimited and outside the org's borrow ceilings, but usable only
	// while its agent is still published here, and only for a sandboxed
	// drive.
	current, getErr := s.Grants.GetGrant(r.Context(), grantID)
	platform := getErr == nil && current.Source == store.GrantSourcePlatform
	var spec wire.PlatformAgentSpec
	published := false
	if platform {
		spec, published = s.platformGrantSpec(r.Context(), current)
	}
	deny := func(reason string) {
		shareagent.RecordDecision(s.Metrics, "deny", reason)
		_ = s.Grants.AppendAudit(r.Context(), store.AuditRecord{
			RoomID: roomID, GrantID: grantID, ActorUserID: borrower, OwnerUserID: owner,
			Action: "turn", Decision: "deny", DenyReason: reason, Model: model, ChannelID: channelID, AtMs: s.Now(),
		})
		writeJSON(w, http.StatusForbidden, map[string]any{"reserved": false, "denyReason": reason, "sandboxRequired": platform})
	}
	switch {
	case platform && (current.RevokedAtMs != nil || current.ExpiresAtMs != nil):
		// Removed from the room (platform grants never expire).
		deny(DenyOwnerNotAuthorized)
		return
	case platform && !published:
		deny(shareagent.DenyPlatformAgentUnavailable)
		return
	case platform && !body.Sandboxed:
		deny(shareagent.DenySandboxRequired)
		return
	case !platform:
		// Org-level ceilings: on top of the per-grant quota, refuse once the
		// tenant's aggregate borrow runs or cost reach the tenant's configured
		// ceilings (0 = unlimited). Reserved runs are already in runs_used.
		// A failed read refuses the reservation (fails closed).
		runQuota, costAllowance, err := s.tenantCeilings(r.Context())
		if err != nil {
			obs.ServerError(w, "reserve borrow failed", err)
			writeErr(w, http.StatusInternalServerError, "reserve borrow failed")
			return
		}
		if runQuota > 0 || costAllowance > 0 {
			runs, cost, err := s.Grants.TenantUsage(r.Context())
			if err != nil {
				obs.ServerError(w, "reserve borrow failed", err)
				writeErr(w, http.StatusInternalServerError, "reserve borrow failed")
				return
			}
			if (runQuota > 0 && runs >= runQuota) || (costAllowance > 0 && cost >= costAllowance) {
				deny(DenyOrgQuotaExhausted)
				return
			}
		}
	}
	if platform && model == "" {
		// The host's default model drives a turn that named none: the audit
		// row records it as the run's requested model.
		model = clipModel(spec.DefaultModel)
	}
	now := s.Now()
	reservationID := ids.New("rsv_")
	grant, err := s.Grants.ReserveRun(r.Context(), store.RunReservation{
		ReservationID: reservationID, GrantID: grantID, RoomID: roomID, OwnerUserID: owner,
		BorrowerUserID: borrower, Model: model, ChannelID: channelID, AtMs: now,
	})
	switch {
	case errors.Is(err, store.ErrGrantNotFound):
		deny(DenyOwnerNotAuthorized)
		return
	case errors.Is(err, store.ErrRunQuotaExhausted), errors.Is(err, store.ErrCostAllowanceExhausted):
		deny(shareagent.ConsumeDenyReason(err))
		return
	case err != nil:
		obs.ServerError(w, "reserve borrow failed", err)
		writeErr(w, http.StatusInternalServerError, "reserve borrow failed")
		return
	}
	shareagent.RecordDecision(s.Metrics, "allow", "")
	resp := map[string]any{
		"reserved":        true,
		"reservationId":   reservationID,
		"expiresAtMs":     now + store.RunReservationTTLms,
		"runsUsed":        grant.RunsUsed,
		"sandboxRequired": grant.Source == store.GrantSourcePlatform,
	}
	if grant.RunQuota != nil {
		resp["runQuota"] = *grant.RunQuota
	}
	if platform && spec.DefaultModel != "" {
		resp["defaultModel"] = spec.DefaultModel
	}
	writeJSON(w, http.StatusOK, resp)
}

// clipModel trims a core-reported model label and clips an over-long one to
// the audit column rather than refusing the call (which would drop the run).
func clipModel(model string) string {
	model = strings.TrimSpace(model)
	if r := []rune(model); len(r) > maxRecordedModel {
		model = string(r[:maxRecordedModel])
	}
	return model
}
