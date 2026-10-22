package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/pluginruntime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeDesktop is a DesktopRoute: members in has hold a desktop stream; each
// delivery is recorded with its outcome callback.
type fakeDesktop struct {
	mu        sync.Mutex
	has       map[string]bool
	delivered []wire.PluginNotification
	done      []func(bool)
}

func (d *fakeDesktop) HasDesktop(tenantID, member string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return tenantID == tid && d.has[member]
}

func (d *fakeDesktop) DeliverNotification(_ string, n wire.PluginNotification, done func(bool)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.delivered = append(d.delivered, n)
	d.done = append(d.done, done)
}

// outcome reports delivery i's outcome synchronously (the dispatcher runs it
// on its own goroutine; the worker serializes outcomes itself).
func (d *fakeDesktop) outcome(i int, delivered bool) {
	d.mu.Lock()
	fn := d.done[i]
	d.mu.Unlock()
	fn(delivered)
}

// installNotificationPlugin installs a notification.v1 plugin package and
// points the emitter's plugin directory at the records.
func (f *fixture) installNotificationPlugin(t *testing.T) {
	t.Helper()
	f.pipe.Installers[pkgmeta.KindPlugin] = pluginruntime.Installer(nil, nil)
	body, err := json.Marshal(map[string]any{
		"kind": pkgmeta.KindPluginPackageV1, "name": "personal-bot", "version": "0.1.0", "publisher": "test",
		"runtime": "sidecar", "contract": pkgmeta.PluginContractV1, "hooks": []string{pkgmeta.NotificationHook},
		"artifacts":   map[string]any{"darwin-arm64": map[string]string{"url": "https://gw.example/plugin", "sha256": strings.Repeat("0", 64)}},
		"sidecar":     map[string]any{"localUrl": "http://127.0.0.1:7193", "listenEnv": "BNBLOC_PLUGIN_LISTEN"},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pipe.Install(context.Background(), extpipe.InstallRequest{Body: body, Actor: "root"}); err != nil {
		t.Fatalf("install plugin: %v", err)
	}
	f.emitter.Plugins = NewPluginDirectory(f.settings)
}

func (f *fixture) desktopWorker(desk DesktopRoute) *Worker {
	w := f.worker()
	w.Desktop, w.Emitter = desk, f.emitter
	return w
}

func (f *fixture) desktopItem(t *testing.T, eventID string) (store.NotificationOutboxItem, desktopItem) {
	t.Helper()
	it, ok := f.mem.Item(tid, eventID, DesktopChannel)
	if !ok {
		t.Fatalf("no %s item for %s", DesktopChannel, eventID)
	}
	var d desktopItem
	if err := json.Unmarshal(it.EventJSON, &d); err != nil {
		t.Fatal(err)
	}
	return it, d
}

func (f *fixture) rowPayload(t *testing.T, rowID, notifier string) (wire.NotificationPayload, bool) {
	t.Helper()
	it, ok := f.mem.Item(tid, rowID, notifier)
	if !ok {
		return wire.NotificationPayload{}, false
	}
	var p wire.NotificationPayload
	if err := json.Unmarshal(it.EventJSON, &p); err != nil {
		t.Fatal(err)
	}
	return p, true
}

// With a notification plugin installed, an event is queued once on the
// desktop channel (no server item yet). The worker routes a member with a
// desktop to their plugin and everyone else to the server notifiers under
// the event id; a plugin that does not deliver queues that member's server
// delivery (same event id) exactly once, and the item finishes.
func TestDesktopChannelRoutesAndFallsBack(t *testing.T) {
	f := newFixture(t)
	f.install(t, "all", fakeNotifier(t, nil).URL+"/hook", "")
	f.installNotificationPlugin(t)
	desk := &fakeDesktop{has: map[string]bool{"bob": true}}
	w := f.desktopWorker(desk)

	reply := &wire.NotificationReply{ChannelID: "ch_1", Recipient: &wire.NotificationReplyRecipient{OwnerID: "x", Provider: "codex"}}
	ev := Event{Type: wire.NotificationAgentFinished, RoomID: "r1", Recipients: []string{"bob", "carol"}, Actor: "x/codex", DedupKey: "job:d1", Seq: 7, ChannelID: "ch_1", Reply: reply}
	if ok, err := f.emitter.Emit(tctx(), ev); !ok || err != nil {
		t.Fatalf("emit = %v %v", ok, err)
	}
	id := EventID(tid, ev.Type, ev.DedupKey)
	if _, found := f.rowPayload(t, id, "all"); found {
		t.Fatal("server item queued before routing")
	}
	if _, d := f.desktopItem(t, id); !reflect.DeepEqual(userIDs(d.Payload), []string{"bob", "carol"}) || d.Seq != 7 {
		t.Fatalf("desktop item = %+v", d)
	}

	if _, err := w.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	server, found := f.rowPayload(t, id, "all")
	if !found || !reflect.DeepEqual(userIDs(server), []string{"carol"}) {
		t.Fatalf("server item = %+v found=%v, want carol only", server, found)
	}
	if len(desk.delivered) != 1 {
		t.Fatalf("desktop deliveries = %+v", desk.delivered)
	}
	n := desk.delivered[0]
	if n.UserID != "bob" || n.ID != id || n.Seq != 7 || n.ChannelID != "ch_1" || n.RoomName != "Design" || n.URL != server.URL ||
		n.Reply == nil || n.Reply.To != wire.PluginReplyToAgent || n.Reply.RoomID != "r1" || n.Reply.Agent.Provider != "codex" {
		t.Fatalf("desktop notification = %+v", n)
	}
	it, d := f.desktopItem(t, id)
	if it.DeliveredAtMs != 0 || !d.Routed || !reflect.DeepEqual(userIDs(d.Payload), []string{"bob"}) || it.Attempts != 0 {
		t.Fatalf("dispatched item = %+v %+v", it, d)
	}

	desk.outcome(0, false)
	fb, found := f.rowPayload(t, FallbackRowID(id, "bob"), "all")
	if !found || fb.ID != id || !reflect.DeepEqual(userIDs(fb), []string{"bob"}) || fb.Reply == nil {
		t.Fatalf("fallback = %+v found=%v", fb, found)
	}
	if it, _ := f.desktopItem(t, id); it.DeliveredAtMs == 0 || !strings.Contains(it.LastError, "bob") {
		t.Fatalf("desktop item not finished: %+v", it)
	}
	if len(FallbackRowID(id, "someone@example.com")) > 64 {
		t.Fatal("fallback row id exceeds the outbox id column")
	}
}

// A delivered outcome finishes the item without any server delivery for
// that member; a member without a desktop alone finishes it at once.
func TestDesktopChannelDeliveredNeedsNoFallback(t *testing.T) {
	f := newFixture(t)
	f.install(t, "all", fakeNotifier(t, nil).URL+"/hook", "")
	f.installNotificationPlugin(t)
	desk := &fakeDesktop{has: map[string]bool{"bob": true}}
	w := f.desktopWorker(desk)

	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob"}, Actor: "alice", DedupKey: "msg:1"})
	id := EventID(tid, wire.NotificationMention, "msg:1")
	_, _ = w.DrainOnce(context.Background())
	desk.outcome(0, true)
	if it, _ := f.desktopItem(t, id); it.DeliveredAtMs == 0 || it.LastError != "" {
		t.Fatalf("desktop item = %+v", it)
	}
	if _, found := f.rowPayload(t, FallbackRowID(id, "bob"), "all"); found {
		t.Fatal("delivered member fell back")
	}
	if _, found := f.rowPayload(t, id, "all"); found {
		t.Fatal("server item for nobody")
	}

	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"carol"}, Actor: "alice", DedupKey: "msg:2"})
	id2 := EventID(tid, wire.NotificationMention, "msg:2")
	_, _ = w.DrainOnce(context.Background())
	if it, _ := f.desktopItem(t, id2); it.DeliveredAtMs == 0 {
		t.Fatalf("desktop item without routed members not finished: %+v", it)
	}
	if p, found := f.rowPayload(t, id2, "all"); !found || !reflect.DeepEqual(userIDs(p), []string{"carol"}) {
		t.Fatalf("server item = %+v %v", p, found)
	}
}

