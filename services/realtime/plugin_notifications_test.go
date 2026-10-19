package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// notificationPluginBody is a plugin package subscribing to notification.v1
// (a personal-bot style plugin: host chat permission, local settings).
func notificationPluginBody(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"kind": pkgmeta.KindPluginPackageV1, "name": name, "version": "0.1.0", "publisher": "Acme Corp",
		"runtime": "sidecar", "contract": pkgmeta.PluginContractV1, "hooks": []string{pkgmeta.NotificationHook},
		"artifacts":   map[string]any{"darwin-arm64": map[string]string{"url": "https://gw.example/plugin", "sha256": strings.Repeat("0", 64)}},
		"sidecar":     map[string]any{"localUrl": "http://127.0.0.1:7193"},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}, "host": []string{pkgmeta.PluginHostChat}},
		"settings":    []map[string]any{{"key": "appKey", "label": "App key", "type": "secret", "scope": "member", "required": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *pluginFixture) installNotificationPlugin(t *testing.T, name, rollout string) {
	t.Helper()
	if _, err := f.pipe.Install(t.Context(), extpipe.InstallRequest{Body: notificationPluginBody(t, name), Actor: "admin", Rollout: rollout}); err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
}

// outcomes records the reported delivery outcomes (each runs on its own
// goroutine).
type outcomes struct {
	mu  sync.Mutex
	got []bool
}

func (o *outcomes) fn() func(bool) {
	return func(delivered bool) { o.mu.Lock(); o.got = append(o.got, delivered); o.mu.Unlock() }
}

func (o *outcomes) snapshot() []bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]bool(nil), o.got...)
}

// waitFor waits until the outcomes are exactly want (and stay so briefly):
// the outcome is reported once.
func (o *outcomes) waitFor(t *testing.T, want ...bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(o.snapshot()) < len(want) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond) // and no extra report right after
	if got := o.snapshot(); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Fatalf("outcomes = %v, want %v", got, want)
	}
}

func bobNotification(id string) wire.PluginNotification {
	return wire.PluginNotification{
		ID: id, Type: wire.NotificationAgentFinished, UserID: "bob", RoomID: "room-1", RoomName: "Design",
		ChannelID: "ch_1", Seq: 42, Actor: "carol/codex", Title: "carol/codex finished your request in Design", AtMs: 5,
		Reply: &wire.PluginNotificationReply{RoomID: "room-1", ChannelID: "ch_1", To: wire.PluginReplyToAgent,
			Agent: &wire.NotificationReplyRecipient{OwnerID: "carol", Provider: "codex", Core: "local"}},
	}
}

