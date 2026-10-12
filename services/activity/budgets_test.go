package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type captureNotifier struct {
	mu     sync.Mutex
	events []notify.Event
}

func (c *captureNotifier) Emit(_ context.Context, ev notify.Event) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return true, nil
}

func (c *captureNotifier) all() []notify.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]notify.Event(nil), c.events...)
}

type budgetFixture struct {
	svc      *Service
	act      *store.Memory
	jobs     *store.MemoryBridgeJobs
	notifier *captureNotifier
	seq      int64
	// nowMu guards now: Service.Now is also read by the debounced budget
	// evaluation, which runs on its own timer goroutine.
	nowMu sync.Mutex
	now   time.Time
}

// clock is the Service clock (safe for concurrent use).
func (f *budgetFixture) clock() time.Time {
	f.nowMu.Lock()
	defer f.nowMu.Unlock()
	return f.now
}

func (f *budgetFixture) setNow(now time.Time) {
	f.nowMu.Lock()
	defer f.nowMu.Unlock()
	f.now = now
}

func bctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func newBudgetFixture(t *testing.T) *budgetFixture {
	t.Helper()
	now := time.UnixMilli(1_800_000_000_000) // mid-January 2027 (UTC)
	tenants := store.NewMemoryTenant()
	for _, m := range []store.TenantMember{
		{TenantID: tenant.Default, UserID: "root", Role: store.OrgRoleAdmin, JoinedAtMs: 1},
		{TenantID: tenant.Default, UserID: "alice", Role: store.OrgRoleMember, JoinedAtMs: 2},
		{TenantID: tenant.Default, UserID: "bob", Role: store.OrgRoleMember, JoinedAtMs: 3},
	} {
		if err := tenants.PutMember(bctx(), m); err != nil {
			t.Fatal(err)
		}
	}
	mem := membership.NewStatic()
	for _, u := range []string{"alice", "bob"} {
		mem.Add("room-1", u)
	}
	f := &budgetFixture{act: store.NewMemory(), jobs: store.NewMemoryBridgeJobs(), notifier: &captureNotifier{}, now: now}
	f.svc = &Service{
		Store: f.act, Publisher: &capturePublisher{}, Membership: mem, BridgeJobs: f.jobs,
		Usage: f.act, Now: f.clock,
		Budgets: store.NewMemoryUsageBudgets(), Tenants: tenants, Notifications: f.notifier,
		PublicURL: "https://bnbloc.example.com", BudgetDebounce: time.Millisecond,
	}
	return f
}

// burn records one reported reply of owner's agent (tokens in+out, cost).
func (f *budgetFixture) burn(t *testing.T, owner string, in, out int, cost float64, atMs int64) {
	t.Helper()
	f.seq++
	if _, _, err := f.act.AppendMessages(bctx(), "room-1", wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s-" + owner, Messages: []wire.Message{{
		Seq: f.seq, CreatedAtUnixMs: atMs,
		Payload: map[string]any{"ownerUserId": owner, "provider": "codex", "metrics": map[string]any{
			"inputTokens": float64(in), "outputTokens": float64(out), "costUsd": cost, "reported": true}},
	}}}); err != nil {
		t.Fatal(err)
	}
}

func (f *budgetFixture) do(t *testing.T, method, path, caller string, body any) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("X-Bnbloc-User", caller)
	w := httptest.NewRecorder()
	f.svc.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

