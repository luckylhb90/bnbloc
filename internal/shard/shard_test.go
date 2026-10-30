package shard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func keys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("room:r%04d", i)
	}
	return out
}

// The hash is part of the protocol: during a rolling upgrade old and new
// binaries share one peer set, and a changed hash would make them disagree
// about every key. These pinned owners fail loudly on any change to it.
func TestOwnerIsStable(t *testing.T) {
	peers := []string{"10.0.0.1:8082", "10.0.0.2:8082", "10.0.0.3:8082"}
	want := map[string]string{}
	for _, k := range []string{"room:general", "room:r0001", "member:maya", "member:jack", "relay-session-key"} {
		want[k] = Owner(peers, k)
	}
	// Order of the peer list never matters.
	shuffled := []string{peers[2], peers[0], peers[1]}
	for k, w := range want {
		if got := Owner(shuffled, k); got != w {
			t.Fatalf("Owner(%q) depends on peer order: %s vs %s", k, got, w)
		}
	}
	// Pinned values: a change here remaps keys between binary versions.
	if got := score("10.0.0.1:8082", "room:general"); got != 0xa1ddc4251bec5f29 {
		t.Fatalf("score changed: %#x", got)
	}
	if got := score("10.0.0.2:8082", "room:general"); got != 0x801c8ceca11c59b7 {
		t.Fatalf("score changed: %#x", got)
	}
	golden := map[string]string{
		"room:general": "10.0.0.1:8082",
		"member:maya":  "10.0.0.1:8082",
		"room:r0001":   "10.0.0.3:8082",
	}
	for k, w := range golden {
		if got := Owner(peers, k); got != w {
			t.Errorf("Owner(%q) = %s, want pinned %s (the ownership hash changed)", k, got, w)
		}
	}
	if Owner(nil, "room:x") != "" {
		t.Fatal("no peers must own nothing")
	}
}

// Adding a peer moves only the keys the new peer takes; removing one moves
// only the keys it held. Everything else keeps its owner (minimal movement),
// and the load spreads roughly evenly.
func TestOwnerMinimalMovement(t *testing.T) {
	three := []string{"a:1", "b:1", "c:1"}
	four := append([]string{"d:1"}, three...)
	ks := keys(4000)
	counts := map[string]int{}
	moved := 0
	for _, k := range ks {
		before, after := Owner(three, k), Owner(four, k)
		counts[after]++
		if before != after {
			moved++
			if after != "d:1" {
				t.Fatalf("key %s moved %s -> %s, not to the added peer", k, before, after)
			}
		}
	}
	// The new peer takes about a quarter; allow generous slack.
	if moved < 700 || moved > 1300 {
		t.Fatalf("adding one of four peers moved %d of 4000 keys, want ~1000", moved)
	}
	for p, n := range counts {
		if n < 700 || n > 1300 {
			t.Fatalf("peer %s owns %d of 4000 keys, want ~1000 (uneven spread)", p, n)
		}
	}
	two := []string{"a:1", "c:1"} // b removed
	for _, k := range ks {
		before, after := Owner(three, k), Owner(two, k)
		if before != after && before != "b:1" {
			t.Fatalf("key %s moved %s -> %s although its owner stayed", k, before, after)
		}
	}
}

