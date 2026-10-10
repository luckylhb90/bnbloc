package realtimehttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const scaleoutKey = "scaleout-test-internal-key"

// cluster is n in-process realtime replicas on httptest, sharing one store
// (the durable feed, schedules, plugin install records) and one membership
// set, each with its own hub and plugin dispatcher, wired by a static peer
// list whose liveness the test controls.
type cluster struct {
	t        *testing.T
	reps     []*replica
	chat     *store.MemoryChat
	members  *membership.Static
	sched    *store.MemoryRoomSchedules
	plugins  *pluginFixture
	mu       sync.Mutex
	down     map[string]bool
	settings store.PlatformSettingsStore
}

type replica struct {
	addr string
	srv  *httptest.Server
	svc  *Service
	set  *shard.Set
}

func newCluster(t *testing.T, n int) *cluster {
	t.Helper()
	t.Setenv("BNBLOC_INTERNAL_KEY", scaleoutKey)
	c := &cluster{
		t: t, chat: store.NewMemoryChat(), members: membership.NewStatic(), sched: store.NewMemoryRoomSchedules(),
		down: map[string]bool{}, settings: store.NewMemoryPlatformSettings(),
	}
	addrs := make([]string, n)
	for i := 0; i < n; i++ {
		srv := httptest.NewUnstartedServer(nil)
		c.reps = append(c.reps, &replica{addr: srv.Listener.Addr().String(), srv: srv})
		addrs[i] = c.reps[i].addr
	}
	jobs := store.NewMemoryBridgeJobs()
	for i, r := range c.reps {
		set, err := shard.New(shard.Options{Peers: strings.Join(addrs, ","), Self: r.addr, Probe: c.probe})
		if err != nil {
			t.Fatal(err)
		}
		r.set = set
		// Every replica reads the same plugin install records (one store).
		fix := newPluginFixture(t, c.settings)
		if i == 0 {
			c.plugins = fix
		}
		hub := realtime.NewHub(64, 64)
		hub.SeedSeq = func(room string) (int64, error) {
			return c.chat.MaxRoomSeq(tenant.NewContext(context.Background(), tenant.Default), room, "")
		}
		r.svc = &Service{
			Hub: hub, Membership: c.members, Chat: c.chat, BridgeJobs: jobs,
			Schedules: c.sched, RoomRoles: schedRoles{rooms: map[string]bool{}},
			PluginHooks: fix.dispatcher, Peers: NewPeers(set),
		}
		r.srv.Config.Handler = svcauth.Middleware(scaleoutKey, r.svc.Handler())
		r.srv.Start()
		t.Cleanup(r.srv.Close)
	}
	c.refresh()
	return c
}

func (c *cluster) probe(_ context.Context, addr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.down[addr]
}

func (c *cluster) setDown(r *replica, down bool) {
	c.mu.Lock()
	c.down[r.addr] = down
	c.mu.Unlock()
}

// refresh runs one peer-set refresh on every replica (its ownership
// reconcile runs as the set's listener).
func (c *cluster) refresh() {
	for _, r := range c.reps {
		r.set.Refresh(context.Background())
	}
}

func (c *cluster) peers() []string { return c.reps[0].set.Peers() }

// ownedBy finds a key of the given form owned by r in the current view.
func (c *cluster) ownedBy(r *replica, format string, key func(string) string) string {
	c.t.Helper()
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf(format, i)
		if shard.Owner(c.peers(), key(id)) == r.addr {
			return id
		}
	}
	c.t.Fatal("no key found")
	return ""
}

func (c *cluster) roomOwnedBy(r *replica) string { return c.ownedBy(r, "room-%d", RoomKey) }

func (c *cluster) memberOwnedBy(r *replica) string { return c.ownedBy(r, "maya-%d", MemberKey) }

// request builds a gateway-shaped request: the signed identity assertion the
// gateway injects (which must survive the forward to the owner).
func (c *cluster) request(r *replica, method, path, user string, body any) *http.Request {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, r.srv.URL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set(svcauth.HeaderUser, user)
	req.Header.Set(svcauth.HeaderTenant, tenant.Default)
	svcauth.Sign(req, scaleoutKey, user)
	return req
}

func (c *cluster) chatVia(r *replica, room, user, text, clientMsgID string) int64 {
	c.t.Helper()
	resp, err := http.DefaultClient.Do(c.request(r, http.MethodPost, "/v1/rooms/"+room+"/chat", user, map[string]any{"text": text, "clientMsgId": clientMsgID}))
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Sequence int64 `json:"sequence"`
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
		c.t.Fatalf("chat via %s: %d %s", r.addr, resp.StatusCode, body)
	}
	return out.Sequence
}

// sseFrame is one parsed SSE event.
type sseFrame struct {
	id    string
	event string
	data  string
}

