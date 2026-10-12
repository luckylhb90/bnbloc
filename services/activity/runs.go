package activity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Agent run records (R10): every agent job as a record — request, addressed
// agent, per-phase timing, outcome, reply preview, reported usage.
//
//   - GET /v1/rooms/{roomId}/jobs[?before=&limit=&status=&from=&to=&owner=&core=&provider=]
//     — a room's runs, room members only;
//   - GET /v1/rooms/{roomId}/jobs/{jobId} — one run with the first reply's
//     room sequence (jump target), and its raw diagnostics for the agent's
//     owner and org admins only;
//   - GET /v1/my/jobs — runs of the caller's agents plus runs the caller
//     requested;
//   - GET /v1/tenant/jobs[?member=] — every run in the ctx tenant. The gateway
//     gates /v1/tenant/* on manage_org, so this service needs no role logic.
//
// Outside the room scope a viewer may not be a member of a run's room (an
// org admin, an owner who left): such runs keep their metadata, outcome and
// usage but withhold the request/reply text (Redacted).
//
// Raw diagnostics (the bridge's session ids, error text, reply and usage
// summaries as reported) are the agent owner's and an org admin's (the
// signed org role, rbac.Can manage_org): every other viewer reads the
// projection (error, warning, reply, usage) and never the raw map (O4).

// RoomFeedReader pages the persisted room feed (the chat store's RoomFeed); the
// run detail route reads it to find the first reply's room sequence.
type RoomFeedReader interface {
	RoomFeed(ctx context.Context, roomID string, after int64, limit int) ([]store.RoomMessage, error)
}

// Bounds of the reply-sequence lookup: feed pages read after the request.
const (
	runReplyFeedPage  = 200
	runReplyFeedPages = 5
)

// runActiveStatuses is the "active" status alias.
var runActiveStatuses = []store.AgentJobStatus{store.AgentJobQueued, store.AgentJobClaimed, store.AgentJobRunning, store.AgentJobReflowing}

// parseRunQuery reads the shared listing filters.
func parseRunQuery(r *http.Request) (store.AgentRunQuery, error) {
	v := r.URL.Query()
	q := store.AgentRunQuery{
		Owner:    strings.TrimSpace(v.Get("owner")),
		Core:     strings.TrimSpace(v.Get("core")),
		Provider: strings.TrimSpace(v.Get("provider")),
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > store.MaxAgentRunsPage {
			return q, errors.New("limit must be 1-100")
		}
		q.Limit = n
	}
	for _, f := range []struct {
		name string
		dst  *int64
	}{{"from", &q.FromMs}, {"to", &q.ToMs}} {
		if s := v.Get(f.name); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n <= 0 {
				return q, errors.New(f.name + " must be a unix time in milliseconds")
			}
			*f.dst = n
		}
	}
	if s := strings.TrimSpace(v.Get("status")); s != "" {
		for _, part := range strings.Split(s, ",") {
			st := store.AgentJobStatus(strings.TrimSpace(part))
			switch {
			case st == "active":
				q.Statuses = append(q.Statuses, runActiveStatuses...)
			case st == store.AgentJobQueued || validJobStatus(st):
				q.Statuses = append(q.Statuses, st)
			default:
				return q, errors.New("unknown status " + string(st))
			}
		}
	}
	if s := v.Get("before"); s != "" {
		c, ok := parseRunCursor(s)
		if !ok {
			return q, errors.New("invalid before cursor")
		}
		q.Before = c
	}
	return q, nil
}

// The page cursor is "<createdAtMs>:<jobId>" — opaque to clients.
func runCursor(j store.AgentJob) string {
	return strconv.FormatInt(j.CreatedAtMs, 10) + ":" + j.JobID
}

func parseRunCursor(s string) (store.AgentRunCursor, bool) {
	at, id, ok := strings.Cut(s, ":")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || n <= 0 || id == "" {
		return store.AgentRunCursor{}, false
	}
	return store.AgentRunCursor{CreatedAtMs: n, JobID: id}, true
}

// handleRoomRuns lists a room's runs (room members).
func (s *Service) handleRoomRuns(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	q, err := parseRunQuery(r)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q.RoomID = roomID
	s.writeRuns(w, r, wire.RunScopeRoom, q)
}

