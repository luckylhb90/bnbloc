package realtimehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// dispatchFixture is room-1 with members maya (the requester), jack (whose
// bridge "bridge-jack" runs the parent job on jack/codex) and bob; a default
// channel and the people chat.
type dispatchFixture struct {
	t      *testing.T
	srv    *httptest.Server
	svc    *Service
	jobs   *store.MemoryBridgeJobs
	chat   *store.MemoryChat
	audit  *store.MemoryExtensionAudit
	people string
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	mem := membership.NewStatic()
	for _, u := range []string{"maya", "jack", "bob"} {
		mem.Add("room-1", u)
	}
	channels := store.NewMemoryChannel()
	if err := channels.PutChannel(dctx(), store.RoomChannel{ChannelID: "ch_general", TenantID: tenant.Default, RoomID: "room-1", IsDefault: true, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	pc, err := channels.EnsurePeopleChannel(dctx(), "room-1", "maya", 2)
	if err != nil {
		t.Fatal(err)
	}
	f := &dispatchFixture{t: t, jobs: store.NewMemoryBridgeJobs(), chat: store.NewMemoryChat(), audit: store.NewMemoryExtensionAudit(), people: pc.ChannelID}
	f.svc = &Service{
		Hub: realtime.NewHub(64, 16), Membership: mem, Chat: f.chat, Channels: channels,
		BridgeJobs: f.jobs, Dispatches: f.jobs, Audit: f.audit,
	}
	f.srv = httptest.NewServer(f.svc.Handler())
	t.Cleanup(f.srv.Close)
	for _, b := range []struct{ id, owner string }{{"bridge-jack", "jack"}, {"bridge-bob", "bob"}} {
		if err := f.jobs.PutBridgeHeartbeat(dctx(), store.BridgeHeartbeat{BridgeID: b.id, OwnerUserID: b.owner, Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// job stores a job as given (status and lease included) and returns it.
func (f *dispatchFixture) job(j store.AgentJob) store.AgentJob {
	f.t.Helper()
	if j.RoomID == "" {
		j.RoomID = "room-1"
	}
	if j.ChannelID == "" {
		j.ChannelID = "ch_general"
	}
	if j.Status == "" {
		j.Status = store.AgentJobRunning
	}
	if j.CreatedAtMs == 0 {
		j.CreatedAtMs = time.Now().UnixMilli()
	}
	out, err := f.jobs.PutAgentJob(dctx(), j)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// parent is maya's request running on jack's bridge, driving jack/codex.
func (f *dispatchFixture) parent(id string, mutate ...func(*store.AgentJob)) store.AgentJob {
	j := store.AgentJob{JobID: id, MessageSeq: 100 + int64(len(id)), RequesterUserID: "maya", OwnerUserID: "jack", Core: "acme", Provider: "codex", LeaseBridgeID: "bridge-jack"}
	for _, m := range mutate {
		m(&j)
	}
	return f.job(j)
}

func (f *dispatchFixture) dispatch(caller string, in wire.DispatchRequest) (int, map[string]any) {
	f.t.Helper()
	buf, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/internal/v1/rooms/room-1/dispatch", strings.NewReader(string(buf)))
	req.Header.Set("X-Bnbloc-User", caller)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func toBob(parentID string) wire.DispatchRequest {
	return wire.DispatchRequest{ParentJobID: parentID, Recipient: wire.DispatchRecipient{OwnerID: "bob", Provider: "claude", Core: "acme"}, Text: "review the parser change"}
}

// An admitted dispatch is the requester's message: posted as maya, marked
// with the parent job, depth and the dispatching agent, and its job carries
// maya as requester (so bob's bridge runs borrow authorization for her) plus
// the dispatch origin. The decision is audited with the bridge owner as
// actor.
func TestDispatchPostsAsTheParentRequester(t *testing.T) {
	f := newDispatchFixture(t)
	p := f.parent("job_parent")
	code, out := f.dispatch("jack", toBob(p.JobID))
	if code != http.StatusOK {
		t.Fatalf("dispatch = %d %v", code, out)
	}
	jobID, _ := out["jobId"].(string)
	if jobID == "" || out["depth"] != float64(1) {
		t.Fatalf("reply = %v; want a job at depth 1", out)
	}
	msgs, err := f.chat.RoomFeed(dctx(), "room-1", 0, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("feed = %v, %v; want one message", msgs, err)
	}
	var payload struct {
		UserID       string              `json:"userId"`
		Text         string              `json:"text"`
		Recipient    map[string]string   `json:"recipient"`
		DispatchedBy wire.DispatchMarker `json:"dispatchedBy"`
	}
	if err := json.Unmarshal(msgs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != "maya" || payload.Recipient["ownerId"] != "bob" || payload.Recipient["provider"] != "claude" {
		t.Fatalf("payload = %+v; want maya's message to bob/claude", payload)
	}
	if payload.DispatchedBy != (wire.DispatchMarker{JobID: p.JobID, ParentSeq: p.MessageSeq, Depth: 1, Via: "jack/codex"}) {
		t.Fatalf("dispatchedBy = %+v", payload.DispatchedBy)
	}
	if msgs[0].ChannelID != "ch_general" {
		t.Fatalf("posted in %q; want the parent's channel", msgs[0].ChannelID)
	}
	child, err := f.jobs.GetAgentRun(dctx(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if child.RequesterUserID != "maya" || child.OwnerUserID != "bob" || child.DispatchedByJobID != p.JobID || child.DispatchDepth != 1 || child.Status != store.AgentJobQueued {
		t.Fatalf("child job = %+v", child)
	}
	recs, _ := f.audit.AuditLog(dctx(), store.AuditLogQuery{AllTenants: true, Kinds: []string{AgentDispatchAuditKind}, Limit: 10})
	if len(recs) != 1 || recs[0].Actor != "jack" || recs[0].Outcome != "allow" || recs[0].Name != "bob/claude" ||
		!strings.Contains(recs[0].Detail, "parentJob=job_parent") || !strings.Contains(recs[0].Detail, "requester=maya") || !strings.Contains(recs[0].Detail, "job="+jobID) {
		t.Fatalf("audit = %+v", recs)
	}
}

// Each guard refuses with its own status and code, posts nothing, and is
// audited as a denial.
func TestDispatchGuards(t *testing.T) {
	cases := []struct {
		name   string
		caller string
		setup  func(f *dispatchFixture) wire.DispatchRequest
		status int
		code   string
	}{
		{"depth", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_deep", func(j *store.AgentJob) { j.DispatchDepth = wire.MaxDispatchDepth; j.DispatchedByJobID = "job_gone" }).JobID)
		}, http.StatusConflict, wire.DispatchDepthExceeded},
		{"fan-out", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			p := f.parent("job_busy")
			for i := 0; i < wire.MaxDispatchFanOut; i++ {
				f.job(store.AgentJob{JobID: fmt.Sprintf("job_kid_%d", i), MessageSeq: int64(200 + i), RequesterUserID: "maya", OwnerUserID: "bob", Core: "acme", Provider: fmt.Sprintf("p%d", i), Status: store.AgentJobFailed, DispatchedByJobID: p.JobID, DispatchDepth: 1})
			}
			return toBob(p.JobID)
		}, http.StatusConflict, wire.DispatchFanOutExceeded},
		{"self", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			in := toBob(f.parent("job_self").JobID)
			in.Recipient = wire.DispatchRecipient{OwnerID: "jack", Provider: "codex"} // default core = any core
			return in
		}, http.StatusConflict, wire.DispatchSelf},
		{"cycle", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			root := f.job(store.AgentJob{JobID: "job_root", MessageSeq: 50, RequesterUserID: "maya", OwnerUserID: "bob", Core: "acme", Provider: "claude", LeaseBridgeID: "bridge-bob"})
			p := f.parent("job_mid", func(j *store.AgentJob) { j.DispatchedByJobID = root.JobID; j.DispatchDepth = 1 })
			return toBob(p.JobID)
		}, http.StatusConflict, wire.DispatchCycle},
		{"wrong owner", "bob", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_theirs").JobID)
		}, http.StatusForbidden, wire.DispatchNotParentBridge},
		{"unknown bridge", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_ghost", func(j *store.AgentJob) { j.LeaseBridgeID = "bridge-unknown" }).JobID)
		}, http.StatusForbidden, wire.DispatchNotParentBridge},
		{"not running", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_done", func(j *store.AgentJob) { j.Status = store.AgentJobReplied }).JobID)
		}, http.StatusConflict, wire.DispatchParentNotRunning},
		{"queued", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_queued", func(j *store.AgentJob) { j.Status = store.AgentJobQueued; j.LeaseBridgeID = "" }).JobID)
		}, http.StatusConflict, wire.DispatchParentNotRunning},
		{"other room", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_elsewhere", func(j *store.AgentJob) { j.RoomID = "room-2" }).JobID)
		}, http.StatusNotFound, wire.DispatchParentNotFound},
		{"requester left", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_orphan", func(j *store.AgentJob) { j.RequesterUserID = "zoe" }).JobID)
		}, http.StatusForbidden, wire.DispatchRequesterGone},
		{"people chat", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			return toBob(f.parent("job_people", func(j *store.AgentJob) { j.ChannelID = f.people }).JobID)
		}, http.StatusBadRequest, wire.DispatchAgentsNotAdmitted},
		{"no recipient", "jack", func(f *dispatchFixture) wire.DispatchRequest {
			in := toBob(f.parent("job_norecipient").JobID)
			in.Recipient = wire.DispatchRecipient{}
			return in
		}, http.StatusBadRequest, wire.DispatchInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchFixture(t)
			in := tc.setup(f)
			code, out := f.dispatch(tc.caller, in)
			if code != tc.status || out["code"] != tc.code {
				t.Fatalf("dispatch = %d %v; want %d %s", code, out, tc.status, tc.code)
			}
			if msgs, _ := f.chat.RoomFeed(dctx(), "room-1", 0, 10); len(msgs) != 0 {
				t.Fatalf("a refused dispatch posted %d messages", len(msgs))
			}
			recs, _ := f.audit.AuditLog(dctx(), store.AuditLogQuery{AllTenants: true, Kinds: []string{AgentDispatchAuditKind}, Limit: 10})
			if len(recs) != 1 || recs[0].Outcome != "deny" || !strings.Contains(recs[0].Detail, "reason="+tc.code) {
				t.Fatalf("audit = %+v; want one denial with reason %s", recs, tc.code)
			}
		})
	}
}