// A notification goes only to the member's own streams in the event's tenant
// as a notification.v1 envelope; the outcome is reported once: delivered, or
// not (failed ack, expiry, or nothing to deliver to).
func TestDeliverNotification_routesToMemberDesktopAndFallsBack(t *testing.T) {
	f := newPluginFixture(t, nil, "room-auditor") // a chat_message.v1 plugin: never sees notifications
	f.installNotificationPlugin(t, "personal-bot", "")
	bob := f.subscribe(t, "bob", tenant.Default)
	alice := f.subscribe(t, "alice", tenant.Default)
	bobElsewhere := f.subscribe(t, "bob", "acme")

	if !f.dispatcher.HasDesktop(tenant.Default, "bob") || f.dispatcher.HasDesktop(tenant.Default, "dave") || f.dispatcher.HasDesktop("other", "alice") {
		t.Fatal("HasDesktop must match member and session tenant")
	}

	// Delivered; envelope carries the member's notification.
	var c outcomes
	f.dispatcher.DeliverNotification(tenant.Default, bobNotification("ntf_1"), c.fn())
	d := expectDelivery(t, bob)
	expectNoDelivery(t, alice)
	expectNoDelivery(t, bobElsewhere)
	env := decodeEnvelope(t, d)
	if d.Plugin != "personal-bot" || env.Event != "notification" || env.Version != "1" || env.TenantID != tenant.Default || env.RoomID != "room-1" {
		t.Fatalf("delivery = %+v envelope = %+v", d, env)
	}
	var n wire.PluginNotification
	raw, _ := json.Marshal(env.Payload)
	if err := json.Unmarshal(raw, &n); err != nil || n.UserID != "bob" || n.Seq != 42 || n.Reply == nil || n.Reply.To != "agent" || n.Reply.Agent.Provider != "codex" {
		t.Fatalf("payload = %s (%v)", raw, err)
	}
	if !f.dispatcher.ack(t.Context(), "bob", d.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryDelivered}) {
		t.Fatal("ack refused")
	}
	c.waitFor(t, true)

	// Failed ack: not delivered, once.
	var failed outcomes
	f.dispatcher.DeliverNotification(tenant.Default, bobNotification("ntf_2"), failed.fn())
	d = expectDelivery(t, bob)
	f.dispatcher.ack(t.Context(), "bob", d.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryFailed, Detail: "bot API 503"})
	failed.waitFor(t, false)

	// Never acknowledged: the expiry sweep reports it.
	var expired outcomes
	f.dispatcher.DeliverNotification(tenant.Default, bobNotification("ntf_3"), expired.fn())
	expectDelivery(t, bob)
	expired.waitFor(t)
	f.clock.Add(pendingAckTimeout.Milliseconds() + 1)
	f.dispatcher.expirePending()
	expired.waitFor(t, false)

	// No stream of that member in the tenant: nothing offered.
	var nobody outcomes
	n2 := bobNotification("ntf_4")
	n2.UserID = "dave"
	f.dispatcher.DeliverNotification(tenant.Default, n2, nobody.fn())
	nobody.waitFor(t, false)

	recs := auditOutcomes(t, f.audit)
	var outcomes []string
	for _, r := range recs {
		if r.Detail == "notification.v1" || strings.HasPrefix(r.Detail, "notification.v1:") {
			outcomes = append(outcomes, r.Outcome)
		}
	}
	if len(outcomes) != 3 {
		t.Fatalf("notification audit = %+v", recs)
	}
}

// Two desktops of one member: one delivery is enough; "not delivered" is
// reported only when every desktop failed.
func TestDeliverNotification_anyDesktopDeliveringSuppressesFallback(t *testing.T) {
	f := newPluginFixture(t, nil)
	f.installNotificationPlugin(t, "personal-bot", "")
	one := f.subscribe(t, "bob", tenant.Default)
	two := f.subscribe(t, "bob", tenant.Default)

	var c outcomes
	f.dispatcher.DeliverNotification(tenant.Default, bobNotification("ntf_a"), c.fn())
	d1, d2 := expectDelivery(t, one), expectDelivery(t, two)
	f.dispatcher.ack(t.Context(), "bob", d1.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryFailed})
	f.dispatcher.ack(t.Context(), "bob", d2.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryDelivered})
	c.waitFor(t, true)

	var both outcomes
	f.dispatcher.DeliverNotification(tenant.Default, bobNotification("ntf_b"), both.fn())
	d1, d2 = expectDelivery(t, one), expectDelivery(t, two)
	f.dispatcher.ack(t.Context(), "bob", d1.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryFailed})
	both.waitFor(t)
	f.dispatcher.ack(t.Context(), "bob", d2.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryFailed})
	both.waitFor(t, false)
}

// Entitlement is G1's: an opt-in notification plugin reaches only members who
// installed it; rollout off reaches nobody. Not entitled = not delivered.
func TestDeliverNotification_honoursRolloutAndOptIn(t *testing.T) {
	f := newPluginFixture(t, nil)
	f.installNotificationPlugin(t, "personal-bot", extpipe.RolloutOptIn)
	bob := f.subscribe(t, "bob", tenant.Default)

	var c outcomes
	f.dispatcher.DeliverNotification(tenant.Default, bobNotification("ntf_o1"), c.fn())
	expectNoDelivery(t, bob)
	c.waitFor(t, false)

	if err := f.installs.PutComponentInstall(tenant.NewContext(t.Context(), tenant.Default), "bob", string(pkgmeta.KindPlugin), "personal-bot", 1); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(recordsTTL.Milliseconds() + 1) // the per-stream install cache refreshes
	var opted outcomes
	f.dispatcher.DeliverNotification(tenant.Default, bobNotification("ntf_o2"), opted.fn())
	d := expectDelivery(t, bob)
	f.dispatcher.ack(t.Context(), "bob", d.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryDelivered})
	opted.waitFor(t, true)
}

