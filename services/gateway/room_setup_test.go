package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
)

// The room tool room_setup and components save's room kind
// (docs/prd/2026-10-20-room-setup-by-conversation.md) against the real
// gateway, room service and realtime (galleryGW): every change through the
// agent door or a content route's grant, as the person who asked.

// setupJob puts a running job of requester's in room, leased by jack's
// bridge (jack's agent, asked by requester).
func (e *appGW) setupJob(jobID, room, requester string) {
	e.t.Helper()
	if err := e.jobs.PutBridgeHeartbeat(e.ctx(), store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.jobs.PutAgentJob(e.ctx(), store.AgentJob{JobID: jobID, RoomID: room, MessageSeq: time.Now().UnixNano(), RequesterUserID: requester, OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
}

// toolFor runs a room tool on jack's bridge in room, bound to job (asked by
// requester).
func (e *appGW) toolFor(room, job, requester, name string, args map[string]any) roomtools.Result {
	e.t.Helper()
	gw := bridgeGateway{e: e, bearer: e.session("jack")}
	srv, err := roomtools.Start(roomtools.Config{Gateway: gw})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = srv.Close() })
	_, sess, err := srv.Open(roomtools.Scope{RoomID: room, TenantID: e.tenant, Owner: "jack", Provider: "codex"})
	if err != nil {
		e.t.Fatal(err)
	}
	sess.Bind(roomtools.Job{JobID: job, RequesterUserID: requester})
	tool, ok := roomtools.DefaultRegistry().Lookup(name)
	if !ok {
		e.t.Fatalf("no room tool %s", name)
	}
	raw, _ := json.Marshal(args)
	return tool.Call(context.Background(), &roomtools.Env{Session: sess, Gateway: gw}, raw)
}

// roomLines are the room lines the tools posted (activity's notices).
func roomLines(rec *recorder, kind string) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, s := range rec.list {
		var batch wire.ReportMessagesInput
		if json.Unmarshal([]byte(s.body), &batch) != nil || len(batch.Messages) == 0 {
			continue
		}
		if p, _ := batch.Messages[0].Payload.(map[string]any); p["notice"] == kind {
			out = append(out, p["text"].(string))
		}
	}
	return out
}