// A retried dispatch (same clientMsgId) answers the original job instead of
// posting again, even when the parent is at its fan-out limit by then.
func TestDispatchRetryIsIdempotent(t *testing.T) {
	f := newDispatchFixture(t)
	p := f.parent("job_retry")
	in := toBob(p.JobID)
	in.ClientMsgID = "ask-1"
	code, first := f.dispatch("jack", in)
	if code != http.StatusOK {
		t.Fatalf("first = %d %v", code, first)
	}
	code, again := f.dispatch("jack", in)
	if code != http.StatusOK || again["jobId"] != first["jobId"] || again["sequence"] != first["sequence"] {
		t.Fatalf("retry = %d %v; want the original %v", code, again, first)
	}
	kids, _ := f.jobs.ListDispatchedJobs(dctx(), p.JobID)
	if len(kids) != 1 {
		t.Fatalf("dispatched jobs = %d; want 1", len(kids))
	}
}

// Concurrent dispatches of one parent cannot overrun the fan-out limit.
func TestDispatchFanOutHoldsUnderConcurrency(t *testing.T) {
	f := newDispatchFixture(t)
	p := f.parent("job_race")
	results := make(chan int, 2*wire.MaxDispatchFanOut)
	for i := 0; i < 2*wire.MaxDispatchFanOut; i++ {
		go func(i int) {
			in := toBob(p.JobID)
			in.Recipient.Provider = fmt.Sprintf("agent%d", i)
			code, _ := f.dispatch("jack", in)
			results <- code
		}(i)
	}
	ok := 0
	for i := 0; i < 2*wire.MaxDispatchFanOut; i++ {
		if <-results == http.StatusOK {
			ok++
		}
	}
	kids, _ := f.jobs.ListDispatchedJobs(dctx(), p.JobID)
	if ok != wire.MaxDispatchFanOut || len(kids) != wire.MaxDispatchFanOut {
		t.Fatalf("admitted %d, persisted %d; want %d each", ok, len(kids), wire.MaxDispatchFanOut)
	}
}

