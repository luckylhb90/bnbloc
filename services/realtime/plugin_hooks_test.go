package realtimehttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func realtimePluginBody(t *testing.T, name string) []byte {
	t.Helper()
	body := map[string]any{
		"kind":        pkgmeta.KindPluginPackageV1,
		"name":        name,
		"version":     "0.3.1",
		"runtime":     "sidecar",
		"contract":    pkgmeta.PluginContractV1,
		"hooks":       []string{"chat_message.v1"},
		"artifacts":   map[string]any{"darwin-arm64": map[string]string{"url": "https://gw.example/plugin", "sha256": strings.Repeat("0", 64)}},
		"sidecar":     map[string]any{"localUrl": "http://127.0.0.1:7191", "listenEnv": "BNBLOC_PLUGIN_LISTEN"},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}},
		"publisher":   "Acme Corp",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal plugin package: %v", err)
	}
	return raw
}

// pluginFixture installs one plugin into an in-memory pipeline wired to a
// fresh dispatcher.
type pluginFixture struct {
	settings   store.PlatformSettingsStore
	audit      *store.MemoryExtensionAudit
	installs   *store.MemoryComponentInstall
	dispatcher *PluginHookDispatcher
	pipe       *extpipe.Pipeline
	clock      *atomic.Int64
}

func newPluginFixture(t *testing.T, settings store.PlatformSettingsStore, names ...string) *pluginFixture {
	t.Helper()
	if settings == nil {
		settings = store.NewMemoryPlatformSettings()
	}
	f := &pluginFixture{settings: settings, audit: store.NewMemoryExtensionAudit(), installs: store.NewMemoryComponentInstall(), clock: &atomic.Int64{}}
	f.clock.Store(1_000)
	f.dispatcher = NewPluginHookDispatcher(f.audit, f.installs, f.clock.Load)
	f.pipe = NewComponentPipeline(settings, f.audit, f.dispatcher, f.installs, func() int64 { return 100 })
	f.pipe.AllowUnsigned = true
	for _, name := range names {
		if _, err := f.pipe.Install(t.Context(), extpipe.InstallRequest{Body: realtimePluginBody(t, name), Actor: "admin"}); err != nil {
			t.Fatalf("install plugin %s: %v", name, err)
		}
	}
	return f
}

func (f *pluginFixture) subscribe(t *testing.T, subject, tenantID string) *pluginStream {
	t.Helper()
	st, err := f.dispatcher.subscribe(subject, tenantID, true)
	if err != nil {
		t.Fatalf("subscribe %s: %v", subject, err)
	}
	t.Cleanup(func() { f.dispatcher.unsubscribe(st) })
	return st
}

func expectDelivery(t *testing.T, st *pluginStream) wire.PluginDelivery {
	t.Helper()
	select {
	case d := <-st.ch:
		return d
	case <-time.After(2 * time.Second):
		t.Fatalf("no plugin delivery for %s", st.subject)
		return wire.PluginDelivery{}
	}
}

func expectNoDelivery(t *testing.T, st *pluginStream) {
	t.Helper()
	select {
	case d := <-st.ch:
		t.Fatalf("unexpected delivery to %s: %+v", st.subject, d)
	case <-time.After(150 * time.Millisecond):
	}
}

func decodeEnvelope(t *testing.T, d wire.PluginDelivery) wire.PluginEnvelope {
	t.Helper()
	var env wire.PluginEnvelope
	if err := json.Unmarshal(d.Envelope, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

type scopedMembers struct {
	mu   sync.Mutex
	rows map[string]map[string]map[string]bool
}

// Lookup finds the tenant that holds the room (room ids are global).
func (s *scopedMembers) Lookup(roomID, userID string) (membership.Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tid, rooms := range s.rows {
		if rooms[roomID][userID] {
			return membership.Member{Role: store.RoleMember, Tenant: tid}, true
		}
	}
	return membership.Member{}, false
}

func (s *scopedMembers) Fresh(roomID, userID string) (membership.Member, bool) {
	return s.Lookup(roomID, userID)
}

func (s *scopedMembers) Forget(string, string) {}

func chatEvent(room, author, text string) wire.Event {
	return wire.Event{RoomID: room, Kind: "chat_message", Payload: map[string]any{"userId": author, "text": text}}
}

func auditOutcomes(t *testing.T, audit *store.MemoryExtensionAudit) []store.ExtAuditRecord {
	t.Helper()
	recs, err := audit.ExtAuditRecords(context.Background(), string(pkgmeta.KindPlugin), 50, 0)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var out []store.ExtAuditRecord
	for _, rec := range recs {
		if rec.Action == "deliver" {
			out = append(out, rec)
		}
	}
	return out
}

// sseFrames reads plugin_event frames from a live SSE response.
func sseFrames(t *testing.T, body io.Reader) <-chan wire.PluginDelivery {
	t.Helper()
	out := make(chan wire.PluginDelivery, 8)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && event == wire.PluginEventKind:
				var d wire.PluginDelivery
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d) == nil {
					out <- d
				}
			case line == "":
				event = ""
			}
		}
	}()
	return out
}