// The room's owner's agent sets the room up as her: the description, the
// welcome and quick prompts every member sees, the room expert from her
// organization's Library, a channel, an AI task (and its removal) and an
// ask for a source. Each change lands through the routes the dialogs use,
// is audited as hers with the agent named, and shows in the room as a line.
func TestRoomSetupAsTheOwner(t *testing.T) {
	e := galleryGW(t)
	audit := store.NewMemoryExtensionAudit()
	e.gw.ExtAudit = audit
	activity := newRecorder(t)
	e.gw.Backends.Activity = activity.srv.URL
	e.content(store.ContentScopeOrg, contentpkg.KindExpert, "reviewer", map[string]any{})
	e.setupJob("job-maya", e.roomID, "maya")
	run := func(args map[string]any) map[string]any {
		t.Helper()
		r := e.toolFor(e.roomID, "job-maya", "maya", "room_setup", args)
		if r.Err != "" {
			t.Fatalf("%v: %s", args, r.Err)
		}
		return r.Value.(map[string]any)
	}
	run(map[string]any{"action": "set", "description": "Reviews the week.", "welcome": "Ask me about last week.",
		"quickPrompts": []map[string]string{{"title": "Recap", "prompt": "Recap the week"}}})
	run(map[string]any{"action": "use", "name": "reviewer"})
	run(map[string]any{"action": "add_channel", "name": "digest"})
	sched := run(map[string]any{"action": "add_schedule", "task": "review yesterday's conversations", "timezone": "UTC",
		"recurrence": map[string]any{"kind": "daily", "time": "09:00"}})
	run(map[string]any{"action": "remove_schedule", "id": sched["id"]})
	run(map[string]any{"action": "ask_source", "source": "runs", "purpose": "Judge whether conversations reached their goal."})

	room, err := e.b.Room.GetRoom(e.ctx(), e.roomID)
	if err != nil || room.Description != "Reviews the week." || room.Welcome != "Ask me about last week." ||
		!slices.Equal(room.QuickPrompts, []wire.RoomQuickPrompt{{Title: "Recap", Prompt: "Recap the week"}}) {
		t.Fatalf("the room = %+v, %v", room, err)
	}
	if expert, _ := e.gw.roomExpertOf(e.ctx(), e.roomID); expert == "" {
		t.Fatal("no room expert")
	}
	channels, _ := e.b.Channel.ListChannels(e.ctx(), e.roomID)
	if !slices.ContainsFunc(channels, func(c store.RoomChannel) bool { return c.Name == "digest" && c.CreatedBy == "maya" }) {
		t.Fatalf("channels = %+v", channels)
	}
	if scheds, _ := e.b.Schedules.ListSchedules(e.ctx(), e.roomID); len(scheds) != 0 {
		t.Fatalf("the removed schedule = %+v", scheds)
	}
	ask, err := e.b.RoomSources.GetRoomSourceGrant(e.ctx(), e.roomID, wire.RoomSourceRuns)
	if err != nil || ask.Scope != "" || ask.AskedBy != "maya" || ask.ComponentID != "" || !strings.Contains(ask.Purpose, "reached their goal") {
		t.Fatalf("the ask = %+v, %v", ask, err)
	}

	// get reads it all back as maya.
	got := run(map[string]any{"action": "get"})
	if !strings.Contains(got["welcome"].(string), "Ask me about last week.") || !strings.EqualFold(got["expert"].(string), "reviewer") || len(got["sources"].([]map[string]string)) != 1 {
		b, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("get = %s", b)
	}

	recs, _ := audit.AuditLog(context.Background(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindRoomSetup}, Limit: 50})
	actions := []string{}
	for _, r := range recs {
		if r.Actor != "maya" || r.Outcome != wire.AuditOutcomeAllow || !strings.Contains(r.Detail, "agent jack/codex") || strings.Contains(r.Detail, "last week") {
			t.Errorf("audit record %+v", r)
		}
		actions = append(actions, r.Action)
	}
	slices.Sort(actions)
	if want := []string{"add-channel", "add-schedule", "remove-schedule", "set-description", "set-welcome", "use", "use"}; !slices.Equal(actions, want) {
		t.Fatalf("audited = %v, want %v", actions, want)
	}
	if recs, _ := audit.AuditLog(context.Background(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindSource}}); len(recs) != 1 || recs[0].Action != "ask" {
		t.Fatalf("source audit = %+v", recs)
	}
	lines := roomLines(activity, wire.NoticeRoomSetup)
	if len(lines) != 7 || lines[0] != "maya's agent set the room's description" || !strings.EqualFold(lines[2], "maya's agent turned on the expert reviewer as the room's expert") {
		t.Fatalf("room lines = %q", lines)
	}
}

