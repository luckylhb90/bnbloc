package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// activityGateway is a fake gateway whose agent-activity/messages endpoint is
// backed by the REAL activity store dedup (store.Memory.AppendMessages): rows
// at or below a session's cursor are dropped as replays. failPosts makes the
// first N message posts answer 503.
type activityGateway struct {
	srv       *httptest.Server
	mem       *store.Memory
	failPosts atomic.Int64
	mu        sync.Mutex
	posts     []wire.ReportMessagesInput
	fresh     []wire.Message
}

func newActivityGateway(t *testing.T, room string) *activityGateway {
	t.Helper()
	g := &activityGateway{mem: store.NewMemory()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/rooms/"+room+"/channels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"channels":[]}`))
	})
	mux.HandleFunc("GET /v1/rooms/"+room+"/history", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	mux.HandleFunc("POST /v1/rooms/"+room+"/agent-activity/session-state", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /v1/rooms/"+room+"/agent-activity/messages", func(w http.ResponseWriter, r *http.Request) {
		if g.failPosts.Load() > 0 {
			g.failPosts.Add(-1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var in wire.ReportMessagesInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fresh, _, err := g.mem.AppendMessages(tenant.NewContext(r.Context(), "t1"), room, in)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		g.mu.Lock()
		g.posts = append(g.posts, in)
		g.fresh = append(g.fresh, fresh...)
		g.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *activityGateway) stored() ([]wire.ReportMessagesInput, []wire.Message) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]wire.ReportMessagesInput(nil), g.posts...), append([]wire.Message(nil), g.fresh...)
}

func storedTexts(msgs []wire.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, payloadText(m))
	}
	return out
}

func payloadText(m wire.Message) string {
	p, _ := m.Payload.(map[string]any)
	t, _ := p["text"].(string)
	return t
}

// turnScriptProv scripts a session across turns: turn 1 fails with no reply,
// turn 2 answers. Each turn reports busy on its first poll, then idle.
type turnScriptProv struct {
	recordingProv
	mu    sync.Mutex
	turn  int
	polls int
}

func (p *turnScriptProv) CreateSession(context.Context, corepkg.Endpoint, corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turn, p.polls = 1, 0
	return corepkg.SessionResult{SessionID: "s"}, nil
}

func (p *turnScriptProv) SendInput(context.Context, corepkg.Endpoint, string, string, string, corepkg.SessionOptions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turn++
	p.polls = 0
	return nil
}

func (p *turnScriptProv) PollTurn(context.Context, corepkg.Endpoint, string, string) (corepkg.TurnStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	if p.polls == 1 {
		return corepkg.TurnStatus{Idle: false}, nil
	}
	if p.turn == 1 {
		return corepkg.TurnStatus{Idle: true, Failed: true, FailMessage: "missing API key"}, nil
	}
	return corepkg.TurnStatus{Idle: true}, nil
}

func (p *turnScriptProv) Messages(_ context.Context, _ corepkg.Endpoint, _, _ string, after int64) ([]corepkg.Message, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turn < 2 || after >= 2 {
		return nil, after, nil
	}
	return []corepkg.Message{{Version: 2, Seq: 2, Role: "assistant", Text: "the answer", UserVisible: true, Completed: true, MessageID: "m2", CreatedAtMs: 1}}, 2, nil
}

// BR-1: a failed turn's notice must not poison the real session's activity
// cursor. Before the fix the notice was posted under the session's
// agentSessionId with seq = UnixMilli, so the activity cursor jumped to ~1.8e12
// and the next real reply (seq 2) was dropped as a replay.
func TestReplyAfterFailedTurnIsStillStored(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	prov := &turnScriptProv{}
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	ctx := context.Background()

	if _, err := b.driveClaim(ctx, jobRef{ID: "job-1"}, "room-1", "alice", "core-a", "codex", "t", "first", nil, corepkg.SessionOptions{}, nil, "ch", nil); err == nil {
		t.Fatal("turn 1 must report the failed turn")
	}
	res, err := b.driveClaim(ctx, jobRef{ID: "job-2"}, "room-1", "alice", "core-a", "codex", "t", "second", nil, corepkg.SessionOptions{}, nil, "ch", nil)
	if err != nil || !res.replied {
		t.Fatalf("turn 2 = %+v, %v; want a posted reply", res, err)
	}

	posts, fresh := g.stored()
	texts := storedTexts(fresh)
	if len(texts) != 2 || !strings.Contains(texts[0], "missing API key") || texts[1] != "the answer" {
		t.Fatalf("stored rows = %q; want the turn-failed notice AND the later real reply", texts)
	}
	for _, p := range posts {
		if p.AgentSessionID == "" {
			t.Fatalf("activity post without agentSessionId: %+v", p)
		}
		isNotice := strings.Contains(payloadText(p.Messages[0]), "could not complete")
		if isNotice && p.AgentSessionID == "s" {
			t.Fatal("notice posted under the real agent session's cursor")
		}
	}
}

// BR-1: borrow-denied and drive-failed notices always carry their own
// (per-job) agentSessionId and a channel.
func TestNoticesCarryPerJobAgentSession(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	ctx := context.Background()
	b.postBorrowDenied(ctx, "job-7", "room-1", "alice", "codex", "ch", "owner_not_authorized")
	b.postDriveFailed(ctx, "job-8", "room-1", "alice", "codex", "daemon down", "ch")
	b.postBorrowDenied(ctx, "job-7", "room-1", "alice", "codex", "ch", "owner_not_authorized") // retry: idempotent

	posts, fresh := g.stored()
	if len(posts) != 3 || len(fresh) != 2 {
		t.Fatalf("posts=%d fresh=%d; want 3 posts, 2 stored (retry deduped)", len(posts), len(fresh))
	}
	if posts[0].AgentSessionID != "job-job-7" || posts[1].AgentSessionID != "job-job-8" || posts[0].ChannelID != "ch" {
		t.Fatalf("notice sessions = %q/%q channel %q", posts[0].AgentSessionID, posts[1].AgentSessionID, posts[0].ChannelID)
	}
}

// BR-11: a reply whose post fails transiently is re-posted on the next poll
// instead of being lost behind an advanced cursor.
func TestReplyPostFailureIsRetried(t *testing.T) {
	g := newActivityGateway(t, "room-1")
	g.failPosts.Store(2)
	prov := &jobProv{recordingProv: recordingProv{targets: []corepkg.Target{{ID: "c", Provider: "codex"}}},
		messages: []corepkg.Message{{Version: 1, Seq: 1, Role: "assistant", Text: "done", UserVisible: true, Completed: true, MessageID: "m1"}}}
	prov.pollsBusy = 3 // stay busy long enough for the retries to happen mid-turn
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})

	res, err := b.driveClaim(context.Background(), jobRef{ID: "job-1"}, "room-1", "alice", "core-a", "codex", "c", "go", nil, corepkg.SessionOptions{}, nil, "ch", nil)
	if err != nil || !res.replied {
		t.Fatalf("drive = %+v, %v; want the reply delivered after retries", res, err)
	}
	if _, fresh := g.stored(); len(fresh) != 1 || storedTexts(fresh)[0] != "done" {
		t.Fatalf("stored = %q; want the reply exactly once", storedTexts(fresh))
	}
}

// BR-12: a job naming a core this bridge does not run fails as
// agent_not_advertised instead of being driven on the default core.
func TestJobForUnknownCoreIsNotDrivenOnDefault(t *testing.T) {
	var status, reason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			var body struct {
				Status        string `json:"status"`
				FailureReason string `json:"failureReason"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			status, reason = body.Status, body.FailureReason
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	var creates int64
	prov := &noDriveProv{createCalls: &creates}
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)})
	b.runBridgeJob(context.Background(), bridgeJob{JobID: "j", RoomID: "r", OwnerUserID: "alice", Core: "other-core", Provider: "codex", MessageText: "hi"})
	if status != string(store.AgentJobFailed) || reason != string(store.FailureAgentNotAdvertised) {
		t.Fatalf("status = %s/%s; want failed/agent_not_advertised", status, reason)
	}
	if atomic.LoadInt64(&creates) != 0 {
		t.Fatal("job for an unknown core was driven on the default core")
	}
}