// stream opens an SSE stream and parses frames until it ends (frames
// closes).
type stream struct {
	frames chan sseFrame
	cancel context.CancelFunc
}

func (c *cluster) open(r *replica, path, user string) *stream {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := c.request(r, http.MethodGet, path, user, nil).WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		c.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		cancel()
		c.t.Fatalf("open %s via %s: %d %s", path, r.addr, resp.StatusCode, body)
	}
	s := &stream{frames: make(chan sseFrame, 256), cancel: cancel}
	go func() {
		defer close(s.frames)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		var f sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			case line == "" && f.event != "":
				s.frames <- f
				f = sseFrame{}
			}
		}
	}()
	c.t.Cleanup(cancel)
	return s
}

// next waits for the next frame of kind, failing on timeout or stream end.
func (s *stream) next(t *testing.T, kind string) sseFrame {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case f, open := <-s.frames:
			if !open {
				t.Fatalf("stream ended while waiting for %s", kind)
			}
			if f.event == kind {
				return f
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

// ended waits for the stream to close (its key moved to another replica).
func (s *stream) ended(t *testing.T) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-s.frames:
			if !open {
				return
			}
		case <-timeout:
			t.Fatal("stream still open after its key moved")
		}
	}
}

func eventOf(t *testing.T, f sseFrame) wire.Event {
	t.Helper()
	var ev wire.Event
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
		t.Fatalf("decode %s: %v", f.data, err)
	}
	return ev
}

