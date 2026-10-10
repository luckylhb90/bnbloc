package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func newTestServer(t *testing.T) (*httptest.Server, *membership.Static) {
	t.Helper()
	mem := membership.NewStatic()
	svc := &Service{Store: store.NewMemory(), Publisher: realtime.HubPublisher{Hub: realtime.NewHub(64, 16)}, Membership: mem}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return srv, mem
}

func post(t *testing.T, srv *httptest.Server, path, user string, body any) *http.Response {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(buf))
	if user != "" {
		req.Header.Set("X-Bnbloc-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	return resp
}

// Non-member is rejected: fail closed on room scope.
func TestSessionStateRejectsNonMember(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/session-state", "intruder",
		wire.ReportSessionStateInput{WorkspaceID: "room-1", AgentSessionID: "s1", MessageVersion: 1})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// workspaceId must equal the room scope: no implicit translation.
func TestSessionStateRejectsScopeMismatch(t *testing.T) {
	srv, mem := newTestServer(t)
	mem.Add("room-1", "alice")
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/session-state", "alice",
		wire.ReportSessionStateInput{WorkspaceID: "room-2", AgentSessionID: "s1", MessageVersion: 1})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// A member's session state is accepted and returns the accepted version.
func TestSessionStateAcceptsMember(t *testing.T) {
	srv, mem := newTestServer(t)
	mem.Add("room-1", "alice")
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/session-state", "alice",
		wire.ReportSessionStateInput{WorkspaceID: "room-1", AgentSessionID: "s1", MessageVersion: 5})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var reply wire.ReportSessionStateReply
	_ = json.NewDecoder(resp.Body).Decode(&reply)
	if reply.SyncCursor != 5 {
		t.Fatalf("syncCursor = %d, want 5", reply.SyncCursor)
	}
}

// The submit-provenance barrier is idempotent: a replayed client-submit id
// returns 200, never a duplicate admission error.
func TestSubmitProvenanceIsIdempotent(t *testing.T) {
	srv, mem := newTestServer(t)
	mem.Add("room-1", "alice")
	in := wire.SubmitProvenanceInput{WorkspaceID: "room-1", AgentSessionID: "s1", ClientSubmitID: "cs-1"}
	for attempt := range 2 {
		resp := post(t, srv, "/v1/rooms/room-1/agent-activity/submit-provenance", "alice", in)
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK {
			t.Fatalf("attempt %d status = %d, want 200", attempt, status)
		}
	}
}

// Empty client-submit id is rejected: the barrier requires a stable id.
func TestSubmitProvenanceRequiresID(t *testing.T) {
	srv, mem := newTestServer(t)
	mem.Add("room-1", "alice")
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/submit-provenance", "alice",
		wire.SubmitProvenanceInput{WorkspaceID: "room-1", AgentSessionID: "s1", ClientSubmitID: ""})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// The activity service publishes an event through its realtime.Publisher on a
// session-state report, so a separate realtime process can fan it out.
func TestSessionStatePublishesEvent(t *testing.T) {
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	captured := &capturePublisher{}
	svc := &Service{Store: store.NewMemory(), Publisher: captured, Membership: mem}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/session-state", "alice",
		wire.ReportSessionStateInput{WorkspaceID: "room-1", AgentSessionID: "s1", MessageVersion: 1})
	resp.Body.Close()

	if len(captured.events) != 1 || captured.events[0].Kind != "agent_session_state" {
		t.Fatalf("published = %+v, want one agent_session_state", captured.events)
	}
	if captured.events[0].RoomID != "room-1" {
		t.Fatalf("published roomId = %q, want room-1", captured.events[0].RoomID)
	}
}

// A re-drained message batch publishes only messages newer than the durable
// cursor, so a reporter restart/reconnect never duplicates a reply in the room.
func TestMessagesPublishOnlyNewOnReplay(t *testing.T) {
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	captured := &capturePublisher{}
	svc := &Service{Store: store.NewMemory(), Publisher: captured, Membership: mem}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	first := wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{
		{Seq: 1, Payload: map[string]any{"text": "a"}},
		{Seq: 2, Payload: map[string]any{"text": "b"}},
	}}
	post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "alice", first).Body.Close()

	// A reporter re-draining from seq 0 replays 1..2 and adds a new seq 3.
	replay := wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{
		{Seq: 1, Payload: map[string]any{"text": "a"}},
		{Seq: 2, Payload: map[string]any{"text": "b"}},
		{Seq: 3, Payload: map[string]any{"text": "c"}},
	}}
	post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "alice", replay).Body.Close()

	if len(captured.events) != 2 {
		t.Fatalf("published events = %d, want 2 (first batch, then only the new message)", len(captured.events))
	}
	got := captured.events[1].Payload.(wire.ReportMessagesInput).Messages
	if len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("replay published = %+v, want only seq 3", got)
	}
}

// An all-replay batch (nothing above the cursor) publishes nothing.
func TestMessagesReplayPublishesNothing(t *testing.T) {
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	captured := &capturePublisher{}
	svc := &Service{Store: store.NewMemory(), Publisher: captured, Membership: mem}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	batch := wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{
		{Seq: 1, Payload: map[string]any{"text": "a"}},
	}}
	post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "alice", batch).Body.Close()
	post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "alice", batch).Body.Close()

	if len(captured.events) != 1 {
		t.Fatalf("published events = %d, want 1 (the pure replay publishes nothing)", len(captured.events))
	}
}

type capturePublisher struct{ events []wire.Event }

func (c *capturePublisher) Publish(_ context.Context, ev wire.Event) { c.events = append(c.events, ev) }