// BR-10: live sync finds the room's session although sessions are keyed per
// (room, core, provider).
func TestSyncPlanUsesRoomSessionWorkspace(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"syncMode":"mutagen"}`))
	}))
	defer gw.Close()
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "a",
		Cores: []CoreConfig{{Name: "core-a", Provider: &workdirProv{dirs: map[string]string{"w1/s1": "/proj/one", "w2/s2": "/proj/two"}}, Endpoint: corepkg.Endpoint{BaseURL: "http://core-a"}}}})
	older := &roomSession{core: "core-a", workspaceID: "w1", sessionID: "s1"}
	older.markTurn(deliverableTurn{StartMs: 100})
	newer := &roomSession{core: "core-a", workspaceID: "w2", sessionID: "s2"}
	newer.markTurn(deliverableTurn{StartMs: 200})
	b.sessions[sessionKey("room1", "core-a", "codex")] = older
	b.sessions[sessionKey("room1", "core-a", "claude")] = newer
	b.sessions[sessionKey("room10", "core-a", "codex")] = &roomSession{core: "core-a", workspaceID: "wx", sessionID: "sx"}

	mode, dir, _ := b.syncPlan(context.Background(), "room1")
	if mode != "mutagen" || dir != "/proj/two" {
		t.Fatalf("syncPlan = %q %q; want mutagen /proj/two (the room's latest session)", mode, dir)
	}
}

// BR-14: the post-turn publish goroutine and the next turn's periodic publish
// may run on the same session at once; they must not race on lastPublished
// (run with -race).
func TestConcurrentPublishDeliverablesIsSerialized(t *testing.T) {
	fc := newFakeCore()
	fc.set("a.md", "x")
	b := New(Config{GatewayBaseURL: "http://gw", OwnerSubject: "a", Cores: newSessionFilesCore(fc)})
	rs := &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.publishDeliverables(context.Background(), "r", "ch", rs, deliverableTurn{StartMs: 1 << 50}) // cold, pre-turn file: snapshot only
		}()
	}
	wg.Wait()
	if rs.lastPublished == nil {
		t.Fatal("publish did not record the file-listing snapshot")
	}
}

// lateProv is a discovery-capable core whose daemon is absent at boot. Its
// Discover also reports whether the bridge held b.mu during discovery.
type lateProv struct {
	recordingProv
	mu       sync.Mutex
	ep       corepkg.Endpoint
	b        *Bridge
	heldLock bool
	lists    int
}

func (p *lateProv) Discover(context.Context) (corepkg.Endpoint, bool) {
	if p.b != nil {
		if p.b.mu.TryLock() {
			p.b.mu.Unlock()
		} else {
			p.heldLock = true
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ep, p.ep.BaseURL != ""
}

func (p *lateProv) ListTargets(context.Context, corepkg.Endpoint) ([]corepkg.Target, error) {
	p.mu.Lock()
	p.lists++
	p.mu.Unlock()
	return []corepkg.Target{{ID: "c", Provider: "codex"}}, nil
}

// BR-4/BR-17: a core with no endpoint at boot stays configured and is resolved
// when its daemon comes up; discovery runs without b.mu held; one roster tick
// plus a heartbeat list targets once, not twice.
func TestCoreResolvedAfterBootWithoutHoldingBridgeLock(t *testing.T) {
	hb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer hb.Close()
	prov := &lateProv{}
	b := New(Config{GatewayBaseURL: hb.URL, GatewayToken: "t", OwnerSubject: "a",
		Cores: []CoreConfig{{Name: "core-a", Provider: prov}}})
	prov.b = b
	ctx := context.Background()

	b.refreshRoster(ctx)
	if got := b.advertiseParam(); got != "" {
		t.Fatalf("roster before the daemon is up = %q, want empty", got)
	}
	if _, ok := b.resolveCore("core-a"); !ok {
		t.Fatal("a core without an endpoint must stay configured")
	}

	prov.mu.Lock()
	prov.ep = corepkg.Endpoint{BaseURL: "http://127.0.0.1:4444"}
	prov.mu.Unlock()
	b.refreshRoster(ctx)
	if ep := b.cores["core-a"].endpoint; ep.BaseURL != "http://127.0.0.1:4444" {
		t.Fatalf("endpoint = %+v, want the late daemon", ep)
	}
	if got := b.advertiseParam(); got != "core-a/codex" {
		t.Fatalf("roster after the daemon came up = %q", got)
	}
	if err := b.reportHeartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if prov.lists != 1 {
		t.Fatalf("ListTargets calls = %d; want 1 (roster tick feeds the heartbeat)", prov.lists)
	}
	if prov.heldLock {
		t.Fatal("refreshRoster held b.mu across Discover")
	}
}

// Follow-up to BE-8 / R8: shareagent accepts a borrow reservation (and its
// settle) only from the agent's owner. A serve-all bridge driving another
// member's agent uses the read-only authorization check and reserves nothing;
// a bridge acting as the owner reserves.
func TestBorrowReservedOnlyAsOwner(t *testing.T) {
	var reserves, reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/authorization/reserve"):
			reserves.Add(1)
			_, _ = w.Write([]byte(`{"reserved":true,"reservationId":"rsv_1"}`))
		case strings.HasSuffix(r.URL.Path, "/authorization"):
			reads.Add(1)
			_, _ = w.Write([]byte(`{"authorized":true}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", ServeAllOwners: true})
	ctx := context.Background()
	job := bridgeJob{RoomID: "r", RequesterUserID: "carol", Provider: "codex", ChannelID: "ch"}
	if rsv := b.authorizeBorrow(ctx, job, "bob", "core-a", false); rsv.ID != "" || rsv.DenyReason != "" || reserves.Load() != 0 || reads.Load() != 1 {
		t.Fatalf("serve-all borrow of bob's agent: %+v reserves=%d reads=%d; want read-only allow", rsv, reserves.Load(), reads.Load())
	}
	if rsv := b.authorizeBorrow(ctx, job, "alice", "core-a", false); rsv.ID != "rsv_1" || rsv.DenyReason != "" || reserves.Load() != 1 {
		t.Fatalf("owner borrow: %+v reserves=%d; want a reservation", rsv, reserves.Load())
	}
}

