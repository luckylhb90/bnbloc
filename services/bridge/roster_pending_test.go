package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

// fakeDaemon is a Core Protocol daemon whose targets and pending ids a test
// sets; withPending says whether it advertises pendingTargets.
type fakeDaemon struct {
	mu          sync.Mutex
	targets     []protocol.Target
	pending     []string
	withPending bool
}

func (d *fakeDaemon) set(targets []string, pending ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.targets = nil
	for _, id := range targets {
		d.targets = append(d.targets, protocol.Target{ID: id, Provider: id})
	}
	d.pending = pending
}

func (d *fakeDaemon) serve(t *testing.T) corepkg.Endpoint {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case protocol.PathDiscover:
			caps := []string{}
			if d.withPending {
				caps = append(caps, protocol.CapabilityPendingTargets)
			}
			_ = json.NewEncoder(w).Encode(protocol.Discovery{Name: "acp", Version: "1", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"}, Capabilities: caps})
		case protocol.PathTargets:
			_ = json.NewEncoder(w).Encode(protocol.ListTargetsResponse{Targets: d.targets, Pending: d.pending})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return corepkg.Endpoint{BaseURL: srv.URL}
}

// A core restarting lists only the agents it has checked, and says the others
// are pending: the roster keeps them, so no room is told they went away; once
// the core answers, an agent it no longer lists (and does not call pending)
// leaves. A core without the capability is listed as it answers. Two
// candidates the list alone cannot tell apart: an agent being checked
// (opencode) and one the member removed (codex).
func TestRosterKeepsAgentsTheCoreIsStillChecking(t *testing.T) {
	for _, withPending := range []bool{true, false} {
		d := &fakeDaemon{withPending: withPending}
		ep := d.serve(t)
		b := New(Config{GatewayBaseURL: "http://gw", OwnerSubject: "me",
			Cores: []CoreConfig{{Name: "acp", Provider: protocol.NewClient("acp", nil), Endpoint: ep}}})
		ctx := context.Background()

		d.set([]string{"opencode", "codex", "kilo-sandbox"})
		b.refreshTargets(ctx)
		if got := b.advertiseParam(); got != "acp/opencode,acp/codex,acp/kilo-sandbox" {
			t.Fatalf("before the restart: %q", got)
		}
		// Restarted: kilo answers at once, opencode is still being checked,
		// codex was removed from the core's config.
		d.set([]string{"kilo-sandbox"}, "opencode")
		b.refreshTargets(ctx)
		got := b.advertiseParam()
		if withPending {
			if !strings.Contains(got, "acp/opencode") || strings.Contains(got, "acp/codex") {
				t.Fatalf("while checking: %q, want opencode kept and codex gone", got)
			}
		} else if got != "acp/kilo-sandbox" {
			t.Fatalf("a core without pendingTargets is listed as it answers: %q", got)
		}
		d.set([]string{"kilo-sandbox"}) // opencode's check failed: it is gone
		b.refreshTargets(ctx)
		if got := b.advertiseParam(); got != "acp/kilo-sandbox" {
			t.Fatalf("after the checks: %q", got)
		}
	}
}

// Each room's subscription is reopened after the re-advertise period, staggered
// by room so a bridge's rooms do not reconnect at once, and never sooner than
// the period.
func TestRosterReadvertiseIsStaggeredPerRoom(t *testing.T) {
	seen := map[time.Duration]bool{}
	for _, room := range []string{"room-a", "room-b", "room-c", "room-d"} {
		d := rosterReadvertiseAfter(room)
		if d < rosterReadvertise || d > rosterReadvertise+rosterReadvertise/5 {
			t.Fatalf("%s reopens after %s, want within [%s, %s]", room, d, rosterReadvertise, rosterReadvertise+rosterReadvertise/5)
		}
		if rosterReadvertiseAfter(room) != d {
			t.Fatalf("%s: the stagger is not stable", room)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("every room reopens at the same moment")
	}
}

// A room stream is reopened on purpose after the period: it ends with
// errReadvertise (the caller reconnects at once, without a warning), and the
// reopened subscription carries the bridge's current roster.
func TestRoomStreamReopensToReadvertise(t *testing.T) {
	old := rosterReadvertise
	rosterReadvertise = 200 * time.Millisecond
	t.Cleanup(func() { rosterReadvertise = old })
	var mu sync.Mutex
	var subs []string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		subs = append(subs, r.URL.Query().Get("agents"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer gw.Close()
	d := &fakeDaemon{withPending: true}
	ep := d.serve(t)
	d.set([]string{"opencode"})
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "me",
		Cores: []CoreConfig{{Name: "acp", Provider: protocol.NewClient("acp", nil), Endpoint: ep}}})
	b.refreshTargets(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	err := b.consumeRoom(ctx, "room-1")
	if err != errReadvertise {
		t.Fatalf("consumeRoom = %v after %s, want errReadvertise", err, time.Since(start))
	}
	if time.Since(start) < rosterReadvertise {
		t.Fatalf("reopened after %s, before the period", time.Since(start))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(subs) != 1 || subs[0] != "acp/opencode" {
		t.Fatalf("subscriptions advertised %q", subs)
	}
}
