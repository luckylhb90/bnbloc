package shareagenthttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Borrow quota requests (F3). A borrower asks an agent's owner for more runs
// on the owner's room authorization; the owner approves (raising the
// authorization's run quota, keeping its usage history) or declines. When
// the owner has no live authorization the request asks for access (kind
// access, docs/prd/2026-10-14-multi-member-rooms.md rule 9): approving it
// authorizes the agent in the room in one step. A borrow the reservation
// refused for want of an authorization records that request itself
// (requestAccess). Like the
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
		Kind:          string(quotaKind(q)),
		RequestedRuns: q.RequestedRuns,
		Note:          q.Note,
		Status:        string(q.Status),
		GrantedRuns:   q.GrantedRuns,
		CreatedAtMs:   q.CreatedAtMs,
		DecidedAtMs:   q.DecidedAtMs,
	}
}

// quotaKind is a request's kind (more runs when it names none).
func quotaKind(q store.QuotaRequest) store.QuotaRequestKind {
	if q.Kind == "" {
		return store.QuotaRequestMoreRuns
	}
	return q.Kind
}

func validRuns(n *int) bool { return n == nil || (*n >= 1 && *n <= maxQuotaRequestRuns) }

// handleCreateQuotaRequest records a borrower's request. The requester must be
// a room member other than the owner, and the owner must hold a live,
// run-limited authorization of the provider in the room (there is nothing to
// raise otherwise).
func (s *Service) handleCreateQuotaRequest(w http.ResponseWriter, r *http.Request) {
	if s.QuotaRequests == nil {
		httpx.Unavailable(w, "quota requests")
		return
	}
	requester := callerSubject(r)
	var in wire.CreateQuotaRequestInput
	if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &in) {
		return
	}
	roomID, err := roomctx.Normalize(in.RoomID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Asking for more runs is part of borrowing: a member who may chat.
	if _, ok := s.requireMember(w, r, roomID, requester, membership.Chat); !ok {
		return
	}
	owner := strings.TrimSpace(in.Owner)
	provider := strings.TrimSpace(in.Provider)
	if owner == "" || provider == "" {
		httpx.Error(w, "owner and provider are required", http.StatusBadRequest)
		return
	}
	if owner == requester {
		httpx.Error(w, "you cannot request quota on your own agent", http.StatusBadRequest)
		return
	}
	if !validRuns(in.RequestedRuns) {
		httpx.Error(w, "requestedRuns must be between 1 and 10000", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > maxQuotaNoteRunes {
		httpx.Error(w, "note must be at most 500 characters", http.StatusBadRequest)
		return
	}
	// No live authorization: the request asks for access.
	kind := store.QuotaRequestAccess
	if grant, live := s.liveAuthorization(r.Context(), roomID, owner, provider); live {
		if grant.RunQuota == nil {
			httpx.Error(w, "this authorization has no run limit", http.StatusConflict)
			return
		}
		kind = store.QuotaRequestMoreRuns
	}
	created, err := s.QuotaRequests.CreateQuotaRequest(r.Context(), store.QuotaRequest{
		RequestID:       ids.New("qr_"),
		RoomID:          roomID,
		OwnerUserID:     owner,
		RequesterUserID: requester,
		Provider:        provider,
		Kind:            kind,
		RequestedRuns:   in.RequestedRuns,
		Note:            note,
		CreatedAtMs:     s.Now(),
	})
	if errors.Is(err, store.ErrQuotaRequestPending) {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":   "you already have a pending request for this agent",
			"request": quotaRequestView(created),
		})

		return
	}
	if err != nil {
		obs.ServerError(w, "create quota request failed", err)
		httpx.Error(w, "create quota request failed", http.StatusInternalServerError)
		return
	}
	s.notifyQuotaRequest(r.Context(), created)
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"request": quotaRequestView(created)})
}

