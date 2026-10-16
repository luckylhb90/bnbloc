package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The agent door: what an agent does for the person who asked it, through its
// job's grant (?job=, jobGrant: the job is live on the caller's bridge and its
// requester may still post in the room), on routes realtime and the room
// service already serve to people. The proxy acts as the job's requester, so
// every rule those routes apply to a person applies to the agent's act: the
// schedule is the requester's, counts against their limits, runs with their
// rights and spends their assist budget.
//
//	GET    /v1/rooms/{id}/schedules          the room's schedules
//	POST   /v1/rooms/{id}/schedules          a new schedule, an AI task or a notice only
//	DELETE /v1/rooms/{id}/schedules/{sid}    one of the requester's own
//	GET    /v1/rooms/{id}/groups             the connected group chats' names
//
// Nothing else is opened: a schedule that posts as the person or drives an
// agent is theirs to create, and editing or running one is theirs too. A notice
// goes where the person could send one themselves (the room, their organization's
// logins, a group chat their room connected), through the one notify path.

// maxAgentScheduleBody bounds the body the door reads to check a schedule.
const maxAgentScheduleBody = 64 << 10

// isAgentDoor reports whether the request is one of the door's routes.
func (p apiPath) isAgentDoor(method string) bool {
	switch p.RoomSub {
	case "schedules":
		return (len(p.segs) == 4 && (method == http.MethodGet || method == http.MethodPost)) || (len(p.segs) == 5 && method == http.MethodDelete)
	case "groups":
		return len(p.segs) == 4 && method == http.MethodGet
	}
	return false
}

// agentDoor resolves who an agent-door request acts as: the caller (no ?job=),
// or the job's requester. It answers the refusals itself.
func (s *Service) agentDoor(w http.ResponseWriter, r *http.Request, p apiPath, caller auth.Claims) (auth.Claims, bool) {
	job := strings.TrimSpace(r.URL.Query().Get("job"))
	if job == "" || !p.isAgentDoor(r.Method) {
		return caller, true
	}
	actor, err := s.jobGrant(r.Context(), caller, caller, p.RoomID, job)
	if err != nil {
		writeGrantErr(w, err)
		return caller, false
	}
	q := r.URL.Query()
	q.Del("job")
	r.URL.RawQuery = q.Encode()
	if p.RoomSub == "schedules" && r.Method == http.MethodPost {
		if !s.agentScheduleAllowed(w, r, actor) {
			return caller, false
		}
	}
	if p.RoomSub == "schedules" && r.Method != http.MethodGet {
		s.auditAgentSchedule(r, p, actor)
	}
	return actor.Claims, true
}

// agentScheduleAllowed lets an agent create an AI task or a notice only, and spends the
// requester's cost budget (a loop is stopped, as a person's would be). The body
// is read to check and put back for the proxy.
func (s *Service) agentScheduleAllowed(w http.ResponseWriter, r *http.Request, actor contentActor) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAgentScheduleBody+1))
	if err != nil || len(raw) > maxAgentScheduleBody {
		httpx.Error(w, "the schedule is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	var in struct {
		Recipient wire.ScheduleRecipient `json:"recipient"`
	}
	if json.Unmarshal(raw, &in) != nil || !(in.Recipient.Kind == wire.ScheduleRecipientAI || in.Recipient.Kind == wire.ScheduleRecipientNotice) {
		httpx.Error(w, `an agent schedules AI tasks and notices only (recipient kind "ai" or "notice"): a schedule that posts as the person or drives an agent is theirs to create`, http.StatusForbidden)
		return false
	}
	return s.spend(w, actor.Claims, costBudget)
}

// auditAgentSchedule records an agent's schedule create or delete for the
// requester: which, where and by whom, never the task's text.
func (s *Service) auditAgentSchedule(r *http.Request, p apiPath, actor contentActor) {
	action := "create"
	if r.Method == http.MethodDelete {
		action = "delete"
	}
	j := actor.Job
	s.audit(r.Context(), j.TenantID, store.ExtAuditRecord{
		Kind: wire.AuditKindSchedule, Name: p.RoomID, Action: action, Actor: j.RequesterUserID, Outcome: wire.AuditOutcomeAllow,
		Detail: fmt.Sprintf("schedule %sd by agent %s for %s (job %s, bridge of %s)", action, jobAgent(*j), j.RequesterUserID, j.JobID, actor.Bridge),
	})
}