// notificationE2E is realtime's notification subsystem (emitter, outbox
// worker routing through the plugin dispatcher) with one server notifier
// ("corp", recording what it receives) and one notification plugin
// ("personal-bot") installed.
type notificationE2E struct {
	notes    *store.MemoryNotifications
	settings *store.MemoryPlatformSettings
	ns       *notify.Service
	f        *pluginFixture

	mu       sync.Mutex
	got      []wire.NotificationPayload
	eventIDs []string
}

func newNotificationE2E(t *testing.T, ctx context.Context, mem *membership.Static) *notificationE2E {
	t.Helper()
	e := &notificationE2E{notes: store.NewMemoryNotifications(), settings: store.NewMemoryPlatformSettings()}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/n/discover" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "corp", "contract": pkgmeta.NotifierContractV1})
			return
		}
		b, _ := io.ReadAll(r.Body)
		var p wire.NotificationPayload
		_ = json.Unmarshal(b, &p)
		e.mu.Lock()
		e.got = append(e.got, p)
		e.eventIDs = append(e.eventIDs, r.Header.Get(wire.NotificationEventIDHeader))
		e.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	e.ns = notify.NewService(notify.Stores{Prefs: e.notes, Outbox: e.notes, Settings: e.settings, Audit: store.NewMemoryExtensionAudit()}, mem, notify.Options{})
	e.f = newPluginFixture(t, e.settings)
	pipe := NewComponentPipeline(e.settings, e.f.audit, e.f.dispatcher, e.f.installs, nil, e.ns.Notifiers.Installer())
	pipe.AllowUnsigned = true
	pkg, _ := json.Marshal(map[string]any{
		"kind": pkgmeta.KindNotifierPackageV1, "name": "corp", "version": "0.1.0", "publisher": "test",
		"runtime": "endpoint", "contract": pkgmeta.NotifierContractV1,
		"endpoint": map[string]any{"baseUrl": receiver.URL + "/n"}, "permissions": map[string]any{"network": []string{"127.0.0.1"}},
	})
	if _, err := pipe.Install(ctx, extpipe.InstallRequest{Body: pkg, Actor: "root"}); err != nil {
		t.Fatalf("install notifier: %v", err)
	}
	if _, err := pipe.Install(ctx, extpipe.InstallRequest{Body: notificationPluginBody(t, "personal-bot"), Actor: "root"}); err != nil {
		t.Fatalf("install plugin: %v", err)
	}
	if _, _, err := (&notify.SecretStore{Settings: e.settings}).Ensure(ctx, "corp", "root"); err != nil {
		t.Fatal(err)
	}
	e.ns.Worker.Desktop = e.f.dispatcher
	e.ns.Start(ctx)
	return e
}

// waitPayloads waits until the notifier received at least n payloads (or
// 5 s passed) and returns what it received.
func (e *notificationE2E) waitPayloads(n int) []wire.NotificationPayload {
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		out := append([]wire.NotificationPayload(nil), e.got...)
		e.mu.Unlock()
		if len(out) >= n || time.Now().After(deadline) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// End to end through the notification emitter: a mention of a member whose
// desktop runs a notification plugin goes to that plugin and NOT to the
// server notifier; when the plugin fails, the server notifier delivers it
// for that member with the same event id. Other recipients are unaffected.
func TestMentionRoutesToDesktopPluginWithServerFallback(t *testing.T) {
	mem := membership.NewStatic()
	for _, u := range []string{"alice", "bob", "carol"} {
		mem.Add("room-1", u)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newNotificationE2E(t, ctx, mem)
	f, ns, waitPayloads := e.f, e.ns, e.waitPayloads
	bob := f.subscribe(t, "bob", tenant.Default)

	srv := httptest.NewServer((&Service{Hub: realtime.NewHub(64, 16), Membership: mem, Notifications: ns, PluginHooks: f.dispatcher}).Handler())
	defer srv.Close()
	post := func(text string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"text": text})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", bytes.NewReader(b))
		req.Header.Set("X-Bnbloc-User", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("chat: %v %v", err, resp)
		}
		resp.Body.Close()
	}

	post("@bob @carol please review")
	d := expectDelivery(t, bob)
	env := decodeEnvelope(t, d)
	raw, _ := json.Marshal(env.Payload)
	var n wire.PluginNotification
	_ = json.Unmarshal(raw, &n)
	if env.Event != "notification" || n.Type != wire.NotificationMention || n.UserID != "bob" || n.Seq == 0 || n.Reply == nil || n.Reply.To != "room" || n.Reply.RoomID != "room-1" {
		t.Fatalf("desktop notification = %s", raw)
	}
	// carol (no desktop) gets the server delivery; bob does not.
	first := waitPayloads(1)
	time.Sleep(100 * time.Millisecond)
	first = waitPayloads(1)
	if len(first) != 1 || len(first[0].Recipients) != 1 || first[0].Recipients[0].UserID != "carol" {
		t.Fatalf("server deliveries = %+v, want carol only", first)
	}
	// bob's plugin fails: the server notifier delivers to bob, same event id.
	f.dispatcher.ack(ctx, "bob", d.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryFailed, Detail: "bot unreachable"})
	all := waitPayloads(2)
	if len(all) != 2 || len(all[1].Recipients) != 1 || all[1].Recipients[0].UserID != "bob" || all[1].ID != n.ID || all[0].ID != n.ID {
		t.Fatalf("server deliveries after fallback = %+v", all)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.eventIDs[1] != n.ID {
		t.Fatalf("fallback X-Bnbloc-Event-Id = %q, want the event id %q", e.eventIDs[1], n.ID)
	}
}

