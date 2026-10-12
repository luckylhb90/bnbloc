package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// runsFixture: in room-1 (alice, bob) bob borrowed alice's codex (replied,
// reported through the bridge status route with a reply + usage summary) and
// alice's own run failed; in room-2 (carol) carol's run is still queued.
func runsFixture(t *testing.T) *Service {
	t.Helper()
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	jobs := store.NewMemoryBridgeJobs()
	rooms := store.NewMemoryRoom()
	for _, r := range []store.Room{{RoomID: "room-1", Name: "Design", CreatedBy: "alice", CreatedAtMs: 1}, {RoomID: "room-2", Name: "Ops", CreatedBy: "carol", CreatedAtMs: 1}} {
		if err := rooms.CreateRoom(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	mem.Add("room-1", "bob")
	mem.Add("room-2", "carol")
	chat := store.NewMemoryChat()
	// The feed: bob's request at seq 10, the reply batch at seq 11.
	batch, _ := json.Marshal(wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{{Seq: 1, Payload: map[string]any{"messageId": "m-reply", "text": "done"}}}})
	for _, m := range []store.RoomMessage{
		{RoomID: "room-1", Seq: 10, Kind: "chat_message", Payload: json.RawMessage(`{"text":"please fix"}`)},
		{RoomID: "room-1", Seq: 11, Kind: "agent_messages", Payload: batch},
	} {
		if err := chat.AppendRoomMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	put := func(id, room, owner, requester string, seq, created int64) {
		j := store.AgentJob{JobID: id, RoomID: room, MessageSeq: seq, RequesterUserID: requester,
			OwnerUserID: owner, Core: "c1", Provider: "codex", MessageText: "please fix " + strings.Repeat("x", 400), Model: "m-large", CreatedAtMs: created}
		if id == "j-borrow" {
			j.ExpertID, j.ExpertName, j.ExpertVersion = "cc_rev", "Reviewer", "1.2.0"
			j.RoomExpertID, j.RoomExpertName, j.RoomExpertVersion = "cc_house", "House style", "2.0.0"
		}
		if _, err := jobs.PutAgentJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	put("j-borrow", "room-1", "alice", "bob", 10, 1_000)
	put("j-failed", "room-1", "alice", "alice", 12, 2_000)
	put("j-queued", "room-2", "carol", "carol", 3, 3_000)
	svc := &Service{Store: store.NewMemory(), Publisher: &capturePublisher{}, Membership: mem, BridgeJobs: jobs, Rooms: rooms, Runs: jobs, Feed: chat}

	// alice's bridge drives both of her jobs through the real worker routes.
	post := func(path, body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("X-Bnbloc-User", "alice")
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s = %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/v1/bridge/heartbeat", `{"bridgeId":"b-alice","agents":[{"core":"c1","provider":"codex"}]}`)
	post("/v1/bridge/jobs/claim", `{"bridgeId":"b-alice","maxJobs":5}`)
	// The borrowed run's first attempt finds the core down and is retried
	// once its retry delay has passed (claimed past it, through the store).
	post("/v1/bridge/jobs/j-borrow/status", `{"bridgeId":"b-alice","status":"failed","failureReason":"core_unavailable","diagnostics":{"error":"dial tcp: connection refused"}}`)
	requeued, err := jobs.GetAgentRun(ctx, "j-borrow")
	if err != nil || requeued.NotBeforeMs == 0 {
		t.Fatalf("requeued = %+v, %v", requeued, err)
	}
	if got, err := jobs.ClaimAgentJobs(ctx, store.ClaimRequest{BridgeID: "b-alice", OwnerUserID: "alice", MaxJobs: 5, NowMs: requeued.NotBeforeMs}); err != nil || len(got) != 1 {
		t.Fatalf("re-claim after the delay = %+v, %v", got, err)
	}
	post("/v1/bridge/jobs/j-borrow/status", `{"bridgeId":"b-alice","status":"running"}`)
	post("/v1/bridge/jobs/j-borrow/status", `{"bridgeId":"b-alice","status":"replied","diagnostics":{"sessionId":"s1",
		"reply":{"messageId":"m-reply","text":"done","atMs":5000,"count":2},
		"usage":{"model":"m-large","inputTokens":120,"outputTokens":30,"costUsd":0.4}}}`)
	post("/v1/bridge/jobs/j-failed/status", `{"bridgeId":"b-alice","status":"failed","failureReason":"borrow_not_authorized","diagnostics":{"error":"core exploded"}}`)
	return svc
}

func getRuns(t *testing.T, svc *Service, path, caller string) (int, wire.AgentRunsReply) {
	t.Helper()
	code, body := getRaw(t, svc, path, caller)
	var out wire.AgentRunsReply
	_ = json.Unmarshal(body, &out)
	return code, out
}

func getRaw(t *testing.T, svc *Service, path, caller string) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if caller != "" {
		r.Header.Set("X-Bnbloc-User", caller)
	}
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func runIDs(rs []wire.AgentRun) string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.JobID
	}
	return strings.Join(ids, ",")
}

func TestRoomRunsRecordPhasesReplyAndUsage(t *testing.T) {
	svc := runsFixture(t)
	code, out := getRuns(t, svc, "/v1/rooms/room-1/jobs", "bob")
	if code != http.StatusOK || out.Scope != wire.RunScopeRoom || runIDs(out.Runs) != "j-failed,j-borrow" || out.NextBefore != "" {
		t.Fatalf("room runs = %d %+v", code, out)
	}
	b := out.Runs[1]
	if !b.Borrowed || b.Requester != "bob" || b.Agent.Owner != "alice" || b.Agent.Model != "m-large" || b.RoomName != "Design" || b.Status != "replied" {
		t.Fatalf("borrow run = %+v", b)
	}
	p := b.Phases
	if p.QueuedAtMs != 1_000 || p.ClaimedAtMs == 0 || p.StartedAtMs == 0 || p.FinishedAtMs == 0 || p.FirstReplyAtMs != 5_000 {
		t.Fatalf("phases = %+v", p)
	}
	if b.Request.MessageSeq != 10 || !strings.HasPrefix(b.Request.Text, "please fix") || len([]rune(b.Request.Text)) != wire.RunPreviewRunes+1 {
		t.Fatalf("request = %+v", b.Request)
	}
	if b.Reply == nil || b.Reply.MessageID != "m-reply" || b.Reply.Count != 2 || b.Reply.RoomSeq != 0 {
		t.Fatalf("reply = %+v (room seq is detail-only)", b.Reply)
	}
	if b.Usage == nil || *b.Usage.InputTokens != 120 || *b.Usage.OutputTokens != 30 || *b.Usage.CostUsd != 0.4 {
		t.Fatalf("usage = %+v", b.Usage)
	}
	if b.Diagnostics != nil {
		t.Fatal("list rows must not carry raw diagnostics")
	}
	if b.Expert == nil || b.Expert.ID != "cc_rev" || b.Expert.Name != "Reviewer" || b.Expert.Version != "1.2.0" {
		t.Fatalf("expert = %+v", b.Expert)
	}
	if b.RoomExpert == nil || b.RoomExpert.ID != "cc_house" || b.RoomExpert.Name != "House style" || b.RoomExpert.Version != "2.0.0" {
		t.Fatalf("room expert = %+v", b.RoomExpert)
	}
	f := out.Runs[0]
	if f.Status != "failed" || f.FailureReason != "borrow_not_authorized" || f.Error != "core exploded" || f.Borrowed || f.Usage != nil || f.Expert != nil || f.RoomExpert != nil {
		t.Fatalf("failed run = %+v", f)
	}

	if code, _ := getRuns(t, svc, "/v1/rooms/room-1/jobs", "carol"); code != http.StatusForbidden {
		t.Fatalf("non-member = %d, want 403", code)
	}
	if code, _ := getRuns(t, svc, "/v1/rooms/room-1/jobs", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", code)
	}
}

func TestRoomRunDetailResolvesReplySeq(t *testing.T) {
	svc := runsFixture(t)
	code, body := getRaw(t, svc, "/v1/rooms/room-1/jobs/j-borrow", "alice")
	var run wire.AgentRun
	_ = json.Unmarshal(body, &run)
	if code != http.StatusOK || run.Reply == nil || run.Reply.RoomSeq != 11 || run.Diagnostics["sessionId"] != "s1" {
		t.Fatalf("detail = %d %s", code, body)
	}
	// The retried first attempt says why, as a reason code.
	if run.Attempt != 2 || len(run.Attempts) != 1 || run.Attempts[0].Attempt != 1 || run.Attempts[0].Reason != "core_unavailable" || run.Attempts[0].AtMs == 0 {
		t.Fatalf("detail attempts = %d %+v", run.Attempt, run.Attempts)
	}
	// A job of another room is not reachable through this room.
	if code, _ := getRaw(t, svc, "/v1/rooms/room-1/jobs/j-queued", "alice"); code != http.StatusNotFound {
		t.Fatalf("foreign room job = %d, want 404", code)
	}
	if code, _ := getRaw(t, svc, "/v1/rooms/room-1/jobs/nope", "alice"); code != http.StatusNotFound {
		t.Fatalf("missing job = %d, want 404", code)
	}
	if code, _ := getRaw(t, svc, "/v1/rooms/room-2/jobs/j-queued", "bob"); code != http.StatusForbidden {
		t.Fatalf("non-member detail = %d, want 403", code)
	}
}

// O4: a run's raw diagnostics are its agent owner's and an org admin's
// (the signed org role); any other member, the requester included, reads
// the projection only.
func TestRoomRunDetailDiagnosticsOwnerAndOrgAdminOnly(t *testing.T) {
	svc := runsFixture(t)
	detail := func(caller, role string) wire.AgentRun {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1/rooms/room-1/jobs/j-borrow", nil)
		svcauth.Key("").Assert(r, caller, tenant.Default, role)
		w := httptest.NewRecorder()
		svc.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("detail as %s/%q = %d %s", caller, role, w.Code, w.Body.String())
		}
		var run wire.AgentRun
		_ = json.Unmarshal(w.Body.Bytes(), &run)
		return run
	}
	if run := detail("alice", "member"); run.Diagnostics["sessionId"] != "s1" {
		t.Fatalf("the agent owner reads the diagnostics: %+v", run.Diagnostics)
	}
	run := detail("bob", "member")
	if run.Diagnostics != nil {
		t.Fatalf("the requester reads the projection only, got diagnostics %+v", run.Diagnostics)
	}
	if run.Reply == nil || run.Reply.RoomSeq != 11 || run.Usage == nil || run.Status != "replied" {
		t.Fatalf("the projection stays whole for the requester: %+v", run)
	}
	if run := detail("bob", "org_admin"); run.Diagnostics["sessionId"] != "s1" {
		t.Fatalf("an org admin reads the diagnostics: %+v", run.Diagnostics)
	}
}

func TestMyRunsAreOwnAgentsAndOwnRequests(t *testing.T) {
	svc := runsFixture(t)
	for caller, want := range map[string]string{"alice": "j-failed,j-borrow", "bob": "j-borrow", "carol": "j-queued", "dave": ""} {
		code, out := getRuns(t, svc, "/v1/my/jobs", caller)
		if code != http.StatusOK || out.Scope != wire.RunScopeSelf || runIDs(out.Runs) != want {
			t.Errorf("%s my runs = %d %q, want %q", caller, code, runIDs(out.Runs), want)
		}
	}
	// The Team page's per-agent link narrows to one agent.
	if _, out := getRuns(t, svc, "/v1/my/jobs?owner=alice&core=c1&provider=codex&status=replied", "bob"); runIDs(out.Runs) != "j-borrow" {
		t.Fatalf("agent filter = %q", runIDs(out.Runs))
	}
	if code, _ := getRuns(t, svc, "/v1/my/jobs", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", code)
	}
}

func TestTenantRunsRedactRoomsTheViewerIsNotIn(t *testing.T) {
	svc := runsFixture(t)
	_, out := getRuns(t, svc, "/v1/tenant/jobs", "carol")
	if out.Scope != wire.RunScopeTenant || runIDs(out.Runs) != "j-queued,j-failed,j-borrow" {
		t.Fatalf("tenant runs = %+v", out)
	}
	for _, r := range out.Runs {
		inRoom := r.RoomID == "room-2"
		if r.Redacted == inRoom || (r.Request.Text == "") == inRoom {
			t.Errorf("%s redacted=%v text=%q (carol in room: %v)", r.JobID, r.Redacted, r.Request.Text, inRoom)
		}
		if r.Reply != nil && r.Reply.Text != "" && !inRoom {
			t.Errorf("%s leaks reply text", r.JobID)
		}
	}
	// Outcome, error and usage stay visible to the admin.
	if f := out.Runs[1]; f.Error != "core exploded" || out.Runs[2].Usage == nil {
		t.Fatalf("redaction dropped metadata: %+v", out.Runs[1:])
	}
	if _, out := getRuns(t, svc, "/v1/tenant/jobs?member=bob", "carol"); runIDs(out.Runs) != "j-borrow" {
		t.Fatalf("member filter = %q", runIDs(out.Runs))
	}
	if _, out := getRuns(t, svc, "/v1/tenant/jobs?status=failed,timed_out", "carol"); runIDs(out.Runs) != "j-failed" {
		t.Fatalf("status filter = %q", runIDs(out.Runs))
	}
	if _, out := getRuns(t, svc, "/v1/tenant/jobs?status=active", "carol"); runIDs(out.Runs) != "j-queued" {
		t.Fatalf("active filter = %q", runIDs(out.Runs))
	}
	if _, out := getRuns(t, svc, "/v1/tenant/jobs?from=1500&to=3000", "carol"); runIDs(out.Runs) != "j-failed" {
		t.Fatalf("time filter = %q", runIDs(out.Runs))
	}
}

func TestRunsPageWithOpaqueCursor(t *testing.T) {
	svc := runsFixture(t)
	var seen []string
	path := "/v1/tenant/jobs?limit=1"
	for i := 0; i < 5; i++ {
		_, out := getRuns(t, svc, path, "carol")
		seen = append(seen, runIDs(out.Runs))
		if out.NextBefore == "" {
			break
		}
		path = "/v1/tenant/jobs?limit=1&before=" + url.QueryEscape(out.NextBefore)
	}
	if got := strings.Join(seen, "|"); got != "j-queued|j-failed|j-borrow|" {
		t.Fatalf("pages = %q", got)
	}
	for _, q := range []string{"limit=0", "limit=101", "before=x", "before=12:", "status=bogus", "from=-1", "to=abc"} {
		if code, _ := getRuns(t, svc, "/v1/tenant/jobs?"+q, "carol"); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
	svc.Runs = nil
	if code, _ := getRuns(t, svc, "/v1/my/jobs", "carol"); code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d, want 503", code)
	}
}
