package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// eventRecorder stands in front of a gateway's realtime: it takes the
// platform events the gateway hands off (refusing the first fail of them)
// and proxies everything else to the realtime that was there.
type eventRecorder struct {
	mu     sync.Mutex
	events []wire.PlatformEvent
	fail   int
	got    chan wire.PlatformEvent
}

func recordEvents(t *testing.T, svc *Service) *eventRecorder {
	t.Helper()
	rec := &eventRecorder{got: make(chan wire.PlatformEvent, 16)}
	var proxy http.Handler = http.NotFoundHandler()
	if svc.Backends.Realtime != "" {
		upstream, err := url.Parse(svc.Backends.Realtime)
		if err != nil {
			t.Fatal(err)
		}
		proxy = httputil.NewSingleHostReverseProxy(upstream)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/platform-events" {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(svcauth.HeaderUser) != svcauth.ServiceSubject("gateway") {
			t.Errorf("a platform event not from the gateway: %v", r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		var ev wire.PlatformEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Errorf("event body %s: %v", raw, err)
		}
		rec.mu.Lock()
		failing := rec.fail > 0
		if failing {
			rec.fail--
		}
		rec.mu.Unlock()
		if failing {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		rec.mu.Lock()
		rec.events = append(rec.events, ev)
		rec.mu.Unlock()
		rec.got <- ev
		_ = json.NewEncoder(w).Encode(wire.PlatformEventReply{})
	}))
	t.Cleanup(srv.Close)
	svc.Backends.Realtime = srv.URL
	svc.eventRetry = time.Millisecond
	return rec
}

// next waits for the next event handed off.
func (rec *eventRecorder) next(t *testing.T) wire.PlatformEvent {
	t.Helper()
	select {
	case ev := <-rec.got:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no platform event was handed to realtime")
		return wire.PlatformEvent{}
	}
}

