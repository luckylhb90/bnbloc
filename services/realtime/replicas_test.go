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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const scaleoutKey = "scaleout-test-internal-key"

// cluster is n in-process realtime replicas on httptest, sharing one store
// (the durable feed, schedules, plugin install records), one membership set
// and one bus (topics, claims, marks), each with its own hub, plugin
// dispatcher and views: replicas without owners, any of which serves any
// request.
type cluster struct {
	t        *testing.T
	reps     []*replica
	chat     *store.MemoryChat
	members  *membership.Static
	sched    *store.MemoryRoomSchedules
	plugins  *pluginFixture
	bus      *realtime.MemoryBus
	settings store.PlatformSettingsStore
}

type replica struct {
	addr string
	srv  *httptest.Server
	svc  *Service
}

func newCluster(t *testing.T, n int) *cluster {
	t.Helper()
	t.Setenv("BNBLOC_INTERNAL_KEY", scaleoutKey)
	c := &cluster{
		t: t, chat: store.NewMemoryChat(), members: membership.NewStatic(), sched: store.NewMemoryRoomSchedules(),
		bus: realtime.NewMemoryBus(), settings: store.NewMemoryPlatformSettings(),
	}
	jobs := store.NewMemoryBridgeJobs()
	claims, marks := &realtime.MemoryClaims{}, &realtime.MemoryMarks{}
	for i := 0; i < n; i++ {
		srv := httptest.NewUnstartedServer(nil)
		r := &replica{addr: srv.Listener.Addr().String(), srv: srv}
		c.reps = append(c.reps, r)
		// Every replica reads the same plugin install records (one store).
		fix := newPluginFixture(t, c.settings)
		if i == 0 {
			c.plugins = fix
		}
		topics := c.bus.Endpoint()
		hub := realtime.NewHub(64, 64)
		hub.Bus = realtime.RoomBus(topics)
		hub.RoomHead = func(room string) (int64, error) { return c.chat.RoomSeqHead(context.Background(), room) }
		r.svc = &Service{
			Hub: hub, Membership: c.members, Rooms: c.members, Chat: c.chat, BridgeJobs: jobs, Dispatches: jobs,
			Schedules: c.sched, RoomRoles: schedRoles{anyRoom: true},
			PluginHooks: fix.dispatcher, Claims: claims, Replicas: NewReplicas(topics, marks, func() int { return n }),
		}
		r.srv.Config.Handler = svcauth.Middleware(scaleoutKey, r.svc.Handler())
		r.srv.Start()
		t.Cleanup(r.srv.Close)
	}
	return c
}

// request builds a gateway-shaped request: the signed identity assertion the
// gateway injects (which must survive the forward to the owner).
func (c *cluster) request(r *replica, method, path, user string, body any) *http.Request {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rd = bytes.NewReader(buf)
	}
	req, err := svcauth.Key(scaleoutKey).NewRequest(context.Background(), method, r.srv.URL+path, rd, user, tenant.Default)
	if err != nil {
		c.t.Fatal(err)
	}
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

// ended waits for the stream to close.
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
			t.Fatal("stream still open")
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