// Restart safety: the routing worker dies with outcomes pending (one member
// already delivered, one not). A NEW worker instance on the same outbox does
// nothing before the outcome deadline, then falls back for the pending
// member only, exactly once; the old worker's late outcome changes nothing,
// and the notifier receives that member once.
func TestDesktopChannelSurvivesRestartAndFallsBackOnce(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	var got []wire.NotificationPayload
	f.install(t, "all", fakeNotifier(t, func(w http.ResponseWriter, r *http.Request) {
		var p wire.NotificationPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}).URL+"/hook", "")
	f.installNotificationPlugin(t)
	desk := &fakeDesktop{has: map[string]bool{"bob": true, "dave": true}}
	before := f.desktopWorker(desk)

	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationAgentFailed, RoomID: "r1", Recipients: []string{"bob", "dave"}, Actor: "x/codex", DedupKey: "job:r"})
	id := EventID(tid, wire.NotificationAgentFailed, "job:r")
	_, _ = before.DrainOnce(context.Background())
	desk.outcome(0, true) // bob's plugin delivered; dave's outcome is lost with the process
	if _, d := f.desktopItem(t, id); !reflect.DeepEqual(userIDs(d.Payload), []string{"dave"}) {
		t.Fatalf("pending after bob's ack = %+v", d.Payload.Recipients)
	}

	after := f.desktopWorker(&fakeDesktop{}) // restarted: no streams, no memory
	if n, _ := after.DrainOnce(context.Background()); n != 0 {
		t.Fatalf("claimed %d item(s) before the outcome deadline", n)
	}
	f.clock.Add(desktopOutcomeWindow + time.Second)
	if _, err := after.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it, _ := f.desktopItem(t, id); it.DeliveredAtMs == 0 || !strings.Contains(it.LastError, "dave") || strings.Contains(it.LastError, "bob") {
		t.Fatalf("desktop item after the deadline = %+v", it)
	}
	if _, found := f.rowPayload(t, FallbackRowID(id, "bob"), "all"); found {
		t.Fatal("a delivered member fell back after the restart")
	}
	rows := f.mem.Len()
	// The dead process's late outcome: lease gone, fallback row already there.
	desk.outcome(1, false)
	if f.mem.Len() != rows {
		t.Fatalf("late outcome queued again: %d rows, want %d", f.mem.Len(), rows)
	}
	for i := 0; i < 3; i++ {
		_, _ = after.DrainOnce(context.Background())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !reflect.DeepEqual(userIDs(got[0]), []string{"dave"}) || got[0].ID != id {
		t.Fatalf("notifier deliveries = %+v, want dave once", got)
	}
}

