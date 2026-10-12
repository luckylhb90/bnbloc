package realtimehttp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func expectHint(t *testing.T, st *pluginStream) wire.ComponentsChanged {
	t.Helper()
	select {
	case c := <-st.hints:
		return c
	case <-time.After(2 * time.Second):
		t.Fatalf("no components_changed hint for %s", st.subject)
		return wire.ComponentsChanged{}
	}
}

func expectNoHint(t *testing.T, st *pluginStream) {
	t.Helper()
	select {
	case c := <-st.hints:
		t.Fatalf("unexpected hint for %s: %+v", st.subject, c)
	case <-time.After(100 * time.Millisecond):
	}
}

func (f *pluginFixture) subscribeHintsOnly(t *testing.T, subject, tenantID string) *pluginStream {
	t.Helper()
	st, err := f.dispatcher.subscribe(subject, tenantID, false)
	if err != nil {
		t.Fatalf("subscribe %s: %v", subject, err)
	}
	t.Cleanup(func() { f.dispatcher.unsubscribe(st) })
	return st
}

// The internal route queues a hint on the streams it concerns: every member
// for a platform change, one organization's, or one member's (an opt-in), on
// streams with or without plugins. Hints coalesce per stream.
func TestComponentsChanged_filtersByTenantAndMember(t *testing.T) {
	f := newPluginFixture(t, nil)
	svc := &Service{Hub: realtime.NewHub(64, 16), PluginHooks: f.dispatcher}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	alice := f.subscribeHintsOnly(t, "alice", "tenant-a")
	bob := f.subscribe(t, "bob", "tenant-a")
	carol := f.subscribeHintsOnly(t, "carol", "tenant-b")

	post := func(body string) {
		t.Helper()
		resp := doAs(t, http.MethodPost, srv.URL+"/internal/v1/components/changed", svcauth.ServiceSubject("gateway"), body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	post(`{"atMs": 5}`)
	for _, st := range []*pluginStream{alice, bob, carol} {
		if c := expectHint(t, st); c.AtMs != 5 {
			t.Fatalf("hint = %+v", c)
		}
	}
	post(`{"tenantId": "tenant-a"}`)
	expectHint(t, alice)
	expectHint(t, bob)
	expectNoHint(t, carol)
	post(`{"tenantId": "tenant-a", "member": "bob"}`)
	expectNoHint(t, alice)
	if c := expectHint(t, bob); c.Member != "bob" || c.AtMs == 0 {
		t.Fatalf("member hint = %+v", c)
	}
	// Two changes before the desktop reads: one hint is enough.
	post(`{}`)
	post(`{}`)
	expectHint(t, alice)
	expectNoHint(t, alice)

	resp := doAs(t, http.MethodPost, srv.URL+"/internal/v1/components/changed", svcauth.ServiceSubject("gateway"), `nope`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", resp.StatusCode)
	}
}

// The hint reaches the desktop as a components_changed frame on the member
// stream, including a stream opened without plugins.
func TestComponentsChanged_streamFrame(t *testing.T) {
	f := newPluginFixture(t, nil)
	svc := &Service{Hub: realtime.NewHub(64, 16), PluginHooks: f.dispatcher}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/my/plugin-events", nil)
	req.Header.Set("X-Bnbloc-User", "alice")
	req.Header.Set(svcauth.HeaderTenant, tenant.Default)
	stream, err := http.DefaultClient.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		t.Fatalf("open stream: %v %v", err, stream)
	}
	defer stream.Body.Close()
	frames := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(stream.Body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frames <- event + " " + strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !f.dispatcher.hasStream("alice") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	resp := doAs(t, http.MethodPost, srv.URL+"/internal/v1/components/changed", svcauth.ServiceSubject("gateway"), `{"atMs": 42}`)
	resp.Body.Close()
	select {
	case got := <-frames:
		if got != wire.ComponentsChangedKind+` {"atMs":42}` {
			t.Fatalf("frame = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no components_changed frame")
	}
}

// A desktop holding the stream only for catalog hints (no plugin sidecar)
// changes nothing for plugins or notifications: it gets no deliveries and
// HasDesktop stays false, so its notifications keep going to the server
// notifiers directly.
func TestComponentsChanged_hintStreamIsNoPluginDesktop(t *testing.T) {
	f := newPluginFixture(t, nil, "room-auditor")
	hintsOnly := f.subscribeHintsOnly(t, "alice", tenant.Default)
	if f.dispatcher.HasDesktop(tenant.Default, "alice") {
		t.Fatal("a hints-only stream counts as a plugin desktop")
	}
	members := &scopedMembers{rows: map[string]map[string]map[string]bool{tenant.Default: {"room-1": {"alice": true}}}}
	f.dispatcher.Deliver(tenant.NewContext(context.Background(), tenant.Default), members, chatEvent("room-1", "bob", "hi"))
	expectNoDelivery(t, hintsOnly)
	done := make(chan bool, 1)
	f.dispatcher.DeliverNotification(tenant.Default, wire.PluginNotification{UserID: "alice"}, func(ok bool) { done <- ok })
	select {
	case ok := <-done:
		if ok {
			t.Fatal("notification reported delivered to a hints-only stream")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification outcome never reported")
	}
	expectNoDelivery(t, hintsOnly)
	// The same member's plugin desktop is still one.
	plugin := f.subscribe(t, "alice", tenant.Default)
	if !f.dispatcher.HasDesktop(tenant.Default, "alice") {
		t.Fatal("plugin stream not a desktop")
	}
	f.dispatcher.Deliver(tenant.NewContext(context.Background(), tenant.Default), members, chatEvent("room-1", "bob", "hi again"))
	expectDelivery(t, plugin)
	expectNoDelivery(t, hintsOnly)
}

// A gateway signal on one replica reaches the member streams of every
// replica; a peer's hand-off is served locally and never forwarded again.
func TestComponentsChanged_handedToEveryPeer(t *testing.T) {
	c := newCluster(t, 3)
	var streams []*pluginStream
	for _, r := range c.reps {
		st, err := r.svc.PluginHooks.subscribe(c.memberOwnedBy(r), tenant.Default, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.svc.PluginHooks.unsubscribe(st) })
		streams = append(streams, st)
	}
	resp, err := http.DefaultClient.Do(c.request(c.reps[1], http.MethodPost, "/internal/v1/components/changed", svcauth.ServiceSubject("gateway"), wire.ComponentsChanged{AtMs: 7}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, st := range streams {
		if got := expectHint(t, st); got.AtMs != 7 {
			t.Fatalf("hint = %+v", got)
		}
	}
	// A peer's hand-off (forwarded marker) is served locally only.
	req := c.request(c.reps[0], http.MethodPost, "/internal/v1/components/changed", svcauth.ServiceSubject("realtime"), wire.ComponentsChanged{AtMs: 8})
	req.Header.Set(shard.ForwardedHeader, c.reps[2].addr)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	expectHint(t, streams[0])
	expectNoHint(t, streams[1])
	expectNoHint(t, streams[2])
}

func TestMergeHints(t *testing.T) {
	m := mergeHints(nil, wire.ComponentsChanged{TenantID: "a", Member: "x", AtMs: 1})
	if m.TenantID != "a" || m.Member != "x" {
		t.Fatalf("first = %+v", m)
	}
	m = mergeHints(m, wire.ComponentsChanged{TenantID: "a", Member: "y", AtMs: 3})
	if m.TenantID != "a" || m.Member != "" || m.AtMs != 3 {
		t.Fatalf("member widened = %+v", m)
	}
	m = mergeHints(m, wire.ComponentsChanged{TenantID: "b", AtMs: 2})
	if m.TenantID != "" || m.AtMs != 3 {
		t.Fatalf("tenant widened = %+v", m)
	}
	raw, _ := json.Marshal(wire.ComponentsChanged{AtMs: 1})
	if string(raw) != `{"atMs":1}` {
		t.Fatalf("wire shape = %s", raw)
	}
}

// The gateway's signal (the shared HTTPPublisher, signed, with no tenant for
// a platform-wide change) passes realtime's svcauth and reaches the streams.
func TestComponentsChanged_publisherSignalIsAccepted(t *testing.T) {
	t.Setenv("BNBLOC_INTERNAL_KEY", "components-changed-key")
	f := newPluginFixture(t, nil)
	svc := &Service{Hub: realtime.NewHub(64, 16), PluginHooks: f.dispatcher}
	srv := httptest.NewServer(svcauth.Middleware(svcauth.KeyFromEnv(), svc.Handler()))
	defer srv.Close()
	alice := f.subscribeHintsOnly(t, "alice", "tenant-a")
	bob := f.subscribeHintsOnly(t, "bob", "tenant-b")
	pub := realtime.NewHTTPPublisher(srv.URL, "gateway")
	pub.ComponentsChanged(context.Background(), wire.ComponentsChanged{AtMs: 9})
	expectHint(t, alice)
	expectHint(t, bob)
	pub.ComponentsChanged(context.Background(), wire.ComponentsChanged{TenantID: "tenant-b", Member: "bob", AtMs: 10})
	expectNoHint(t, alice)
	if c := expectHint(t, bob); c.AtMs != 10 {
		t.Fatalf("hint = %+v", c)
	}
	// An unsigned caller is refused.
	resp, err := http.Post(srv.URL+"/internal/v1/components/changed", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		t.Fatalf("unsigned signal accepted: %d", resp.StatusCode)
	}
	expectNoHint(t, alice)
}
