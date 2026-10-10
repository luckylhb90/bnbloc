package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeSmokeGateway is the slice of the gateway the smoke drives: rooms,
// a room SSE stream fed by chat publishes, history, and my agents.
type fakeSmokeGateway struct {
	*httptest.Server
	t *testing.T

	mu       sync.Mutex
	rooms    map[string]bool
	deleted  []string
	history  map[string][]smokeEvent
	subs     map[string][]chan smokeEvent
	seq      int64
	bearers  []string
	agents   []myAgent
	lastRcpt json.RawMessage

	dropEcho    bool // publish nothing on chat (echo never arrives)
	deleteFirst bool // the room vanishes before the smoke's DELETE (404)
	agentReply  bool // answer a recipient-addressed chat with agent_messages
	failJob     bool // ...as a job-failure notice
	// versions answers GET /v1/platform/versions (nil: 403, not a super-admin).
	versions *wire.DeploymentVersions
}

func newFakeSmokeGateway(t *testing.T) *fakeSmokeGateway {
	g := &fakeSmokeGateway{t: t, rooms: map[string]bool{}, history: map[string][]smokeEvent{}, subs: map[string][]chan smokeEvent{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users/{subject}/rooms", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"rooms": []string{}})
	})
	mux.HandleFunc("POST /v1/rooms", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RoomID     string `json:"roomId"`
			Visibility string `json:"visibility"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.HasPrefix(body.RoomID, "smoke-") || body.Visibility != "private" {
			http.Error(w, "bad room", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.rooms[body.RoomID] = true
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"roomId": body.RoomID})
	})
	mux.HandleFunc("DELETE /v1/rooms/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.deleteFirst {
			delete(g.rooms, id)
		}
		if !g.rooms[id] {
			http.Error(w, "room not found", http.StatusNotFound)
			return
		}
		delete(g.rooms, id)
		g.deleted = append(g.deleted, id)
	})
	mux.HandleFunc("GET /v1/rooms/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.Query().Get("presence") != "0" || r.URL.Query().Get("allChannels") != "1" {
			http.Error(w, "smoke must subscribe as a non-presence, all-channel observer", http.StatusBadRequest)
			return
		}
		ch := make(chan smokeEvent, 16)
		g.mu.Lock()
		g.subs[id] = append(g.subs[id], ch)
		g.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, ": keep-alive\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-ch:
				data, _ := json.Marshal(ev)
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Sequence, ev.Kind, data)
				w.(http.Flusher).Flush()
			}
		}
	})
	mux.HandleFunc("POST /v1/rooms/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Text        string          `json:"text"`
			ClientMsgID string          `json:"clientMsgId"`
			Recipient   json.RawMessage `json:"recipient"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		payload := map[string]any{"userId": "alice", "text": body.Text, "clientMsgId": body.ClientMsgID}
		if len(body.Recipient) > 0 {
			payload["recipient"] = body.Recipient
		}
		raw, _ := json.Marshal(payload)
		g.mu.Lock()
		g.seq++
		ev := smokeEvent{Sequence: g.seq, Kind: "chat_message", Payload: raw}
		if len(body.Recipient) > 0 {
			g.lastRcpt = body.Recipient
		}
		g.mu.Unlock()
		if !g.dropEcho {
			g.publish(id, ev, true)
		}
		if len(body.Recipient) > 0 && g.agentReply {
			msg := map[string]any{"role": "assistant", "text": "OK", "ownerUserId": "alice", "provider": "codex"}
			if g.failJob {
				msg["failReason"] = "timeout"
				msg["text"] = "the agent did not answer"
			}
			raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"payload": msg}}})
			g.mu.Lock()
			g.seq++
			reply := smokeEvent{Sequence: g.seq, Kind: "agent_messages", Payload: raw}
			g.mu.Unlock()
			go func() {
				time.Sleep(20 * time.Millisecond)
				g.publish(id, reply, true)
			}()
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"sequence": ev.Sequence})
	})
	mux.HandleFunc("GET /v1/rooms/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		msgs := g.history[r.PathValue("id")]
		if msgs == nil {
			msgs = []smokeEvent{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": msgs})
	})
	mux.HandleFunc("GET /v1/platform/versions", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.versions == nil {
			http.Error(w, "insufficient org role", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(g.versions)
	})
	mux.HandleFunc("GET /v1/my/agents", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"agents": g.agents})
	})
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.bearers = append(g.bearers, r.Header.Get("Authorization"))
		g.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeSmokeGateway) publish(room string, ev smokeEvent, persist bool) {
	g.mu.Lock()
	if persist {
		g.history[room] = append(g.history[room], ev)
	}
	subs := append([]chan smokeEvent(nil), g.subs[room]...)
	g.mu.Unlock()
	for _, ch := range subs {
		ch <- ev
	}
}

