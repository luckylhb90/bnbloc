package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// schedMembers is a mutable membership set (a member can be removed).
type schedMembers struct {
	mu sync.Mutex
	m  map[string]bool
}

func (s *schedMembers) set(room, user string, member bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[room+"\x00"+user] = member
}

func (s *schedMembers) Lookup(room, user string) (membership.Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return membership.Member{Role: store.RoleMember}, s.m[room+"\x00"+user]
}

func (s *schedMembers) Fresh(room, user string) (membership.Member, bool) {
	return s.Lookup(room, user)
}

func (s *schedMembers) Forget(string, string) {}

// schedRoles answers room roles; a room absent from rooms reads as deleted.
type schedRoles struct {
	roles map[string]store.Role
	rooms map[string]bool
}

func (r schedRoles) GetRoom(_ context.Context, roomID string) (store.Room, error) {
	if !r.rooms[roomID] {
		return store.Room{}, store.ErrRoomNotFound
	}
	return store.Room{RoomID: roomID}, nil
}

func (r schedRoles) MemberRole(_ context.Context, roomID, userID string) (store.Role, bool, error) {
	role, ok := r.roles[roomID+"\x00"+userID]
	return role, ok, nil
}

func (r schedRoles) Members(_ context.Context, roomID string) ([]store.Membership, error) {
	var out []store.Membership
	for k, role := range r.roles {
		if room, user, _ := strings.Cut(k, "\x00"); room == roomID {
			out = append(out, store.Membership{RoomID: room, UserID: user, Role: role})
		}
	}
	return out, nil
}

type schedFixture struct {
	t       *testing.T
	srv     *httptest.Server
	svc     *Service
	members *schedMembers
	chat    *store.MemoryChat
	jobs    *store.MemoryBridgeJobs
	sched   *store.MemoryRoomSchedules
}