func doAs(t *testing.T, method, url, subject, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("X-Bnbloc-User", subject)
	req.Header.Set(svcauth.HeaderTenant, tenant.Default)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// The full member path: the member's desktop holds GET /v1/my/plugin-events,
// a chat in their room arrives as a plugin_event frame carrying the versioned
// envelope, and the desktop's ack is audited as that member's delivery.
func TestPluginEvents_streamDeliversEnvelopeAndAuditsAck(t *testing.T) {
	f := newPluginFixture(t, nil, "room-auditor")
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	svc := &Service{Hub: realtime.NewHub(64, 16), Membership: mem, PluginHooks: f.dispatcher}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/my/plugin-events?plugins=1", nil)
	req.Header.Set("X-Bnbloc-User", "alice")
	stream, err := http.DefaultClient.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		t.Fatalf("open stream: %v %v", err, stream)
	}
	defer stream.Body.Close()
	frames := sseFrames(t, stream.Body)
	deadline := time.Now().Add(2 * time.Second)
	for len(f.dispatcher.pluginStreams()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	resp := doAs(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", "alice", `{"text":"hello plugin"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status=%d, want 200", resp.StatusCode)
	}

	var d wire.PluginDelivery
	select {
	case d = <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("no plugin_event frame on the member stream")
	}
	env := decodeEnvelope(t, d)
	if d.Plugin != "room-auditor" || env.Event != "chat_message" || env.Version != "1" || env.TenantID != tenant.Default || env.RoomID != "room-1" {
		t.Fatalf("delivery = %+v envelope = %+v", d, env)
	}
	if p, _ := env.Payload.(map[string]any); p["text"] != "hello plugin" {
		t.Fatalf("payload = %+v, want the source chat payload", env.Payload)
	}

	// Another member cannot acknowledge alice's delivery; bad status is 400.
	if r := doAs(t, http.MethodPost, srv.URL+"/v1/my/plugin-events/"+d.ID+"/ack", "mallory", `{"status":"delivered"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign ack status=%d, want 404", r.StatusCode)
	}
	if r := doAs(t, http.MethodPost, srv.URL+"/v1/my/plugin-events/"+d.ID+"/ack", "alice", `{"status":"maybe"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status ack=%d, want 400", r.StatusCode)
	}
	if r := doAs(t, http.MethodPost, srv.URL+"/v1/my/plugin-events/"+d.ID+"/ack", "alice", `{"status":"delivered"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("ack status=%d, want 204", r.StatusCode)
	}
	if r := doAs(t, http.MethodPost, srv.URL+"/v1/my/plugin-events/"+d.ID+"/ack", "alice", `{"status":"delivered"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("second ack status=%d, want 404", r.StatusCode)
	}
	recs := auditOutcomes(t, f.audit)
	if len(recs) != 1 || recs[0].Outcome != "delivered" || recs[0].Actor != "alice" || recs[0].Name != "room-auditor" || recs[0].Detail != "chat_message.v1" {
		t.Fatalf("audit = %+v, want one delivered record for alice", recs)
	}
}

func TestPluginEvents_failedAckAndExpiryAreAudited(t *testing.T) {
	f := newPluginFixture(t, nil, "auditor")
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true}}}}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	st := f.subscribe(t, "alice", tenant.Default)

	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "one"))
	first := expectDelivery(t, st)
	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "two"))
	expectDelivery(t, st) // never acknowledged

	if !f.dispatcher.ack(ctx, "alice", first.ID, wire.PluginDeliveryAck{Status: wire.PluginDeliveryFailed, Detail: "sidecar not running"}) {
		t.Fatal("ack of a pending delivery refused")
	}
	f.clock.Add(pendingAckTimeout.Milliseconds() + 1)
	f.dispatcher.expirePending()

	recs := auditOutcomes(t, f.audit)
	if len(recs) != 2 {
		t.Fatalf("audit = %+v, want failed ack + expiry", recs)
	}
	details := recs[0].Detail + "|" + recs[1].Detail
	for _, rec := range recs {
		if rec.Outcome != "failed" || rec.Actor != "alice" {
			t.Fatalf("audit record %+v, want failed for alice", rec)
		}
	}
	if !strings.Contains(details, "sidecar not running") || !strings.Contains(details, "not acknowledged") {
		t.Fatalf("audit details = %q", details)
	}
}

