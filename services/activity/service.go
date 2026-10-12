// Package activity implements the bnbloc-activity HTTP service: the server side
// of the open-source SessionActivityReporter, plus the atomic
// submit-provenance admission barrier. All routes are scoped by roomId, which
// must equal the report's workspaceId (roomId ≡ workspaceId).
package activity

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Membership authorizes a caller for a room through membership.RoomScope.
// bnbloc-activity never accepts a report for a room the caller is not a
// member of, in the room's own tenant (fail closed).
type Membership = membership.Directory

// Service holds the activity handler dependencies.
type Service struct {
	Store      store.ActivityStore
	Publisher  realtime.Publisher
	Membership Membership
	BridgeJobs store.BridgeJobStore
	// AllowServeAllOwners is the SERVER-side gate for single-host serve-all
	// testing (one bridge driving every member's agent). Only when it is set
	// does a bridge's serveAllOwners request take effect, and only then may a
	// caller attribute an agent reply/state to another room member. It is set
	// from BNBLOC_ACTIVITY_ALLOW_SERVE_ALL and refused in production; a request
	// body alone can never grant it.
	AllowServeAllOwners bool
	// Team, Usage, and Rooms back the read-only team agent dashboard (F1):
	// tenant-wide agents/jobs, reported reply metrics, and room names. nil
	// Team answers 501; nil Usage/Rooms omit those fields.
	Team  store.TeamAgentReader
	Usage store.AgentUsageReader
	Rooms store.RoomStore
	// Runs backs the agent run records (R10); nil answers 501. Feed lets the
	// run detail resolve the first reply's room sequence (nil = omitted).
	Runs store.AgentRunReader
	Feed RoomFeedReader
	// Now is the dashboard clock (nil = time.Now). It must be safe for
	// concurrent use: request handlers and the debounced budget evaluation
	// (a timer goroutine, noteBudgetBurn) call it from their own goroutines.
	Now func() time.Time
	// Budgets holds the monthly usage budgets (R12); nil answers the budget
	// routes with 501 and refuses no drive. Tenants resolves budget members
	// and the org admins a tenant budget warning goes to; Notifications sends
	// budget warnings (nil = none). PublicURL links a warning to the web app.
	Budgets       store.UsageBudgetStore
	Tenants       store.TenantStore
	Notifications BudgetNotifier
	PublicURL     string
	// BudgetDebounce coalesces budget warning evaluations (0 = 5s).
	BudgetDebounce time.Duration
	budgetWatch    budgetWatch
	// Metrics records bridge heartbeats and online bridges (nil = off).
	Metrics *obs.Metrics
	// Grants and Settings let the room agent catalogs admit an agent-host
	// member's platform agents (the room's platform grants and the
	// platform-agent policy); nil leaves hosts' agents out (fail closed).
	Grants   store.GrantStore
	Settings store.PlatformSettingsStore
	// Leases admits a bridge's team plan for the job it runs
	// (store.JobLeasedBy); nil refuses the ?job= form.
	Leases store.AgentDispatchReader
}

// Handler builds the HTTP mux for the activity service.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/rooms/{roomId}/agent-activity/session-state", s.handleSessionState)
	mux.HandleFunc("POST /v1/rooms/{roomId}/agent-activity/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/rooms/{roomId}/agent-activity/submit-provenance", s.handleSubmitProvenance)
	mux.HandleFunc("GET /v1/rooms/{roomId}/agent-activity/sync-states", s.handleSyncStates)
	mux.HandleFunc("POST /v1/bridge/heartbeat", s.handleBridgeHeartbeat)
	mux.HandleFunc("GET /v1/my/agents", s.handleMyAgents)
	// The model catalogs of the agents a member may address in a room.
	mux.HandleFunc("GET /v1/rooms/{roomId}/agent-catalogs", s.handleRoomAgentCatalogs)
	// Which agent takes each role of an expert team, and its estimate.
	mux.HandleFunc("POST /v1/rooms/{roomId}/team-plan", s.handleTeamPlan)
	mux.HandleFunc("POST /v1/bridge/jobs/claim", s.handleBridgeJobClaim)
	mux.HandleFunc("POST /v1/bridge/jobs/{jobId}/status", s.handleBridgeJobStatus)
	mux.HandleFunc("POST /v1/bridge/jobs/{jobId}/lease", s.handleBridgeJobLease)
	mux.HandleFunc("GET /v1/rooms/{roomId}/agent-jobs", s.handleRoomAgentJobs)
	mux.HandleFunc("POST /v1/rooms/{roomId}/agent-jobs/{jobId}/cancel", s.handleCancelAgentJob)
	// Team agent dashboard (F1): own agents, and the tenant-wide view behind
	// the gateway's manage_org gate on /v1/tenant/*.
	mux.HandleFunc("GET /v1/team/agents", s.handleTeamAgents(teamScopeSelf))
	mux.HandleFunc("GET /v1/tenant/team/agents", s.handleTeamAgents(teamScopeTenant))
	// Usage budgets (R12): the org admin's budget set (manage_org gate on
	// /v1/tenant/*) and the caller's own budget and burn.
	mux.HandleFunc("GET /v1/tenant/budgets", s.handleTenantBudgets)
	mux.HandleFunc("PUT /v1/tenant/budgets", s.handlePutTenantBudgets)
	mux.HandleFunc("GET /v1/my/budget", s.handleMyBudget)
	// Agent run records (R10): a room's runs (members), the caller's own
	// (their agents + their requests), and the tenant's (manage_org gate).
	mux.HandleFunc("GET /v1/rooms/{roomId}/jobs", s.handleRoomRuns)
	mux.HandleFunc("GET /v1/rooms/{roomId}/jobs/{jobId}", s.handleRoomRun)
	mux.HandleFunc("GET /v1/my/jobs", s.handleRuns(wire.RunScopeSelf))
	mux.HandleFunc("GET /v1/tenant/jobs", s.handleRuns(wire.RunScopeTenant))
	return svcauth.EnsureTenant(mux)
}