// R8: a refused reservation carries the authority's reason (403 body), and an
// authority that cannot be asked fails closed.
func TestReserveBorrowDenyReasons(t *testing.T) {
	status := http.StatusForbidden
	body := `{"reserved":false,"denyReason":"quota_exhausted"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	ctx := context.Background()
	if rsv := b.reserveBorrow(ctx, "r", "alice", "core-a", "codex", "bob", "", "ch", false); rsv.ID != "" || rsv.DenyReason != "quota_exhausted" {
		t.Fatalf("%+v; want quota_exhausted", rsv)
	}
	status, body = http.StatusBadGateway, `upstream down`
	if rsv := b.reserveBorrow(ctx, "r", "alice", "core-a", "codex", "bob", "", "ch", false); rsv.ID != "" || rsv.DenyReason != borrowDenyUnavailable {
		t.Fatalf("%+v; want %s", rsv, borrowDenyUnavailable)
	}
	if got := borrowDeniedText("alice", "codex", "quota_exhausted"); !strings.Contains(got, "Ask alice for more runs") {
		t.Fatalf("quota notice = %q", got)
	}
	if got := borrowDeniedText("root", "codex", "platform_agent_unavailable"); !strings.Contains(got, "no longer offered as a platform agent") {
		t.Fatalf("platform notice = %q", got)
	}
}

// A reservation answering sandboxRequired for a drive the bridge does not run
// sandboxed is refused as sandbox_required (and settled at once, empty),
// even when the authority reserved it.
func TestReserveBorrowHonoursSandboxRequired(t *testing.T) {
	var settles atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/authorization/reserve"):
			_, _ = w.Write([]byte(`{"reserved":true,"reservationId":"rsv_9","sandboxRequired":true}`))
		case strings.HasSuffix(r.URL.Path, "/authorization/record"):
			settles.Add(1)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	ctx := context.Background()
	if rsv := b.reserveBorrow(ctx, "r", "alice", "core-a", "codex", "bob", "", "ch", false); rsv.ID != "" || rsv.DenyReason != "sandbox_required" {
		t.Fatalf("unsandboxed: %+v; want sandbox_required", rsv)
	}
	deadline := time.Now().Add(2 * time.Second)
	for settles.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if settles.Load() != 1 {
		t.Fatal("the refused reservation was not settled")
	}
	if rsv := b.reserveBorrow(ctx, "r", "alice", "core-a", "codex", "bob", "", "ch", true); rsv.ID != "rsv_9" || rsv.DenyReason != "" {
		t.Fatalf("sandboxed: %+v; want the reservation", rsv)
	}
}

// Room discovery asks for agent-host rooms; the bridge serves them (streams)
// but never live-syncs its owner's workspace into a room it only hosts a
// platform agent in.
func TestDiscoveryServesAgentHostRoomsWithoutSync(t *testing.T) {
	var query atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/users/"):
			query.Store(r.URL.RawQuery)
			_, _ = w.Write([]byte(`{"rooms":["own","hosted"],"agentHostRooms":["hosted"]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "root", RelayBaseURL: "http://127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.reconcile(ctx)
	if q, _ := query.Load().(string); !strings.Contains(q, "allTenants=1") || !strings.Contains(q, "includeAgentHost=1") {
		t.Fatalf("discovery query = %q", q)
	}
	b.mu.Lock()
	_, streamsHosted := b.rooms["hosted"]
	_, syncsHosted := b.syncs["hosted"]
	_, syncsOwn := b.syncs["own"]
	b.mu.Unlock()
	if !streamsHosted || syncsHosted || !syncsOwn {
		t.Fatalf("hosted streamed=%v synced=%v; own synced=%v", streamsHosted, syncsHosted, syncsOwn)
	}
}