// Per-member delivery: each member of the room with a connected desktop gets
// the event on their own stream; a non-member's stream gets nothing.
func TestPluginEvents_deliverPerMemberOnlyToRoomMembers(t *testing.T) {
	f := newPluginFixture(t, nil, "auditor")
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true, "bob": true}}}}
	alice := f.subscribe(t, "alice", tenant.Default)
	bob := f.subscribe(t, "bob", tenant.Default)
	carol := f.subscribe(t, "carol", tenant.Default)

	f.dispatcher.Deliver(tenant.NewContext(context.Background(), tenant.Default), members, chatEvent("room-1", "alice", "hi"))

	a, b := expectDelivery(t, alice), expectDelivery(t, bob)
	if a.ID == b.ID {
		t.Fatalf("members share delivery id %q; each delivery is audited separately", a.ID)
	}
	expectNoDelivery(t, carol)
}

func TestPluginEvents_optInAndOffRolloutGatePerMember(t *testing.T) {
	f := newPluginFixture(t, nil, "rollout-auditor")
	if err := f.pipe.SetRollout(t.Context(), pkgmeta.KindPlugin, "rollout-auditor", "optin", "admin"); err != nil {
		t.Fatalf("set optin: %v", err)
	}
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true, "bob": true}}}}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	alice := f.subscribe(t, "alice", tenant.Default)
	bob := f.subscribe(t, "bob", tenant.Default)

	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "bob", "one"))
	expectNoDelivery(t, alice)
	expectNoDelivery(t, bob)

	if err := f.installs.PutComponentInstall(t.Context(), "alice", string(pkgmeta.KindPlugin), "rollout-auditor", 200); err != nil {
		t.Fatalf("put install: %v", err)
	}
	f.clock.Add(recordsTTL.Milliseconds()) // the per-stream installs cache refreshes
	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "bob", "two"))
	expectDelivery(t, alice)
	expectNoDelivery(t, bob) // bob did not opt in, even though he wrote the message

	if err := f.pipe.SetRollout(t.Context(), pkgmeta.KindPlugin, "rollout-auditor", "off", "admin"); err != nil {
		t.Fatalf("set off: %v", err)
	}
	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "bob", "three"))
	expectNoDelivery(t, alice)
}

// Tenant scope must admit both the event's tenant and the member's session
// tenant (the tenant whose component catalog their desktop runs).
func TestPluginEvents_scopedToEventAndMemberTenant(t *testing.T) {
	f := newPluginFixture(t, nil, "tenant-plugin")
	if err := f.pipe.SetTenants(t.Context(), pkgmeta.KindPlugin, "tenant-plugin", []string{"tenant-a"}, "admin"); err != nil {
		t.Fatalf("set tenants: %v", err)
	}
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{
		"tenant-a": {"room-a": {"alice": true, "dave": true}},
		"tenant-b": {"room-b": {"alice": true}},
	}}
	alice := f.subscribe(t, "alice", "tenant-a")
	dave := f.subscribe(t, "dave", "tenant-b") // signed in to tenant-b: plugin not in his catalog

	f.dispatcher.Deliver(tenant.NewContext(context.Background(), "tenant-b"), members, chatEvent("room-b", "alice", "b"))
	expectNoDelivery(t, alice)

	f.dispatcher.Deliver(tenant.NewContext(context.Background(), "tenant-a"), members, chatEvent("room-a", "alice", "a"))
	env := decodeEnvelope(t, expectDelivery(t, alice))
	if env.TenantID != "tenant-a" || env.RoomID != "room-a" {
		t.Fatalf("envelope = %+v, want tenant-a/room-a", env)
	}
	expectNoDelivery(t, dave)
}

func TestPluginEvents_rehydratedOptInStaysGatedPerMember(t *testing.T) {
	settings := store.NewMemoryPlatformSettings()
	installFixture := newPluginFixture(t, settings, "rehydrate-plugin")
	if err := installFixture.pipe.SetRollout(t.Context(), pkgmeta.KindPlugin, "rehydrate-plugin", "optin", "admin"); err != nil {
		t.Fatalf("set optin: %v", err)
	}
	restart := newPluginFixture(t, settings)
	if err := restart.pipe.Rehydrate(t.Context(), extreg.ServiceRealtime); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true}}}}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	alice := restart.subscribe(t, "alice", tenant.Default)

	restart.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "x"))
	expectNoDelivery(t, alice)
	if err := restart.installs.PutComponentInstall(t.Context(), "alice", string(pkgmeta.KindPlugin), "rehydrate-plugin", 200); err != nil {
		t.Fatalf("put install: %v", err)
	}
	restart.clock.Add(recordsTTL.Milliseconds())
	restart.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "y"))
	expectDelivery(t, alice)
}