// handleRuns lists the caller's runs (scope self) or the tenant's (scope
// tenant, behind the gateway's manage_org gate).
func (s *Service) handleRuns(scope string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := svcauth.Subject(r)
		if caller == "" {
			httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
			return
		}
		q, err := parseRunQuery(r)
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if scope == wire.RunScopeSelf {
			q.Participant = caller
		} else {
			q.Participant = strings.TrimSpace(r.URL.Query().Get("member"))
		}
		s.writeRuns(w, r, scope, q)
	}
}

func (s *Service) writeRuns(w http.ResponseWriter, r *http.Request, scope string, q store.AgentRunQuery) {
	if s.Runs == nil {
		httpx.Unavailable(w, "run records")
		return
	}
	jobs, err := s.Runs.ListAgentRuns(r.Context(), q)
	if err != nil {
		obs.ServerError(w, "list runs failed", err)
		httpx.Error(w, "list runs failed", http.StatusInternalServerError)
		return
	}
	v := s.runViewer(r, scope)
	out := wire.AgentRunsReply{Scope: scope, Runs: make([]wire.AgentRun, 0, len(jobs))}
	for _, j := range jobs {
		out.Runs = append(out.Runs, v.record(j, false))
	}
	if n := len(jobs); n > 0 && n == q.PageSize() {
		out.NextBefore = runCursor(jobs[n-1])
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleRoomRun returns one run of a room (room members) with the first
// reply's room sequence, and its raw diagnostics when the viewer may read
// them (runViewer.seesDiagnostics).
func (s *Service) handleRoomRun(w http.ResponseWriter, r *http.Request) {
	roomID, err := roomctx.Normalize(r.PathValue("roomId"))
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, roomID) {
		return
	}
	if s.Runs == nil {
		httpx.Unavailable(w, "run records")
		return
	}
	j, err := s.Runs.GetAgentRun(r.Context(), strings.TrimSpace(r.PathValue("jobId")))
	if errors.Is(err, store.ErrAgentJobNotFound) || (err == nil && j.RoomID != roomID) {
		httpx.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if err != nil {
		obs.ServerError(w, "read run failed", err)
		httpx.Error(w, "read run failed", http.StatusInternalServerError)
		return
	}
	run := s.runViewer(r, wire.RunScopeRoom).record(j, true)
	if run.Reply != nil && run.Reply.MessageID != "" {
		run.Reply.RoomSeq = s.replyRoomSeq(r.Context(), j, run.Reply.MessageID)
	}
	httpx.WriteJSON(w, http.StatusOK, run)
}

// replyRoomSeq finds the room sequence of the feed batch carrying the reply
// messageID, scanning a bounded window of the feed after the request. 0 when
// not found (no feed store, or the reply is beyond the window).
func (s *Service) replyRoomSeq(ctx context.Context, j store.AgentJob, messageID string) int64 {
	if s.Feed == nil {
		return 0
	}
	after := j.MessageSeq
	for page := 0; page < runReplyFeedPages; page++ {
		msgs, err := s.Feed.RoomFeed(ctx, j.RoomID, after, runReplyFeedPage)
		if err != nil || len(msgs) == 0 {
			return 0
		}
		for _, m := range msgs {
			if m.Kind == wire.KindAgentMessages && batchHasMessage(m.Payload, messageID) {
				return m.Seq
			}
			after = m.Seq
		}
		if len(msgs) < runReplyFeedPage {
			return 0
		}
	}
	return 0
}

// batchHasMessage reports whether a persisted agent_messages batch carries
// the reply with this messageId.
func batchHasMessage(payload json.RawMessage, messageID string) bool {
	p, ok := wire.DecodePayload[wire.AgentMessagesPayload](payload)
	if !ok {
		return false
	}
	for _, m := range p.Messages {
		if m.Payload.MessageID == messageID {
			return true
		}
	}
	return false
}

// runViewer renders runs for one request: room names resolved once per room,
// and (outside the room scope) the viewer's membership per room decides
// whether text is shown.
type runViewer struct {
	s      *Service
	r      *http.Request
	scope  string
	caller string
	// orgAdmin is the signed org role's manage_org grant.
	orgAdmin bool
	rooms    roomNamer
	members  map[string]bool
}

func (s *Service) runViewer(r *http.Request, scope string) *runViewer {
	return &runViewer{
		s:        s,
		r:        r,
		scope:    scope,
		caller:   svcauth.Subject(r),
		orgAdmin: rbac.Can(svcauth.Role(r), rbac.PermManageOrg),
		rooms:    roomNamer{s: s, r: r, names: map[string]string{}},
		members:  map[string]bool{},
	}
}

// canRead reports whether the viewer may see a run's text: always in the room
// scope (membership was checked), else only as a member of the run's room.
func (v *runViewer) canRead(roomID string) bool {
	if v.scope == wire.RunScopeRoom {
		return true
	}
	ok, seen := v.members[roomID]
	if !seen {
		_, err := membership.RoomScope(v.r.Context(), v.s.Membership, roomID, v.caller, membership.AnyMember)
		ok = err == nil
		v.members[roomID] = ok
	}
	return ok
}

// seesDiagnostics reports whether the viewer may read a run's raw
// diagnostics: the agent's owner and org admins only (O4).
func (v *runViewer) seesDiagnostics(j store.AgentJob) bool {
	return (v.caller != "" && v.caller == j.OwnerUserID) || v.orgAdmin
}

// record renders one job as a run record; detail adds the raw diagnostics
// for a viewer who may read them.
func (v *runViewer) record(j store.AgentJob, detail bool) wire.AgentRun {
	diag := map[string]any{}
	if len(j.DiagnosticsJSON) > 0 {
		_ = json.Unmarshal(j.DiagnosticsJSON, &diag)
	}
	run := wire.AgentRun{
		JobID:         j.JobID,
		RoomID:        j.RoomID,
		RoomName:      v.rooms.name(j.RoomID),
		ChannelID:     j.ChannelID,
		Requester:     j.RequesterUserID,
		Borrowed:      j.RequesterUserID != "" && j.RequesterUserID != j.OwnerUserID,
		Status:        string(j.Status),
		FailureReason: string(j.FailureReason),
		Attempt:       j.Attempt,
		Request: wire.AgentRunRequest{
			MessageSeq:  j.MessageSeq,
			ClientMsgID: j.ClientMsgID,
			Text:        wire.RunPreview(j.MessageText),
			Attachments: len(j.AttachmentIDs),
		},
		Agent: wire.AgentRunAgent{
			Owner:           j.OwnerUserID,
			Core:            j.Core,
			Provider:        j.Provider,
			TargetID:        j.TargetID,
			Model:           j.Model,
			ReasoningEffort: j.ReasoningEffort,
		},
		Phases: wire.AgentRunPhases{
			QueuedAtMs:   j.CreatedAtMs,
			ClaimedAtMs:  j.ClaimedAtMs,
			StartedAtMs:  j.StartedAtMs,
			FinishedAtMs: j.FinishedAtMs,
			UpdatedAtMs:  j.UpdatedAtMs,
		},
	}
	if j.DispatchedByJobID != "" {
		run.DispatchedBy = &wire.AgentRunDispatch{JobID: j.DispatchedByJobID, Depth: j.DispatchDepth}
	}
	if j.ExpertID != "" {
		run.Expert = &wire.AgentRunExpert{ID: j.ExpertID, Name: j.ExpertName, Version: j.ExpertVersion}
	}
	if j.RoomExpertID != "" {
		run.RoomExpert = &wire.AgentRunExpert{ID: j.RoomExpertID, Name: j.RoomExpertName, Version: j.RoomExpertVersion}
	}
	// A failure's diagnostics say why; a replied job may carry a warning
	// (the drive ended in error after a reply was already posted). A requeued
	// retry keeps the last attempt's error while it waits.
	run.Error, _ = diag["error"].(string)
	run.Warning, _ = diag["warning"].(string)
	if rep := diagObject[wire.AgentRunReply](diag, wire.RunDiagReply); rep != nil {
		rep.RoomSeq = 0 // resolved by the detail route only
		rep.Text = wire.RunPreview(rep.Text)
		run.Reply = rep
		run.Phases.FirstReplyAtMs = rep.AtMs
	}
	if u := diagObject[wire.AgentRunUsage](diag, wire.RunDiagUsage); u != nil && (u.Model != "" || u.InputTokens != nil || u.OutputTokens != nil || u.CostUsd != nil) {
		run.Usage = u
	}
	if !v.canRead(j.RoomID) {
		run.Redacted = true
		run.Request.Text = ""
		if run.Reply != nil {
			run.Reply.Text = ""
		}
		return run
	}
	if detail && len(diag) > 0 && v.seesDiagnostics(j) {
		run.Diagnostics = diag
	}
	return run
}

// diagObject decodes one object-valued diagnostics key into T (nil when
// absent or malformed).
func diagObject[T any](diag map[string]any, key string) *T {
	raw, ok := diag[key].(map[string]any)
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out T
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return &out
}