// R6: notifications produced in other services — a borrow request
// (shareagent) and an admin alert (gateway), each through its own
// producer-only emitter over the shared stores — are routed by realtime's
// worker to the member's connected desktop plugin like any other, with the
// same durable fallback: the delivered borrow request never reaches the
// server notifier, the failed alert does (same event id).
func TestOtherServicesNotificationsReachDesktopPlugin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newNotificationE2E(t, ctx, membership.NewStatic())
	bob := e.f.subscribe(t, "bob", tenant.Default)
	producer := notify.NewEmitter(notify.Stores{Prefs: e.notes, Outbox: e.notes, Settings: e.settings}, "")
	tctx := tenant.NewContext(ctx, tenant.Default)
	// Rows from another process are seen at the worker's next poll; wake it.
	drained := func(id string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := e.notes.Item(tenant.Default, id, notify.DesktopChannel); ok {
				e.ns.Worker.Wake()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("no desktop item %s", id)
	}
	plugin := func(d wire.PluginDelivery) wire.PluginNotification {
		t.Helper()
		raw, _ := json.Marshal(decodeEnvelope(t, d).Payload)
		var n wire.PluginNotification
		_ = json.Unmarshal(raw, &n)
		return n
	}

	producer.BorrowRequested(tctx, store.QuotaRequest{RequestID: "qr_1", RoomID: "room-1", OwnerUserID: "bob", RequesterUserID: "carol", Provider: "codex"})
	drained(notify.EventID(tenant.Default, wire.NotificationBorrowRequest, "quota:qr_1"))
	d := expectDelivery(t, bob)
	if n := plugin(d); n.Type != wire.NotificationBorrowRequest || n.UserID != "bob" || n.Actor != "carol" || n.RoomID != "room-1" {
		t.Fatalf("borrow request on the desktop = %+v", n)
	}
	e.f.dispatcher.ack(ctx, "bob", d.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryDelivered})

	res, err := producer.SendAlert(tctx, notify.Alert{Title: "smoke failed", Recipients: []string{"bob"}, Actor: "root", DedupKey: "a1"})
	if err != nil || !reflect.DeepEqual(res.Notifiers, []string{"corp", notify.DesktopChannel}) {
		t.Fatalf("alert = %+v %v", res, err)
	}
	drained(res.ID)
	d = expectDelivery(t, bob)
	if n := plugin(d); n.Type != wire.NotificationAdminAlert || n.UserID != "bob" || n.ID != res.ID || n.RoomID != "" {
		t.Fatalf("alert on the desktop = %+v", n)
	}
	e.f.dispatcher.ack(ctx, "bob", d.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryFailed, Detail: "bot unreachable"})
	got := e.waitPayloads(1)
	time.Sleep(100 * time.Millisecond)
	got = e.waitPayloads(1)
	if len(got) != 1 || got[0].ID != res.ID || got[0].Type != wire.NotificationAdminAlert || len(got[0].Recipients) != 1 || got[0].Recipients[0].UserID != "bob" {
		t.Fatalf("server deliveries = %+v, want only the alert's fallback for bob", got)
	}
}