// An uninstall done by another process (the gateway's pipeline) only deletes
// the persisted record; delivery stops once the snapshot expires.
func TestPluginEvents_stopWhenRecordRemovedElsewhere(t *testing.T) {
	f := newPluginFixture(t, nil, "gone-plugin")
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true}}}}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	alice := f.subscribe(t, "alice", tenant.Default)
	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "x"))
	expectDelivery(t, alice)

	if err := f.settings.DeletePlatformSetting(t.Context(), "ext.plugin.gone-plugin.installed"); err != nil {
		t.Fatalf("delete record: %v", err)
	}
	f.clock.Add(recordsTTL.Milliseconds() + 1)
	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "y"))
	expectNoDelivery(t, alice)
}

// countingSettings counts platform-settings scans.
type countingSettings struct {
	store.PlatformSettingsStore
	lists atomic.Int64
	fail  atomic.Bool
}

func (c *countingSettings) ListPlatformSettings(ctx context.Context) ([]store.PlatformSetting, error) {
	c.lists.Add(1)
	if c.fail.Load() {
		return nil, errors.New("settings down")
	}
	return c.PlatformSettingsStore.ListPlatformSettings(ctx)
}

// The fan-out reads the install records at most once per recordsTTL, and a
// failed read delivers nothing (fail closed).
func TestPluginEvents_recordsAreSnapshottedAndFailClosed(t *testing.T) {
	settings := &countingSettings{PlatformSettingsStore: store.NewMemoryPlatformSettings()}
	f := newPluginFixture(t, settings, "hot-plugin")
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true}}}}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	alice := f.subscribe(t, "alice", tenant.Default)
	before := settings.lists.Load()
	for i := 0; i < 5; i++ {
		f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "x"))
	}
	for i := 0; i < 5; i++ {
		expectDelivery(t, alice)
	}
	if reads := settings.lists.Load() - before; reads != 1 {
		t.Fatalf("settings scans for 5 publishes = %d, want 1 (snapshot)", reads)
	}

	settings.fail.Store(true)
	f.clock.Add(recordsTTL.Milliseconds() + 1)
	f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "y"))
	expectNoDelivery(t, alice)
}

// A desktop that stops reading fills its bounded buffer: further deliveries
// are dropped and audited, and publishing never blocks.
func TestPluginEvents_fullStreamDropsAndAudits(t *testing.T) {
	f := newPluginFixture(t, nil, "slow-plugin")
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true}}}}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	f.subscribe(t, "alice", tenant.Default) // never read

	done := make(chan struct{})
	go func() {
		for i := 0; i < pluginStreamBuffer+5; i++ {
			f.dispatcher.Deliver(ctx, members, chatEvent("room-1", "alice", "x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver blocked on a full member stream")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, rec := range auditOutcomes(t, f.audit) {
			if rec.Outcome == "failed" && strings.Contains(rec.Detail, "buffer full") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no dropped-delivery audit for a full member stream")
}

// With no desktop stream connected nothing is queued and chat still posts.
func TestPluginEvents_noStreamsQueuesNothing(t *testing.T) {
	f := newPluginFixture(t, nil, "idle-plugin")
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	svc := &Service{Hub: realtime.NewHub(64, 16), Membership: mem, PluginHooks: f.dispatcher}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", bytes.NewReader([]byte(`{"text":"nobody listens"}`)))
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post chat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status=%d, want 200", resp.StatusCode)
	}
	if n := len(f.dispatcher.queue); n != 0 {
		t.Fatalf("queued %d events with no stream connected", n)
	}
}

func TestPluginEvents_streamsPerMemberAreBounded(t *testing.T) {
	f := newPluginFixture(t, nil)
	for i := 0; i < maxPluginStreamsPerSubject; i++ {
		f.subscribe(t, "alice", tenant.Default)
	}
	if _, err := f.dispatcher.subscribe("alice", tenant.Default, true); !errors.Is(err, errTooManyPluginStreams) {
		t.Fatalf("subscribe over the cap = %v, want errTooManyPluginStreams", err)
	}
	f.subscribe(t, "bob", tenant.Default)
}
