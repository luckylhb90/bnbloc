package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/componentcred"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

const tid = "acme"

func tctx() context.Context { return tenant.NewContext(context.Background(), tid) }

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	mem      *store.MemoryNotifications
	settings *store.MemoryPlatformSettings
	installs *store.MemoryComponentInstall
	audit    *store.MemoryExtensionAudit
	dir      *Directory
	pipe     *extpipe.Pipeline
	secrets  *componentcred.Store
	emitter  *Emitter
	clock    *clock
}

// testBox is a secret store key for the notifiers' sealed credentials.
func testBox(t *testing.T) *sealbox.Box {
	t.Helper()
	box, err := sealbox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		mem: store.NewMemoryNotifications(), settings: store.NewMemoryPlatformSettings(), installs: store.NewMemoryComponentInstall(),
		audit: store.NewMemoryExtensionAudit(), clock: &clock{t: time.UnixMilli(1_800_000_000_000)},
	}
	f.dir = NewDirectory(f.settings)
	f.pipe = &extpipe.Pipeline{Settings: f.settings, Audit: f.audit, AllowUnsigned: true, Installers: map[extreg.Kind]extpipe.KindInstaller{extreg.KindNotifier: f.dir.Installer()}}
	f.secrets = &componentcred.Store{Settings: f.settings, Box: testBox(t)}
	f.emitter = &Emitter{
		Prefs: f.mem, Outbox: f.mem, Notifiers: f.dir, Installs: f.installs, Now: f.clock.Now, PublicURL: "https://bnbloc.example.com/",
		RoomName: func(context.Context, string) string { return "Design" },
	}
	return f
}

func (f *fixture) worker() *Worker {
	return &Worker{Outbox: f.mem, Notifiers: f.dir, Secrets: f.secrets, Audit: f.audit, Now: f.clock.Now}
}