func (s *Service) handleSessionState(w http.ResponseWriter, r *http.Request) {
	var in wire.ReportSessionStateInput
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &in) {
		return
	}
	roomID, err := roomctx.RequireEqual(r.PathValue("roomId"), in.WorkspaceID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	owner, ok := s.attributeOwner(r, roomID, in.OwnerUserID)
	if !ok {
		httpx.Error(w, "agent owner mismatch", http.StatusForbidden)
		return
	}
	in.OwnerUserID = owner
	cursor, err := s.Store.PutSessionState(r.Context(), in)
	if err != nil {
		obs.ServerError(w, "session state failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Publisher.Publish(r.Context(), wire.Event{RoomID: roomID, Kind: "agent_session_state", Payload: in, AtUnixMs: in.OccurredAtUnixMs})
	httpx.WriteJSON(w, http.StatusOK, wire.ReportSessionStateReply{SyncCursor: cursor})
}

func (s *Service) handleMessages(w http.ResponseWriter, r *http.Request) {
	var in wire.ReportMessagesInput
	if !httpx.DecodeJSON(w, r, maxMessagesBodyBytes, &in) {
		return
	}
	roomID, err := roomctx.RequireEqual(r.PathValue("roomId"), in.WorkspaceID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	// Every reply is attributed to an agent owner (payload ownerUserId). The
	// attribution must be the authenticated caller — their own bridge posting
	// their own agent's reply — or a member could post as another member's
	// agent. A missing owner is filled with the caller.
	for i := range in.Messages {
		p, isMap := in.Messages[i].Payload.(map[string]any)
		if !isMap {
			continue
		}
		asserted, _ := p["ownerUserId"].(string)
		owner, ok := s.attributeOwner(r, roomID, asserted)
		if !ok {
			httpx.Error(w, "agent owner mismatch", http.StatusForbidden)
			return
		}
		p["ownerUserId"] = owner
	}
	fresh, next, err := s.Store.AppendMessages(r.Context(), roomID, in)
	if err != nil {
		obs.ServerError(w, "messages failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Publish only genuinely-new messages so a reporter that re-drains (restart,
	// reconnect) does not duplicate replies in the room. An all-replay batch
	// publishes nothing.
	if len(fresh) > 0 {
		out := in
		out.Messages = fresh
		s.Publisher.Publish(r.Context(), wire.Event{RoomID: roomID, ChannelID: in.ChannelID, Kind: "agent_messages", Payload: out})
		// Reported usage moves the owners' monthly budgets (R12).
		s.noteReplyBurn(r.Context(), fresh)
	}
	httpx.WriteJSON(w, http.StatusOK, wire.ReportMessagesReply{NextCursor: next})
}

// handleSubmitProvenance is the atomic admission barrier. It acks only after
// the client-submit id is durably queryable. A duplicate id is a benign replay
// and returns 200 (idempotent), never a second provider admission.
func (s *Service) handleSubmitProvenance(w http.ResponseWriter, r *http.Request) {
	var in wire.SubmitProvenanceInput
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &in) {
		return
	}
	roomID, err := roomctx.RequireEqual(r.PathValue("roomId"), in.WorkspaceID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	if strings.TrimSpace(in.ClientSubmitID) == "" {
		httpx.Error(w, "clientSubmitId is required", http.StatusBadRequest)
		return
	}
	if err := s.Store.RecordSubmitProvenance(r.Context(), in); err != nil && !errors.Is(err, store.ErrDuplicateSubmit) {
		obs.ServerError(w, "submit provenance failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Barrier contract: only ack once the id is queryable.
	if !s.Store.SubmitQueryable(r.Context(), roomID, in.AgentSessionID, in.ClientSubmitID) {
		obs.ServerError(w, "submit provenance not queryable after write", nil)
		httpx.Error(w, "submit provenance not queryable after write", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Service) handleSyncStates(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.SyncStatesReply{States: s.Store.SyncStates(r.Context(), roomID)})
}

// authorize enforces room membership (fail closed). The caller identity is the
// gateway-verified subject forwarded in X-Bnbloc-User; a missing subject or a
// non-member is rejected.
func (s *Service) authorize(w http.ResponseWriter, r *http.Request, roomID string) bool {
	userID := svcauth.Subject(r)
	if userID == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return false
	}
	if _, err := membership.RoomScope(r.Context(), s.Membership, roomID, userID, membership.AnyMember); err != nil {
		httpx.Error(w, err.Error(), http.StatusForbidden)
		return false
	}
	return true
}

// attributeOwner resolves the agent owner a report is attributed to. The
// authenticated caller always may attribute to themselves (empty asserted
// means the caller). Attributing to someone else is only possible under the
// server-side serve-all gate, and only to a member of the room — never from a
// body field alone.
func (s *Service) attributeOwner(r *http.Request, roomID, asserted string) (string, bool) {
	caller := svcauth.Subject(r)
	asserted = strings.TrimSpace(asserted)
	if asserted == "" || asserted == caller {
		return caller, true
	}
	if !s.AllowServeAllOwners {
		return "", false
	}
	if _, err := membership.RoomScope(r.Context(), s.Membership, roomID, asserted, membership.AnyMember); err == nil {
		return asserted, true
	}
	return "", false
}

// Request body caps. A message batch carries completed assistant replies
// (bounded by the bridge's per-post batching); everything else is a small
// control document.
const (
	maxBodyBytes         = 64 << 10
	maxMessagesBodyBytes = 4 << 20
)
