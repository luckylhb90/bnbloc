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
// service already serve to people: the room tool room_setup
// (docs/prd/2026-10-20-room-setup-by-conversation.md §2.2). The proxy acts as
// the job's requester, so every rule those routes apply to a person applies to
// the agent's act: the requester's role in the room decides (the room service
// refuses a member who may not change the room), a schedule is the
// requester's, counts against their limits, runs with their rights and spends
// their assist budget.
//
//	GET    /v1/rooms/{id}                    the room: its description, welcome and quick prompts
//	PATCH  /v1/rooms/{id}                    its description only (the owner's)
//	PATCH  /v1/rooms/{id}/welcome            its welcome and quick prompts (the owner's or an admin's)
//	GET    /v1/rooms/{id}/channels           its channels
//	POST   /v1/rooms/{id}/channels           a new channel
//	GET    /v1/rooms/{id}/schedules          the room's schedules
//	POST   /v1/rooms/{id}/schedules          a new schedule, an AI task or a notice only
//	DELETE /v1/rooms/{id}/schedules/{sid}    one of the requester's own
//	GET    /v1/rooms/{id}/groups             the connected group chats' names
//
// The rest of a room's setup is served by the gateway itself, whose routes
// resolve the same grant (contentGrant, ?job=&room=): the room's expert and
// packs (/v1/content-uses, audited below as room setup when granted), its
// sources and an ask for one (room_sources.go) and its capture as a room
// package (component-draft, room_components.go).
//
// Nothing else is opened: the room's name, visibility and sync, its members,
// a schedule that posts as the person or drives an agent, editing or running a
// schedule stay the person's. A notice goes where the person could send one
// themselves (the room, their organization's logins, a group chat their room
// connected), through the one notify path. Every change spends the requester's
// cost budget and is audited when the backend has answered (allowed or
// refused, never the text).

// maxAgentDoorBody bounds the body the door reads to check a change.
const maxAgentDoorBody = 64 << 10

// isAgentDoor reports whether the request is one of the door's routes.
func (p apiPath) isAgentDoor(method string) bool {
	get, n := method == http.MethodGet, len(p.segs)
	switch p.RoomSub {
	case "":
		return p.RoomID != "" && n == 3 && (get || method == http.MethodPatch)
	case "welcome":
		return n == 4 && method == http.MethodPatch
	case "channels":
		return n == 4 && (get || method == http.MethodPost)
	case "schedules":
		return (n == 4 && (get || method == http.MethodPost)) || (n == 5 && method == http.MethodDelete)
	case "groups":
		return n == 4 && get
	}
	return false
}

// agentChange is what a door change is, as its audit names it.
func (p apiPath) agentChange(method string) (action, what string) {
	switch {
	case p.RoomSub == "":
		return "set-description", "the description"
	case p.RoomSub == "welcome":
		return "set-welcome", "the welcome and quick prompts"
	case p.RoomSub == "channels":
		return "add-channel", "a channel"
	case method == http.MethodDelete:
		return "remove-schedule", "a schedule"
	default:
		return "add-schedule", "a schedule"
	}
}

// agentDoor resolves who an agent-door request acts as: the caller (no ?job=),
// or the job's requester. It answers the refusals itself. done, when not nil,
// is called with the backend's status once the change was proxied (its audit).
func (s *Service) agentDoor(w http.ResponseWriter, r *http.Request, p apiPath, caller auth.Claims) (claims auth.Claims, done func(status int), ok bool) {
	job := strings.TrimSpace(r.URL.Query().Get("job"))
	if job == "" || !p.isAgentDoor(r.Method) {
		return caller, nil, true
	}
	actor, err := s.jobGrant(r.Context(), caller, caller, p.RoomID, job)
	if err != nil {
		writeGrantErr(w, err)
		return caller, nil, false
	}
	q := r.URL.Query()
	q.Del("job")
	r.URL.RawQuery = q.Encode()
	if r.Method == http.MethodGet {
		return actor.Claims, nil, true
	}
	if !s.agentChangeAllowed(w, r, p) || !s.spend(w, actor.Claims, costBudget) {
		return caller, nil, false
	}
	action, what := p.agentChange(r.Method)
	return actor.Claims, func(status int) { s.auditAgentSetup(r, actor, p.RoomID, action, what, okStatus(status)) }, true
}

// agentChangeAllowed applies the door's own limits to a change: a schedule
// is an AI task or a notice, and a room patch changes the description only.
// The body is read to check and put back for the proxy.
func (s *Service) agentChangeAllowed(w http.ResponseWriter, r *http.Request, p apiPath) bool {
	if (p.RoomSub != "schedules" && p.RoomSub != "") || r.Method == http.MethodDelete {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAgentDoorBody+1))
	if err != nil || len(raw) > maxAgentDoorBody {
		httpx.Error(w, "the change is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	if p.RoomSub == "" {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || fields["description"] == nil {
			httpx.Error(w, "an agent changes a room's description only: its name, visibility and sync are the person's to change", http.StatusForbidden)
			return false
		}
		return true
	}
	var in struct {
		Recipient wire.ScheduleRecipient `json:"recipient"`
	}
	if json.Unmarshal(raw, &in) != nil || !(in.Recipient.Kind == wire.ScheduleRecipientAI || in.Recipient.Kind == wire.ScheduleRecipientNotice) {
		httpx.Error(w, `an agent schedules AI tasks and notices only (recipient kind "ai" or "notice"): a schedule that posts as the person or drives an agent is theirs to create`, http.StatusForbidden)
		return false
	}
	return true
}

// auditAgentSetup records a change of roomID's setup an agent made (or was
// refused) for the job's requester: what, where and by whom, never the
// text. The one audit of room_setup's acts (wire.AuditKindRoomSetup).
func (s *Service) auditAgentSetup(r *http.Request, actor contentActor, roomID, action, what string, allowed bool) {
	j := actor.Job
	outcome, verb := wire.AuditOutcomeAllow, "changed"
	if !allowed {
		outcome, verb = wire.AuditOutcomeDeny, "refused"
	}
	s.audit(r.Context(), j.TenantID, store.ExtAuditRecord{
		Kind: wire.AuditKindRoomSetup, Name: roomID, Action: action, Actor: j.RequesterUserID, Outcome: outcome,
		Detail: fmt.Sprintf("%s %s by agent %s for %s (job %s, bridge of %s)", what, verb, jobAgent(*j), j.RequesterUserID, j.JobID, actor.Bridge),
	})
}

// grantedSetup wraps w so that, once a gateway route a job's grant reached
// (contentGrant) has answered, its change of the room *room names is
// audited as the agent's (auditAgentSetup), *what saying what. A person's
// own request, or one that names no room (*room == ""), is not.
func (s *Service) grantedSetup(w http.ResponseWriter, r *http.Request, actor contentActor, action string, room, what *string) (http.ResponseWriter, func()) {
	if actor.Job == nil {
		return w, func() {}
	}
	sw := &statusWriter{ResponseWriter: w}
	return sw, func() {
		if *room != "" {
			s.auditAgentSetup(r, actor, *room, action, *what, okStatus(sw.status))
		}
	}
}

// okStatus reports a route's answer as a change made: a 2xx (nothing
// written, a proxy that failed before answering, is none).
func okStatus(status int) bool { return status >= 200 && status < 300 }

// statusWriter remembers the status a door change answered.
type statusWriter struct {
	http.ResponseWriter
	status int
}

// Unwrap lets http.ResponseController reach the writer underneath (the
// proxy flushes through it).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}
