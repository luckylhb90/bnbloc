package realtimehttp

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A subscribed room member receives a published event over the SSE lane,
// proving cross-member fan-out (member A publishes, member B observes).
func TestEventsFanOutToMember(t *testing.T) {
	hub := realtime.NewHub(64, 16)
	mem := membership.NewStatic()
	mem.Add("room-1", "bob")
	srv := httptest.NewServer((&Service{Hub: hub, Membership: mem}).Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events", nil)
	req.Header.Set("X-Bnbloc-User", "bob")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status = %d, want 200", resp.StatusCode)
	}

	// Wait until the subscription is registered, then publish as if from A.
	deadline := time.Now().Add(time.Second)
	for hub.SubscriberCount("room-1") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	hub.Publish(wire.Event{RoomID: "room-1", Kind: "agent_session_state", Payload: map[string]any{"agentSessionId": "s1"}})

	reader := bufio.NewReader(resp.Body)
	got := make(chan string, 8)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event: ") {
				got <- strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			}
		}
	}()
	// Presence emits join/snapshot events first; wait for the agent event.
	deadlineC := time.After(2 * time.Second)
	for {
		select {
		case kind := <-got:
			if kind == "agent_session_state" {
				return
			}
		case <-deadlineC:
			t.Fatal("timed out waiting for fanned-out agent event")
		}
	}
}

// A non-member cannot subscribe to a room's events (fail closed).
func TestEventsRejectsNonMember(t *testing.T) {
	srv := httptest.NewServer((&Service{Hub: realtime.NewHub(64, 16), Membership: membership.NewStatic()}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events", nil)
	req.Header.Set("X-Bnbloc-User", "intruder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

type captureBridgeJobs struct{ job store.AgentJob }

func (c *captureBridgeJobs) PutAgentJob(_ context.Context, j store.AgentJob) (store.AgentJob, error) {
	c.job = j
	return j, nil
}

func TestChatCreatesAgentJobWithPromptPayload(t *testing.T) {
	hub := realtime.NewHub(64, 16)
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	jobs := &captureBridgeJobs{}
	srv := httptest.NewServer((&Service{Hub: hub, Membership: mem, BridgeJobs: jobs}).Handler())
	defer srv.Close()

	body := []byte(`{"text":"fix the flaky test","clientMsgId":"client-1","channelId":"chan-1","recipient":{"ownerId":"alice","provider":"codex","core":"acme","agentOptions":{"model":"gpt-5.5","reasoningEffort":"high"}}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", bytes.NewReader(body))
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if jobs.job.MessageText != "fix the flaky test" || jobs.job.Model != "gpt-5.5" || jobs.job.ReasoningEffort != "high" {
		t.Fatalf("job payload = text %q model %q reasoning %q", jobs.job.MessageText, jobs.job.Model, jobs.job.ReasoningEffort)
	}
	if jobs.job.ChannelID != "chan-1" || jobs.job.RequesterUserID != "alice" || jobs.job.OwnerUserID != "alice" || jobs.job.Core != "acme" || jobs.job.Provider != "codex" {
		t.Fatalf("job routing = %+v", jobs.job)
	}
}

// The live chat_message echo carries the sender's clientMsgId, so a client whose
// send response was lost reconciles its optimistic row instead of duplicating it.
func TestChatEchoCarriesClientMsgID(t *testing.T) {
	hub := realtime.NewHub(64, 16)
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	srv := httptest.NewServer((&Service{Hub: hub, Membership: mem}).Handler())
	defer srv.Close()
	sub := hub.SubscribeLive("room-1")
	defer hub.Unsubscribe("room-1", sub)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/chat",
		strings.NewReader(`{"text":"hi","clientMsgId":"c-123"}`))
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d", resp.StatusCode)
	}
	for {
		select {
		case ev := <-sub.Events():
			if ev.Kind != "chat_message" {
				continue
			}
			p, _ := ev.Payload.(map[string]any)
			if p["clientMsgId"] != "c-123" {
				t.Fatalf("chat payload = %+v, want clientMsgId c-123", p)
			}
			return
		case <-time.After(2 * time.Second):
			t.Fatal("no chat_message published")
		}
	}
}

// The stream opens with body bytes (and asks nginx-style edges not to
// buffer), so a proxy that holds headers until the first body byte does not
// delay a quiet room's subscription until the 15s heartbeat.
func TestEventsStreamOpensWithoutWaitingForHeartbeat(t *testing.T) {
	mem := membership.NewStatic()
	mem.Add("room-1", "bob")
	srv := httptest.NewServer((&Service{Hub: realtime.NewHub(64, 16), Membership: mem}).Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events?presence=0&replay=0", nil)
	req.Header.Set("X-Bnbloc-User", "bob")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if !strings.HasPrefix(l, ":") {
			t.Fatalf("first stream line = %q, want an SSE comment", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no body bytes before the heartbeat: a header-holding proxy would stall the open")
	}
}
