package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// pullGateway serves everything the pull-context flow fetches: channels,
// per-channel history, artifacts, and the activity ingest endpoints the drive
// path posts to.
type pullGateway struct {
	mu        sync.Mutex
	history   map[string]string // channel -> rows json (pre-rendered pages)
	artifacts string
	srv       *httptest.Server
}

func newPullGateway(t *testing.T) *pullGateway {
	t.Helper()
	g := &pullGateway{history: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/rooms/r1/channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"channels":[{"channelId":"general"},{"channelId":"test"}]}`)
	})
	mux.HandleFunc("GET /v1/rooms/r1/history", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		// Mirrors the realtime service: an omitted channelId resolves to the
		// room's default channel.
		ch := r.URL.Query().Get("channelId")
		if ch == "" {
			ch = "general"
		}
		body, ok := g.history[ch]
		if !ok {
			body = `{"messages":[]}`
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("GET /v1/rooms/r1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		fmt.Fprint(w, g.artifacts)
	})
	mux.HandleFunc("POST /v1/rooms/r1/agent-activity/session-state", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *pullGateway) setHistory(ch string, seqs []int64, texts map[int64]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Mirror the real API: newest `limit` below the cursor, ASCENDING order.
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i, seq := range seqs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"sequence":%d,"kind":"chat_message","payload":{"userId":"jack","text":%q}}`, seq, texts[seq])
	}
	b.WriteString(`]}`)
	g.history[ch] = b.String()
}

// addChatRow appends a chat_message row (a person's line) to the channel's
// page. Rows must be added in ascending seq order.
func (g *pullGateway) addChatRow(ch string, seq int64, user, text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	body := g.history[ch]
	if body == "" {
		body = `{"messages":[]}`
	}
	trimmed := strings.TrimSuffix(body, "]}")
	if !strings.HasSuffix(trimmed, "[") {
		trimmed += ","
	}
	body = trimmed + fmt.Sprintf(`{"sequence":%d,"kind":"chat_message","payload":{"userId":%q,"text":%q}}`, seq, user, text) + `]}`
	g.history[ch] = body
}

// addAgentRow appends an agent_messages row (speaker owner/provider) to the
// channel's page — the shape rowsToEntries produces agent replies from.
// Rows must be added in ascending seq order.
func (g *pullGateway) addAgentRow(ch string, seq int64, owner, provider, text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	body := g.history[ch]
	if body == "" {
		body = `{"messages":[]}`
	}
	// Trim the closing "]}"; if the array is empty ("[" + "]}") keep the "[".
	trimmed := strings.TrimSuffix(body, "]}")
	if !strings.HasSuffix(trimmed, "[") {
		trimmed += ","
	}
	body = trimmed + fmt.Sprintf(`{"sequence":%d,"kind":"agent_messages","payload":{"messages":[{"payload":{"role":"assistant","text":%q,"ownerUserId":%q,"provider":%q}}]}}`, seq, text, owner, provider) + `]}`
	g.history[ch] = body
}

// A fresh session under a pull-capable core gets a POINTER prompt: no history
// inline, .room/ materialized into the workspace first, INDEX written last.
func TestDriveClaimPullWritesRoomFilesAndPointerPrompt(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{1, 3}, map[int64]string{1: "first in general", 3: "third in general"})
	g.setHistory("test", []int64{2, 4}, map[int64]string{2: "first in test", 4: "second in test"})
	g.mu.Lock()
	g.artifacts = `{"artifacts":[{"title":"Spec","kind":"markdown","currentVersion":2,"createdBy":"maya"}]}`
	g.mu.Unlock()

	prov := &fakeCoreProvider{url: g.srv.URL}
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})

	_, err := b.driveClaim(context.Background(), jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "write hello.rs", nil, corepkg.SessionOptions{}, nil, "ch", nil)
	if err != nil {
		t.Fatalf("driveClaim: %v", err)
	}

	writes := prov.recordedWrites()
	if len(writes) == 0 {
		t.Fatal("no .room files written")
	}
	if writes[len(writes)-1] != ".room/INDEX.md" {
		t.Fatalf("INDEX must be written last, got order %v", writes)
	}
	var sawChunk, sawRecent, sawArtifacts bool
	for _, w := range writes {
		switch {
		case strings.HasPrefix(w, ".room/chunk-"):
			sawChunk = true
		case w == ".room/recent.md":
			sawRecent = true
		case w == ".room/artifacts.md":
			sawArtifacts = true
		}
	}
	if !sawChunk || !sawRecent || !sawArtifacts {
		t.Fatalf("missing .room files: %v", writes)
	}

	prompts := prov.recordedPrompts()
	if len(prompts) == 0 {
		t.Fatal("no session prompt recorded")
	}
	prompt := prompts[0]
	if !strings.Contains(prompt, ".room/INDEX.md") || !strings.Contains(prompt, "write hello.rs") {
		t.Fatalf("pointer prompt malformed:\n%s", prompt)
	}
	// The whole point: history must NOT be in the prompt.
	for _, leaked := range []string{"first in general", "second in test", "story so far"} {
		if strings.Contains(prompt, leaked) {
			t.Fatalf("history leaked into pointer prompt (%q):\n%s", leaked, prompt)
		}
	}
}

