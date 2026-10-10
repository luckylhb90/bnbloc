package shareagenthttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Borrow quota requests (F3). A borrower asks an agent's owner for more runs
// on the owner's room authorization; the owner approves (raising the
// authorization's run quota, keeping its usage history) or declines. Like the
// authorization itself this is fail-closed on identity: the requester and the
// deciding owner are always X-Bnbloc-User, never a body field, and only the
// request's owner may decide it.
//
//	POST /v1/borrow/quota-requests                      {roomId, owner, provider, requestedRuns?, note}
//	GET  /v1/borrow/quota-requests?role=owner|requester [&status=][&roomId=]
//	POST /v1/borrow/quota-requests/{requestId}/approve  {runs?}
//	POST /v1/borrow/quota-requests/{requestId}/decline

// Quota request bounds: one request adds at most maxQuotaRequestRuns runs, and
// a note is a short human message.
const (
	maxQuotaRequestRuns = 10_000
	maxQuotaNoteRunes   = 500
)

func quotaRequestView(q store.QuotaRequest) wire.QuotaRequest {
	return wire.QuotaRequest{
		RequestID:     q.RequestID,
		RoomID:        q.RoomID,
		Owner:         q.OwnerUserID,
		Requester:     q.RequesterUserID,
		Provider:      q.Provider,
		RequestedRuns: q.RequestedRuns,
		Note:          q.Note,
		Status:        string(q.Status),
		GrantedRuns:   q.GrantedRuns,
		CreatedAtMs:   q.CreatedAtMs,
		DecidedAtMs:   q.DecidedAtMs,
	}
}

func validRuns(n *int) bool { return n == nil || (*n >= 1 && *n <= maxQuotaRequestRuns) }

func newQuotaRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "qr_" + hex.EncodeToString(b)
}

// handleCreateQuotaRequest records a borrower's request. The requester must be
// a room member other than the owner, and the owner must hold a live,
// run-limited authorization of the provider in the room (there is nothing to
// raise otherwise).
func (s *Service) handleCreateQuotaRequest(w http.ResponseWriter, r *http.Request) {
	if s.QuotaRequests == nil {
		obs.ServerError(w, "quota requests not configured", nil)
		writeErr(w, http.StatusNotImplemented, "quota requests not configured")
		return
	}
	requester := callerSubject(r)
	var in wire.CreateQuotaRequestInput
	if !decodeJSON(w, r, &in) {
		return
	}
	roomID, err := roomctx.Normalize(in.RoomID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	tid := tenant.FromContext(r.Context())
	if !s.requireMember(w, roomID, requester, tid) {
		return
	}
	owner := strings.TrimSpace(in.Owner)
	provider := strings.TrimSpace(in.Provider)
	if owner == "" || provider == "" {
		writeErr(w, http.StatusBadRequest, "owner and provider are required")
		return
	}
	if strings.EqualFold(owner, requester) {
		writeErr(w, http.StatusBadRequest, "you cannot request quota on your own agent")
		return
	}
	if !validRuns(in.RequestedRuns) {
		writeErr(w, http.StatusBadRequest, "requestedRuns must be between 1 and 10000")
		return
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > maxQuotaNoteRunes {
		writeErr(w, http.StatusBadRequest, "note must be at most 500 characters")
		return
	}
	grant, live := s.liveAuthorization(r.Context(), roomID, owner, provider)
	if !live {
		writeErr(w, http.StatusConflict, "the owner has no active authorization for this agent in this room")
		return
	}
	if grant.RunQuota == nil {
		writeErr(w, http.StatusConflict, "this authorization has no run limit")
		return
	}
	created, err := s.QuotaRequests.CreateQuotaRequest(r.Context(), store.QuotaRequest{
		RequestID:       newQuotaRequestID(),
		RoomID:          roomID,
		OwnerUserID:     owner,
		RequesterUserID: requester,
		Provider:        provider,
		RequestedRuns:   in.RequestedRuns,
		Note:            note,
		CreatedAtMs:     s.Now(),
	})
	if errors.Is(err, store.ErrQuotaRequestPending) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "you already have a pending request for this agent",
			"request": quotaRequestView(created),
		})
		return
	}
	if err != nil {
		obs.ServerError(w, "create quota request failed", err)
		writeErr(w, http.StatusInternalServerError, "create quota request failed")
		return
	}
	s.notifyQuotaRequest(r.Context(), created)
	writeJSON(w, http.StatusCreated, map[string]any{"request": quotaRequestView(created)})
}