func TestNewParsesDNSAndList(t *testing.T) {
	dns, err := New(Options{Peers: "bnbloc-realtime-peers:8082", Self: "10.1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if dns.dnsHost != "bnbloc-realtime-peers" || dns.dnsPort != "8082" || dns.Self() != "10.1.2.3:8082" {
		t.Fatalf("dns set = %+v", dns)
	}
	v6, err := New(Options{Peers: "peers.svc:8082", Self: "fd00::7"})
	if err != nil || v6.Self() != "[fd00::7]:8082" {
		t.Fatalf("bare IPv6 self = %q, %v", v6.Self(), err)
	}
	list, err := New(Options{Peers: " rt-2:8082, rt-1:8082 ,rt-3:8082,rt-1:8082", Self: "rt-2:8082"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(list.static, ",") != "rt-1:8082,rt-2:8082,rt-3:8082" || list.Self() != "rt-2:8082" {
		t.Fatalf("list set = %v self %s", list.static, list.Self())
	}
	count, err := New(Options{Peers: "gw-1:8080,gw-2:8080"})
	if err != nil || count.Self() != "" {
		t.Fatalf("count-only set: %v %v", count, err)
	}
	for _, bad := range []Options{
		{Peers: ""},
		{Peers: "no-port"},
		{Peers: "host:0"},
		{Peers: ":8082"},
		{Peers: "rt-1:8082,rt-2:8082", Self: "rt-9:8082"}, // self not in list
		{Peers: "rt-1:8082,rt-2:8082", Self: "10.0.0.1"},  // bare self needs DNS mode
		{Peers: "peers:8082", Self: "host-without-port"},
	} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%+v) accepted a malformed configuration", bad)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("BNBLOC_TESTSVC_PEERS", "")
	t.Setenv("BNBLOC_TESTSVC_SELF", "")
	if s, err := FromEnv("BNBLOC_TESTSVC"); s != nil || err != nil {
		t.Fatalf("both unset = %v, %v; want a single node (nil, nil)", s, err)
	}
	if s := (*Set)(nil); !s.Owns("anything") || s.Size() != 1 || s.Owner("k") != "" {
		t.Fatal("a nil set must own every key and count one replica")
	}
	t.Setenv("BNBLOC_TESTSVC_PEERS", "peers:8082")
	if _, err := FromEnv("BNBLOC_TESTSVC"); !errors.Is(err, ErrSelfRequired) {
		t.Fatalf("peers without self: %v", err)
	}
	t.Setenv("BNBLOC_TESTSVC_PEERS", "")
	t.Setenv("BNBLOC_TESTSVC_SELF", "10.0.0.1")
	if _, err := FromEnv("BNBLOC_TESTSVC"); !errors.Is(err, ErrPeersRequired) {
		t.Fatalf("self without peers: %v", err)
	}
	t.Setenv("BNBLOC_TESTSVC_PEERS", "gw-1:8080,gw-2:8080")
	if s, err := CountFromEnv("BNBLOC_TESTSVC"); err != nil || s == nil {
		t.Fatalf("count-only: %v %v", s, err)
	}
}

// fakeNet scripts DNS answers and probe outcomes.
type fakeNet struct {
	mu      sync.Mutex
	addrs   []string
	lookErr error
	down    map[string]bool
}

func (f *fakeNet) lookup(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.addrs...), f.lookErr
}

func (f *fakeNet) probe(_ context.Context, addr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.down[addr]
}

func TestRefreshTracksLivePeers(t *testing.T) {
	f := &fakeNet{addrs: []string{"10.0.0.2", "10.0.0.1"}, down: map[string]bool{}}
	s, err := New(Options{Peers: "peers:8082", Self: "10.0.0.1", Lookup: f.lookup, Probe: f.probe})
	if err != nil {
		t.Fatal(err)
	}
	changes, refreshes := 0, 0
	s.OnRefresh(func(changed bool) {
		refreshes++
		if changed {
			changes++
		}
	})
	ctx := context.Background()
	s.Refresh(ctx)
	if got := strings.Join(s.Peers(), ","); got != "10.0.0.1:8082,10.0.0.2:8082" || changes != 1 {
		t.Fatalf("initial view %s (changes %d)", got, changes)
	}
	s.Refresh(ctx)
	if changes != 1 || refreshes != 2 {
		t.Fatalf("unchanged view: changes=%d refreshes=%d, want 1/2 (every refresh notifies, changed=false)", changes, refreshes)
	}
	// A resolver failure keeps the previous candidates.
	f.lookErr = errors.New("SERVFAIL")
	s.Refresh(ctx)
	if s.Size() != 2 || changes != 1 {
		t.Fatalf("lookup failure changed the view: %v", s.Peers())
	}
	f.lookErr = nil
	// One failed probe is tolerated; the second drops the peer.
	f.down["10.0.0.2:8082"] = true
	s.Refresh(ctx)
	if s.Size() != 2 {
		t.Fatal("one lost probe dropped a peer")
	}
	s.Refresh(ctx)
	if got := strings.Join(s.Peers(), ","); got != "10.0.0.1:8082" || changes != 2 {
		t.Fatalf("after two failed probes view=%s changes=%d", got, changes)
	}
	if !s.Owns("room:any") {
		t.Fatal("the sole live replica must own every key")
	}
	// A new pod appears in DNS and answers its probe: it joins at once.
	f.addrs = append(f.addrs, "10.0.0.3")
	delete(f.down, "10.0.0.2:8082")
	s.Refresh(ctx)
	if s.Size() != 3 || changes != 3 {
		t.Fatalf("view %v changes %d, want 3 peers", s.Peers(), changes)
	}
	// This replica gone from its own view (draining) owns nothing.
	f.addrs = []string{"10.0.0.2", "10.0.0.3"}
	s.Refresh(ctx)
	for _, k := range keys(50) {
		if s.Owns(k) {
			t.Fatalf("a replica outside its own view owns %s", k)
		}
	}
	// An empty view serves locally.
	f.addrs = nil
	s.Refresh(ctx)
	if !s.Owns("room:x") || s.Size() != 1 {
		t.Fatal("an empty view must serve every key locally")
	}
}

// A failed call to a live peer re-probes it at once: with the periodic tick
// effectively off, two suspect wake-ups (the usual downAfter threshold) drop
// a crashed peer in well under a second, while a peer that is already out of
// the view, or a call its own caller abandoned, wakes nothing.
func TestSuspectReprobesAtOnce(t *testing.T) {
	f := &fakeNet{addrs: []string{"10.0.0.1", "10.0.0.2"}, down: map[string]bool{}}
	var mu sync.Mutex
	probes := 0
	probe := func(ctx context.Context, addr string) bool {
		mu.Lock()
		probes++
		mu.Unlock()
		return f.probe(ctx, addr)
	}
	s, err := New(Options{Peers: "peers:8082", Self: "10.0.0.1", Lookup: f.lookup, Probe: probe, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	if s.Size() != 2 {
		t.Fatalf("initial view %v", s.Peers())
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return probes }

	// Not in the live view: ignored.
	before := count()
	s.Suspect("10.0.0.9:8082")
	time.Sleep(50 * time.Millisecond)
	if count() != before {
		t.Fatal("a suspect for a peer outside the view probed")
	}

	f.mu.Lock()
	f.down["10.0.0.2:8082"] = true
	f.mu.Unlock()
	start := time.Now()
	deadline := start.Add(2 * time.Second)
	for s.Size() == 2 && time.Now().Before(deadline) {
		s.Suspect("10.0.0.2:8082")
		time.Sleep(20 * time.Millisecond)
	}
	if s.Size() != 1 {
		t.Fatal("repeated failed calls never dropped the dead peer")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("dropping the dead peer took %v, want well under a second", took)
	}
}

// The peer transport marks the target suspect on a failed call, but not when
// the caller itself gave up.
func TestTransportSuspectsFailedCalls(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close() // nothing listens: connection refused
	s, err := New(Options{Peers: "127.0.0.1:1," + dead, Self: "127.0.0.1:1", Probe: func(context.Context, string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	s.Refresh(context.Background())

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(canceled, http.MethodGet, "http://"+dead+"/x", nil)
	if _, err := s.Client().Do(req); err == nil {
		t.Fatal("expected an error")
	}
	select {
	case <-s.suspect:
		t.Fatal("a call its caller canceled marked the peer suspect")
	default:
	}

	req, _ = http.NewRequest(http.MethodGet, "http://"+dead+"/x", nil)
	if _, err := s.Client().Do(req); err == nil {
		t.Fatal("expected connection refused")
	}
	select {
	case <-s.suspect:
	default:
		t.Fatal("a refused call did not mark the peer suspect")
	}
}

// End to end with real listeners, the real HTTP probe and the real refresh
// loop: a replica that stops answering leaves a peer's view within about two
// probe intervals with no traffic, and sooner when a call to it fails.
func TestDeadReplicaLeavesViewQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("real timers")
	}
	start := func() (*http.Server, string) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}
		go srv.Serve(ln)
		return srv, ln.Addr().String()
	}
	run := func(t *testing.T, withTraffic bool) time.Duration {
		a, addrA := start()
		defer a.Close()
		b, addrB := start()
		s, err := New(Options{Peers: addrA + "," + addrB, Self: addrA})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.Start(ctx)
		if s.Size() != 2 {
			t.Fatalf("initial view %v", s.Peers())
		}
		b.Close()
		killed := time.Now()
		for s.Size() == 2 && time.Since(killed) < 5*time.Second {
			if withTraffic {
				req, _ := http.NewRequest(http.MethodGet, "http://"+addrB+"/v1/rooms/r1/events", nil)
				if resp, err := s.Client().Do(req); err == nil {
					resp.Body.Close()
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		if s.Size() != 1 {
			t.Fatal("the dead replica never left the view")
		}
		return time.Since(killed)
	}
	idle := run(t, false)
	busy := run(t, true)
	t.Logf("dead replica dropped after %v idle, %v with failing calls", idle, busy)
	if idle > 2500*time.Millisecond {
		t.Fatalf("idle detection took %v, want about two probe intervals", idle)
	}
	if busy > 1200*time.Millisecond {
		t.Fatalf("detection with failing calls took %v, want well under the idle bound", busy)
	}
}

// SELF=auto takes this host's own address, with the DNS entry's port.
func TestSelfAuto(t *testing.T) {
	s, err := New(Options{Peers: "realtime:8082", Self: SelfAuto, Lookup: func(context.Context, string) ([]string, error) { return nil, nil }})
	if err != nil {
		t.Skipf("no non-loopback address on this host: %v", err)
	}
	host, port, err := net.SplitHostPort(s.Self())
	if err != nil || port != "8082" || net.ParseIP(host) == nil || net.ParseIP(host).IsLoopback() {
		t.Fatalf("self = %q", s.Self())
	}
}