// A dispatch posted through one replica reaches the room's streams on
// another.
func TestReplicasDispatchReachesEveryReplica(t *testing.T) {
	c := newCluster(t, 2)
	other, owner := c.reps[0], c.reps[1]
	room := "room-1"
	for _, u := range []string{"maya", "jack", "bob"} {
		c.members.Add(room, u)
	}
	jobs := owner.svc.BridgeJobs.(*store.MemoryBridgeJobs)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if err := jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "bridge-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job_p", RoomID: room, MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Core: "acme", Provider: "codex", Status: store.AgentJobRunning, LeaseBridgeID: "bridge-jack"}); err != nil {
		t.Fatal(err)
	}
	s := c.open(owner, "/v1/rooms/"+room+"/events?allChannels=1", "maya")
	waitSubscribers(t, owner, room, 1)
	resp, err := http.DefaultClient.Do(c.request(other, http.MethodPost, "/internal/v1/rooms/"+room+"/dispatch", "jack", toBob("job_p")))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch = %d %s", resp.StatusCode, body)
	}
	ev := eventOf(t, s.next(t, "chat_message"))
	if !strings.Contains(fmt.Sprint(ev.Payload), "dispatchedBy") {
		t.Fatalf("chat_message = %+v; want the dispatch marker", ev)
	}
}