// handleListQuotaRequests lists the caller's requests: role=owner those
// addressed to the caller, role=requester (default) those the caller made.
func (s *Service) handleListQuotaRequests(w http.ResponseWriter, r *http.Request) {
	if s.QuotaRequests == nil {
		obs.ServerError(w, "quota requests not configured", nil)
		writeErr(w, http.StatusNotImplemented, "quota requests not configured")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	query := r.URL.Query()
	q := store.QuotaRequestQuery{}
	switch query.Get("role") {
	case "owner":
		q.OwnerUserID = caller
	case "", "requester":
		q.RequesterUserID = caller
	default:
		writeErr(w, http.StatusBadRequest, "role must be owner or requester")
		return
	}
	switch status := store.QuotaRequestStatus(query.Get("status")); status {
	case "", store.QuotaRequestPending, store.QuotaRequestApproved, store.QuotaRequestDeclined:
		q.Status = status
	default:
		writeErr(w, http.StatusBadRequest, "status must be pending, approved, or declined")
		return
	}
	if v := query.Get("roomId"); v != "" {
		roomID, err := roomctx.Normalize(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		q.RoomID = roomID
	}
	reqs := s.QuotaRequests.ListQuotaRequests(r.Context(), q)
	out := make([]wire.QuotaRequest, 0, len(reqs))
	for _, rq := range reqs {
		out = append(out, quotaRequestView(rq))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// decidableRequest loads a pending request the caller owns, writing the error
// response otherwise. Only the request's owner may decide it — never its
// requester, whatever the body says.
func (s *Service) decidableRequest(w http.ResponseWriter, r *http.Request) (store.QuotaRequest, bool) {
	if s.QuotaRequests == nil {
		obs.ServerError(w, "quota requests not configured", nil)
		writeErr(w, http.StatusNotImplemented, "quota requests not configured")
		return store.QuotaRequest{}, false
	}
	caller := callerSubject(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return store.QuotaRequest{}, false
	}
	req, err := s.QuotaRequests.GetQuotaRequest(r.Context(), r.PathValue("requestId"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "quota request not found")
		return store.QuotaRequest{}, false
	}
	if !strings.EqualFold(req.OwnerUserID, caller) {
		writeErr(w, http.StatusForbidden, "only the agent owner may decide this request")
		return store.QuotaRequest{}, false
	}
	if req.Status != store.QuotaRequestPending {
		writeErr(w, http.StatusConflict, "quota request already decided")
		return store.QuotaRequest{}, false
	}
	return req, true
}

// handleApproveQuotaRequest raises the owner's room authorization run quota by
// the approved amount (body runs, else the requested runs). The raise keeps
// the authorization's usage history — it never goes through the re-authorize
// upsert, which resets usage.
func (s *Service) handleApproveQuotaRequest(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decidableRequest(w, r)
	if !ok {
		return
	}
	var in wire.ApproveQuotaRequestInput
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	runs := in.Runs
	if runs == nil {
		runs = req.RequestedRuns
	}
	if runs == nil {
		writeErr(w, http.StatusBadRequest, "runs is required (the request named no amount)")
		return
	}
	if !validRuns(runs) {
		writeErr(w, http.StatusBadRequest, "runs must be between 1 and 10000")
		return
	}
	owner := callerSubject(r)
	if _, live := s.liveAuthorization(r.Context(), req.RoomID, owner, req.Provider); !live {
		writeErr(w, http.StatusConflict, "your authorization for this agent is no longer active; authorize it again first")
		return
	}
	decided, err := s.QuotaRequests.DecideQuotaRequest(r.Context(), store.QuotaDecision{
		RequestID: req.RequestID, OwnerUserID: owner, Status: store.QuotaRequestApproved, GrantedRuns: runs, AtMs: s.Now(),
	})
	if err != nil {
		s.writeDecideErr(w, err)
		return
	}
	grant, err := s.Grants.RaiseRunQuota(r.Context(), roomAuthGrantID(r.Context(), req.RoomID, owner, req.Provider), *runs)
	if err != nil {
		// The authorization was revoked (or made unlimited) between the check
		// and the raise: return the request to pending rather than leave it
		// approved with no effect.
		_ = s.QuotaRequests.ReopenQuotaRequest(r.Context(), req.RequestID)
		if errors.Is(err, store.ErrGrantNotFound) || errors.Is(err, store.ErrRunQuotaUnlimited) {
			writeErr(w, http.StatusConflict, "your authorization for this agent changed; try again")
			return
		}
		obs.ServerError(w, "raise run quota failed", err)
		writeErr(w, http.StatusInternalServerError, "raise run quota failed")
		return
	}
	resp := map[string]any{"request": quotaRequestView(decided), "runsUsed": grant.RunsUsed}
	if grant.RunQuota != nil {
		resp["runQuota"] = *grant.RunQuota
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDeclineQuotaRequest declines a pending request (owner only).
func (s *Service) handleDeclineQuotaRequest(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decidableRequest(w, r)
	if !ok {
		return
	}
	decided, err := s.QuotaRequests.DecideQuotaRequest(r.Context(), store.QuotaDecision{
		RequestID: req.RequestID, OwnerUserID: callerSubject(r), Status: store.QuotaRequestDeclined, AtMs: s.Now(),
	})
	if err != nil {
		s.writeDecideErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": quotaRequestView(decided)})
}

func (s *Service) writeDecideErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrQuotaRequestDecided):
		writeErr(w, http.StatusConflict, "quota request already decided")
	case errors.Is(err, store.ErrQuotaRequestNotFound):
		writeErr(w, http.StatusNotFound, "quota request not found")
	default:
		obs.ServerError(w, "decide quota request failed", err)
		writeErr(w, http.StatusInternalServerError, "decide quota request failed")
	}
}

// liveAuthorization returns the owner's room authorization grant for the
// provider when it is live (present, not revoked, not expired; a platform
// grant also still published by its host). It is the one
// liveness rule: roomAuthorized and the quota request paths all read it.
func (s *Service) liveAuthorization(ctx context.Context, roomID, owner, provider string) (store.Grant, bool) {
	g, err := s.Grants.GetGrant(ctx, roomAuthGrantID(ctx, roomID, owner, provider))
	if err != nil || g.RevokedAtMs != nil || (g.ExpiresAtMs != nil && s.Now() >= *g.ExpiresAtMs) {
		return store.Grant{}, false
	}
	// A platform grant lives only while its host still publishes the agent
	// (unpublishing takes effect at read time).
	if g.Source == store.GrantSourcePlatform {
		if _, published := s.platformGrantSpec(ctx, g); !published {
			return store.Grant{}, false
		}
	}
	return g, true
}