// testSession is an (unsigned) session bearer for subject; the fake gateway
// does not verify signatures, the CLI only reads the subject from it.
func testSession(subject string, extra map[string]any) string {
	claims := map[string]any{"sub": subject, "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range extra {
		claims[k] = v
	}
	raw, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func smokeTestOptions() smokeOptions {
	return smokeOptions{AgentTimeout: 2 * time.Second, StepTimeout: 2 * time.Second, Now: time.Now}
}

func runFakeSmoke(t *testing.T, g *fakeSmokeGateway, opts smokeOptions) (string, error) {
	t.Helper()
	isolateHome(t)
	c := newClient(config{Gateway: g.URL, Token: testSession("alice", nil)})
	var out bytes.Buffer
	err := runSmokeChecks(context.Background(), c, opts, &out)
	return out.String(), err
}

func assertLines(t *testing.T, out string, prefixes ...string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(prefixes) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(prefixes), out)
	}
	for i, p := range prefixes {
		if !strings.HasPrefix(lines[i], p) {
			t.Fatalf("line %d = %q, want prefix %q\n%s", i, lines[i], p, out)
		}
	}
}

func TestSmokePassesWithoutAgent(t *testing.T) {
	g := newFakeSmokeGateway(t)
	out, err := runFakeSmoke(t, g, smokeTestOptions())
	if err != nil {
		t.Fatalf("smoke failed: %v\n%s", err, out)
	}
	assertLines(t, out,
		"PASS auth (", "WARN versions: not checked", "PASS create room (", "PASS open stream (", "PASS chat echo (", "PASS history (",
		"SKIP agent reply (pass --agent)", "PASS delete room (")
	if !strings.Contains(out, "subject alice") {
		t.Fatalf("auth line should name the subject:\n%s", out)
	}
	if len(g.deleted) != 1 || len(g.rooms) != 0 {
		t.Fatalf("room not cleaned up: deleted=%v rooms=%v", g.deleted, g.rooms)
	}
	for _, b := range g.bearers {
		if !strings.HasPrefix(b, "Bearer ") {
			t.Fatalf("unauthenticated request: %q", b)
		}
	}
}