// A plain member's agent changes only what the member may: the room
// service and the content routes refuse the rest with their own words, the
// refusal is audited and nothing shows in the room. The door itself admits
// a description patch only.
func TestRoomSetupAsAPlainMember(t *testing.T) {
	e := galleryGW(t)
	audit := store.NewMemoryExtensionAudit()
	e.gw.ExtAudit = audit
	activity := newRecorder(t)
	e.gw.Backends.Activity = activity.srv.URL
	e.content(store.ContentScopeOrg, contentpkg.KindExpert, "reviewer", map[string]any{})
	e.setupJob("job-jack", e.roomID, "jack")
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "set", "welcome": "Hi"}, "only the room's owner or admins change its welcome"},
		{map[string]any{"action": "set", "description": "Mine now."}, "only the owner may edit the room"},
		{map[string]any{"action": "use", "name": "reviewer"}, "only the room's owner or admins change its experts"},
		{map[string]any{"action": "ask_source", "source": "runs", "purpose": "Read everything."}, "only the room's owner or admins ask"},
	} {
		if r := e.toolFor(e.roomID, "job-jack", "jack", "room_setup", c.args); !strings.Contains(r.Err, c.want) {
			t.Errorf("%v = %q, want %q", c.args, r.Err, c.want)
		}
	}
	// What a member may do, their agent may do for them.
	if r := e.toolFor(e.roomID, "job-jack", "jack", "room_setup", map[string]any{"action": "add_channel", "name": "jacks"}); r.Err != "" {
		t.Fatalf("a member's channel = %s", r.Err)
	}
	room, _ := e.b.Room.GetRoom(e.ctx(), e.roomID)
	if room.Welcome != "" || room.Description != "" {
		t.Fatalf("the room changed: %+v", room)
	}
	if _, err := e.b.RoomSources.GetRoomSourceGrant(e.ctx(), e.roomID, wire.RoomSourceRuns); err == nil {
		t.Fatal("a member's agent asked for a source")
	}
	recs, _ := audit.AuditLog(context.Background(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindRoomSetup}, Limit: 50})
	denied := 0
	for _, r := range recs {
		if r.Outcome == wire.AuditOutcomeDeny {
			denied++
		}
	}
	if denied != 3 || len(recs) != 4 {
		t.Fatalf("audit = %+v", recs)
	}
	if lines := roomLines(activity, wire.NoticeRoomSetup); len(lines) != 1 || lines[0] != "jack's agent added the channel #jacks" {
		t.Fatalf("room lines = %q", lines)
	}
	// The door admits the description only, whoever asks.
	e.setupJob("job-maya", e.roomID, "maya")
	jack := "Authorization: Bearer " + e.session("jack")
	if r := e.call(http.MethodPatch, "/v1/rooms/"+e.roomID+"?job=job-maya", map[string]any{"description": "x", "visibility": "shared"}, jack); r.status != http.StatusForbidden ||
		!strings.Contains(string(r.body), "description only") {
		t.Fatalf("a patch beyond the description = %d %s", r.status, r.body)
	}
}

// An agent schedules a request to one of the room's agents online now for
// the person who asked it (the agents the Schedules dialog offers them):
// the request is theirs, enabled, to that agent. The door refuses an agent
// that is not in the room, and a request that names no agent.
func TestRoomSetupAgentSchedule(t *testing.T) {
	e := galleryGW(t)
	e.rt.Roster.Advertise(e.roomID, "jack", []realtime.AgentRef{{Core: "acme", Provider: "claude"}})
	e.setupJob("job-maya", e.roomID, "maya")
	args := map[string]any{"action": "add_schedule", "task": "What changed since yesterday?", "agent": "jack/claude", "timezone": "UTC",
		"recurrence": map[string]any{"kind": "daily", "time": "09:00"}}
	if r := e.toolFor(e.roomID, "job-maya", "maya", "room_setup", args); r.Err != "" {
		t.Fatalf("a request to jack's agent = %s", r.Err)
	}
	scheds, _ := e.b.Schedules.ListSchedules(e.ctx(), e.roomID)
	if len(scheds) != 1 || scheds[0].CreatedBy != "maya" || !scheds[0].Enabled || scheds[0].Text != "What changed since yesterday?" ||
		scheds[0].Recipient.Kind != wire.ScheduleRecipientAgent || scheds[0].Recipient.OwnerID != "jack" || scheds[0].Recipient.Provider != "claude" || scheds[0].Recipient.Core != "acme" {
		t.Fatalf("schedules = %+v", scheds)
	}
	// The door itself: an agent not in the room, and a request naming none.
	jack := "Authorization: Bearer " + e.session("jack")
	for _, c := range []struct {
		rcpt   wire.ScheduleRecipient
		status int
		want   string
	}{
		{wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "olaf", Provider: "codex"}, http.StatusForbidden, "not one of the room's agents"},
		{wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "jack", Provider: "claude", Core: "other"}, http.StatusForbidden, "not one of the room's agents"},
		{wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent}, http.StatusBadRequest, "names the agent"},
		{wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom}, http.StatusForbidden, "posts as the person is theirs"},
	} {
		body := map[string]any{"text": "x", "recipient": c.rcpt, "timezone": "UTC", "recurrence": map[string]any{"kind": "daily", "time": "09:00"}}
		if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/schedules?job=job-maya", body, jack); r.status != c.status || !strings.Contains(string(r.body), c.want) {
			t.Errorf("%+v = %d %s, want %d %q", c.rcpt, r.status, r.body, c.status, c.want)
		}
	}
	if scheds, _ := e.b.Schedules.ListSchedules(e.ctx(), e.roomID); len(scheds) != 1 {
		t.Fatalf("a refused request was stored: %+v", scheds)
	}
}

