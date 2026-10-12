package realtimehttp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Every surface that acts in a room's conversation admits only a member
// who may chat (membership.Member.MayChat): never a visitor (read-only,
// D1) and never a platform agent host's membership. Reads stay open to
// both.
func TestHumanOnlyRoutes(t *testing.T) {
	mem := membership.NewStatic()
	mem.Add("room-1", "maya")
	mem.AddRole("room-1", "vic", store.RoleVisitor)
	if err := mem.PutAgentHost(t.Context(), "", "room-1", "root"); err != nil {
		t.Fatal(err)
	}
	sched := store.NewMemoryRoomSchedules()
	svc := &Service{
		Hub: realtime.NewHub(64, 16), Membership: mem, Chat: store.NewMemoryChat(),
		BridgeJobs: store.NewMemoryBridgeJobs(), Schedules: sched,
		RoomRoles: schedRoles{rooms: map[string]bool{"room-1": true}, roles: map[string]store.Role{}},
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	call := func(method, path, user, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("X-Bnbloc-User", user)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	schedule := `{"text":"ping","recipient":{"kind":"room"},"timezone":"UTC","recurrence":{"kind":"daily","time":"09:00"}}`
	routes := []struct {
		name, method, path, body string
		writes                   bool
	}{
		{"chat", http.MethodPost, "/v1/rooms/room-1/chat", `{"text":"hi"}`, true},
		{"typing", http.MethodPost, "/v1/rooms/room-1/typing", `{}`, true},
		{"schedule create", http.MethodPost, "/v1/rooms/room-1/schedules", schedule, true},
		{"history", http.MethodGet, "/v1/rooms/room-1/history", "", false},
		{"schedules list", http.MethodGet, "/v1/rooms/room-1/schedules", "", false},
	}
	for _, rt := range routes {
		for _, user := range []string{"vic", "root", "maya"} {
			code := call(rt.method, rt.path, user, rt.body)
			refused := code == http.StatusForbidden
			if want := rt.writes && user != "maya"; refused != want {
				t.Errorf("%s as %s = %d, refused=%v want %v", rt.name, user, code, refused, want)
			}
		}
	}

	// A schedule whose creator became a visitor no longer posts: not when
	// run by hand (PostChat checks the author), not when it comes due.
	var sc wire.RoomSchedule
	{
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/schedules", strings.NewReader(schedule))
		req.Header.Set("X-Bnbloc-User", "maya")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(resp.Body).Decode(&sc)
		resp.Body.Close()
	}
	if sc.ScheduleID == "" {
		t.Fatal("no schedule created")
	}
	mem.AddRole("room-1", "maya", store.RoleVisitor)
	if code := call(http.MethodPost, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/run", "maya", ""); code != http.StatusForbidden {
		t.Fatalf("run by a creator made visitor = %d, want 403", code)
	}
	svc.RunDueSchedules(context.Background(), time.UnixMilli(sc.NextRunAtMs).Add(time.Second))
	got, err := sched.GetSchedule(tenant.NewContext(context.Background(), tenant.Default), "room-1", sc.ScheduleID)
	if err != nil || got.Enabled || got.LastStatus != wire.ScheduleRunSkipped {
		t.Fatalf("due schedule of a visitor = %+v %v, want paused and skipped", got, err)
	}
	if msgs, _ := svc.Chat.RoomFeed(dctx(), "room-1", 0, 10); len(msgs) != 1 {
		t.Fatalf("feed = %d messages, want only maya's chat from before", len(msgs))
	}
}

// A dispatch posts as the parent job's requester: once the requester is a
// visitor, it is refused.
func TestHumanOnlyRoutesDispatchRequester(t *testing.T) {
	f := newDispatchFixture(t)
	p := f.parent("job_parent")
	f.svc.Membership.(*membership.Static).AddRole("room-1", "maya", store.RoleVisitor)
	if code, out := f.dispatch("jack", toBob(p.JobID)); code != http.StatusForbidden || out["code"] != wire.DispatchRequesterGone {
		t.Fatalf("dispatch for a visitor requester = %d %v, want 403 %s", code, out, wire.DispatchRequesterGone)
	}
}

// revocableRoom is a room service membership probe whose answers a test can
// change, counting the probes.
type revocableRoom struct {
	mu      sync.Mutex
	members map[string]bool
	probes  int
}

func (r *revocableRoom) set(user string, member bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[user] = member
}

func (r *revocableRoom) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probes++
	user := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
	_ = json.NewEncoder(w).Encode(map[string]any{"member": r.members[user], "role": "member", "tenantId": tenant.Default})
}

// A removed member keeps no access: the revocation event ends the stream and
// clears the cached membership (the next read or reconnect is refused at
// once, not after the cache TTL), and a stream whose event never arrived
// ends at its next heartbeat re-check.
func TestRevocationClosesAccess(t *testing.T) {
	room := &revocableRoom{members: map[string]bool{"maya": true, "jack": true}}
	roomSrv := httptest.NewServer(room)
	t.Cleanup(roomSrv.Close)
	rig := func(ttl time.Duration) *httptest.Server {
		rc := membership.NewRoomClient(roomSrv.URL)
		rc.TTL = ttl
		svc := &Service{Hub: realtime.NewHub(64, 16), Membership: rc, Chat: store.NewMemoryChat(), HeartbeatEvery: 50 * time.Millisecond}
		srv := httptest.NewServer(svc.Handler())
		t.Cleanup(srv.Close)
		return srv
	}
	do := func(srv *httptest.Server, method, path, user, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("X-Bnbloc-User", user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	status := func(srv *httptest.Server, method, path, user, body string) int {
		resp := do(srv, method, path, user, body)
		resp.Body.Close()
		return resp.StatusCode
	}
	ended := func(resp *http.Response) bool {
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, bufio.NewReader(resp.Body)); close(done) }()
		select {
		case <-done:
			return true
		case <-time.After(3 * time.Second):
			return false
		}
	}

	// maya streams the room, and a read has cached her membership.
	srv := rig(time.Hour)
	stream := do(srv, http.MethodGet, "/v1/rooms/room-1/events?presence=0", "maya", "")
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("open = %d", stream.StatusCode)
	}
	if code := status(srv, http.MethodGet, "/v1/rooms/room-1/history", "maya", ""); code != http.StatusOK {
		t.Fatalf("history = %d", code)
	}
	// She is removed: the room service publishes member_removed.
	room.set("maya", false)
	ev, _ := json.Marshal(wire.Event{Kind: wire.EventMemberRemoved, Payload: map[string]any{"userId": "maya"}})
	if code := status(srv, http.MethodPost, "/internal/v1/rooms/room-1/publish", "room-service", string(ev)); code != http.StatusOK {
		t.Fatalf("publish = %d", code)
	}
	if !ended(stream) {
		t.Fatal("the removed member's stream did not end on member_removed")
	}
	if code := status(srv, http.MethodGet, "/v1/rooms/room-1/history", "maya", ""); code != http.StatusForbidden {
		t.Fatalf("read after removal = %d, want 403 (the cached membership must be forgotten)", code)
	}
	if code := status(srv, http.MethodGet, "/v1/rooms/room-1/events", "maya", ""); code != http.StatusForbidden {
		t.Fatalf("reconnect after removal = %d, want 403", code)
	}

	// jack is removed without any event reaching this replica: his stream
	// ends at a heartbeat re-check once the cached answer expires.
	srv = rig(10 * time.Millisecond)
	stream = do(srv, http.MethodGet, "/v1/rooms/room-1/events?presence=0", "jack", "")
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("open jack = %d", stream.StatusCode)
	}
	room.set("jack", false)
	if !ended(stream) {
		t.Fatal("a stream outlived its membership past the heartbeat re-check")
	}
}