// none checks no further event is handed off.
func (rec *eventRecorder) none(t *testing.T) {
	t.Helper()
	select {
	case ev := <-rec.got:
		t.Fatalf("an unexpected platform event: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// Every replica of a deploy asks at boot; one records platform.deployed and
// hands it to realtime (retrying while realtime starts). A restart of the
// same build records nothing; the next build records it with the previous
// version.
func TestDeployRecordedOncePerVersionAcrossReplicas(t *testing.T) {
	t.Setenv("BNBLOC_INTERNAL_KEY", "events-test-internal-key")
	settings := store.NewMemoryPlatformSettings()
	a, b := newGateway(t, ""), newGateway(t, "")
	a.PlatformSettings, b.PlatformSettings = settings, settings
	rec := recordEvents(t, a)
	b.Backends.Realtime, b.eventRetry = a.Backends.Realtime, time.Millisecond
	rec.fail = 2

	var wg sync.WaitGroup
	recorded := make([]bool, 8)
	for i := range recorded {
		gw := a
		if i%2 == 1 {
			gw = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorded[i] = gw.RecordDeploy(context.Background(), "abc123def4567890")
		}()
	}
	wg.Wait()
	n := 0
	for _, r := range recorded {
		if r {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d replicas recorded the deploy, want exactly one", n)
	}
	if ev := rec.next(t); ev.Event != wire.PlatformDeployed || ev.Version != "abc123def4567890" || ev.Previous != "" || ev.AtMs == 0 {
		t.Fatalf("deploy event = %+v", ev)
	}
	rec.none(t)

	// The same build again, stamped short or full, is no deploy.
	if a.RecordDeploy(context.Background(), "abc123def456") || b.RecordDeploy(context.Background(), "abc123def4567890") {
		t.Fatal("a restart of the same build recorded a deploy")
	}
	if a.RecordDeploy(context.Background(), "") {
		t.Fatal("an unstamped build recorded a deploy")
	}
	rec.none(t)
	if !b.RecordDeploy(context.Background(), "fedcba987654") {
		t.Fatal("a new build was not recorded")
	}
	if ev := rec.next(t); ev.Version != "fedcba987654" || ev.Previous != "abc123def4567890" {
		t.Fatalf("second deploy = %+v", ev)
	}
}

// An installed component's new version is component.published; installing
// the same version with the same bytes again is not.
func TestComponentPublishedOnNewVersionsOnly(t *testing.T) {
	t.Setenv("BNBLOC_INTERNAL_KEY", "events-test-internal-key")
	svc, srv, adminToken, _ := rolloutTestGateway(t)
	rec := recordEvents(t, svc)

	artifact := uploadComponentArtifact(t, srv.URL, adminToken, []byte("events sidecar"))
	body := sidecarPackageBodyWithArtifact(t, "event-core", artifact)
	install := func(body []byte) {
		t.Helper()
		if raw, status := postComponent(t, srv.URL, adminToken, body, ""); status != http.StatusOK {
			t.Fatalf("install = %d %s", status, raw)
		}
	}
	install(body)
	t.Cleanup(func() { _ = svc.corePipeline().Uninstall(context.Background(), "core", "event-core", "admin") })
	ev := rec.next(t)
	if ev.Event != wire.ComponentPublished || ev.Name != "event-core" || ev.Version != "0.2.0" || ev.Kind == "" || ev.Rollout != "auto" || len(ev.Tenants) != 0 {
		t.Fatalf("first install = %+v", ev)
	}
	install(body)
	rec.none(t)

	var pkg map[string]any
	if err := json.Unmarshal(body, &pkg); err != nil {
		t.Fatal(err)
	}
	pkg["version"] = "0.3.0"
	next, _ := json.Marshal(pkg)
	install(next)
	if ev := rec.next(t); ev.Name != "event-core" || ev.Version != "0.3.0" {
		t.Fatalf("new version = %+v", ev)
	}
	// A rollout change is no new version.
	if status := setRollout(t, srv, adminToken, "event-core", "optin"); status != http.StatusNoContent {
		t.Fatalf("rollout = %d", status)
	}
	rec.none(t)
}

// The updater serving a new desktop version is desktop.published, with the
// targets it has files for; saving the same version again is not.
func TestDesktopPublishedOnNewVersionsOnly(t *testing.T) {
	t.Setenv("BNBLOC_INTERNAL_KEY", "events-test-internal-key")
	svc, srv, adminToken, _ := rolloutTestGateway(t)
	rec := recordEvents(t, svc)
	put := func(cfg wire.UpdateConfig) {
		t.Helper()
		raw, _ := json.Marshal(cfg)
		if status := adminCall(t, srv, adminToken, http.MethodPut, "/v1/platform/update-config", string(raw)); status != http.StatusOK {
			t.Fatalf("put update config = %d", status)
		}
	}
	off := testUpdateConfig()
	off.Policy.Enabled = false
	put(off)
	rec.none(t)
	cfg := testUpdateConfig()
	put(cfg)
	if ev := rec.next(t); ev.Event != wire.DesktopPublished || ev.Version != "0.1.10" || strings.Join(ev.Targets, ",") != "darwin-aarch64" {
		t.Fatalf("desktop event = %+v", ev)
	}
	cfg.Release.Platforms["darwin-x86_64"] = wire.UpdatePlatform{URL: "https://updates.example/x86.tar.gz", Signature: "sig"}
	put(cfg)
	rec.none(t)
	cfg.Release.Version = "0.1.11"
	put(cfg)
	if ev := rec.next(t); ev.Version != "0.1.11" || strings.Join(ev.Targets, ",") != "darwin-aarch64,darwin-x86_64" {
		t.Fatalf("next desktop event = %+v", ev)
	}
}

// End to end: platform content installed for every organization reaches
// realtime, which sets the run of a schedule following component
// publishing, with the content's facts.
func TestPlatformContentPublishSetsFollowingSchedules(t *testing.T) {
	e := galleryGW(t)
	e.gw.eventRetry = time.Millisecond
	sc := wire.RoomSchedule{ScheduleID: "sch_follow", RoomID: e.roomID, CreatedBy: "maya", Text: "Draft the release notes.", Enabled: true,
		Recipient:  wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom},
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurEvent, Events: []string{wire.ComponentPublished}, Component: "release-notes"}, Timezone: "UTC"}
	if _, err := e.b.Schedules.CreateSchedule(e.ctx(), sc); err != nil {
		t.Fatal(err)
	}
	e.publishDefault(map[string]any{"kind": "skill", "name": "release-notes", "displayName": "Release notes", "description": "Write release notes.",
		"skills": []map[string]string{{"name": "release-notes", "description": "Write release notes.", "body": "Write them."}}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := e.b.Schedules.GetSchedule(e.ctx(), e.roomID, sc.ScheduleID)
		if err != nil {
			t.Fatal(err)
		}
		if got.NextRunAtMs != 0 {
			if got.Event == nil || got.Event.Event != wire.ComponentPublished || got.Event.Kind != "skill" || got.Event.Name != "release-notes" || got.Event.Version == "" || got.Event.Rollout != "auto" {
				t.Fatalf("the schedule's event = %+v", got.Event)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the platform content install never set the following schedule's run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Platform events have one owner: only platform_events.go makes one and
// hands it to realtime (the publisher's PlatformEvent), and only it moves
// an event's mark.
func TestPlatformEventsSingleOwner(t *testing.T) {
	const owner = "services/gateway/platform_events.go"
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "cmd"}}, func(rel string, src []byte) {
		text := string(src)
		if rel != owner && rel != "internal/realtime/publisher.go" && strings.Contains(text, ".PlatformEvent(") {
			t.Errorf("%s hands a platform event to realtime: record it through %s (recordPlatformEvent)", rel, owner)
		}
		if rel != owner && strings.HasPrefix(rel, "services/gateway/") && (strings.Contains(text, "wire.PlatformEvent{") || strings.Contains(text, "MovePlatformSetting(")) {
			t.Errorf("%s makes a platform event or moves its mark: only %s does", rel, owner)
		}
	})
}

// A run an event set carries the event's facts: fenced as data in an AI
// task's text, as a plain last line of a notice people read.
func TestEventRunTextCarriesTheFacts(t *testing.T) {
	e, model, outbox := scheduleGW(t)
	slot := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	ev := &wire.PlatformEvent{Event: wire.PlatformDeployed, Version: "abc123", Previous: "9f8e7d"}
	onEvent := wire.ScheduleRecurrence{Kind: wire.RecurEvent, Events: []string{wire.PlatformDeployed}}

	ai := wire.RoomSchedule{ScheduleID: "sch_ai", Text: "Run the end-to-end test plan.", Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI}, Recurrence: onEvent, Event: ev}
	if code, reply := e.runSlot(ai, slot, false); code != http.StatusOK || reply.Status != wire.ScheduleRunOK {
		t.Fatalf("ai run = %d %+v", code, reply)
	}
	model.mu.Lock()
	sent, _ := json.Marshal(model.seen[len(model.seen)-1])
	model.mu.Unlock()
	if !strings.Contains(string(sent), "Run the end-to-end test plan.") || !strings.Contains(string(sent), "untrusted-content") ||
		!strings.Contains(string(sent), "platform.deployed version=abc123 previous=9f8e7d") {
		t.Fatalf("the model was not given the event's facts fenced: %s", sent)
	}

	notice := noticeSchedule([]string{"nora"}, "Deployed", "A new build is live.", false)
	notice.Recurrence, notice.Event = onEvent, ev
	if code, reply := e.runSlot(notice, slot, false); code != http.StatusOK || reply.Status != wire.ScheduleRunOK {
		t.Fatalf("notice run = %d %+v", code, reply)
	}
	got := queued(t, outbox)
	if len(got) != 1 || got[0].Text != "A new build is live.\nplatform.deployed version=abc123 previous=9f8e7d" {
		t.Fatalf("notices = %+v", got)
	}
}

// An organization's own publish of a member's component reaches only that
// organization; publishing the version it already uses again is no event.
func TestOrgPublishIsAnOrganizationEvent(t *testing.T) {
	g := newSharingGateway(t)
	rec := recordEvents(t, g.svc)
	g.setApproval(wire.PublishApprovalNone)
	src := g.create("maya", expertForm("Review carefully."))
	publish := func() int {
		code, _ := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org"})
		return code
	}
	if code := publish(); code != http.StatusCreated {
		t.Fatalf("publish = %d", code)
	}
	ev := rec.next(t)
	if ev.Event != wire.ComponentPublished || ev.Kind != "expert" || ev.Name != src.Name || ev.Version != "1.0.0" || strings.Join(ev.Tenants, ",") != "home" {
		t.Fatalf("org publish event = %+v", ev)
	}
	if code := publish(); code != http.StatusConflict {
		t.Fatalf("publishing the same version again = %d", code)
	}
	rec.none(t)
	form := expertForm("Review even more carefully.")
	form["baseVersion"] = "1.0.0"
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/versions", form); code != http.StatusCreated {
		t.Fatalf("new version = %d %s", code, raw)
	}
	if ev := rec.next(t); ev.Version != "1.0.1" || strings.Join(ev.Tenants, ",") != "home" {
		t.Fatalf("new org version event = %+v", ev)
	}
}