// A chat sent through any replica reaches the room's streams on every
// replica, once and in sequence order; each replica serves its own
// streams; the signed identity is required everywhere; history answers the
// same through any replica.
func TestReplicasChatReachesStreamsOnEveryReplica(t *testing.T) {
	c := newCluster(t, 3)
	a, b, other := c.reps[0], c.reps[1], c.reps[2]
	room := "room-1"
	c.members.Add(room, "maya")
	c.members.Add(room, "jack")

	maya := c.open(a, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	jack := c.open(b, "/v1/rooms/"+room+"/events?allChannels=1", "jack")
	waitSubscribers(t, a, room, 1)
	waitSubscribers(t, b, room, 1)
	// Presence is transient: no sequence, no SSE id.
	if f := maya.next(t, "presence_snapshot"); f.id != "" || eventOf(t, f).Sequence != 0 {
		t.Fatalf("transient presence frame carries a sequence: %+v", f)
	}

	seq1 := c.chatVia(other, room, "maya", "hello from maya", "cm-1")
	seq2 := c.chatVia(a, room, "jack", "hi maya", "cm-2")
	seq3 := c.chatVia(b, room, "maya", "and again", "cm-3")
	if seq1 != 1 || seq2 != 2 || seq3 != 3 {
		t.Fatalf("sequences %d,%d,%d, want 1,2,3 (dense, whichever replica)", seq1, seq2, seq3)
	}
	for name, s := range map[string]*stream{"maya": maya, "jack": jack} {
		for want := int64(1); want <= 3; want++ {
			if got := eventOf(t, s.next(t, "chat_message")).Sequence; got != want {
				t.Fatalf("%s received %d, want %d", name, got, want)
			}
		}
	}
	resp, err := http.DefaultClient.Do(c.request(other, http.MethodGet, "/v1/rooms/"+room+"/history", "jack", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello from maya") || !strings.Contains(string(body), "and again") {
		t.Fatalf("history = %s", body)
	}
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/v1/rooms/"+room+"/chat", strings.NewReader(`{"text":"forged"}`))
	req.Header.Set(svcauth.HeaderUser, "maya")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned chat: %v %v", resp.StatusCode, err)
	}
}

// Who is here is the room's, not a replica's: a stream on one replica is
// told of a member whose stream is on another.
func TestReplicasSharePresence(t *testing.T) {
	c := newCluster(t, 2)
	a, b := c.reps[0], c.reps[1]
	room := "room-1"
	c.members.Add(room, "maya")
	c.members.Add(room, "jack")
	maya := c.open(a, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	waitSubscribers(t, a, room, 1)
	c.open(b, "/v1/rooms/"+room+"/events?allChannels=1", "jack")
	deadline := time.After(5 * time.Second)
	for {
		var p wire.PresenceDeltaPayload
		f := maya.next(t, "presence_delta")
		_ = json.Unmarshal([]byte(f.data), &struct {
			Payload *wire.PresenceDeltaPayload `json:"payload"`
		}{&p})
		if slices.Contains(p.Joined, "jack") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("maya's stream was never told jack joined on the other replica")
		default:
		}
	}
	if got := a.svc.Views.Members(room); !slices.Contains(got, "jack") || !slices.Contains(got, "maya") {
		t.Fatalf("replica a sees %v here", got)
	}
}

// A member's plugin stream may be on any replica: a chat posted through
// another is delivered to it, and the ack sent through a third resolves it.
func TestReplicasPluginDeliveryCrossesReplicas(t *testing.T) {
	c := newCluster(t, 3)
	c.plugins.pipe.AllowUnsigned = true
	if _, err := c.plugins.pipe.Install(t.Context(), installReq(t, "acme-notes")); err != nil {
		t.Fatal(err)
	}
	holder, poster, acker := c.reps[0], c.reps[1], c.reps[2]
	room, member := "room-1", "maya"
	c.members.Add(room, member)
	c.members.Add(room, "jack")

	plugin := c.open(holder, "/v1/my/plugin-events?plugins=1", member)
	deadline := time.Now().Add(3 * time.Second)
	for !holder.svc.PluginHooks.HasDesktop(tenant.Default, member) {
		if time.Now().After(deadline) {
			t.Fatal("plugin stream not held")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.chatVia(poster, room, "jack", "deploy is green", "cm-plugin")
	f := plugin.next(t, wire.PluginEventKind)
	var d wire.PluginDelivery
	if err := json.Unmarshal([]byte(f.data), &d); err != nil {
		t.Fatal(err)
	}
	env := decodeEnvelope(t, d)
	if d.Plugin != "acme-notes" || env.RoomID != room || !strings.Contains(string(d.Envelope), "deploy is green") {
		t.Fatalf("delivery = %+v envelope %s", d, d.Envelope)
	}
	resp, err := http.DefaultClient.Do(c.request(acker, http.MethodPost, "/v1/my/plugin-events/"+d.ID+"/ack", member, map[string]string{"status": wire.PluginDeliveryDelivered}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ack through another replica = %d, want 204 (resolved by the replica that sent it)", resp.StatusCode)
	}
	resp, _ = http.DefaultClient.Do(c.request(acker, http.MethodPost, "/v1/my/plugin-events/"+d.ID+"/ack", member, map[string]string{"status": wire.PluginDeliveryDelivered}))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a second ack = %d, want 404", resp.StatusCode)
	}
}

func installReq(t *testing.T, name string) extpipe.InstallRequest {
	return extpipe.InstallRequest{Body: realtimePluginBody(t, name), Actor: "admin"}
}

// The notification worker runs on any replica; a member's desktop stream
// may be held by another, so the route finds it there (its mark) and
// reports that replica's outcome.
func TestReplicasNotificationRouteCrossesReplicas(t *testing.T) {
	c := newCluster(t, 2)
	c.plugins.pipe.AllowUnsigned = true
	c.plugins.installNotificationPlugin(t, "nimbus-bot", "")
	worker, holder := c.reps[0], c.reps[1]
	member := "maya"
	route := MemberDesktopRoute(worker.svc.PluginHooks, worker.svc.Replicas)
	if route.HasDesktop(tenant.Default, member) {
		t.Fatal("desktop reported before the member connected")
	}
	plugin := c.open(holder, "/v1/my/plugin-events?plugins=1", member)
	deadline := time.Now().Add(3 * time.Second)
	for !route.HasDesktop(tenant.Default, member) {
		if time.Now().After(deadline) {
			t.Fatal("route never saw the member's desktop on the other replica")
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
	plugin.cancel()
	deadline = time.Now().Add(3 * time.Second)
	for route.HasDesktop(tenant.Default, member) {
		if time.Now().After(deadline) {
			t.Fatal("the desktop's mark outlived its stream")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A replica going away ends only its own streams: the client reopens on
// another replica after the last sequence it holds and misses nothing,
// while streams on the other replicas stay open; a resent post is stored
// once.
func TestReplicasStreamReopensElsewhere(t *testing.T) {
	c := newCluster(t, 3)
	gone, b, poster := c.reps[0], c.reps[1], c.reps[2]
	room := "room-1"
	c.members.Add(room, "maya")
	c.members.Add(room, "jack")

	s := c.open(gone, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	jack := c.open(b, "/v1/rooms/"+room+"/events?allChannels=1", "jack")
	waitSubscribers(t, gone, room, 1)
	waitSubscribers(t, b, room, 1)
	if seq := c.chatVia(poster, room, "jack", "one", "m-1"); seq != 1 {
		t.Fatalf("first seq %d", seq)
	}
	if ev := eventOf(t, s.next(t, "chat_message")); ev.Sequence != 1 {
		t.Fatalf("got %d", ev.Sequence)
	}
	gone.srv.CloseClientConnections()
	s.ended(t)
	if seq := c.chatVia(poster, room, "jack", "two", "m-2"); seq != 2 {
		t.Fatalf("while the stream was away seq %d", seq)
	}
	s = c.open(b, "/v1/rooms/"+room+"/events?allChannels=1&since=1", "maya")
	if seq := c.chatVia(poster, room, "jack", "three", "m-3"); seq != 3 {
		t.Fatalf("after reopening seq %d", seq)
	}
	got := map[int64]bool{}
	for len(got) < 2 {
		ev := eventOf(t, s.next(t, "chat_message"))
		got[ev.Sequence] = true
	}
	if !got[2] || !got[3] {
		t.Fatalf("reopened stream got %v, want 2 (replayed) and 3 (live)", got)
	}
	for want := int64(1); want <= 3; want++ {
		if ev := eventOf(t, jack.next(t, "chat_message")); ev.Sequence != want {
			t.Fatalf("the other replica's stream got %d, want %d", ev.Sequence, want)
		}
	}
	if seq := c.chatVia(b, room, "jack", "two", "m-2"); seq != 2 {
		t.Fatalf("a resent post = %d, want the original 2", seq)
	}
	msgs, _ := c.chat.RoomFeed(tenant.NewContext(context.Background(), tenant.Default), room, 0, 50)
	if len(msgs) != 3 {
		t.Fatalf("history has %d rows, want 3", len(msgs))
	}
}

// The scheduler claims slots on whichever replica runs the tick and posts
// there; the room's streams on other replicas see the post.
func TestReplicasScheduledPostReachesEveryReplica(t *testing.T) {
	c := newCluster(t, 2)
	ticker, other := c.reps[0], c.reps[1]
	room := "room-1"
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
	s := c.open(other, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	waitSubscribers(t, other, room, 1)
	if n := ticker.svc.RunDueSchedules(context.Background(), time.UnixMilli(sc.NextRunAtMs).Add(time.Second)); n != 1 {
		t.Fatalf("ran %d schedules, want 1", n)
	}
	if ev := eventOf(t, s.next(t, "chat_message")); ev.Sequence != 1 || !strings.Contains(fmt.Sprint(ev.Payload), "standup notes") {
		t.Fatalf("scheduled post = %+v", ev)
	}
	got, err := c.sched.GetSchedule(tenant.NewContext(context.Background(), tenant.Default), room, sc.ScheduleID)
	if err != nil || got.LastStatus != wire.ScheduleRunOK || got.LastSeq != 1 {
		t.Fatalf("schedule after run = %+v, %v", got, err)
	}
}

// A member removed through one replica while a message is posted through
// another: the removal takes the room's sequence, so every replica delivers
// it before the message; the removed member's stream ends without it.
func TestReplicasRevocationIsOrderedWithMessages(t *testing.T) {
	c := newCluster(t, 3)
	a, b, poster := c.reps[0], c.reps[1], c.reps[2]
	room := "room-1"
	c.members.Add(room, "maya")
	c.members.Add(room, "jack")
	maya := c.open(a, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	jack := c.open(a, "/v1/rooms/"+room+"/events?allChannels=1", "jack")
	waitSubscribers(t, a, room, 2)

	resp, err := http.DefaultClient.Do(c.request(b, http.MethodPost, "/internal/v1/rooms/"+room+"/publish", svcauth.ServiceSubject("room"),
		wire.Event{Kind: wire.EventMemberRemoved, Payload: wire.MemberRemovedPayload{UserID: "maya"}}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("removal publish = %d", resp.StatusCode)
	}
	seq := c.chatVia(poster, room, "jack", "after the removal", "m-after")

	removal := eventOf(t, jack.next(t, wire.EventMemberRemoved))
	if removal.Sequence <= 0 || removal.Sequence >= seq {
		t.Fatalf("removal seq %d, message seq %d: want the removal numbered before the message", removal.Sequence, seq)
	}
	if ev := eventOf(t, jack.next(t, "chat_message")); ev.Sequence != seq {
		t.Fatalf("jack got %d, want %d", ev.Sequence, seq)
	}
	maya.next(t, wire.EventMemberRemoved)
	for {
		select {
		case f, open := <-maya.frames:
			if !open {
				return // ended after the removal, the message never written
			}
			if f.event == "chat_message" {
				t.Fatal("the removed member's stream got the message posted after the removal")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the removed member's stream did not end")
		}
	}
}