func TestTenantBudgetsRoundTripWithBurn(t *testing.T) {
	f := newBudgetFixture(t)
	f.burn(t, "alice", 400, 100, 1.5, f.clock().UnixMilli()-1000)
	f.burn(t, "bob", 50, 0, 0, f.clock().UnixMilli()-2000)
	// Last month's usage is not this month's burn.
	f.burn(t, "alice", 9000, 0, 9, time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC).UnixMilli())

	code, raw := f.do(t, http.MethodPut, "/v1/tenant/budgets", "root", wire.TenantBudgetsUpdate{
		Tenant: &wire.UsageBudgetLimits{Tokens: i64(10_000), HardStop: true},
		Members: []wire.MemberBudgetLimits{
			{UserID: "alice", UsageBudgetLimits: wire.UsageBudgetLimits{Tokens: i64(1000), Cost: f64(10), WarnAtPercent: 40}},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("put = %d %s", code, raw)
	}
	var out wire.TenantBudgetsReply
	_ = json.Unmarshal(raw, &out)
	if out.Period != "2027-01" || out.Tenant == nil || out.Tenant.Burn.Tokens != 550 || out.Tenant.Percent != 5 || !out.Tenant.HardStop || out.Tenant.WarnAtPercent != 80 {
		t.Fatalf("tenant view = %+v (period %s)", out.Tenant, out.Period)
	}
	if len(out.Members) != 1 || out.Members[0].UserID != "alice" || out.Members[0].Burn.Tokens != 500 || out.Members[0].Burn.Cost != 1.5 ||
		out.Members[0].Percent != 50 || out.Members[0].Reached || out.Members[0].UpdatedBy != "root" {
		t.Fatalf("member views = %+v", out.Members)
	}
	if len(out.Usage) != 2 || out.Usage[0].UserID != "alice" || out.Usage[1].UserID != "bob" {
		t.Fatalf("usage = %+v (highest burn first)", out.Usage)
	}

	for name, body := range map[string]wire.TenantBudgetsUpdate{
		"no limit":    {Tenant: &wire.UsageBudgetLimits{WarnAtPercent: 50}},
		"warn at 100": {Tenant: &wire.UsageBudgetLimits{Tokens: i64(1), WarnAtPercent: 100}},
		"zero cost":   {Tenant: &wire.UsageBudgetLimits{Cost: f64(0)}},
		"non-member":  {Members: []wire.MemberBudgetLimits{{UserID: "mallory", UsageBudgetLimits: wire.UsageBudgetLimits{Tokens: i64(1)}}}},
		"duplicate": {Members: []wire.MemberBudgetLimits{
			{UserID: "bob", UsageBudgetLimits: wire.UsageBudgetLimits{Tokens: i64(1)}},
			{UserID: "BOB", UsageBudgetLimits: wire.UsageBudgetLimits{Tokens: i64(2)}},
		}},
	} {
		if code, raw := f.do(t, http.MethodPut, "/v1/tenant/budgets", "root", body); code != http.StatusBadRequest {
			t.Errorf("%s: put = %d %s, want 400", name, code, raw)
		}
	}
	// The member sees their own budget and burn.
	code, raw = f.do(t, http.MethodGet, "/v1/my/budget", "alice", nil)
	var mine wire.MyBudgetReply
	_ = json.Unmarshal(raw, &mine)
	if code != http.StatusOK || mine.Budget == nil || mine.Budget.Percent != 50 || mine.Burn.Tokens != 500 || mine.Blocked {
		t.Fatalf("my budget = %d %s", code, raw)
	}
	code, raw = f.do(t, http.MethodGet, "/v1/my/budget", "bob", nil)
	_ = json.Unmarshal(raw, &mine)
	if code != http.StatusOK || mine.Budget != nil || mine.Burn.Tokens != 50 {
		t.Fatalf("bob's budget = %d %s (want none, burn 50)", code, raw)
	}
}

// claimAs registers owner's bridge, queues one job per id, and claims.
func (f *budgetFixture) claimAs(t *testing.T, owner string, jobIDs ...string) []store.AgentJob {
	t.Helper()
	ctx := bctx()
	bridge := "bridge-" + owner
	_ = f.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: bridge, OwnerUserID: owner, Status: store.BridgeOnline, LastSeenAtMs: f.clock().UnixMilli()})
	_ = f.jobs.ReplaceBridgeAgents(ctx, bridge, []store.BridgeAgent{{OwnerUserID: owner, Core: "c1", Provider: "codex", LastSeenAtMs: f.clock().UnixMilli()}})
	for _, id := range jobIDs {
		f.seq++
		if _, err := f.jobs.PutAgentJob(ctx, store.AgentJob{JobID: id, RoomID: "room-1", ChannelID: "ch", MessageSeq: f.seq, OwnerUserID: owner, RequesterUserID: "bob",
			Core: "c1", Provider: "codex", MessageText: "go", CreatedAtMs: f.clock().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	code, raw := f.do(t, http.MethodPost, "/v1/bridge/jobs/claim", owner, map[string]any{"bridgeId": bridge, "maxJobs": 5})
	if code != http.StatusOK {
		t.Fatalf("claim = %d %s", code, raw)
	}
	var out struct {
		Jobs []store.AgentJob `json:"jobs"`
	}
	_ = json.Unmarshal(raw, &out)
	// Finish what was handed over so the next claim of the same agent is not
	// held back by a running turn.
	for _, j := range out.Jobs {
		if _, err := f.jobs.UpdateAgentJobStatus(ctx, store.JobStatusUpdate{JobID: j.JobID, BridgeID: bridge, Status: store.AgentJobReplied, NowMs: f.clock().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	return out.Jobs
}

// R12 hard stop: a reached hard-stop budget refuses the owner's jobs at claim
// (failed budget_exhausted + a visible room notice); other owners still run.
// Borrowed runs count against the owner: the job here is bob's borrow of
// alice's agent.
func TestBudgetHardStopRefusesDriveAtClaim(t *testing.T) {
	f := newBudgetFixture(t)
	f.burn(t, "alice", 900, 100, 0, f.clock().UnixMilli()-1000)
	if code, raw := f.do(t, http.MethodPut, "/v1/tenant/budgets", "root", wire.TenantBudgetsUpdate{
		Members: []wire.MemberBudgetLimits{{UserID: "alice", UsageBudgetLimits: wire.UsageBudgetLimits{Tokens: i64(1000), HardStop: true}}},
	}); code != http.StatusOK {
		t.Fatalf("put = %d %s", code, raw)
	}
	if got := f.claimAs(t, "alice", "j-alice"); len(got) != 0 {
		t.Fatalf("claimed %d jobs over a reached hard-stop budget, want 0", len(got))
	}
	var job store.AgentJob
	jobList, err := f.jobs.ListAgentJobs(bctx(), "room-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobList {
		if j.JobID == "j-alice" {
			job = j
		}
	}
	if job.Status != store.AgentJobFailed || job.FailureReason != store.FailureBudgetExhausted {
		t.Fatalf("refused job = %+v", job)
	}
	var notice string
	for _, ev := range f.svc.Publisher.(*capturePublisher).events {
		if in, ok := ev.Payload.(wire.ReportMessagesInput); ok && ev.Kind == "agent_messages" && in.AgentSessionID == wire.JobNoticeSessionID("j-alice") {
			notice, _ = in.Messages[0].Payload.(map[string]any)["text"].(string)
		}
	}
	if !strings.Contains(notice, "monthly budget reached") {
		t.Fatalf("room notice = %q", notice)
	}
	if got := f.claimAs(t, "bob", "j-bob"); len(got) != 1 {
		t.Fatalf("bob (no budget) claimed %d jobs, want 1", len(got))
	}
	code, raw := f.do(t, http.MethodGet, "/v1/my/budget", "alice", nil)
	var mine wire.MyBudgetReply
	_ = json.Unmarshal(raw, &mine)
	if code != http.StatusOK || !mine.Blocked || mine.BlockedBy != "member" || !mine.Budget.Reached {
		t.Fatalf("my budget = %d %s", code, raw)
	}
	// A tenant hard stop refuses every owner's drives.
	if code, raw := f.do(t, http.MethodPut, "/v1/tenant/budgets", "root", wire.TenantBudgetsUpdate{
		Tenant: &wire.UsageBudgetLimits{Tokens: i64(500), HardStop: true},
	}); code != http.StatusOK {
		t.Fatalf("put = %d %s", code, raw)
	}
	if got := f.claimAs(t, "bob", "j-bob-2"); len(got) != 0 {
		t.Fatalf("bob claimed %d jobs over the tenant's reached hard stop, want 0", len(got))
	}
	// Without hard stop a reached budget only warns.
	if code, raw := f.do(t, http.MethodPut, "/v1/tenant/budgets", "root", wire.TenantBudgetsUpdate{
		Tenant: &wire.UsageBudgetLimits{Tokens: i64(500)},
	}); code != http.StatusOK {
		t.Fatalf("put = %d %s", code, raw)
	}
	if got := f.claimAs(t, "bob", "j-bob-3"); len(got) != 1 {
		t.Fatalf("bob claimed %d jobs with no hard stop, want 1", len(got))
	}
}

// R12 warnings: crossing warnAtPercent and then 100% each notify once per
// month — the member for a member budget, the org admins for the tenant's.
func TestBudgetWarningsOncePerThresholdPerMonth(t *testing.T) {
	f := newBudgetFixture(t)
	if code, raw := f.do(t, http.MethodPut, "/v1/tenant/budgets", "root", wire.TenantBudgetsUpdate{
		Tenant:  &wire.UsageBudgetLimits{Cost: f64(100), WarnAtPercent: 90},
		Members: []wire.MemberBudgetLimits{{UserID: "alice", UsageBudgetLimits: wire.UsageBudgetLimits{Tokens: i64(1000)}}},
	}); code != http.StatusOK {
		t.Fatalf("put = %d %s", code, raw)
	}
	f.burn(t, "alice", 700, 0, 1, f.clock().UnixMilli()-3000)
	f.svc.EvaluateBudgetWarnings(bctx(), "alice")
	f.svc.EvaluateBudgetWarnings(bctx(), "alice")
	if evs := f.notifier.all(); len(evs) != 0 {
		t.Fatalf("warned at 70%% of an 80%% threshold: %+v", evs)
	}
	f.burn(t, "alice", 100, 50, 1, f.clock().UnixMilli()-2000) // 850 / 1000
	f.svc.EvaluateBudgetWarnings(bctx(), "alice")
	f.svc.EvaluateBudgetWarnings(bctx(), "alice")
	evs := f.notifier.all()
	if len(evs) != 1 || evs[0].Type != wire.NotificationBudgetWarning || evs[0].Recipients[0] != "alice" ||
		!strings.Contains(evs[0].Title, "85%") || evs[0].URL != "https://bnbloc.example.com/#/settings" {
		t.Fatalf("80%% warning = %+v", evs)
	}
	f.burn(t, "alice", 200, 0, 98, f.clock().UnixMilli()-1000) // 1050 tokens; tenant cost 100 / 100
	f.svc.EvaluateBudgetWarnings(bctx(), "alice")
	f.svc.EvaluateBudgetWarnings(bctx(), "alice")
	evs = f.notifier.all()
	if len(evs) != 3 {
		t.Fatalf("events = %+v, want the member's 100%% and the tenant's 100%% warnings", evs)
	}
	member, org := evs[1], evs[2]
	if member.Recipients[0] != "alice" || !strings.Contains(member.Title, "reached") {
		t.Fatalf("member 100%% warning = %+v", member)
	}
	if len(org.Recipients) != 1 || org.Recipients[0] != "root" || !strings.Contains(org.Title, "organization") || org.URL != "https://bnbloc.example.com/#/team" {
		t.Fatalf("tenant warning = %+v (straight to 100%%: one warning, to the org admins)", org)
	}
	// Next month re-arms.
	f.setNow(time.Date(2027, 2, 2, 0, 0, 0, 0, time.UTC))
	f.burn(t, "alice", 1000, 0, 0, f.clock().UnixMilli()-1000)
	f.svc.EvaluateBudgetWarnings(bctx(), "alice")
	if evs := f.notifier.all(); len(evs) != 4 || evs[3].DedupKey != "budget:alice:2027-02:100" {
		t.Fatalf("next month = %+v", evs)
	}
}

// Ingested replies with reported usage schedule the evaluation themselves.
func TestReplyIngestTriggersBudgetWarning(t *testing.T) {
	f := newBudgetFixture(t)
	if code, raw := f.do(t, http.MethodPut, "/v1/tenant/budgets", "root", wire.TenantBudgetsUpdate{
		Members: []wire.MemberBudgetLimits{{UserID: "alice", UsageBudgetLimits: wire.UsageBudgetLimits{Tokens: i64(100)}}},
	}); code != http.StatusOK {
		t.Fatalf("put = %d %s", code, raw)
	}
	code, raw := f.do(t, http.MethodPost, "/v1/rooms/room-1/agent-activity/messages", "alice", wire.ReportMessagesInput{
		WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{{Seq: 1, CreatedAtUnixMs: f.clock().UnixMilli(),
			Payload: map[string]any{"role": "assistant", "provider": "codex", "metrics": map[string]any{"inputTokens": 150.0, "reported": true}}}},
	})
	if code != http.StatusOK {
		t.Fatalf("messages = %d %s", code, raw)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(f.notifier.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if evs := f.notifier.all(); len(evs) != 1 || evs[0].Recipients[0] != "alice" {
		t.Fatalf("events = %+v", evs)
	}
}

// failingBudgets is a budget store whose reads fail.
type failingBudgets struct{ store.UsageBudgetStore }

func (failingBudgets) GetUsageBudget(context.Context, string) (store.UsageBudget, bool, error) {
	return store.UsageBudget{}, false, errBudgetRead
}

var errBudgetRead = errors.New("budget table unavailable")

// The hard stop fails closed: when the budget cannot be read the claimed job
// is not handed to the bridge (it would run unbudgeted); it is refused with
// the retryable budget_unavailable and requeued, so it runs once the budget
// reads again.
func TestBudgetHardStopFailsClosed(t *testing.T) {
	f := newBudgetFixture(t)
	healthy := f.svc.Budgets
	f.svc.Budgets = failingBudgets{healthy}
	if got := f.claimAs(t, "alice", "j-unread"); len(got) != 0 {
		t.Fatalf("claimed %d jobs with an unreadable budget, want 0", len(got))
	}
	job, err := f.jobs.GetAgentRun(bctx(), "j-unread")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.AgentJobQueued || job.FailureReason != store.FailureBudgetUnavailable {
		t.Fatalf("refused job = %s/%s, want queued for a retry with %s", job.Status, job.FailureReason, store.FailureBudgetUnavailable)
	}
	f.svc.Budgets = healthy
	// The requeue waits out its retry delay first.
	if got := f.claimAs(t, "alice"); len(got) != 0 {
		t.Fatalf("claimed %+v inside the retry delay", got)
	}
	f.setNow(time.UnixMilli(job.NotBeforeMs))
	if got := f.claimAs(t, "alice"); len(got) != 1 || got[0].JobID != "j-unread" {
		t.Fatalf("after the budget reads again, claimed %+v; want the requeued job", got)
	}
	// The member's own budget view reports the failure instead of "not blocked".
	f.svc.Budgets = failingBudgets{healthy}
	if code, raw := f.do(t, http.MethodGet, "/v1/my/budget", "alice", nil); code != http.StatusInternalServerError {
		t.Fatalf("my budget with an unreadable budget = %d %s, want 500", code, raw)
	}
}
