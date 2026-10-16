package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// seen is one request a backend received through the gateway.
type seen struct{ method, path, query, subject, body string }

// recorder is a backend that answers 200 and remembers who each request was
// signed for.
type recorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	list []seen
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.list = append(rec.list, seen{r.Method, r.URL.Path, r.URL.RawQuery, svcauth.Subject(r), string(raw)})
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *recorder) last() (seen, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.list) == 0 {
		return seen{}, false
	}
	return r.list[len(r.list)-1], true
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.list)
}

// An agent schedules for the person who asked it: through its job's grant, as
// that person, an AI task only, only while the job runs on its bridge.
func TestAgentDoorSchedulesAsTheRequester(t *testing.T) {
	g := newContentGateway(t)
	audit := store.NewMemoryExtensionAudit()
	g.svc.ExtAudit = audit
	back := newRecorder(t)
	g.svc.Backends.Realtime, g.svc.Backends.Room = back.srv.URL, back.srv.URL
	ctx := tenant.NewContext(context.Background(), "home")
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	seq := int64(0)
	put := func(id, requester string) {
		t.Helper()
		seq++
		if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: id, RoomID: "room-1", MessageSeq: seq, RequesterUserID: requester, OwnerUserID: "jack", Provider: "opencode",
			Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	put("job-maya", "maya") // maya asked jack's agent
	put("job-gone", "kim")  // kim is not a member of the room
	base := "/v1/rooms/room-1/schedules"
	ai := `{"text":"remind the group","recipient":{"kind":"ai"},"recurrence":{"kind":"weekly","time":"10:00","days":[4]},"timezone":"Asia/Shanghai"}`

	// As the requester; the grant never reaches the backend.
	code, _ := g.do("jack", http.MethodPost, base+"?job=job-maya", "application/json", []byte(ai))
	got, _ := back.last()
	if code != http.StatusOK || got.subject != "maya" || got.method != http.MethodPost || got.path != base || strings.Contains(got.query, "job") || got.body != ai {
		t.Fatalf("create = %d, backend saw %+v", code, got)
	}
	if code, _ := g.do("jack", http.MethodGet, base+"?job=job-maya", "", nil); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if got, _ := back.last(); got.subject != "maya" || got.method != http.MethodGet {
		t.Fatalf("list backend saw %+v", got)
	}
	if code, _ := g.do("jack", http.MethodGet, "/v1/rooms/room-1/groups?job=job-maya", "", nil); code != http.StatusOK {
		t.Fatalf("groups = %d", code)
	}
	if got, _ := back.last(); got.subject != "maya" || got.path != "/v1/rooms/room-1/groups" {
		t.Fatalf("groups backend saw %+v", got)
	}
	if code, _ := g.do("jack", http.MethodDelete, base+"/sch_1?job=job-maya", "", nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if got, _ := back.last(); got.subject != "maya" || got.method != http.MethodDelete {
		t.Fatalf("delete backend saw %+v", got)
	}

	// Without a grant the caller acts as themselves.
	if code, _ := g.do("jack", http.MethodGet, base, "", nil); code != http.StatusOK {
		t.Fatalf("a person's own list = %d", code)
	}
	if got, _ := back.last(); got.subject != "jack" {
		t.Fatalf("no grant acts as the caller: %+v", got)
	}

	// An agent schedules AI tasks only.
	n := back.count()
	for name, body := range map[string]string{
		"a message as the person": `{"text":"x","recipient":{"kind":"room"},"recurrence":{"kind":"daily","time":"09:00"},"timezone":"UTC"}`,
		"an agent's turn":         `{"text":"x","recipient":{"kind":"agent","ownerId":"jack","provider":"codex"},"recurrence":{"kind":"daily","time":"09:00"},"timezone":"UTC"}`,
		"no recipient":            `{"text":"x","recurrence":{"kind":"daily","time":"09:00"},"timezone":"UTC"}`,
		"not json":                `nope`,
	} {
		if code, raw := g.do("jack", http.MethodPost, base+"?job=job-maya", "application/json", []byte(body)); code != http.StatusForbidden || !strings.Contains(string(raw), "AI tasks only") {
			t.Errorf("%s = %d %s", name, code, raw)
		}
	}
	if back.count() != n {
		t.Fatal("a refused schedule reached the backend")
	}

	// Only the bridge running the job, only a requester who may still post.
	if code, _ := g.do("bob", http.MethodPost, base+"?job=job-maya", "application/json", []byte(ai)); code != http.StatusForbidden {
		t.Fatalf("someone else's bridge = %d", code)
	}
	if code, _ := g.do("jack", http.MethodPost, base+"?job=job-gone", "application/json", []byte(ai)); code != http.StatusForbidden {
		t.Fatalf("a requester who left the room = %d", code)
	}
	if code, _ := g.do("jack", http.MethodPost, base+"?job=nope", "application/json", []byte(ai)); code != http.StatusNotFound {
		t.Fatalf("no such job = %d", code)
	}
	if _, err := g.jobs.UpdateAgentJobStatus(ctx, store.JobStatusUpdate{JobID: "job-maya", BridgeID: "br-jack", Status: store.AgentJobReplied, NowMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if code, _ := g.do("jack", http.MethodPost, base+"?job=job-maya", "application/json", []byte(ai)); code != http.StatusConflict {
		t.Fatalf("an ended job = %d", code)
	}

	// Creating and deleting are audited with the agent and the requester, never the text.
	recs, err := audit.AuditLog(context.Background(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindSchedule}, Limit: 20})
	if err != nil || len(recs) != 2 {
		t.Fatalf("audit = %+v, %v", recs, err)
	}
	for _, r := range recs {
		if r.Actor != "maya" || !strings.Contains(r.Detail, "agent jack/opencode") || strings.Contains(r.Detail, "remind") {
			t.Errorf("audit record %+v", r)
		}
	}
}