// notifierPackage is an unsigned notifier package for baseURL (dev install).
func notifierPackage(t *testing.T, name, baseURL string) []byte {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"kind": pkgmeta.KindNotifierPackageV1, "name": name, "version": "0.1.0", "publisher": "test",
		"runtime": "endpoint", "contract": pkgmeta.NotifierContractV1,
		"endpoint":    map[string]any{"baseUrl": baseURL},
		"permissions": map[string]any{"network": []string{u.Hostname()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// install installs (and activates) a notifier at baseURL and stores its
// signing secret, returning the secret.
func (f *fixture) install(t *testing.T, name, baseURL, rollout string) string {
	t.Helper()
	if _, err := f.pipe.Install(context.Background(), extpipe.InstallRequest{Body: notifierPackage(t, name, baseURL), Actor: "root", Rollout: rollout}); err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
	secret, _, err := f.secrets.Ensure(context.Background(), string(extreg.KindNotifier), name, "root")
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// fakeNotifier serves the notifier contract; deliver handles POSTs.
func fakeNotifier(t *testing.T, deliver http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hook"+DiscoverPath:
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "fake", "contract": pkgmeta.NotifierContractV1})
		case r.Method == http.MethodPost && r.URL.Path == "/hook"+DeliveryPath && deliver != nil:
			deliver(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fixture) payload(t *testing.T, typ, dedup, notifier string) (wire.NotificationPayload, bool) {
	t.Helper()
	it, ok := f.mem.Item(tid, EventID(tid, typ, dedup), notifier)
	if !ok {
		return wire.NotificationPayload{}, false
	}
	var p wire.NotificationPayload
	if err := json.Unmarshal(it.EventJSON, &p); err != nil {
		t.Fatal(err)
	}
	return p, true
}

func userIDs(p wire.NotificationPayload) []string {
	out := []string{}
	for _, r := range p.Recipients {
		out = append(out, r.UserID)
	}
	return out
}

func TestSignVerify(t *testing.T) {
	body := []byte(`{"id":"ntf_1"}`)
	now := time.Unix(1_800_000_000, 0)
	h := Sign("secret", now, body)
	if !strings.HasPrefix(h, "t=1800000000,v1=") {
		t.Fatalf("header = %q", h)
	}
	if err := Verify(h, "secret", body, now.Add(time.Minute), SignatureTolerance); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := Verify(h, "other", body, now, SignatureTolerance); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("wrong secret = %v", err)
	}
	if err := Verify(h, "secret", []byte(`{"id":"ntf_2"}`), now, SignatureTolerance); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("tampered body = %v", err)
	}
	if err := Verify(h, "secret", body, now.Add(6*time.Minute), SignatureTolerance); !errors.Is(err, ErrSignatureExpired) {
		t.Fatalf("stale = %v", err)
	}
	if err := Verify("garbage", "secret", body, now, SignatureTolerance); !errors.Is(err, ErrSignatureMalformed) {
		t.Fatalf("malformed = %v", err)
	}
}

func TestMentionCandidates(t *testing.T) {
	got := MentionCandidates("hey @bob, and @carol@corp.example.com! cc @bob @Bob @jack/codex email@x.com (@dave) @")
	want := []string{"bob", "carol@corp.example.com", "Bob"} // "Bob" is another subject
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if got := MentionCandidates("@alice\n@eve."); !reflect.DeepEqual(got, []string{"alice", "eve"}) {
		t.Fatalf("line-start mentions = %v", got)
	}
}

func TestEmitterFiltersPrefsQuietRoomsActorAndCollapses(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	ctx := tctx()
	_ = f.mem.PutNotificationPrefs(ctx, "carol", store.NotificationPrefs{Mentions: false, AgentFinished: true, AgentFailed: true, BorrowRequests: true})
	_ = f.mem.PutNotificationPrefs(ctx, "dave", store.NotificationPrefs{Mentions: true, AgentFinished: true, AgentFailed: true, BorrowRequests: true, QuietRooms: []string{"r1"}})

	ok, err := f.emitter.Emit(ctx, Event{
		Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"alice", "bob", "carol", "dave", "bob", "Alice"},
		Actor: "alice", Title: "alice mentioned you", Text: strings.Repeat("x", 900), DedupKey: "msg:r1:1",
	})
	if err != nil || !ok {
		t.Fatalf("emit = %v %v", ok, err)
	}
	p, found := f.payload(t, wire.NotificationMention, "msg:r1:1", "n1")
	if !found {
		t.Fatal("no outbox item")
	}
	// alice is the actor, carol turned mentions off, dave muted the room;
	// "Alice" is another subject than the actor (subjects are case-sensitive).
	if !reflect.DeepEqual(userIDs(p), []string{"bob", "Alice"}) {
		t.Fatalf("recipients = %+v, want [bob Alice]", p.Recipients)
	}
	if p.Title != "alice mentioned you in Design" || p.RoomName != "Design" || p.TenantID != tid || p.Actor != "alice" {
		t.Fatalf("payload = %+v", p)
	}
	if p.URL != "https://bnbloc.example.com/#/room/r1" {
		t.Fatalf("url = %q", p.URL)
	}
	if n := len([]rune(p.Text)); n != maxTextRunes {
		t.Fatalf("text runes = %d, want truncated to %d", n, maxTextRunes)
	}
	// Same logical event again: no second item.
	if ok, _ := f.emitter.Emit(ctx, Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"erin"}, Actor: "alice", DedupKey: "msg:r1:1"}); ok {
		t.Fatal("duplicate event enqueued twice")
	}
	// Burst: bob is inside the 30s mention window for r1.
	if ok, _ := f.emitter.Emit(ctx, Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob"}, Actor: "alice", DedupKey: "msg:r1:2"}); ok {
		t.Fatal("burst not collapsed")
	}
	// Another room is a separate window.
	if ok, _ := f.emitter.Emit(ctx, Event{Type: wire.NotificationMention, RoomID: "r2", Recipients: []string{"bob"}, Actor: "alice", DedupKey: "msg:r2:1"}); !ok {
		t.Fatal("other room collapsed")
	}
	f.clock.Add(31 * time.Second)
	if ok, _ := f.emitter.Emit(ctx, Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob"}, Actor: "alice", DedupKey: "msg:r1:3"}); !ok {
		t.Fatal("window did not reopen")
	}
}