// A follow-up turn on the same session refreshes .room/ (delta chunks) and
// still drives with a pointer prompt.
func TestDriveClaimPullDeltaOnReuse(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{1}, map[int64]string{1: "old message"})
	g.setHistory("test", []int64{}, nil)
	prov := &fakeCoreProvider{url: g.srv.URL}
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	if _, err := b.driveClaim(context.Background(), jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "turn one", nil, corepkg.SessionOptions{}, nil, "ch", nil); err != nil {
		t.Fatal(err)
	}
	firstWrites := len(prov.recordedWrites())

	// New messages arrive before turn two.
	g.setHistory("general", []int64{1, 5}, map[int64]string{1: "old message", 5: "new message"})
	if _, err := b.driveClaim(context.Background(), jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "turn two", nil, corepkg.SessionOptions{}, nil, "ch", nil); err != nil {
		t.Fatal(err)
	}

	all := prov.recordedWrites()
	if len(all) <= firstWrites {
		t.Fatalf("reuse wrote nothing new: %v", all)
	}
	// The delta must land in a chunk covering seq 5.
	var sawDelta bool
	for _, w := range all[firstWrites:] {
		if strings.Contains(w, "-0000000005") {
			sawDelta = true
		}
	}
	if !sawDelta {
		t.Fatalf("no chunk covering the new seq 5: %v", all)
	}
	// Turn two's prompt is a pointer again.
	prompts := prov.recordedPrompts()
	last := prompts[len(prompts)-1]
	if strings.Contains(last, "new message") {
		t.Fatalf("history leaked into reuse prompt:\n%s", last)
	}
}

// When the writer fails, the drive falls back to the pushed context so the
// turn still carries the room's history.
func TestDriveClaimPullFallsBackOnWriterFailure(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{1}, map[int64]string{1: "fallback marker message"})
	g.setHistory("test", []int64{}, nil)
	prov := &fakeCoreProvider{url: g.srv.URL}
	prov.failWrites = true
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	if _, err := b.driveClaim(context.Background(), jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "still drive", nil, corepkg.SessionOptions{}, nil, "ch", nil); err != nil {
		t.Fatal(err)
	}
	prompt := prov.recordedPrompts()[0]
	if !strings.Contains(prompt, "fallback marker message") {
		t.Fatalf("writer failure must fall back to pushed history:\n%s", prompt)
	}
}

// The current instruction must not appear in the materialized records: it is
// delivered as the [now] section of the prompt, and a copy in recent.md would
// both duplicate it and re-inject it as history on later turns.
func TestMaterializeDropsCurrentInstructionFromRecords(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{10, 11}, map[int64]string{
		10: "older unrelated message",
		11: "create final_check.rs: print done",
	})
	g.setHistory("test", []int64{}, nil)
	prov := &fakeCoreProvider{url: g.srv.URL}
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	w, ep, ok := b.pullWriter(context.Background(), "core-a")
	if !ok {
		t.Fatal("fake provider must satisfy WorkspaceFileWriter")
	}
	_, err := b.materializeRoomContext(context.Background(), w, ep, "r1", "ws", "create final_check.rs: print done", "maya/agent-a", true, roomlog.CtxState{})
	if err != nil {
		t.Fatal(err)
	}
	// The written recent.md must contain the older message but not the instruction.
	var recent string
	for _, wr := range prov.roomFileWrites() {
		if strings.HasSuffix(wr.path, "recent.md") {
			recent = wr.content
		}
	}
	if recent == "" {
		t.Fatal("recent.md not written")
	}
	if strings.Contains(recent, "final_check") {
		t.Fatalf("current instruction leaked into records:\n%s", recent)
	}
	if !strings.Contains(recent, "older unrelated message") {
		t.Fatalf("genuine history must remain:\n%s", recent)
	}
}

