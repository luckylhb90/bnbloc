package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
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

// A room the gateway answers 404 or 403 for is not watched any more, but
// its agents' workspaces stay bound and nothing is deleted: either answer is
// the absence of a room, never evidence it was deleted (a room-scoped token
// answers 403 for a deleted room and a live one alike).
func TestGoneRoomKeepsItsWorkspaces(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		prov := &deletingCore{fakeCoreProvider: &fakeCoreProvider{url: "http://core.invalid"}, deleted: make(chan string, 1)}
		b := New(Config{GatewayBaseURL: "http://gw.invalid", GatewayToken: "t", OwnerSubject: "alice",
			Cores: []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: "http://core.invalid"}}}})
		k := driveKey{room: "gone-room", core: "core-a", provider: "codex"}
		b.workspaces.bind(k, "ws-1")
		b.dropGoneRoom("gone-room", &gatewayError{Status: status})
		if id, ok := b.workspaces.id(k); !ok || id != "ws-1" {
			t.Fatalf("status %d: the binding was dropped on an absence", status)
		}
		select {
		case id := <-prov.deleted:
			t.Fatalf("status %d: deleted %q on an absence", status, id)
		case <-time.After(100 * time.Millisecond):
		}
		if b.workspaces.goneAt("gone-room") != 0 {
			t.Fatalf("status %d: an absence marked the room deleted", status)
		}
	}
}

// deletedRoomsGateway answers the room service's deletion question:
// deleted lists the deleted rooms; down makes the question fail (an older
// room service without the route answers 404).
type deletedRoomsGateway struct {
	srv     *httptest.Server
	deleted atomic.Value // []string
	down    atomic.Bool
	asked   atomic.Int64
}

func newDeletedRoomsGateway(t *testing.T, deleted ...string) *deletedRoomsGateway {
	t.Helper()
	g := &deletedRoomsGateway{}
	g.deleted.Store(deleted)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/my/deleted-rooms", func(w http.ResponseWriter, r *http.Request) {
		g.asked.Add(1)
		if g.down.Load() {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}
		var out []string
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if slices.Contains(g.deleted.Load().([]string), id) {
				out = append(out, id)
			}
		}
		_ = json.NewEncoder(w).Encode(wire.DeletedRooms{Deleted: out})
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

// A deleted room's workspaces go only on the room service's word, and only
// once the room has been reported deleted for roomPurgeAfter: its sandbox
// workdir and space (purged, with its home) and its core workspace. A room
// the owner merely left, and every room while the question fails, keep
// theirs.
func TestDeletedRoomWorkspacesArePurgedOnEvidence(t *testing.T) {
	g := newDeletedRoomsGateway(t, "gone-room")
	sbx := newFakeSandboxd(t)
	root := t.TempDir()
	prov := &deletingCore{fakeCoreProvider: &fakeCoreProvider{url: "http://core.invalid"}, deleted: make(chan string, 4)}
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice",
		Cores:   []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: "http://core.invalid"}}},
		Sandbox: SandboxConfig{Mode: SandboxAll, URL: sbx.srv.URL, Token: sandboxAdminToken, VMExec: "/apps/sandbox/bnbloc-vmexec", Machine: "bnbloc", WorkspacesRoot: root}})
	now := time.Now()
	b.sweep.now = func() time.Time { return now }
	gone := driveKey{room: "gone-room", core: "core-a", provider: "codex", sandboxed: true}
	left := driveKey{room: "left-room", core: "core-a", provider: "codex", sandboxed: true}
	b.workspaces.bind(gone, "ws-gone")
	b.workspaces.bind(left, "ws-left")
	for _, k := range []driveKey{gone, left} {
		if err := os.MkdirAll(filepath.Join(root, sandboxSpaceName(k)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	listed := map[string]struct{}{"live-room": {}}

	// The question fails: nothing is marked or removed.
	g.down.Store(true)
	b.sweepOnce(ctx, listed)
	if b.workspaces.goneAt("gone-room") != 0 {
		t.Fatal("a failed question marked a room")
	}
	g.down.Store(false)

	// Reported deleted: marked, kept.
	b.sweepOnce(ctx, listed)
	if b.workspaces.goneAt("gone-room") == 0 || b.workspaces.goneAt("left-room") != 0 {
		t.Fatalf("marks: gone %d left %d", b.workspaces.goneAt("gone-room"), b.workspaces.goneAt("left-room"))
	}
	now = now.Add(roomPurgeAfter - time.Minute)
	b.sweepOnce(ctx, listed)
	if _, ok := b.workspaces.id(gone); !ok || len(prov.deleted) != 0 {
		t.Fatal("a room reported deleted for less than roomPurgeAfter was purged")
	}

	// Still reported deleted a day later: purged.
	now = now.Add(2 * time.Minute)
	b.sweepOnce(ctx, listed)
	if _, ok := b.workspaces.id(gone); ok {
		t.Fatal("the deleted room's binding outlived the purge")
	}
	if id := <-prov.deleted; id != "ws-gone" {
		t.Fatalf("core workspace deleted = %q", id)
	}
	if _, err := os.Stat(filepath.Join(root, sandboxSpaceName(gone))); !os.IsNotExist(err) {
		t.Fatalf("the deleted room's workdir is still there: %v", err)
	}
	if !slices.Contains(sbx.spaceCalls(), "delete?purge") {
		t.Fatalf("space calls %v: want the space purged with its home", sbx.spaceCalls())
	}
	if b.workspaces.goneAt("gone-room") != 0 {
		t.Fatal("the mark outlived the purge")
	}
	// The room the owner left keeps everything.
	if id, ok := b.workspaces.id(left); !ok || id != "ws-left" {
		t.Fatal("a room that was not deleted lost its workspace")
	}
	if _, err := os.Stat(filepath.Join(root, sandboxSpaceName(left))); err != nil {
		t.Fatalf("a room that was not deleted lost its workdir: %v", err)
	}
}

// A room listed again (or no longer reported deleted) loses its mark.
func TestDeletedRoomMarkClearsWhenTheRoomIsSeen(t *testing.T) {
	g := newDeletedRoomsGateway(t, "gone-room")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	b.workspaces.bind(driveKey{room: "gone-room", core: "core-a", provider: "codex"}, "ws-1")
	b.sweepOnce(context.Background(), map[string]struct{}{})
	if b.workspaces.goneAt("gone-room") == 0 {
		t.Fatal("not marked")
	}
	b.sweepOnce(context.Background(), map[string]struct{}{"gone-room": {}})
	if b.workspaces.goneAt("gone-room") != 0 {
		t.Fatal("a listed room kept its mark")
	}
}

// The room's own room_deleted event marks it (when first), keeps its
// workspace bound, and stops the stream.
func TestRoomDeletedEventMarksTheRoom(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/users/alice/rooms", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"rooms":["gone-room"]}`))
	})
	mux.HandleFunc("GET /v1/my/deleted-rooms", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"deleted":[]}`))
	})
	mux.HandleFunc("GET /v1/rooms/gone-room/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: " + wire.EventRoomDeleted + "\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice", RelayBaseURL: "http://relay.invalid"})
	k := driveKey{room: "gone-room", core: "core-a", provider: "codex"}
	b.workspaces.bind(k, "ws-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.reconcile(ctx)
	waitFor(t, "the deleted room to be marked and dropped", func() bool {
		stream, _, gone := b.watching("gone-room")
		return !stream && gone && b.workspaces.goneAt("gone-room") != 0
	})
	if id, ok := b.workspaces.id(k); !ok || id != "ws-1" {
		t.Fatal("the event dropped the binding; only the sweep's purge does")
	}
}