// handleListQuotaRequests lists the caller's requests: role=owner those
// addressed to the caller, role=requester (default) those the caller made.
func (s *Service) handleListQuotaRequests(w http.ResponseWriter, r *http.Request) {
	if s.QuotaRequests == nil {
		httpx.Unavailable(w, "quota requests")
		return
	}
	caller := callerSubject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
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
		httpx.Error(w, "role must be owner or requester", http.StatusBadRequest)
		return
	}
	switch status := store.QuotaRequestStatus(query.Get("status")); status {
	case "", store.QuotaRequestPending, store.QuotaRequestApproved, store.QuotaRequestDeclined:
		q.Status = status
	default:
		httpx.Error(w, "status must be pending, approved, or declined", http.StatusBadRequest)
		return
	}
	if v := query.Get("roomId"); v != "" {
		roomID, err := roomctx.Normalize(v)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		q.RoomID = roomID
	}
	reqs, err := s.QuotaRequests.ListQuotaRequests(r.Context(), q)
	if err != nil {
		obs.ServerError(w, "list quota requests failed", err)
		httpx.Error(w, "list quota requests failed", http.StatusInternalServerError)
		return
	}
	out := make([]wire.QuotaRequest, 0, len(reqs))
	for _, rq := range reqs {
		out = append(out, quotaRequestView(rq))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// decidableRequest loads a pending request the caller owns, writing the error
// response otherwise. Only the request's owner may decide it — never its
// requester, whatever the body says.
func (s *Service) decidableRequest(w http.ResponseWriter, r *http.Request) (store.QuotaRequest, bool) {
	if s.QuotaRequests == nil {
		httpx.Unavailable(w, "quota requests")
		return store.QuotaRequest{}, false
	}
	caller := callerSubject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return store.QuotaRequest{}, false
	}
	req, err := s.QuotaRequests.GetQuotaRequest(r.Context(), r.PathValue("requestId"))
	if err != nil {
		httpx.Error(w, "quota request not found", http.StatusNotFound)
		return store.QuotaRequest{}, false
	}
	if req.OwnerUserID != caller {
		httpx.Error(w, "only the agent owner may decide this request", http.StatusForbidden)
		return store.QuotaRequest{}, false
	}
	if req.Status != store.QuotaRequestPending {
		httpx.Error(w, "quota request already decided", http.StatusConflict)
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
	if !httpx.DecodeOptionalJSON(w, r, httpx.DefaultBodyLimit, &in) {
		return
	}
	runs := in.Runs
	if runs == nil {
		runs = req.RequestedRuns
	}
	if !validRuns(runs) {
		httpx.Error(w, "runs must be between 1 and 10000", http.StatusBadRequest)
		return
	}
	owner := callerSubject(r)
	if quotaKind(req) == store.QuotaRequestAccess {
		s.approveAccess(w, r, req, owner, runs)
		return
	}
	if runs == nil {
		httpx.Error(w, "runs is required (the request named no amount)", http.StatusBadRequest)
		return
	}
	if _, live := s.liveAuthorization(r.Context(), req.RoomID, owner, req.Provider); !live {
		httpx.Error(w, "your authorization for this agent is no longer active; authorize it again first", http.StatusConflict)
		return
	}
	decided, err := s.QuotaRequests.DecideQuotaRequest(r.Context(), store.QuotaDecision{
		RequestID: req.RequestID, OwnerUserID: owner, Status: store.QuotaRequestApproved, GrantedRuns: runs, AtMs: s.Now(),
	})
	if err != nil {
		s.writeDecideErr(w, err)
		return
	}
	grant, err := s.Grants.RaiseRunQuota(r.Context(), roomAuthGrantID(r.Context(), req.RoomID, owner, req.Provider), *runs, s.Now())
	if err != nil {
		// The authorization was revoked (or made unlimited) between the check
		// and the raise: return the request to pending rather than leave it
		// approved with no effect.
		_ = s.QuotaRequests.ReopenQuotaRequest(r.Context(), req.RequestID)
		if errors.Is(err, store.ErrGrantNotFound) || errors.Is(err, store.ErrRunQuotaUnlimited) {
			httpx.Error(w, "your authorization for this agent changed; try again", http.StatusConflict)
			return
		}
		obs.ServerError(w, "raise run quota failed", err)
		httpx.Error(w, "raise run quota failed", http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"request": quotaRequestView(decided), "runsUsed": grant.RunsUsed}
	if grant.RunQuota != nil {
		resp["runQuota"] = *grant.RunQuota
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// approveAccess approves an access request: the owner's room authorization
// of the provider (authorizeRoom's defaults; runs, when given, its run
// quota), unless one is live already (the owner authorized meanwhile).
func (s *Service) approveAccess(w http.ResponseWriter, r *http.Request, req store.QuotaRequest, owner string, runs *int) {
	decided, err := s.QuotaRequests.DecideQuotaRequest(r.Context(), store.QuotaDecision{
		RequestID: req.RequestID, OwnerUserID: owner, Status: store.QuotaRequestApproved, GrantedRuns: runs, AtMs: s.Now(),
	})
	if err != nil {
		s.writeDecideErr(w, err)
		return
	}
	grant, live := s.liveAuthorization(r.Context(), req.RoomID, owner, req.Provider)
	if !live {
		if grant, err = s.authorizeRoom(r.Context(), req.RoomID, owner, req.Provider, authorizeBody{RunQuota: runs}); err != nil {
			_ = s.QuotaRequests.ReopenQuotaRequest(r.Context(), req.RequestID)
			obs.ServerError(w, "authorize failed", err)
			httpx.Error(w, "authorize failed", http.StatusInternalServerError)
			return
		}
	}
	resp := map[string]any{"request": quotaRequestView(decided), "authorized": true, "grantId": grant.GrantID}
	if grant.ExpiresAtMs != nil {
		resp["expiresAtMs"] = *grant.ExpiresAtMs
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// requestAccess records borrower's access request to owner for provider in
// roomID (a reservation refused for want of an authorization) and notifies
// the owner; an open request already there is answered as is. Best-effort:
// it reports whether a request is open.
func (s *Service) requestAccess(ctx context.Context, roomID, owner, borrower, provider string) (store.QuotaRequest, bool) {
	if s.QuotaRequests == nil {
		return store.QuotaRequest{}, false
	}
	created, err := s.QuotaRequests.CreateQuotaRequest(ctx, store.QuotaRequest{
		RequestID: ids.New("qr_"), RoomID: roomID, OwnerUserID: owner, RequesterUserID: borrower,
		Provider: provider, Kind: store.QuotaRequestAccess, CreatedAtMs: s.Now(),
	})
	switch {
	case errors.Is(err, store.ErrQuotaRequestPending):
		return created, true
	case err != nil:
		slog.Warn("bnbloc shareagent: access request not recorded", "event", "bnbloc.shareagent.access_request_failed", "room", roomID, "error", err)
		return store.QuotaRequest{}, false
	}
	s.notifyQuotaRequest(ctx, created)
	return created, true
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"request": quotaRequestView(decided)})
}

func (s *Service) writeDecideErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrQuotaRequestDecided):
		httpx.Error(w, "quota request already decided", http.StatusConflict)
	case errors.Is(err, store.ErrQuotaRequestNotFound):
		httpx.Error(w, "quota request not found", http.StatusNotFound)
	default:
		obs.ServerError(w, "decide quota request failed", err)
		httpx.Error(w, "decide quota request failed", http.StatusInternalServerError)
	}
}

// liveAuthorization returns the owner's room authorization grant for the
// provider when it is usable (usableGrant). roomAuthorized and the quota
// request paths all read it.
func (s *Service) liveAuthorization(ctx context.Context, roomID, owner, provider string) (store.Grant, bool) {
	id, _, _ := s.borrowGrant(ctx, roomID, owner, provider)
	g, err := s.Grants.GetGrant(ctx, id)
	if err != nil || !s.usableGrant(ctx, g) {
		return store.Grant{}, false
	}
	return g, true
}

// usableGrant is the one policy-aware liveness rule of a grant: live now
// (store.Grant.Live), and a platform grant only while its host still
// publishes the agent (unpublishing takes effect at read time).
func (s *Service) usableGrant(ctx context.Context, g store.Grant) bool {
	if !g.Live(s.Now()) {
		return false
	}
	if g.Source == store.GrantSourcePlatform {
		_, published := s.platformGrantSpec(ctx, g)
		return published
	}
	return true
}