func waitSubscribers(t *testing.T, r *replica, room string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.svc.Hub.SubscriberCount(room) != n {
		if time.Now().After(deadline) {
			t.Fatalf("replica %s has %d subscribers for %s, want %d", r.addr, r.svc.Hub.SubscriberCount(room), room, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A chat sent to a non-owner reaches subscribers connected through other
// non-owners: every stream and publish of the room lands on its owner, the
// signed identity survives the hop, and transient events carry no sequence.
func TestScaleOutChatReachesSubscribersOnOtherReplicas(t *testing.T) {
	c := newCluster(t, 3)
	owner, a, b := c.reps[0], c.reps[1], c.reps[2]
	room := c.roomOwnedBy(owner)
	c.members.Add(room, "maya")
	c.members.Add(room, "jack")

	maya := c.open(a, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	jack := c.open(b, "/v1/rooms/"+room+"/events?allChannels=1", "jack")
	waitSubscribers(t, owner, room, 2)
	for _, r := range []*replica{a, b} {
		if n := r.svc.Hub.SubscriberCount(room); n != 0 {
			t.Fatalf("non-owner %s holds %d subscribers", r.addr, n)
		}
	}
	// Presence is the owner's: transient, no sequence, no SSE id.
	if f := maya.next(t, "presence_snapshot"); f.id != "" || eventOf(t, f).Sequence != 0 {
		t.Fatalf("transient presence frame carries a sequence: %+v", f)
	}

	seq1 := c.chatVia(b, room, "maya", "hello from maya", "cm-1")
	seq2 := c.chatVia(a, room, "jack", "hi maya", "cm-2")
	if seq1 != 1 || seq2 != 2 {
		t.Fatalf("sequences %d,%d, want 1,2 (dense durable space)", seq1, seq2)
	}
	for name, s := range map[string]*stream{"maya": maya, "jack": jack} {
		first, second := eventOf(t, s.next(t, "chat_message")), eventOf(t, s.next(t, "chat_message"))
		if first.Sequence != seq1 || second.Sequence != seq2 {
			t.Fatalf("%s received %d,%d, want %d,%d in order", name, first.Sequence, second.Sequence, seq1, seq2)
		}
	}
	// History is store-backed and answers the same through any replica.
	resp, err := http.DefaultClient.Do(c.request(b, http.MethodGet, "/v1/rooms/"+room+"/history", "jack", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello from maya") || !strings.Contains(string(body), "hi maya") {
		t.Fatalf("history via non-owner = %s", body)
	}
	// An unsigned identity is refused on the owner even when it arrives
	// forwarded (the forward never mints identity).
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/v1/rooms/"+room+"/chat", strings.NewReader(`{"text":"forged"}`))
	req.Header.Set(svcauth.HeaderUser, "maya")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned chat: %v %v", resp.StatusCode, err)
	}
}

// A member's plugin stream lives on the member's owner; a chat published on
// the room's owner (another replica) is handed across and delivered there,
// and the ack routed through yet another replica resolves it.
func TestScaleOutPluginDeliveryCrossesReplicas(t *testing.T) {
	c := newCluster(t, 3)
	c.plugins.pipe.AllowUnsigned = true
	if _, err := c.plugins.pipe.Install(t.Context(), installReq(t, "acme-notes")); err != nil {
		t.Fatal(err)
	}
	roomOwner, memberOwner, other := c.reps[0], c.reps[1], c.reps[2]
	room := c.roomOwnedBy(roomOwner)
	member := c.memberOwnedBy(memberOwner)
	c.members.Add(room, member)
	c.members.Add(room, "jack")

	plugin := c.open(other, "/v1/my/plugin-events", member)
	deadline := time.Now().Add(3 * time.Second)
	for !memberOwner.svc.PluginHooks.HasDesktop(tenant.Default, member) {
		if time.Now().After(deadline) {
			t.Fatal("plugin stream did not land on the member's owner")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if other.svc.PluginHooks.HasDesktop(tenant.Default, member) || roomOwner.svc.PluginHooks.HasDesktop(tenant.Default, member) {
		t.Fatal("a non-owner holds the member's plugin stream")
	}

	c.chatVia(other, room, "jack", "deploy is green", "cm-plugin")
	f := plugin.next(t, wire.PluginEventKind)
	var d wire.PluginDelivery
	if err := json.Unmarshal([]byte(f.data), &d); err != nil {
		t.Fatal(err)
	}
	env := decodeEnvelope(t, d)
	if d.Plugin != "acme-notes" || env.RoomID != room || !strings.Contains(string(d.Envelope), "deploy is green") {
		t.Fatalf("delivery = %+v envelope %s", d, d.Envelope)
	}
	resp, err := http.DefaultClient.Do(c.request(roomOwner, http.MethodPost, "/v1/my/plugin-events/"+d.ID+"/ack", member, map[string]string{"status": wire.PluginDeliveryDelivered}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack via a non-owner = %d, want 204 (routed to the member's owner)", resp.StatusCode)
	}
}

func installReq(t *testing.T, name string) extpipe.InstallRequest {
	return extpipe.InstallRequest{Body: realtimePluginBody(t, name), Actor: "admin"}
}

// The notification worker runs on any replica; a member's desktop lives on
// the member's owner, so the route asks and delivers there and reports the
// owner's outcome.
func TestScaleOutNotificationRouteCrossesReplicas(t *testing.T) {
	c := newCluster(t, 2)
	c.plugins.pipe.AllowUnsigned = true
	c.plugins.installNotificationPlugin(t, "nimbus-bot", "")
	worker, memberOwner := c.reps[0], c.reps[1]
	member := c.memberOwnedBy(memberOwner)
	route := MemberDesktopRoute(worker.svc.PluginHooks, worker.svc.Peers)
	if route.HasDesktop(tenant.Default, member) {
		t.Fatal("desktop reported before the member connected")
	}
	plugin := c.open(worker, "/v1/my/plugin-events", member) // forwarded to the owner
	deadline := time.Now().Add(3 * time.Second)
	for !route.HasDesktop(tenant.Default, member) {
		if time.Now().After(deadline) {
			t.Fatal("route never saw the member's desktop on its owner")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got outcomes
	route.DeliverNotification(tenant.Default, wire.PluginNotification{ID: "ntf-1", Type: "mention", UserID: member, RoomID: "room-x", Text: "you were mentioned"}, got.fn())
	f := plugin.next(t, wire.PluginEventKind)
	var d wire.PluginDelivery
	_ = json.Unmarshal([]byte(f.data), &d)
	if d.Plugin != "nimbus-bot" || !strings.Contains(string(d.Envelope), "you were mentioned") {
		t.Fatalf("notification delivery = %+v %s", d, d.Envelope)
	}
	resp, err := http.DefaultClient.Do(c.request(worker, http.MethodPost, "/v1/my/plugin-events/"+d.ID+"/ack", member, map[string]string{"status": wire.PluginDeliveryDelivered}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got.waitFor(t, true)
}

// When the peer set changes, a replica closes the streams of keys it no
// longer owns; the client reconnects (here: through the same entry replica),
// lands on the new owner, and durable sequences continue from the durable
// max with no gap, repeat or loss — also when the room moves back.
func TestScaleOutOwnershipChangeMovesStreams(t *testing.T) {
	c := newCluster(t, 2)
	entry, owner := c.reps[0], c.reps[1]
	room := c.roomOwnedBy(owner)
	c.members.Add(room, "maya")
	c.members.Add(room, "jack")

	s := c.open(entry, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	waitSubscribers(t, owner, room, 1)
	if seq := c.chatVia(entry, room, "jack", "one", "m-1"); seq != 1 {
		t.Fatalf("first seq %d", seq)
	}
	if ev := eventOf(t, s.next(t, "chat_message")); ev.Sequence != 1 {
		t.Fatalf("got %d", ev.Sequence)
	}

	// The owner leaves every view (two failed probes): it releases the room.
	c.setDown(owner, true)
	c.refresh()
	c.refresh()
	s.ended(t)
	if owner.svc.Hub.SubscriberCount(room) != 0 {
		t.Fatal("released room still has subscribers on the old owner")
	}
	s = c.open(entry, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	waitSubscribers(t, entry, room, 1)
	// A publish sent to the old owner is forwarded to the new one.
	if seq := c.chatVia(owner, room, "jack", "two", "m-2"); seq != 2 {
		t.Fatalf("after move seq %d, want 2 (seeded from the durable max)", seq)
	}
	if ev := eventOf(t, s.next(t, "chat_message")); ev.Sequence != 2 {
		t.Fatalf("got %d after move", ev.Sequence)
	}

	// It comes back: the room returns to it, whose forgotten counter seeds
	// afresh (a stale 1 would collide with the stored 2).
	c.setDown(owner, false)
	c.refresh()
	s.ended(t)
	s = c.open(entry, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	waitSubscribers(t, owner, room, 1)
	if seq := c.chatVia(entry, room, "jack", "three", "m-3"); seq != 3 {
		t.Fatalf("after move back seq %d, want 3", seq)
	}
	if ev := eventOf(t, s.next(t, "chat_message")); ev.Sequence != 3 {
		t.Fatalf("got %d after move back", ev.Sequence)
	}
	// A client retry of an already-stored send (its response was lost in the
	// move) is idempotent on the new owner.
	if seq := c.chatVia(entry, room, "jack", "two", "m-2"); seq != 2 {
		t.Fatalf("retried send = %d, want the original 2", seq)
	}
	msgs, _ := c.chat.RoomFeed(tenant.NewContext(context.Background(), tenant.Default), room, 0, 50)
	if len(msgs) != 3 {
		t.Fatalf("history has %d rows, want 3 (no loss, no duplicate)", len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != int64(i+1) {
			t.Fatalf("history seqs %+v not 1..3", msgs)
		}
	}
}

// A request forwarded to a replica whose view names another owner (the
// views briefly disagree) is served locally rather than looped; the stream
// it opened is swept on that replica's next refresh, so the client moves to
// the real owner instead of missing the room's events indefinitely.
func TestScaleOutUnownedForwardedStreamIsSwept(t *testing.T) {
	c := newCluster(t, 2)
	stray, owner := c.reps[0], c.reps[1]
	room := c.roomOwnedBy(owner)
	c.members.Add(room, "maya")
	req := c.request(stray, http.MethodGet, "/v1/rooms/"+room+"/events", "maya", nil)
	req.Header.Set(shard.ForwardedHeader, "10.9.9.9:8082") // a peer with a stale view
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	waitSubscribers(t, stray, room, 1)
	stray.set.Refresh(context.Background())
	waitSubscribers(t, stray, room, 0)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("stream did not end cleanly: %v", err)
	}
}

// The scheduler claims slots on whichever replica runs the tick; a slot for
// a room another replica owns is posted by that owner, so live subscribers
// (on the owner) see it and the sequence comes from the owner's counter.
func TestScaleOutScheduledPostRunsOnRoomOwner(t *testing.T) {
	c := newCluster(t, 2)
	ticker, owner := c.reps[0], c.reps[1]
	room := c.roomOwnedBy(owner)
	c.members.Add(room, "maya")
	resp, err := http.DefaultClient.Do(c.request(ticker, http.MethodPost, "/v1/rooms/"+room+"/schedules", "maya", dailyBody("standup notes", map[string]any{"kind": "room"})))
	if err != nil {
		t.Fatal(err)
	}
	var sc wire.RoomSchedule
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &sc) != nil {
		t.Fatalf("create schedule: %d %s", resp.StatusCode, body)
	}
	s := c.open(ticker, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	waitSubscribers(t, owner, room, 1)
	if n := ticker.svc.RunDueSchedules(context.Background(), time.UnixMilli(sc.NextRunAtMs).Add(time.Second)); n != 1 {
		t.Fatalf("ran %d schedules, want 1", n)
	}
	if ev := eventOf(t, s.next(t, "chat_message")); ev.Sequence != 1 || !strings.Contains(fmt.Sprint(ev.Payload), "standup notes") {
		t.Fatalf("scheduled post = %+v", ev)
	}
	if ticker.svc.Hub.SubscriberCount(room) != 0 {
		t.Fatal("the ticking replica touched the room's hub")
	}
	got, err := c.sched.GetSchedule(tenant.NewContext(context.Background(), tenant.Default), room, sc.ScheduleID)
	if err != nil || got.LastStatus != wire.ScheduleRunOK || got.LastSeq != 1 {
		t.Fatalf("schedule after run = %+v, %v", got, err)
	}
}