func TestEmitterNoopWithoutVisibleNotifier(t *testing.T) {
	f := newFixture(t)
	ev := Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob"}, Actor: "alice", DedupKey: "m"}
	if ok, err := f.emitter.Emit(tctx(), ev); ok || err != nil {
		t.Fatalf("emit without notifier = %v %v", ok, err)
	}
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", extpipe.RolloutOff)
	if ok, _ := f.emitter.Emit(tctx(), ev); ok {
		t.Fatal("emit through a rollout-off notifier enqueued")
	}
	if err := f.pipe.SetRollout(context.Background(), extreg.KindNotifier, "n1", extpipe.RolloutAuto, "root"); err != nil {
		t.Fatal(err)
	}
	if err := f.pipe.SetTenants(context.Background(), extreg.KindNotifier, "n1", []string{"other-tenant"}, "root"); err != nil {
		t.Fatal(err)
	}
	f.dir.valid = false // the tenant write bypassed this directory
	if ok, _ := f.emitter.Emit(tctx(), ev); ok {
		t.Fatal("emit through a notifier scoped to another tenant enqueued")
	}
	if f.mem.Len() != 0 {
		t.Fatal("outbox not empty")
	}
}

func TestEmitterOneItemPerNotifierAndOptInFiltersRecipients(t *testing.T) {
	f := newFixture(t)
	url := fakeNotifier(t, nil).URL + "/hook"
	f.install(t, "all", url, "")
	f.install(t, "chosen", url, extpipe.RolloutOptIn)
	_ = f.installs.PutComponentInstall(context.Background(), "carol", string(extreg.KindNotifier), "chosen", 1)
	if ok, err := f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFinished, RoomID: "r1", Recipients: []string{"bob", "carol"}, Actor: "x/codex", DedupKey: "job:1"}); !ok || err != nil {
		t.Fatalf("emit = %v %v", ok, err)
	}
	all, ok := f.payload(t, wire.NotificationAgentFinished, "job:1", "all")
	if !ok || !reflect.DeepEqual(userIDs(all), []string{"bob", "carol"}) {
		t.Fatalf("auto notifier item = %+v ok=%v", all, ok)
	}
	chosen, ok := f.payload(t, wire.NotificationAgentFinished, "job:1", "chosen")
	if !ok || !reflect.DeepEqual(userIDs(chosen), []string{"carol"}) || chosen.ID != all.ID {
		t.Fatalf("opt-in notifier item = %+v ok=%v", chosen, ok)
	}
	// Nobody opted in: no item for the opt-in notifier.
	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFinished, RoomID: "r1", Recipients: []string{"dave"}, Actor: "x/codex", DedupKey: "job:2"})
	if _, ok := f.payload(t, wire.NotificationAgentFinished, "job:2", "chosen"); ok {
		t.Fatal("opt-in notifier got a member who did not install it")
	}
}

// fakeMembership is a static (room, user) member set.
type fakeMembership map[string]bool

func (m fakeMembership) Lookup(roomID, userID string) (membership.Member, bool) {
	return membership.Member{Role: store.RoleMember}, m[roomID+"/"+userID]
}