// brokenRuns fails every parent read.
type brokenRuns struct{ *store.MemoryBridgeJobs }

func (brokenRuns) GetAgentRun(context.Context, string) (store.AgentJob, error) {
	return store.AgentJob{}, fmt.Errorf("connection reset")
}

// A store failure is a named 500 (never a guard refusal, never silent), and
// is audited as failed.
func TestDispatchStoreFailureIs500(t *testing.T) {
	f := newDispatchFixture(t)
	f.svc.Dispatches = brokenRuns{f.jobs}
	code, out := f.dispatch("jack", toBob("job_any"))
	if code != http.StatusInternalServerError || out["code"] != nil {
		t.Fatalf("dispatch = %d %v; want a 500 without a guard code", code, out)
	}
	recs, _ := f.audit.AuditLog(dctx(), store.AuditLogQuery{AllTenants: true, Kinds: []string{AgentDispatchAuditKind}, Limit: 10})
	if len(recs) != 1 || recs[0].Outcome != "failed" {
		t.Fatalf("audit = %+v; want one failed row", recs)
	}
}

// A dispatch's expert resolves as the requester's, exactly like a typed
// chat: maya's expert is pinned on the dispatched job, while an expert only
// the bridge owner (jack) may use is refused.
func TestDispatchExpertIsTheRequesters(t *testing.T) {
	f := newDispatchFixture(t)
	content := store.NewMemoryContentComponents()
	manifest := func(name string) json.RawMessage {
		return json.RawMessage(`{"kind":"` + contentpkg.KindExpert + `","name":"` + name + `","version":"1.0.0","displayName":"` + name + `"}`)
	}
	for _, c := range []store.ContentComponent{
		{ID: "cc_maya", Kind: contentpkg.KindExpert, Name: "reviewer", TenantID: tenant.Default, Owner: "maya", Scope: store.ContentScopePersonal, Status: store.ContentStatusActive},
		{ID: "cc_jack", Kind: contentpkg.KindExpert, Name: "auditor", TenantID: tenant.Default, Owner: "jack", Scope: store.ContentScopePersonal, Status: store.ContentStatusActive},
	} {
		if err := content.CreateContentComponent(dctx(), c, store.ContentComponentVersion{Version: "1.0.0", ManifestJSON: manifest(c.Name), BundleBlobKey: "sha256/" + c.ID}); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.Content = content
	p := f.parent("job_expert")

	in := toBob(p.JobID)
	in.Expert = &wire.ChatExpert{ID: "cc_maya"}
	code, out := f.dispatch("jack", in)
	if code != http.StatusOK {
		t.Fatalf("dispatch with the requester's expert = %d %v", code, out)
	}
	child, err := f.jobs.GetAgentRun(dctx(), out["jobId"].(string))
	if err != nil || child.ExpertID != "cc_maya" || child.ExpertVersion != "1.0.0" {
		t.Fatalf("child = %+v, %v; want maya's expert pinned", child, err)
	}

	in.Expert = &wire.ChatExpert{ID: "cc_jack"}
	if code, out := f.dispatch("jack", in); code != http.StatusBadRequest || out["code"] != wire.DispatchExpertUnusable {
		t.Fatalf("dispatch with the bridge owner's expert = %d %v; want 400 %s", code, out, wire.DispatchExpertUnusable)
	}
}
