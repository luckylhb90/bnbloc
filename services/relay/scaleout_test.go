package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/wsconn"
)

type relayReplica struct {
	addr string
	srv  *httptest.Server
	svc  *Service
	set  *shard.Set
}

// relayCluster starts two relay replicas sharing a static peer list whose
// liveness the test controls.
func relayCluster(t *testing.T) ([]*relayReplica, func(addr string, down bool)) {
	t.Helper()
	var mu sync.Mutex
	down := map[string]bool{}
	probe := func(_ context.Context, addr string) bool {
		mu.Lock()
		defer mu.Unlock()
		return !down[addr]
	}
	reps := make([]*relayReplica, 2)
	addrs := make([]string, 2)
	for i := range reps {
		srv := httptest.NewUnstartedServer(nil)
		reps[i] = &relayReplica{addr: srv.Listener.Addr().String(), srv: srv}
		addrs[i] = reps[i].addr
	}
	for _, r := range reps {
		set, err := shard.New(shard.Options{Peers: strings.Join(addrs, ","), Self: r.addr, Probe: probe})
		if err != nil {
			t.Fatal(err)
		}
		set.Refresh(context.Background())
		r.set = set
		r.svc = New(ICEConfig{})
		r.svc.Peers = set
		r.srv.Config.Handler = r.svc.Handler()
		r.srv.Start()
		t.Cleanup(r.srv.Close)
	}
	return reps, func(addr string, d bool) { mu.Lock(); down[addr] = d; mu.Unlock() }
}

func sessionOwnedBy(t *testing.T, r *relayReplica) string {
	t.Helper()
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("sess-%d", i)
		if shard.Owner(r.set.Peers(), "relay:"+key) == r.addr {
			return key
		}
	}
	t.Fatal("no key")
	return ""
}

// Two peers of one session that reach different replicas still pair: both
// stream upgrades (WebSocket) and the rendezvous exchange are forwarded to
// the session key's owner.
func TestRelayPairsAcrossReplicas(t *testing.T) {
	reps, _ := relayCluster(t)
	key := sessionOwnedBy(t, reps[1])

	// Rendezvous: publish via one replica, poll via the other.
	resp, err := http.Post(reps[0].srv.URL+"/relay/rendezvous/"+key, "application/json", strings.NewReader(`{"peerId":"maya","candidate":"Y2FuZGlkYXRl"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("publish: %v %v", resp, err)
	}
	resp.Body.Close()
	resp, err = http.Get(reps[1].srv.URL + "/relay/rendezvous/" + key + "?peerId=jack")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Y2FuZGlkYXRl") {
		t.Fatalf("poll on the other replica = %s", body)
	}

	first, err := wsconn.Dial(reps[0].srv.URL+"/relay/stream/"+key, 2*time.Second)
	if err != nil {
		t.Fatalf("dial via replica 0: %v", err)
	}
	defer first.Close()
	second, err := wsconn.Dial(reps[1].srv.URL+"/relay/stream/"+key, 2*time.Second)
	if err != nil {
		t.Fatalf("dial via replica 1: %v", err)
	}
	defer second.Close()
	if _, err := first.Write([]byte("hello-across-replicas")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := second.Read(buf)
	if err != nil || string(buf[:n]) != "hello-across-replicas" {
		t.Fatalf("second read %q, %v", buf[:n], err)
	}
	if _, err := second.Write([]byte("and-back")); err != nil {
		t.Fatal(err)
	}
	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err = first.Read(buf); err != nil || string(buf[:n]) != "and-back" {
		t.Fatalf("first read %q, %v", buf[:n], err)
	}
	// The pairing happened on the owner only.
	reps[0].svc.streamMu.Lock()
	stray := len(reps[0].svc.pendingStreams)
	reps[0].svc.streamMu.Unlock()
	if stray != 0 {
		t.Fatal("the non-owner parked a stream")
	}
}

// When the session key moves, the parked first peer is closed so it
// reconnects to the new owner (where its partner will meet it), and queued
// rendezvous candidates of moved keys are dropped.
func TestRelayReleasesMovedKeys(t *testing.T) {
	reps, setDown := relayCluster(t)
	owner := reps[1]
	key := sessionOwnedBy(t, owner)
	parked, err := wsconn.Dial(reps[0].srv.URL+"/relay/stream/"+key, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer parked.Close()
	resp, err := http.Post(owner.srv.URL+"/relay/rendezvous/"+key, "application/json", strings.NewReader(`{"peerId":"maya","candidate":"eA=="}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		owner.svc.streamMu.Lock()
		n := len(owner.svc.pendingStreams)
		owner.svc.streamMu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first peer never parked on the owner")
		}
		time.Sleep(5 * time.Millisecond)
	}
	setDown(owner.addr, true)
	for i := 0; i < 2; i++ { // two failed probes drop it
		for _, r := range reps {
			r.set.Refresh(context.Background())
		}
	}
	_ = parked.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := parked.Read(make([]byte, 8)); err == nil {
		t.Fatal("parked peer of a moved key was not closed")
	}
	owner.svc.mu.Lock()
	sessions := len(owner.svc.rendezvous)
	owner.svc.mu.Unlock()
	if sessions != 0 {
		t.Fatalf("%d rendezvous sessions kept for moved keys", sessions)
	}
}