func TestObserverMentionAndJobEvents(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	o := NewObserver(f.emitter, fakeMembership{"r1/bob": true, "r1/alice": true}, 8)

	// A human message mentioning a member and a non-member.
	o.Observe(tctx(), wire.Event{RoomID: "r1", Sequence: 7, Kind: "chat_message", Payload: map[string]any{"userId": "alice", "text": "@bob @mallory @alice look"}})
	// Messages without '@' and irrelevant kinds are not even queued.
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "chat_message", Payload: map[string]any{"userId": "alice", "text": "plain"}})
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "presence_typing", Payload: map[string]any{}})
	// A typed job (in-process publisher) that replied.
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "agent_job_updated", Payload: store.AgentJob{
		JobID: "job_1", RequesterUserID: "alice", OwnerUserID: "bob", Provider: "codex", MessageText: "fix the build", Status: store.AgentJobReplied,
	}})
	// A decoded-JSON job (internal ingest) refused for lack of borrow
	// authorization: requester gets agent_failed, owner a borrow_request.
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "agent_job_updated", Payload: map[string]any{
		"jobId": "job_2", "requesterUserId": "carol", "ownerUserId": "bob", "provider": "codex",
		"messageText": "run tests", "status": "failed", "failureReason": "borrow_not_authorized",
	}})
	// Non-terminal and cancelled jobs produce nothing.
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "agent_job_updated", Payload: store.AgentJob{JobID: "job_3", RequesterUserID: "alice", Status: store.AgentJobRunning}})
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "agent_job_updated", Payload: store.AgentJob{JobID: "job_4", RequesterUserID: "alice", Status: store.AgentJobFailed, FailureReason: store.FailureCancelled}})
	if got := len(o.queue); got != 4 {
		t.Fatalf("queued %d observations, want 4", got)
	}
	for len(o.queue) > 0 {
		o.process(context.Background(), <-o.queue)
	}

	m, ok := f.payload(t, wire.NotificationMention, "msg:r1:7", "n1")
	if !ok || !reflect.DeepEqual(userIDs(m), []string{"bob"}) || m.Actor != "alice" {
		t.Fatalf("mention = %+v ok=%v", m, ok)
	}
	fin, ok := f.payload(t, wire.NotificationAgentFinished, "job:job_1", "n1")
	if !ok || !reflect.DeepEqual(userIDs(fin), []string{"alice"}) || fin.Actor != "bob/codex" || !strings.Contains(fin.Text, "fix the build") {
		t.Fatalf("agent_finished = %+v ok=%v", fin, ok)
	}
	failed, ok := f.payload(t, wire.NotificationAgentFailed, "job:job_2", "n1")
	if !ok || !reflect.DeepEqual(userIDs(failed), []string{"carol"}) {
		t.Fatalf("agent_failed = %+v ok=%v", failed, ok)
	}
	br, ok := f.payload(t, wire.NotificationBorrowRequest, "borrow:job_2", "n1")
	if !ok || !reflect.DeepEqual(userIDs(br), []string{"bob"}) || br.Actor != "carol" {
		t.Fatalf("borrow_request = %+v ok=%v", br, ok)
	}
	if f.mem.Len() != 4 {
		t.Fatalf("outbox has %d items, want 4", f.mem.Len())
	}
}

func TestObserverNeverBlocks(t *testing.T) {
	f := newFixture(t)
	o := NewObserver(f.emitter, fakeMembership{}, 1)
	ev := wire.Event{RoomID: "r1", Kind: "chat_message", Payload: map[string]any{"userId": "a", "text": "@b"}}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			o.Observe(tctx(), ev)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Observe blocked on a full queue")
	}
	if o.Dropped() != 99 {
		t.Fatalf("dropped = %d, want 99", o.Dropped())
	}
}

func TestWorkerDeliversSignedAndBacksOff(t *testing.T) {
	var mu sync.Mutex
	status := http.StatusServiceUnavailable
	var got []*http.Request
	var bodies [][]byte
	srv := fakeNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r)
		bodies = append(bodies, b)
		code := status
		mu.Unlock()
		w.WriteHeader(code)
	})
	f := newFixture(t)
	secret := f.install(t, "n1", srv.URL+"/hook", "")
	w := f.worker()
	w.MaxAttempts, w.BaseBackoff = 3, 10*time.Second
	if ok, err := f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFinished, RoomID: "r1", Recipients: []string{"bob"}, Actor: "bob/codex", Title: "done", DedupKey: "job:1"}); !ok || err != nil {
		t.Fatalf("emit = %v %v", ok, err)
	}
	id := EventID(tid, wire.NotificationAgentFinished, "job:1")

	// Attempt 1 fails (503) -> retried after ~10s (±20%).
	if n, err := w.DrainOnce(context.Background()); n != 1 || err != nil {
		t.Fatalf("drain = %d %v", n, err)
	}
	it, _ := f.mem.Item(tid, id, "n1")
	delay := time.Duration(it.NextAttemptAtMs-f.clock.Now().UnixMilli()) * time.Millisecond
	if it.Attempts != 1 || it.DeliveredAtMs != 0 || delay < 8*time.Second || delay > 12*time.Second || !strings.Contains(it.LastError, "503") {
		t.Fatalf("after failure: %+v (delay %s)", it, delay)
	}
	if n, _ := w.DrainOnce(context.Background()); n != 0 {
		t.Fatal("retried before the backoff elapsed")
	}
	// The request was signed with the component secret over the exact body,
	// with the event id header.
	r := got[0]
	if r.Header.Get(wire.NotificationEventIDHeader) != id {
		t.Fatalf("event id header = %q", r.Header.Get(wire.NotificationEventIDHeader))
	}
	if err := Verify(r.Header.Get(wire.NotificationSignatureHeader), secret, bodies[0], f.clock.Now(), SignatureTolerance); err != nil {
		t.Fatalf("signature: %v", err)
	}

	// Attempt 2 succeeds.
	f.clock.Add(13 * time.Second)
	mu.Lock()
	status = http.StatusNoContent
	mu.Unlock()
	if n, _ := w.DrainOnce(context.Background()); n != 1 {
		t.Fatal("due retry not claimed")
	}
	it, _ = f.mem.Item(tid, id, "n1")
	if it.DeliveredAtMs == 0 || it.Attempts != 2 {
		t.Fatalf("after success: %+v", it)
	}
	recs, _ := f.audit.AuditLog(context.Background(), store.AuditLogQuery{AllTenants: true, Kinds: []string{string(extreg.KindNotifier)}, Limit: 10})
	var deliveries []store.AuditLogRow
	for _, rec := range recs {
		if rec.Action == "deliver" {
			deliveries = append(deliveries, rec)
		}
	}
	if len(deliveries) != 2 || deliveries[0].Outcome != "delivered" || deliveries[1].Outcome != "retry" || deliveries[0].Name != "n1" {
		t.Fatalf("audit = %+v", deliveries)
	}
}