// newSchedFixture: room-1 has owner olga, members alice + bob.
func newSchedFixture(t *testing.T) *schedFixture {
	t.Helper()
	members := &schedMembers{m: map[string]bool{}}
	for _, u := range []string{"olga", "alice", "bob"} {
		members.set("room-1", u, true)
	}
	roles := schedRoles{
		roles: map[string]store.Role{"room-1\x00olga": store.RoleOwner, "room-1\x00alice": store.RoleMember, "room-1\x00bob": store.RoleMember},
		rooms: map[string]bool{"room-1": true},
	}
	f := &schedFixture{t: t, members: members, chat: store.NewMemoryChat(), jobs: store.NewMemoryBridgeJobs(), sched: store.NewMemoryRoomSchedules()}
	f.svc = &Service{
		Hub: realtime.NewHub(64, 16), Membership: members, Chat: f.chat, BridgeJobs: f.jobs,
		Schedules: f.sched, RoomRoles: roles, Dispatches: f.jobs, HookBox: testCredentialBox(t),
	}
	f.srv = httptest.NewServer(f.svc.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *schedFixture) do(user, method, path string, body any) (int, []byte) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (f *schedFixture) create(user string, body map[string]any) wire.RoomSchedule {
	f.t.Helper()
	code, out := f.do(user, http.MethodPost, "/v1/rooms/room-1/schedules", body)
	if code != http.StatusCreated {
		f.t.Fatalf("create = %d %s", code, out)
	}
	var sc wire.RoomSchedule
	_ = json.Unmarshal(out, &sc)
	return sc
}

func (f *schedFixture) get(id string) wire.RoomSchedule {
	f.t.Helper()
	sc, err := f.sched.GetSchedule(tenant.NewContext(context.Background(), tenant.Default), "room-1", id)
	if err != nil {
		f.t.Fatal(err)
	}
	return sc
}

func (f *schedFixture) history() []store.RoomMessage {
	f.t.Helper()
	msgs, err := f.chat.RoomFeed(tenant.NewContext(context.Background(), tenant.Default), "room-1", 0, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return msgs
}

func dailyBody(text string, recipient map[string]any) map[string]any {
	return map[string]any{
		"text": text, "recipient": recipient, "timezone": "UTC",
		"recurrence": map[string]any{"kind": "daily", "time": "09:00"},
	}
}

func TestScheduleCreateValidation(t *testing.T) {
	f := newSchedFixture(t)
	room := map[string]any{"kind": "room"}
	bad := []map[string]any{
		{"text": "", "recipient": room, "timezone": "UTC", "recurrence": map[string]any{"kind": "daily", "time": "09:00"}},
		{"text": "x", "recipient": room, "timezone": "Mars/Base", "recurrence": map[string]any{"kind": "daily", "time": "09:00"}},
		{"text": "x", "recipient": room, "timezone": "UTC", "recurrence": map[string]any{"kind": "cron", "cron": "*/2 * * * *"}},
		{"text": "x", "recipient": room, "timezone": "UTC", "recurrence": map[string]any{"kind": "once", "atMs": time.Now().Add(-time.Hour).UnixMilli()}},
		{"text": "x", "recipient": map[string]any{"kind": "agent", "provider": "codex"}, "timezone": "UTC", "recurrence": map[string]any{"kind": "daily", "time": "09:00"}},
	}
	for i, b := range bad {
		if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", b); code != http.StatusBadRequest {
			t.Errorf("case %d: status %d (%s), want 400", i, code, out)
		}
	}
	if code, _ := f.do("mallory", http.MethodPost, "/v1/rooms/room-1/schedules", dailyBody("x", room)); code != http.StatusForbidden {
		t.Fatalf("non-member create = %d, want 403", code)
	}
	sc := f.create("alice", dailyBody("standup", room))
	if !sc.Enabled || sc.NextRunAtMs <= time.Now().UnixMilli() || sc.CreatedBy != "alice" || sc.Recipient.Kind != "room" {
		t.Fatalf("created = %+v", sc)
	}
	if got := time.UnixMilli(sc.NextRunAtMs).UTC(); got.Hour() != 9 || got.Minute() != 0 {
		t.Fatalf("next run %s is not 09:00 UTC", got)
	}
	// A disabled schedule plans no run.
	off := dailyBody("later", room)
	off["enabled"] = false
	if sc := f.create("alice", off); sc.Enabled || sc.NextRunAtMs != 0 {
		t.Fatalf("disabled create = %+v", sc)
	}
	// Bob lists alice's schedules (room-visible).
	code, out := f.do("bob", http.MethodGet, "/v1/rooms/room-1/schedules", nil)
	if code != http.StatusOK || !strings.Contains(string(out), "standup") {
		t.Fatalf("list = %d %s", code, out)
	}
}

func TestScheduleRunPostsAsCreatorThroughChatPath(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", dailyBody("summarize yesterday", map[string]any{"kind": "agent", "ownerId": "bob", "provider": "codex", "core": "c1"}))
	slot := time.UnixMilli(sc.NextRunAtMs)
	if n := f.svc.RunDueSchedules(context.Background(), slot.Add(-time.Minute)); n != 0 {
		t.Fatalf("ran %d schedules before they were due", n)
	}
	if n := f.svc.RunDueSchedules(context.Background(), slot.Add(time.Second)); n != 1 {
		t.Fatalf("ran %d schedules, want 1", n)
	}
	msgs := f.history()
	if len(msgs) != 1 {
		t.Fatalf("history has %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	var p map[string]any
	_ = json.Unmarshal(m.Payload, &p)
	if m.Author != "alice" || p["userId"] != "alice" || p["text"] != "summarize yesterday" {
		t.Fatalf("posted message = %+v %v", m, p)
	}
	if m.ClientMsgID != schedule.RunSlotClientMsgID(sc.ScheduleID, slot) {
		t.Fatalf("client msg id = %q", m.ClientMsgID)
	}
	if marker, _ := p["scheduled"].(map[string]any); marker["scheduleId"] != sc.ScheduleID {
		t.Fatalf("scheduled marker = %v", p["scheduled"])
	}
	if rcpt, _ := p["recipient"].(map[string]any); rcpt["ownerId"] != "bob" || rcpt["provider"] != "codex" || rcpt["core"] != "c1" {
		t.Fatalf("recipient = %v", p["recipient"])
	}
	// The agent job the bridge claims by recipient exists, requested by the
	// creator (the bridge enforces bob's borrow authorization at drive time).
	jobs, err := f.jobs.ListAgentJobs(tenant.NewContext(context.Background(), tenant.Default), "room-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].RequesterUserID != "alice" || jobs[0].OwnerUserID != "bob" || jobs[0].TargetID != "c1/codex" {
		t.Fatalf("agent jobs = %+v", jobs)
	}
	got := f.get(sc.ScheduleID)
	if got.LastStatus != wire.ScheduleRunOK || got.LastSeq != m.Seq || got.RunCount != 1 ||
		got.NextRunAtMs != slot.Add(24*time.Hour).UnixMilli() || !got.Enabled {
		t.Fatalf("after run = %+v", got)
	}
}

func TestScheduleReclaimedSlotDoesNotPostTwice(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", dailyBody("once a day", map[string]any{"kind": "room"}))
	slot := time.UnixMilli(sc.NextRunAtMs)
	// A first scheduler claims and posts but crashes before recording.
	_, due, err := f.sched.ClaimDueSchedules(context.Background(), slot.Add(time.Second).UnixMilli(), slot.Add(time.Minute).UnixMilli(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim = %v, %v", due, err)
	}
	first, err := f.svc.postScheduled(tenant.NewContext(context.Background(), tenant.Default), due[0], slot)
	if err != nil {
		t.Fatal(err)
	}
	// While the claim is live nobody else runs it.
	if n := f.svc.RunDueSchedules(context.Background(), slot.Add(30*time.Second)); n != 0 {
		t.Fatalf("live claim re-run %d", n)
	}
	// After the lease another tick re-claims the same slot: the post dedupes.
	if n := f.svc.RunDueSchedules(context.Background(), slot.Add(2*time.Minute)); n != 1 {
		t.Fatalf("re-claim ran %d, want 1", n)
	}
	if msgs := f.history(); len(msgs) != 1 {
		t.Fatalf("slot posted %d times, want once", len(msgs))
	}
	if got := f.get(sc.ScheduleID); got.LastSeq != first || got.NextRunAtMs != slot.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("after re-claim = %+v (first seq %d)", got, first)
	}
}

func TestScheduleAutoPausesWhenCreatorLeaves(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", dailyBody("ping", map[string]any{"kind": "room"}))
	f.members.set("room-1", "alice", false)
	f.svc.RunDueSchedules(context.Background(), time.UnixMilli(sc.NextRunAtMs).Add(time.Second))
	if msgs := f.history(); len(msgs) != 0 {
		t.Fatalf("posted %d messages for a removed creator", len(msgs))
	}
	got := f.get(sc.ScheduleID)
	if got.Enabled || got.NextRunAtMs != 0 || got.LastStatus != wire.ScheduleRunSkipped || !strings.Contains(got.LastError, "may no longer post") {
		t.Fatalf("after removed creator = %+v", got)
	}
}

func TestScheduleMissedSlotIsSkippedNotPostedLate(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", dailyBody("standup", map[string]any{"kind": "room"}))
	slot := time.UnixMilli(sc.NextRunAtMs)
	f.svc.RunDueSchedules(context.Background(), slot.Add(3*time.Hour))
	if msgs := f.history(); len(msgs) != 0 {
		t.Fatalf("a 3h-late run posted %d messages", len(msgs))
	}
	got := f.get(sc.ScheduleID)
	if got.LastStatus != wire.ScheduleRunSkipped || !got.Enabled || got.NextRunAtMs != slot.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("after missed slot = %+v", got)
	}
}

func TestOneShotDisablesAfterItsRun(t *testing.T) {
	f := newSchedFixture(t)
	at := time.Now().Add(time.Hour).Truncate(time.Minute)
	sc := f.create("alice", map[string]any{
		"text": "release reminder", "timezone": "Europe/Berlin", "recipient": map[string]any{"kind": "room"},
		"recurrence": map[string]any{"kind": "once", "atMs": at.UnixMilli()},
	})
	f.svc.RunDueSchedules(context.Background(), at.Add(time.Second))
	got := f.get(sc.ScheduleID)
	if got.Enabled || got.NextRunAtMs != 0 || got.LastStatus != wire.ScheduleRunOK {
		t.Fatalf("one-shot after run = %+v", got)
	}
}

func TestSchedulePermissions(t *testing.T) {
	f := newSchedFixture(t)
	sc := f.create("alice", dailyBody("ping", map[string]any{"kind": "room"}))
	path := "/v1/rooms/room-1/schedules/" + sc.ScheduleID
	// A plain member cannot edit, pause, delete, or run another's schedule.
	if code, _ := f.do("bob", http.MethodPatch, path, map[string]any{"text": "hijack"}); code != http.StatusForbidden {
		t.Fatalf("bob edit = %d", code)
	}
	if code, _ := f.do("bob", http.MethodPatch, path, map[string]any{"enabled": false}); code != http.StatusForbidden {
		t.Fatalf("bob pause = %d", code)
	}
	if code, _ := f.do("bob", http.MethodDelete, path, nil); code != http.StatusForbidden {
		t.Fatalf("bob delete = %d", code)
	}
	if code, _ := f.do("bob", http.MethodPost, path+"/run", nil); code != http.StatusForbidden {
		t.Fatalf("bob run = %d", code)
	}
	// The owner may pause but not edit or resume (resume posts as alice).
	if code, _ := f.do("olga", http.MethodPatch, path, map[string]any{"text": "x"}); code != http.StatusForbidden {
		t.Fatalf("owner edit = %d", code)
	}
	if code, out := f.do("olga", http.MethodPatch, path, map[string]any{"enabled": false}); code != http.StatusOK {
		t.Fatalf("owner pause = %d %s", code, out)
	}
	if got := f.get(sc.ScheduleID); got.Enabled || got.NextRunAtMs != 0 {
		t.Fatalf("after owner pause = %+v", got)
	}
	if code, _ := f.do("olga", http.MethodPatch, path, map[string]any{"enabled": true}); code != http.StatusForbidden {
		t.Fatalf("owner resume = %d", code)
	}
	// The creator resumes, edits, runs now.
	if code, out := f.do("alice", http.MethodPatch, path, map[string]any{"enabled": true, "recurrence": map[string]any{"kind": "weekly", "time": "10:30", "days": []int{1}}}); code != http.StatusOK {
		t.Fatalf("creator resume = %d %s", code, out)
	}
	got := f.get(sc.ScheduleID)
	if !got.Enabled || got.NextRunAtMs == 0 || time.UnixMilli(got.NextRunAtMs).UTC().Weekday() != time.Monday {
		t.Fatalf("after creator edit = %+v", got)
	}
	code, out := f.do("alice", http.MethodPost, path+"/run", nil)
	if code != http.StatusOK {
		t.Fatalf("run now = %d %s", code, out)
	}
	if msgs := f.history(); len(msgs) != 1 || msgs[0].Author != "alice" {
		t.Fatalf("run now history = %+v", msgs)
	}
	if after := f.get(sc.ScheduleID); after.NextRunAtMs != got.NextRunAtMs || after.LastStatus != wire.ScheduleRunOK || after.RunCount != 1 {
		t.Fatalf("run now moved the plan or did not record: %+v", after)
	}
	// The owner deletes any schedule.
	if code, _ := f.do("olga", http.MethodDelete, path, nil); code != http.StatusNoContent {
		t.Fatalf("owner delete = %d", code)
	}
	if _, err := f.sched.GetSchedule(tenant.NewContext(context.Background(), tenant.Default), "room-1", sc.ScheduleID); !errors.Is(err, store.ErrScheduleNotFound) {
		t.Fatalf("schedule survived delete: %v", err)
	}
}

// A request to an agent that names none waits for one: it is created
// paused only, is never enabled, run now or run by the scheduler, and its
// creator picks an agent for it and then turns it on.
func TestScheduleWaitingForAnAgent(t *testing.T) {
	f := newSchedFixture(t)
	waiting := map[string]any{"kind": "agent"}
	if code, out := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", dailyBody("status?", waiting)); code != http.StatusBadRequest ||
		!strings.Contains(string(out), "waits for an agent") {
		t.Fatalf("an enabled create without an agent = %d %s", code, out)
	}
	off := dailyBody("status?", waiting)
	off["enabled"] = false
	sc := f.create("alice", off)
	if sc.Enabled || sc.NextRunAtMs != 0 || !schedule.WaitsForAgent(sc.Recipient) {
		t.Fatalf("created = %+v", sc)
	}
	path := "/v1/rooms/room-1/schedules/" + sc.ScheduleID
	if code, out := f.do("alice", http.MethodPatch, path, map[string]any{"enabled": true}); code != http.StatusBadRequest || !strings.Contains(string(out), "pick one of your agents") {
		t.Fatalf("enable without an agent = %d %s", code, out)
	}
	if code, out := f.do("alice", http.MethodPost, path+"/run", nil); code != http.StatusConflict {
		t.Fatalf("run now without an agent = %d %s", code, out)
	}
	if _, err := f.svc.postScheduled(tenant.NewContext(context.Background(), tenant.Default), f.get(sc.ScheduleID), time.Now()); !errors.Is(err, errWaitsForAgent) {
		t.Fatalf("a post without an agent = %v", err)
	}
	if got := f.get(sc.ScheduleID); got.Enabled || got.RunCount != 0 || len(f.history()) != 0 {
		t.Fatalf("after the refusals = %+v, %d messages", got, len(f.history()))
	}
	// An enabled schedule cannot lose its agent either.
	on := f.create("alice", dailyBody("standup", map[string]any{"kind": "agent", "ownerId": "bob", "provider": "codex"}))
	if code, _ := f.do("alice", http.MethodPatch, "/v1/rooms/room-1/schedules/"+on.ScheduleID, map[string]any{"recipient": waiting}); code != http.StatusBadRequest {
		t.Fatalf("an enabled schedule's agent removed = %d", code)
	}
	// The creator picks an agent, then turns it on.
	if code, out := f.do("alice", http.MethodPatch, path, map[string]any{"recipient": map[string]any{"kind": "agent", "ownerId": "alice", "provider": "codex"}}); code != http.StatusOK {
		t.Fatalf("pick an agent = %d %s", code, out)
	}
	if got := f.get(sc.ScheduleID); got.Enabled || got.Recipient.OwnerID != "alice" {
		t.Fatalf("after the pick = %+v", got)
	}
	if code, out := f.do("alice", http.MethodPatch, path, map[string]any{"enabled": true}); code != http.StatusOK {
		t.Fatalf("enable after the pick = %d %s", code, out)
	}
	got := f.get(sc.ScheduleID)
	if !got.Enabled || got.NextRunAtMs == 0 {
		t.Fatalf("after enable = %+v", got)
	}
	f.svc.RunDueSchedules(context.Background(), time.UnixMilli(got.NextRunAtMs).Add(time.Second))
	if msgs := f.history(); len(msgs) != 2 {
		t.Fatalf("the picked schedule's run posted %d messages, want 2 (with the other one's)", len(msgs))
	}
}

func TestSchedulePerMemberCap(t *testing.T) {
	f := newSchedFixture(t)
	for i := 0; i < maxSchedulesPerMember; i++ {
		f.create("alice", dailyBody("x", map[string]any{"kind": "room"}))
	}
	if code, _ := f.do("alice", http.MethodPost, "/v1/rooms/room-1/schedules", dailyBody("one more", map[string]any{"kind": "room"})); code != http.StatusConflict {
		t.Fatalf("over the member cap = %d, want 409", code)
	}
	// Another member still has room under the room cap.
	f.create("bob", dailyBody("mine", map[string]any{"kind": "room"}))
}