// Self-reply filtering is bounded by the session-creation boundary. A FRESH
// session keeps its own prior replies — a rebind starts a blank CLI (bridge
// restarts rebind every session, and the bridge's session table is
// in-memory), so without them the agent forgets its own work while
// remembering everyone else's. A follow-up pass drops only the replies THIS
// session produced (its CLI memory holds them); other speakers always stay.
func TestMaterializeSelfFilterIsSessionBounded(t *testing.T) {
	newBridge := func(g *pullGateway) (*Bridge, corepkg.WorkspaceFileWriter, corepkg.Endpoint, *fakeCoreProvider) {
		prov := &fakeCoreProvider{url: g.srv.URL}
		b := New(Config{
			GatewayBaseURL: g.srv.URL,
			GatewayToken:   "t",
			OwnerSubject:   "maya",
			Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
		})
		w, ep, ok := b.pullWriter(context.Background(), "core-a")
		if !ok {
			t.Fatal("fake provider must satisfy WorkspaceFileWriter")
		}
		return b, w, ep, prov
	}
	recentOf := func(prov *fakeCoreProvider) string {
		var recent string
		for _, wr := range prov.roomFileWrites() {
			if strings.HasSuffix(wr.path, "recent.md") {
				recent = wr.content
			}
		}
		return recent
	}

	// Fresh session: its own prior replies STAY (continuity across a rebind).
	g := newPullGateway(t)
	g.setHistory("general", []int64{10}, map[int64]string{10: "person asks"})
	g.addAgentRow("general", 11, "maya", "agent-a", "my earlier-session reply")
	g.setHistory("test", []int64{}, nil)
	g.addAgentRow("test", 12, "maya", "opencode", "peer agent answers")
	b, w, ep, prov := newBridge(g)
	st, err := b.materializeRoomContext(context.Background(), w, ep, "r1", "ws", "", "maya/agent-a", true, roomlog.CtxState{})
	if err != nil {
		t.Fatal(err)
	}
	recent := recentOf(prov)
	if !strings.Contains(recent, "my earlier-session reply") {
		t.Fatalf("a fresh (rebound) session must see its own prior replies:\n%s", recent)
	}
	if !strings.Contains(recent, "person asks") || !strings.Contains(recent, "peer agent answers") {
		t.Fatalf("others must always materialize:\n%s", recent)
	}
	if st.SelfAfterSeq == 0 {
		t.Fatalf("fresh pass must record the session boundary, got %+v", st)
	}

	// Follow-up: only THIS session's own replies drop; prior ones stay.
	g2 := newPullGateway(t)
	g2.setHistory("general", []int64{10}, map[int64]string{10: "person asks"})
	g2.addAgentRow("general", 11, "maya", "agent-a", "my earlier-session reply")
	g2.setHistory("test", []int64{}, nil)
	g2.addAgentRow("test", 12, "maya", "opencode", "peer agent answers")
	// New since the bound: the session's own reply and a new person line.
	g2.addAgentRow("general", 13, "maya", "agent-a", "my this-session reply")
	g2.addChatRow("test", 14, "jack", "new person line")
	b2, w2, ep2, prov2 := newBridge(g2)
	st2 := roomlog.CtxState{EndSeq: 12, Chunks: []string{".room/chunk-x.md"}, SelfAfterSeq: 12}
	if _, err := b2.materializeRoomContext(context.Background(), w2, ep2, "r1", "ws", "", "maya/agent-a", false, st2); err != nil {
		t.Fatal(err)
	}
	recent2 := recentOf(prov2)
	if strings.Contains(recent2, "my this-session reply") {
		t.Fatalf("this session's own reply is session memory; records must drop it:\n%s", recent2)
	}
	if !strings.Contains(recent2, "new person line") {
		t.Fatalf("the delta's other messages must stay:\n%s", recent2)
	}
	if !strings.Contains(recent2, "my earlier-session reply") {
		t.Fatalf("prior-session self replies must stay across passes:\n%s", recent2)
	}
}