func TestSmokeAgentReply(t *testing.T) {
	g := newFakeSmokeGateway(t)
	g.agentReply = true
	g.agents = []myAgent{{Core: "acme", Provider: "codex", TargetID: "acme/local:codex", Online: true}}
	opts := smokeTestOptions()
	opts.Agent = "acme/local:codex"
	out, err := runFakeSmoke(t, g, opts)
	if err != nil {
		t.Fatalf("smoke failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "PASS agent reply (") || !strings.Contains(out, `alice/codex: "OK"`) {
		t.Fatalf("agent reply not reported:\n%s", out)
	}
	var rcpt smokeRecipient
	_ = json.Unmarshal(g.lastRcpt, &rcpt)
	if rcpt != (smokeRecipient{OwnerID: "alice", Provider: "codex", Core: "acme"}) {
		t.Fatalf("recipient = %+v", rcpt)
	}
}

func TestSmokeAgentJobFailureFails(t *testing.T) {
	g := newFakeSmokeGateway(t)
	g.agentReply, g.failJob = true, true
	opts := smokeTestOptions()
	opts.Agent = "alice/codex"
	out, err := runFakeSmoke(t, g, opts)
	if !errors.Is(err, errSmokeFailed) {
		t.Fatalf("err = %v, want errSmokeFailed\n%s", err, out)
	}
	if !strings.Contains(out, "FAIL agent reply: agent job failed (timeout)") || !strings.Contains(out, "PASS delete room") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestSmokeAgentTimeoutStillCleansUp(t *testing.T) {
	g := newFakeSmokeGateway(t)
	opts := smokeTestOptions()
	opts.Agent = "bob/claude"
	opts.AgentTimeout = 150 * time.Millisecond
	out, err := runFakeSmoke(t, g, opts)
	if !errors.Is(err, errSmokeFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "FAIL agent reply: no reply from bob/claude within 150ms") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if len(g.deleted) != 1 {
		t.Fatalf("room not deleted after failure: %v", g.deleted)
	}
}

func TestSmokeMissingEchoFailsAndCleansUp(t *testing.T) {
	g := newFakeSmokeGateway(t)
	g.dropEcho = true
	opts := smokeTestOptions()
	opts.StepTimeout = 200 * time.Millisecond
	out, err := runFakeSmoke(t, g, opts)
	if !errors.Is(err, errSmokeFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	assertLines(t, out, "PASS auth (", "WARN versions: not checked", "PASS create room (", "PASS open stream (", "FAIL chat echo: no chat_message echo within", "PASS delete room (")
	if len(g.deleted) != 1 {
		t.Fatalf("room not deleted after failure: %v", g.deleted)
	}
}

func TestSmokeKeepSkipsCleanup(t *testing.T) {
	g := newFakeSmokeGateway(t)
	opts := smokeTestOptions()
	opts.Keep = true
	out, err := runFakeSmoke(t, g, opts)
	if err != nil {
		t.Fatalf("smoke failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SKIP delete room (--keep): smoke-") || len(g.deleted) != 0 || len(g.rooms) != 1 {
		t.Fatalf("--keep did not keep the room: deleted=%v\n%s", g.deleted, out)
	}
}

func TestSmokeCleanupFailureFailsTheRun(t *testing.T) {
	g := newFakeSmokeGateway(t)
	// Delete the room behind the smoke's back: its cleanup gets a 404.
	g.deleteFirst = true
	out, err := runFakeSmoke(t, g, smokeTestOptions())
	if !errors.Is(err, errSmokeFailed) || !strings.Contains(out, "FAIL delete room:") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestSmokeNotSignedIn(t *testing.T) {
	g := newFakeSmokeGateway(t)
	isolateHome(t)
	var out bytes.Buffer
	err := runSmokeChecks(context.Background(), newClient(config{Gateway: g.URL}), smokeTestOptions(), &out)
	if !errors.Is(err, errSmokeFailed) || !strings.Contains(out.String(), "FAIL auth: not signed in") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if len(g.bearers) != 0 {
		t.Fatalf("no request should reach the gateway without a credential: %v", g.bearers)
	}
}

func TestSessionSubject(t *testing.T) {
	if s, err := sessionSubject(testSession("alice", nil)); err != nil || s != "alice" {
		t.Fatalf("subject = %q, %v", s, err)
	}
	if _, err := sessionSubject(testSession("alice", map[string]any{"kind": "refresh"})); err == nil {
		t.Fatal("a refresh token must not pass as a session")
	}
	if _, err := sessionSubject(testSession("alice", map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})); err == nil {
		t.Fatal("an expired session must be refused")
	}
	if _, err := sessionSubject("opaque"); err == nil {
		t.Fatal("a non-session token must be refused")
	}
}

func TestResolveSmokeAgent(t *testing.T) {
	mine := []myAgent{
		{Core: "acme", Provider: "codex", TargetID: "acme/local:codex", Online: true},
		{Core: "other", Provider: "codex", TargetID: "other/codex", Online: true},
		{Core: "acme", Provider: "claude", TargetID: "acme/local:claude", Online: false},
	}
	cases := []struct {
		spec    string
		want    smokeRecipient
		wantErr string
	}{
		{spec: "acme/local:codex", want: smokeRecipient{OwnerID: "me", Provider: "codex", Core: "acme"}},
		{spec: "other/codex", want: smokeRecipient{OwnerID: "me", Provider: "codex", Core: "other"}},
		{spec: "codex", wantErr: "matches several"},
		{spec: "claude", wantErr: "offline"},
		{spec: "bob/claude", want: smokeRecipient{OwnerID: "bob", Provider: "claude"}},
		{spec: "bob/acme/claude", want: smokeRecipient{OwnerID: "bob", Core: "acme", Provider: "claude"}},
		{spec: "gemini", wantErr: "not one of your agents"},
		{spec: "bob//claude", wantErr: "empty segment"},
	}
	for _, tc := range cases {
		got, err := resolveSmokeAgent(tc.spec, "me", mine)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want %q", tc.spec, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.spec, got, err, tc.want)
		}
	}
}

func TestReadSSEParsesNamedEventsAndSkipsComments(t *testing.T) {
	body := ": keep-alive\n\nid: 1\nevent: chat_message\ndata: {\"sequence\":1,\"kind\":\"chat_message\",\"payload\":{\"clientMsgId\":\"x\"}}\n\n: keep-alive\n\nid: 2\r\nevent: presence_join\r\ndata: {\"sequence\":2,\r\ndata: \"kind\":\"presence_join\",\"payload\":{}}\r\n\r\n"
	out := make(chan smokeEvent, 4)
	if err := readSSE(context.Background(), strings.NewReader(body), out); err != nil {
		t.Fatal(err)
	}
	close(out)
	var got []smokeEvent
	for ev := range out {
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].Kind != "chat_message" || payloadString(got[0].Payload, "clientMsgId") != "x" || got[1].Kind != "presence_join" || got[1].Sequence != 2 {
		t.Fatalf("events = %+v", got)
	}
}