func TestWorkerRetriesOnlyRetryableRecipients(t *testing.T) {
	var mu sync.Mutex
	var seen [][]string
	srv := fakeNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		var p wire.NotificationPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		round := len(seen)
		seen = append(seen, userIDs(p))
		mu.Unlock()
		var out wire.NotificationDeliveryResponse
		for _, rcpt := range p.Recipients {
			res := wire.NotificationRecipientResult{UserID: rcpt.UserID, OK: true}
			switch {
			case rcpt.UserID == "bob" && round == 0:
				res = wire.NotificationRecipientResult{UserID: "bob", Error: "bot api 503", Retryable: true}
			case rcpt.UserID == "carol":
				res = wire.NotificationRecipientResult{UserID: "carol", Error: "no bot for recipient"}
			}
			out.Results = append(out.Results, res)
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	f := newFixture(t)
	f.install(t, "n1", srv.URL+"/hook", "")
	w := f.worker()
	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFinished, RoomID: "r1", Recipients: []string{"bob", "carol", "dave"}, Actor: "x/codex", DedupKey: "job:7"})
	id := EventID(tid, wire.NotificationAgentFinished, "job:7")
	_, _ = w.DrainOnce(context.Background())
	it, _ := f.mem.Item(tid, id, "n1")
	if it.DeliveredAtMs != 0 || it.FailedAtMs != 0 || !strings.Contains(it.LastError, "bob (retry)") || !strings.Contains(it.LastError, "carol: no bot") {
		t.Fatalf("after partial failure: %+v", it)
	}
	f.clock.Add(time.Minute)
	_, _ = w.DrainOnce(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || !reflect.DeepEqual(seen[0], []string{"bob", "carol", "dave"}) || !reflect.DeepEqual(seen[1], []string{"bob"}) {
		t.Fatalf("delivery rounds = %v; want the retry narrowed to bob", seen)
	}
	if it, _ := f.mem.Item(tid, id, "n1"); it.DeliveredAtMs == 0 {
		t.Fatalf("retry not delivered: %+v", it)
	}
}