// The deadline fallback is idempotent: replaying it (a crash after the
// fallback rows were queued but before the item was recorded) queues
// nothing new.
func TestDesktopDeadlineFallbackIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.install(t, "all", fakeNotifier(t, nil).URL+"/hook", "")
	f.installNotificationPlugin(t)
	_, _ = f.emitter.Emit(tctx(), Event{Type: wire.NotificationMention, RoomID: "r1", Recipients: []string{"bob", "carol"}, Actor: "alice", DedupKey: "msg:i"})
	id := EventID(tid, wire.NotificationMention, "msg:i")
	_, _ = f.desktopWorker(&fakeDesktop{has: map[string]bool{"bob": true}}).DrainOnce(context.Background())
	it, d := f.desktopItem(t, id)
	w := f.desktopWorker(&fakeDesktop{})
	w.defaults()
	w.desktopDeadline(tctx(), "stale-token", it, d) // lease not held: the record fails, the rows exist
	rows := f.mem.Len()
	w.desktopDeadline(tctx(), "stale-token", it, d)
	if f.mem.Len() != rows {
		t.Fatalf("replayed fallback queued again: %d rows, want %d", f.mem.Len(), rows)
	}
	if p, ok := f.rowPayload(t, FallbackRowID(id, "bob"), "all"); !ok || !reflect.DeepEqual(userIDs(p), []string{"bob"}) {
		t.Fatalf("fallback = %+v %v", p, ok)
	}
	if p, ok := f.rowPayload(t, id, "all"); !ok || !reflect.DeepEqual(userIDs(p), []string{"carol"}) {
		t.Fatalf("server item = %+v %v", p, ok)
	}
}

// Every producer takes the same path: a borrow request (shareagent) and an
// admin alert (gateway) are queued on the desktop channel while a
// notification plugin is installed; an alert with no notifier is accepted
// when the desktop channel can carry it.
func TestBorrowRequestAndAlertUseTheDesktopChannel(t *testing.T) {
	f := newFixture(t)
	f.installNotificationPlugin(t)
	f.emitter.BorrowRequested(tctx(), store.QuotaRequest{RequestID: "qr_d", RoomID: "r1", OwnerUserID: "bob", RequesterUserID: "carol", Provider: "codex"})
	id := EventID(tid, wire.NotificationBorrowRequest, "quota:qr_d")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := f.mem.Item(tid, id, DesktopChannel); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, d := f.desktopItem(t, id); !reflect.DeepEqual(userIDs(d.Payload), []string{"bob"}) {
		t.Fatalf("borrow request desktop item = %+v", d)
	}

	res, err := f.emitter.SendAlert(tctx(), Alert{Title: "smoke failed", Recipients: []string{"root"}, Actor: "root", DedupKey: "a-d"})
	if err != nil || !reflect.DeepEqual(res.Notifiers, []string{DesktopChannel}) {
		t.Fatalf("alert = %+v %v", res, err)
	}
	if _, d := f.desktopItem(t, res.ID); !reflect.DeepEqual(userIDs(d.Payload), []string{"root"}) || d.Payload.URL != "" || d.Payload.Type != wire.NotificationAdminAlert {
		t.Fatalf("alert desktop item = %+v", d)
	}
}