// driveTextDelta is the push model: same semantics as pull, inline delivery.
// Fresh turn = full cross-channel window; follow-up = delta since the last
// push; nothing new = bare instruction. Self replies never re-inject (the
// session memory already holds them), the current instruction never appears in
// the records (it is the [now] section).
func TestDriveTextDeltaFreshWindow(t *testing.T) {
	g := newPullGateway(t)
	// general: a person's line, then SELF's reply. test: the current
	// instruction (chat), then a peer agent's reply.
	g.setHistory("general", []int64{10}, map[int64]string{10: "person asks"})
	g.addAgentRow("general", 11, "maya", "agent-a", "my own earlier reply")
	g.setHistory("test", []int64{12}, map[int64]string{12: "do the thing"})
	g.addAgentRow("test", 13, "maya", "opencode", "peer agent answers")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya"})

	st := roomlog.CtxState{}
	prompt := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", "do the thing", nil, &st, true)

	if !strings.Contains(prompt, "person asks") {
		t.Fatalf("person's message (general channel) must inject:\n%s", prompt)
	}
	if !strings.Contains(prompt, "peer agent answers") {
		t.Fatalf("peer agent's reply (test channel) must inject — push must read ALL channels:\n%s", prompt)
	}
	// A FRESH session keeps its own prior replies: a rebind starts a blank
	// CLI, so they are continuity, not redundancy.
	if !strings.Contains(prompt, "my own earlier reply") {
		t.Fatalf("fresh session must see its own prior replies (blank CLI memory holds none):\n%s", prompt)
	}
	if strings.Count(prompt, "do the thing") != 1 {
		t.Fatalf("instruction must appear exactly once as [now], never in records:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[now — respond to this]\ndo the thing") {
		t.Fatalf("instruction must directly follow the now marker:\n%s", prompt)
	}
	if st.PushSeenSeq != 13 {
		t.Fatalf("PushSeenSeq = %d, want 13 (newest fetched row, filtered rows included)", st.PushSeenSeq)
	}
}

func TestDriveTextDeltaFollowUpOnlyDelta(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{10}, map[int64]string{10: "person asks"})
	g.addAgentRow("general", 11, "maya", "agent-a", "my own earlier reply")
	g.setHistory("test", []int64{12}, map[int64]string{12: "do the thing"})
	g.addAgentRow("test", 13, "maya", "opencode", "peer agent answers")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya"})

	st := roomlog.CtxState{}
	first := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", "do the thing", nil, &st, true)
	_ = first

	// New activity since the last push: a person question, a peer reply, and
	// THIS session's own reply (posted as the first turn drained).
	g.setHistory("general", []int64{20}, map[int64]string{20: "new question"})
	g.addAgentRow("test", 21, "maya", "opencode", "peer follow-up")
	g.addAgentRow("general", 22, "maya", "agent-a", "my own this-session reply")
	second := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", "next instruction", nil, &st, false)

	if !strings.Contains(second, "New room messages since your last turn") {
		t.Fatalf("follow-up must be framed as a delta:\n%s", second)
	}
	if !strings.Contains(second, "new question") || !strings.Contains(second, "peer follow-up") {
		t.Fatalf("delta must carry the new messages:\n%s", second)
	}
	if strings.Contains(second, "my own this-session reply") {
		t.Fatalf("this session's own reply is session memory; the delta must drop it:\n%s", second)
	}
	if strings.Contains(second, "person asks") {
		t.Fatalf("already-seen messages must not re-inject — that repetition is the push model's old cost:\n%s", second)
	}
	if st.PushSeenSeq != 22 {
		t.Fatalf("PushSeenSeq = %d, want 22 (self replies count as seen — the session authored them)", st.PushSeenSeq)
	}

	// Nothing new: the bare instruction, no framing at all.
	third := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", "nothing new at all", nil, &st, false)
	if third != "nothing new at all" {
		t.Fatalf("empty delta must send the bare instruction, got:\n%s", third)
	}
}
