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
	"github.com/luckylhb90/bnbloc/internal/obs"
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
//	GET    /v1/rooms/{id}                    the room: its description, welcome, quick prompts and rules
//	PATCH  /v1/rooms/{id}                    its description (the owner's), or its rules and recommended
//	                                         channels (the owner's or an admin's): one of the two kinds
//	PATCH  /v1/rooms/{id}/welcome            its welcome and quick prompts (the owner's or an admin's)
//	GET    /v1/rooms/{id}/channels           its channels and sections (?archived=1 the archived)
//	POST   /v1/rooms/{id}/channels           a new channel
//	PATCH  /v1/rooms/{id}/channels/{cid}     a channel's setup: name, purpose, rules, section, position,
//	                                         settings (never its owner: who answers for it is the person's)
//	POST   /v1/rooms/{id}/channels/{cid}/archive|restore
//	                                         archive or restore a channel
//	GET    /v1/rooms/{id}/schedules          the room's schedules
//	POST   /v1/rooms/{id}/schedules          a new schedule: an AI task, a notice or a request to one of the room's agents
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
// a schedule that posts as the person, editing or running a schedule stay the
// person's; so do deleting a channel, a channel's owner, the room's agreement
// switch and channel creation policy, sections and who follows what
// (docs/prd/2026-10-28-channels-and-threads.md §2.6: deleting is a person's
// click, and a section's membership is consent about who reads what). A request to an agent asks one of the room's agents online now,
// the ones the room's Schedules dialog offers the person (agentScheduleTarget). A notice goes where the person could send one
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
		switch n {
		case 4:
			return get || method == http.MethodPost
		case 5:
			return method == http.MethodPatch
		case 6:
			return method == http.MethodPost && (p.segs[5] == "archive" || p.segs[5] == "restore")
		}
		return false
	case "schedules":
		return (n == 4 && (get || method == http.MethodPost)) || (n == 5 && method == http.MethodDelete)
	case "groups":
		return n == 4 && get
	}
	return false
}

// agentChange is what a door change is, as its audit names it. A room patch
// is the description or the room's rules (agentChangeAllowed admits one
// kind per request), told apart by its body.
func (p apiPath) agentChange(method string, roomRules bool) (action, what string) {
	switch {
	case p.RoomSub == "" && roomRules:
		return "set-rules", "the rules and recommended channels"
	case p.RoomSub == "":
		return "set-description", "the description"
	case p.RoomSub == "welcome":
		return "set-welcome", "the welcome and quick prompts"
	case p.RoomSub == "channels" && len(p.segs) == 6:
		return p.segs[5] + "-channel", "a channel's archive"
	case p.RoomSub == "channels" && method == http.MethodPatch:
		return "set-channel", "a channel's setup"
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
	roomRules, allowed := s.agentChangeAllowed(w, r, p, actor.Claims)
	if !allowed || !s.spend(w, actor.Claims, costBudget) {
		return caller, nil, false
	}
	action, what := p.agentChange(r.Method, roomRules)
	return actor.Claims, func(status int) { s.auditAgentSetup(r, actor, p.RoomID, action, what, okStatus(status)) }, true
}

// agentChangeAllowed applies the door's own limits to a change: a schedule
// is an AI task, a notice or a request to an agent of the room the
// requester (claims) may ask (agentScheduleTarget); a room patch changes
// the description alone, or the room's rules and recommended channels
// (roomRules); a channel patch never names the channel's owner. The body is
// read to check and put back for the proxy.
func (s *Service) agentChangeAllowed(w http.ResponseWriter, r *http.Request, p apiPath, claims auth.Claims) (roomRules, ok bool) {
	channelPatch := p.RoomSub == "channels" && r.Method == http.MethodPatch
	if (p.RoomSub != "schedules" && p.RoomSub != "" && !channelPatch) || r.Method == http.MethodDelete {
		return false, true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAgentDoorBody+1))
	if err != nil || len(raw) > maxAgentDoorBody {
		httpx.Error(w, "the change is too large", http.StatusRequestEntityTooLarge)
		return false, false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	if p.RoomSub == "" {
		return roomPatchAllowed(w, raw)
	}
	if channelPatch {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			httpx.Error(w, "invalid json body", http.StatusBadRequest)
			return false, false
		}
		if _, ok := fields["ownerId"]; ok {
			httpx.Error(w, "an agent does not change who owns a channel: that is the person's to do in the room", http.StatusForbidden)
			return false, false
		}
		return false, true
	}
	var in struct {
		Recipient wire.ScheduleRecipient `json:"recipient"`
	}
	if json.Unmarshal(raw, &in) != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return false, false
	}
	switch in.Recipient.Kind {
	case wire.ScheduleRecipientAI, wire.ScheduleRecipientNotice:
		return false, true
	case wire.ScheduleRecipientAgent:
		return false, s.agentScheduleTarget(w, r, claims, p.RoomID, in.Recipient)
	}
	httpx.Error(w, `an agent schedules AI tasks, notices and requests to an agent only (recipient kind "ai", "notice" or "agent"): a schedule that posts as the person is theirs to create`, http.StatusForbidden)
	return false, false
}

// roomPatchAllowed admits a room patch an agent makes: the description
// alone (the owner's), or the room's rules and recommended channels (the
// owner's or an admin's), never both in one request, so the audit names one
// change. The agreement switch and the channel creation policy stay the
// person's (decision: pending owner; §2.6 names the rules and the
// recommended channels only, and the fail-closed reading keeps the rest).
func roomPatchAllowed(w http.ResponseWriter, raw []byte) (roomRules, ok bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return false, false
	}
	if len(fields) == 1 && fields["description"] != nil {
		return false, true
	}
	for name := range fields {
		if name != "rules" && name != "recommendedChannels" {
			httpx.Error(w, "an agent changes a room's description, or its rules and recommended channels: its name, visibility, sync, agreement switch and channel creation are the person's to change", http.StatusForbidden)
			return false, false
		}
	}
	return true, true
}

// agentScheduleTarget admits a request to an agent an agent schedules for
// the requester (claims) when it names one of the room's agents online now
// (realtime's roster, roomRoster): the agents the room's Schedules dialog
// offers them. Another member's agent is a borrow, refused at drive time
// unless its owner authorized the requester, as for the dialog's.
func (s *Service) agentScheduleTarget(w http.ResponseWriter, r *http.Request, claims auth.Claims, roomID string, rcpt wire.ScheduleRecipient) bool {
	owner, provider, core := strings.TrimSpace(rcpt.OwnerID), strings.TrimSpace(rcpt.Provider), strings.TrimSpace(rcpt.Core)
	if owner == "" || provider == "" {
		httpx.Error(w, "a request to an agent names the agent (ownerId and provider): one of the room's agents", http.StatusBadRequest)
		return false
	}
	roster, err := s.roomRoster(r.Context(), claims, roomID)
	if err != nil {
		obs.ServerError(w, "read the room's agents failed", err)
		httpx.Error(w, "read the room's agents failed", http.StatusBadGateway)
		return false
	}
	for _, a := range roster {
		if a.OwnerUserID == owner && a.Provider == provider && (core == "" || a.Core == core) {
			return true
		}
	}
	httpx.Error(w, fmt.Sprintf("%s/%s is not one of the room's agents online now: a schedule asks one of those", owner, provider), http.StatusForbidden)
	return false
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
