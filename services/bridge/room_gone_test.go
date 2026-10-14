package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

// roomGoneGateway lists one room in discovery but answers its event stream and
// room detail with status (a deleted room the listing still names, or a static
// BNBLOC_BRIDGE_ROOMS entry). failListing makes discovery itself fail.
type roomGoneGateway struct {
	srv         *httptest.Server
	streams     atomic.Int64
	details     atomic.Int64
	failListing atomic.Bool
}

func newRoomGoneGateway(t *testing.T, status int) *roomGoneGateway {
	t.Helper()
	g := &roomGoneGateway{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users/alice/rooms", func(w http.ResponseWriter, r *http.Request) {
		if g.failListing.Load() {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"rooms":["gone-room"]}`))
	})
	mux.HandleFunc("GET /v1/rooms/gone-room/events", func(w http.ResponseWriter, r *http.Request) {
		g.streams.Add(1)
		http.Error(w, "caller is not a member of room", status)
	})
	mux.HandleFunc("GET /v1/rooms/gone-room", func(w http.ResponseWriter, r *http.Request) {
		g.details.Add(1)
		http.Error(w, "room not found", status)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *Bridge) watching(room string) (stream, sync, gone bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, stream = b.rooms[room]
	_, sync = b.syncs[room]
	_, gone = b.gone[room]
	return
}

// A room whose stream answers 404/403 is dropped (stream + sync)
// and not re-subscribed by the next reconcile while it is in cooldown — the
// bridge stops re-requesting a deleted room instead of looping on it.
func TestBridgeDropsRoomTheGatewayReportsGone(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		g := newRoomGoneGateway(t, status)
		b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice", RelayBaseURL: "http://relay.invalid"})
		ctx, cancel := context.WithCancel(context.Background())
		b.reconcile(ctx)
		waitFor(t, "the gone room to be dropped", func() bool {
			stream, sync, gone := b.watching("gone-room")
			return !stream && !sync && gone
		})
		streams, details := g.streams.Load(), g.details.Load()
		b.reconcile(ctx)
		time.Sleep(50 * time.Millisecond)
		if stream, _, _ := b.watching("gone-room"); stream {
			t.Fatalf("status %d: reconcile re-subscribed a room in its gone cooldown", status)
		}
		if g.streams.Load() != streams || g.details.Load() != details {
			t.Fatalf("status %d: gone room requested again (streams %d->%d, details %d->%d)",
				status, streams, g.streams.Load(), details, g.details.Load())
		}
		cancel()
	}
}

// The cooldown ends: a room still listed after goneRoomCooldown is retried
// once more (the listing is authoritative for discovered rooms).
func TestBridgeRetriesGoneRoomAfterCooldown(t *testing.T) {
	g := newRoomGoneGateway(t, http.StatusNotFound)
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.reconcile(ctx)
	waitFor(t, "the gone room to be dropped", func() bool { _, _, gone := b.watching("gone-room"); return gone })
	b.mu.Lock()
	b.gone["gone-room"] = time.Now().Add(-goneRoomCooldown)
	b.mu.Unlock()
	before := g.streams.Load()
	b.reconcile(ctx)
	waitFor(t, "a retry after the cooldown", func() bool { return g.streams.Load() > before })
}

// A failed room listing keeps the current subscriptions instead of treating
// the owner as a member of no rooms (which tore every stream down and
// re-subscribed all of them on the next tick).
func TestBridgeKeepsRoomsWhenDiscoveryFails(t *testing.T) {
	g := newRoomGoneGateway(t, http.StatusNotFound)
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	cancelled := false
	b.rooms["live-room"] = func() { cancelled = true }
	g.failListing.Store(true)
	b.reconcile(context.Background())
	if stream, _, _ := b.watching("live-room"); !stream || cancelled {
		t.Fatal("a failed discovery dropped a watched room")
	}
}

// The room detail is tenant-scoped while the listing and the stream span the
// owner's tenants: a 404 from the detail alone (a room of another tenant)
// must not drop the room — its stream (and so its roster entry) stays.
func TestBridgeKeepsRoomWhoseDetailIsOutOfTenant(t *testing.T) {
	var streams atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users/alice/rooms", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rooms":["other-tenant-room"]}`))
	})
	mux.HandleFunc("GET /v1/rooms/other-tenant-room/events", func(w http.ResponseWriter, r *http.Request) {
		streams.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("GET /v1/rooms/other-tenant-room", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"store: room not found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", RelayBaseURL: "http://relay.invalid"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.reconcile(ctx)
	waitFor(t, "the room stream", func() bool { return streams.Load() > 0 })
	time.Sleep(200 * time.Millisecond) // the sync plan lookup has answered 404 by now
	if stream, _, gone := b.watching("other-tenant-room"); !stream || gone {
		t.Fatalf("an out-of-tenant room detail 404 dropped the room (stream=%v gone=%v)", stream, gone)
	}
}

// deletingCore records the workspaces the bridge deletes.
type deletingCore struct {
	*fakeCoreProvider
	deleted chan string
}

func (c *deletingCore) DeleteWorkspace(_ context.Context, _ corepkg.Endpoint, id string) error {
	c.deleted <- id
	return nil
}

// A deleted room's workspaces — its records in .room/ among them — are
// removed with it; a lost membership (403) keeps them.
func TestDeletedRoomRemovesItsWorkspaces(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		prov := &deletingCore{fakeCoreProvider: &fakeCoreProvider{url: "http://core.invalid"}, deleted: make(chan string, 1)}
		b := New(Config{GatewayBaseURL: "http://gw.invalid", GatewayToken: "t", OwnerSubject: "alice",
			Cores: []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: "http://core.invalid"}}}})
		b.workspaces.bind(driveKey{room: "gone-room", core: "core-a", provider: "codex"}, "ws-1")
		b.dropGoneRoom("gone-room", &gatewayError{Status: status})
		if _, ok := b.workspaces.id(driveKey{room: "gone-room", core: "core-a", provider: "codex"}); ok {
			t.Fatalf("status %d: the binding outlived the room", status)
		}
		select {
		case id := <-prov.deleted:
			if status != http.StatusNotFound || id != "ws-1" {
				t.Fatalf("status %d: deleted %q", status, id)
			}
		case <-time.After(300 * time.Millisecond):
			if status == http.StatusNotFound {
				t.Fatal("a deleted room's workspace was kept")
			}
		}
	}
}