func TestWorkerGivesUpAfterMaxAttemptsAndOnUninstall(t *testing.T) {
	srv := fakeNotifier(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	f := newFixture(t)
	f.install(t, "n1", srv.URL+"/hook", "")
	w := f.worker()
	w.MaxAttempts, w.BaseBackoff, w.MaxBackoff = 3, time.Second, 2*time.Second
	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFailed, RoomID: "r1", Recipients: []string{"bob"}, Actor: "x", DedupKey: "job:9"})
	id := EventID(tid, wire.NotificationAgentFailed, "job:9")
	for i := 0; i < 3; i++ {
		_, _ = w.DrainOnce(context.Background())
		f.clock.Add(time.Minute)
	}
	it, _ := f.mem.Item(tid, id, "n1")
	if it.FailedAtMs == 0 || it.Attempts != 3 {
		t.Fatalf("after max attempts: %+v", it)
	}

	// A notifier uninstalled after enqueue fails the item without a request.
	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFailed, RoomID: "r1", Recipients: []string{"bob"}, Actor: "x", DedupKey: "job:10"})
	if err := f.pipe.Uninstall(context.Background(), extreg.KindNotifier, "n1", "root"); err != nil {
		t.Fatal(err)
	}
	_, _ = w.DrainOnce(context.Background())
	it, _ = f.mem.Item(tid, EventID(tid, wire.NotificationAgentFailed, "job:10"), "n1")
	if it.FailedAtMs == 0 || !strings.Contains(it.LastError, "uninstalled") {
		t.Fatalf("uninstalled notifier item: %+v", it)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	w := &Worker{BaseBackoff: 10 * time.Second, MaxBackoff: time.Minute}
	if d := w.backoff(0); d < 8*time.Second || d > 12*time.Second {
		t.Fatalf("backoff(0) = %s", d)
	}
	if d := w.backoff(2); d < 32*time.Second || d > 48*time.Second {
		t.Fatalf("backoff(2) = %s", d)
	}
	if d := w.backoff(10); d > 72*time.Second {
		t.Fatalf("backoff(10) = %s, want capped near 1m", d)
	}
}

func TestBorrowRequestedNotifiesOwnerAsync(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	runs := 5
	f.emitter.BorrowRequested(tctx(), store.QuotaRequest{
		RequestID: "qr_1", RoomID: "r1", OwnerUserID: "bob", RequesterUserID: "carol", Provider: "codex", RequestedRuns: &runs, Note: "release week",
	})
	deadline := time.Now().Add(2 * time.Second)
	var p wire.NotificationPayload
	var ok bool
	for time.Now().Before(deadline) {
		if p, ok = f.payload(t, wire.NotificationBorrowRequest, "quota:qr_1", "n1"); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok || !reflect.DeepEqual(userIDs(p), []string{"bob"}) || p.Actor != "carol" || p.Text != "Requested runs: 5. release week" {
		t.Fatalf("borrow request = %+v ok=%v", p, ok)
	}
}

// A platform agent's host (an agent-host membership: not a human member of
// the room) is never @-mentioned and never asked to authorize a borrow of
// its platform agent.
func TestObserverSkipsAgentHosts(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	o := NewObserver(f.emitter, fakeMembership{"r1/alice": true}, 8) // root only hosts an agent here
	o.Observe(tctx(), wire.Event{RoomID: "r1", Sequence: 9, Kind: "chat_message", Payload: map[string]any{"userId": "alice", "text": "@root hello"}})
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "agent_job_updated", Payload: store.AgentJob{
		JobID: "job_9", RequesterUserID: "alice", OwnerUserID: "root", Provider: "codex", MessageText: "go",
		Status: store.AgentJobFailed, FailureReason: store.FailureBorrowNotAuthorized,
	}})
	for len(o.queue) > 0 {
		o.process(context.Background(), <-o.queue)
	}
	if _, ok := f.payload(t, wire.NotificationMention, "msg:r1:9", "n1"); ok {
		t.Fatal("an agent host was notified of a mention")
	}
	if _, ok := f.payload(t, wire.NotificationBorrowRequest, "borrow:job_9", "n1"); ok {
		t.Fatal("an agent host was asked to authorize a borrow")
	}
	if _, ok := f.payload(t, wire.NotificationAgentFailed, "job:job_9", "n1"); !ok {
		t.Fatal("the requester must still hear that the run failed")
	}
}

// The content notifications follow their own preference switches.
func TestAllowsContentPrefs(t *testing.T) {
	p := store.DefaultNotificationPrefs()
	for _, typ := range []string{wire.NotificationContentShared, wire.NotificationContentReview} {
		if !Allows(p, typ, "") {
			t.Fatalf("%s off by default", typ)
		}
	}
	p.ContentShared = false
	if Allows(p, wire.NotificationContentShared, "") || !Allows(p, wire.NotificationContentReview, "") {
		t.Fatal("contentShared off must silence content_shared only")
	}
	p.ContentShared, p.ContentReview = true, false
	if !Allows(p, wire.NotificationContentShared, "") || Allows(p, wire.NotificationContentReview, "") {
		t.Fatal("contentReview off must silence content_review only")
	}
}