// components save's room kind saves this room into the requester's Library
// through the dialog's capture; a plain member's agent is refused by it.
// components publish offers it to her as a card; her click shares it.
func TestSaveRoomComponentThroughRoomTools(t *testing.T) {
	e := galleryGW(t)
	activity := newRecorder(t)
	e.gw.Backends.Activity = activity.srv.URL
	e.reviewRoom()
	text := "Turn the daily summary on."
	if r := e.call(http.MethodPatch, "/v1/rooms/"+e.roomID+"/welcome", wire.RoomWelcomeRequest{Welcome: &text}, e.as("maya")); r.status != http.StatusOK {
		t.Fatalf("welcome = %d %s", r.status, r.body)
	}
	e.setupJob("job-maya", e.roomID, "maya")
	e.setupJob("job-jack", e.roomID, "jack")
	if r := e.toolFor(e.roomID, "job-jack", "jack", "components", map[string]any{"action": "save", "kind": "room"}); !strings.Contains(r.Err, "only the room's owner or admins save it") {
		t.Fatalf("a member's agent = %q", r.Err)
	}
	r := e.toolFor(e.roomID, "job-maya", "maya", "components", map[string]any{"action": "save", "kind": "room", "name": "weekly-review", "displayName": "Weekly review",
		"description": "Reviews the week.", "category": "review", "tags": []string{"review"}})
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	out := r.Value.(map[string]any)
	if out["kind"] != "room" || out["name"] != "weekly-review" || out["version"] != "1.0.0" {
		t.Fatalf("save = %v", out)
	}
	c, err := e.b.ContentComponents.GetContentComponent(e.ctx(), out["id"].(string))
	if err != nil || c.Owner != "maya" || c.Scope != store.ContentScopePersonal {
		t.Fatalf("saved = %+v, %v", c, err)
	}
	v, _ := e.b.ContentComponents.GetContentComponentVersion(e.ctx(), c.ID, "1.0.0")
	var m contentpkg.Manifest
	if err := json.Unmarshal(v.ManifestJSON, &m); err != nil || m.Welcome != text || m.Requires.Expert != "reviewer@^1" || len(m.Schedules) != 3 || !slices.Equal(m.Channels, []string{"reports"}) {
		t.Fatalf("the package = %+v, %v", m, err)
	}

	if r := e.toolFor(e.roomID, "job-maya", "maya", "components", map[string]any{"action": "publish", "id": c.ID, "scope": "org"}); r.Err != "" {
		t.Fatal(r.Err)
	}
	if cards := roomLines(activity, wire.NoticePublishProposal); len(cards) != 1 || !strings.Contains(cards[0], "Weekly review v1.0.0") {
		t.Fatalf("cards = %q", cards)
	}
	if reqs, _ := e.b.ContentComponents.ListContentRequests(e.ctx(), store.ContentRequestQuery{ComponentID: c.ID}); len(reqs) != 0 {
		t.Fatalf("shared before her click: %+v", reqs)
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+c.ID+"/publish", wire.ContentPublishInput{Scope: "org"}, e.as("maya")); r.status != http.StatusCreated {
		t.Fatalf("her click = %d %s", r.status, r.body)
	}
}
