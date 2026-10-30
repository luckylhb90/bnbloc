// Package shard is bnbloc's key-ownership layer for services that keep
// per-key state in process memory (realtime's room hub and member plugin
// streams, relay's pairing table) and for services that only need to know how
// many replicas share a budget (the gateway's rate limits).
//
// A Set is the live peer set of one service: either a DNS name re-resolved
// periodically (a Kubernetes headless Service, one address per ready pod) or
// a fixed comma list of host:port. Every candidate is probed (GET /readyz)
// every second, so a dead or unready replica leaves the set in both modes
// within about two seconds; a failed call to a live peer (a forward or a
// peer-to-peer request) re-probes it at once, which shortens that further
// for a crashed replica. DNS names are re-resolved every five seconds.
// Ownership of a key is rendezvous (highest-random-weight) hashing over the
// sorted live set: each replica computes the same owner from the same view,
// and adding or removing one peer moves only the keys that peer gains or
// loses.
//
// A nil *Set is a single node: it owns every key, has size 1 and never
// forwards. Both environment variables unset yields nil, so a deployment that
// does not scale out behaves exactly as before.
package shard

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// Refresh cadence and liveness thresholds. These are protocol constants of
// the ownership layer (how fast a view converges), not deployment data.
const (
	// probeInterval is how often every candidate is re-probed. With
	// downAfter it bounds how long a dead replica keeps owning keys (its
	// rooms answer 502 until the view drops it): about 2 s. N replicas cost
	// N*(N-1) probes per second, negligible at realistic N.
	probeInterval = time.Second
	// resolveInterval is how often the DNS name is re-resolved. A replica
	// that died is dropped by its failed probes, not by DNS, so this only
	// paces how fast a new replica is discovered.
	resolveInterval = 5 * time.Second
	// probeTimeout bounds one readiness probe; peers are cluster-internal,
	// so a healthy one answers far sooner, and it stays below probeInterval.
	probeTimeout = 800 * time.Millisecond
	// downAfter is how many consecutive failed probes drop a live peer: one
	// lost probe must not move every key it owns (stream churn).
	downAfter = 2
	// suspectDebounce spaces the out-of-band probes a failed peer call
	// triggers, so a burst of failures probes once, not once per request.
	suspectDebounce = 250 * time.Millisecond
	// probePath is the readiness endpoint every bnbloc service serves
	// (internal/obs): a replica that cannot reach its dependencies owns no
	// keys.
	probePath = "/readyz"
)

// Set is one service's live peer set.
type Set struct {
	self string
	// leaving: this replica is stopping (Leave); it owns nothing from now.
	leaving atomic.Bool
	// Exactly one of dnsHost (DNS mode, with dnsPort) or static (list mode)
	// names the candidates.
	dnsHost string
	dnsPort string
	static  []string

	lookup    func(ctx context.Context, host string) ([]string, error)
	probe     func(ctx context.Context, addr string) bool
	selfCheck func() error
	interval  time.Duration
	// suspect wakes the refresh loop for an immediate probe after a call to
	// a live peer failed (buffered 1: concurrent failures coalesce).
	suspect chan struct{}
	// refreshMu serializes refreshes (the ticker and suspect wake-ups).
	refreshMu sync.Mutex

	mu        sync.RWMutex
	candidate []string
	state     map[string]*peerState
	live      []string // sorted
	listeners []func(changed bool)
	metrics   *obs.Metrics
	label     string
	transport http.RoundTripper
}

type peerState struct {
	up    bool
	fails int
}

// Options configures New. Peers is the BNBLOC_<SVC>_PEERS value; Self the
// BNBLOC_<SVC>_SELF value (empty only for a count-only set). Lookup, Probe and
// Interval (the probe cadence) default to the real resolver, an HTTP GET of
// /readyz, and 1s.
type Options struct {
	Peers    string
	Self     string
	Lookup   func(ctx context.Context, host string) ([]string, error)
	Probe    func(ctx context.Context, addr string) bool
	Interval time.Duration
}

// Errors returned for a malformed configuration (startup is refused).
var (
	ErrSelfRequired  = errors.New("shard: peers configured without this replica's own address")
	ErrPeersRequired = errors.New("shard: own address configured without peers")
)

// FromEnv reads <prefix>_PEERS and <prefix>_SELF (e.g. BNBLOC_REALTIME): both
// unset is a single node (nil, nil); one without the other is an error. The
// returned set is not started.
func FromEnv(prefix string) (*Set, error) {
	peers := strings.TrimSpace(os.Getenv(prefix + "_PEERS"))
	self := strings.TrimSpace(os.Getenv(prefix + "_SELF"))
	switch {
	case peers == "" && self == "":
		return nil, nil
	case self == "":
		return nil, fmt.Errorf("%w: set %s_SELF", ErrSelfRequired, prefix)
	case peers == "":
		return nil, fmt.Errorf("%w: set %s_PEERS", ErrPeersRequired, prefix)
	}
	s, err := New(Options{Peers: peers, Self: self})
	if err != nil {
		return nil, fmt.Errorf("%s_PEERS/%s_SELF: %w", prefix, prefix, err)
	}
	return s, nil
}

// CountFromEnv reads only <prefix>_PEERS, for a service that needs the live
// replica count but owns no keys (the gateway's rate limits). Unset = nil
// (one replica).
func CountFromEnv(prefix string) (*Set, error) {
	peers := strings.TrimSpace(os.Getenv(prefix + "_PEERS"))
	if peers == "" {
		return nil, nil
	}
	s, err := New(Options{Peers: peers})
	if err != nil {
		return nil, fmt.Errorf("%s_PEERS: %w", prefix, err)
	}
	return s, nil
}

// New parses a peer specification. One host:port entry is a DNS name whose
// every address is a peer at that port (an IP literal resolves to itself);
// two or more entries are a fixed list. Self, when set, is this replica's
// address in the same form: host:port, or (DNS mode only) a bare IP that
// takes the DNS entry's port — the pod IP from the downward API, IPv6-safe.
// In list mode Self must be one of the entries.
func New(o Options) (*Set, error) {
	var entries []string
	for _, raw := range strings.Split(o.Peers, ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			entries = append(entries, raw)
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("shard: empty peer list")
	}
	s := &Set{
		lookup:   o.Lookup,
		probe:    o.Probe,
		interval: o.Interval,
		state:    map[string]*peerState{},
		suspect:  make(chan struct{}, 1),
	}
	if s.interval <= 0 {
		s.interval = probeInterval
	}
	if s.lookup == nil {
		s.lookup = net.DefaultResolver.LookupHost
	}
	base := newTransport()
	s.transport = &suspectTransport{base: base, set: s}
	if s.probe == nil {
		// Probes use the bare transport: a failed probe is already counted by
		// the refresh that ran it and must not wake another one.
		client := &http.Client{Transport: base, Timeout: probeTimeout}
		s.probe = func(ctx context.Context, addr string) bool { return httpProbe(ctx, client, addr) }
	}
	if len(entries) == 1 {
		host, port, err := splitAddr(entries[0])
		if err != nil {
			return nil, err
		}
		s.dnsHost, s.dnsPort = host, port
	} else {
		for _, e := range entries {
			host, port, err := splitAddr(e)
			if err != nil {
				return nil, err
			}
			if addr := net.JoinHostPort(host, port); !slices.Contains(s.static, addr) {
				s.static = append(s.static, addr)
			}
		}
		slices.Sort(s.static)
	}
	if self := strings.TrimSpace(o.Self); self != "" {
		addr, err := s.parseSelf(self)
		if err != nil {
			return nil, err
		}
		s.self = addr
	}
	return s, nil
}

func (s *Set) parseSelf(self string) (string, error) {
	if self == SelfAuto {
		ip, err := localAddress()
		if err != nil {
			return "", err
		}
		self = ip
	}
	if ip := net.ParseIP(strings.Trim(self, "[]")); ip != nil {
		if s.dnsHost == "" {
			return "", fmt.Errorf("shard: own address %q has no port (a bare address is accepted only with a DNS peer name)", self)
		}
		return net.JoinHostPort(ip.String(), s.dnsPort), nil
	}
	host, port, err := splitAddr(self)
	if err != nil {
		return "", fmt.Errorf("shard: own address: %w", err)
	}
	addr := net.JoinHostPort(host, port)
	if s.dnsHost == "" && !slices.Contains(s.static, addr) {
		return "", fmt.Errorf("shard: own address %s is not in the peer list %v", addr, s.static)
	}
	return addr, nil
}

func splitAddr(raw string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(raw)
	if err != nil {
		return "", "", fmt.Errorf("shard: peer %q is not host:port: %w", raw, err)
	}
	if host == "" {
		return "", "", fmt.Errorf("shard: peer %q has no host", raw)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n <= 0 || n > 65535 {
		return "", "", fmt.Errorf("shard: peer %q has no valid port", raw)
	}
	return host, port, nil
}

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:               nil, // peers are cluster-internal; never via an egress proxy
		DialContext:         (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
}

// suspectTransport is the transport of every peer call (forwards and
// peer-to-peer requests). A call that fails for any reason other than its own
// caller giving up marks the peer suspect, so a crashed replica is re-probed
// within suspectDebounce instead of at the next tick.
type suspectTransport struct {
	base *http.Transport
	set  *Set
}

func (t *suspectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil && req.Context().Err() == nil {
		t.set.Suspect(req.URL.Host)
	}
	return resp, err
}

func httpProbe(ctx context.Context, client *http.Client, addr string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+probePath, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// CheckSelfWith makes this replica judge its own liveness with check (its
// /readyz dependency check) instead of probing its own address over the
// network, so the first refresh — run before the listener is up — already
// counts it. Call before Start.
func (s *Set) CheckSelfWith(check func() error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.selfCheck = check
	s.mu.Unlock()
}

// Instrument exports the set on m: bnbloc_shard_peers (the live view) and
// the forwarding counters (Forward). label names the service in logs.
func (s *Set) Instrument(m *obs.Metrics, label string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.metrics, s.label = m, label
	s.mu.Unlock()
	if m == nil {
		return
	}
	m.Describe(metricPeers, "Live replicas in this replica's ownership view (scale-out peer set).")
	m.Describe(metricForwarded, "Requests proxied to the replica that owns their key.")
	m.Describe(metricServedUnowned, "Forwarded requests served locally although this replica does not own the key (peer views briefly disagree).")
	m.GaugeSet(metricPeers, float64(len(s.Peers())))
}

const (
	metricPeers         = "shard_peers"
	metricForwarded     = "shard_forwarded_total"
	metricServedUnowned = "shard_served_unowned_total"
)

// Start runs one refresh synchronously (so the first view is real before the
// service serves) and then probes every interval, re-resolving the DNS name
// every resolveInterval, until ctx ends. A Suspect call probes at once
// (debounced).
func (s *Set) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.Refresh(ctx)
	go func() {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		lastResolve := time.Now()
		var lastSuspect time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				resolve := time.Since(lastResolve) >= resolveInterval
				if resolve {
					lastResolve = time.Now()
				}
				s.refresh(ctx, resolve)
			case <-s.suspect:
				if wait := suspectDebounce - time.Since(lastSuspect); wait > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
				}
				lastSuspect = time.Now()
				s.refresh(ctx, false)
			}
		}
	}()
}

// Suspect asks for an immediate probe of addr after a call to it failed. Only
// a peer in the live view matters (a dropped one is probed every tick
// anyway); the probe itself decides, with the usual downAfter threshold, so
// one failed request never moves keys on its own.
func (s *Set) Suspect(addr string) {
	if s == nil {
		return
	}
	s.mu.RLock()
	live := slices.Contains(s.live, addr)
	s.mu.RUnlock()
	if !live {
		return
	}
	select {
	case s.suspect <- struct{}{}:
	default:
	}
}

// Refresh re-resolves the candidates, probes each, and publishes the new live
// view. A failed DNS lookup keeps the previous candidates (a resolver blip
// must not empty the set). Listeners (OnRefresh) run after every refresh,
// outside the lock.
func (s *Set) Refresh(ctx context.Context) { s.refresh(ctx, true) }

// refresh probes the candidates, re-resolving the DNS name first when resolve
// is set (otherwise the last resolved candidates are probed).
func (s *Set) refresh(ctx context.Context, resolve bool) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	candidates := s.static
	if s.dnsHost != "" && !resolve {
		s.mu.RLock()
		candidates = s.candidate
		s.mu.RUnlock()
	} else if s.dnsHost != "" {
		addrs, err := s.lookup(ctx, s.dnsHost)
		if err != nil {
			slog.Warn("bnbloc shard: peer lookup failed; keeping the previous view",
				"event", "bnbloc.shard.lookup_failed", "service", s.label, "host", s.dnsHost, "error", err)
			s.mu.RLock()
			candidates = s.candidate
			s.mu.RUnlock()
		} else {
			candidates = make([]string, 0, len(addrs))
			for _, a := range addrs {
				if addr := net.JoinHostPort(strings.Trim(a, "[]"), s.dnsPort); !slices.Contains(candidates, addr) {
					candidates = append(candidates, addr)
				}
			}
			slices.Sort(candidates)
		}
	}
	s.mu.RLock()
	selfCheck := s.selfCheck
	s.mu.RUnlock()
	results := make([]bool, len(candidates))
	var wg sync.WaitGroup
	for i, addr := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if addr == s.self && selfCheck != nil {
				results[i] = selfCheck() == nil
				return
			}
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			results[i] = s.probe(pctx, addr)
		}()
	}
	wg.Wait()

	s.mu.Lock()
	next := map[string]*peerState{}
	live := make([]string, 0, len(candidates))
	for i, addr := range candidates {
		st := s.state[addr]
		if st == nil {
			st = &peerState{}
		}
		switch {
		case addr == s.self && s.leaving.Load():
			// Leaving (Leave): out of its own view at once, not after
			// downAfter probes.
			st.up = false
		case results[i]:
			st.up, st.fails = true, 0
		default:
			if st.fails++; st.fails >= downAfter {
				st.up = false
			}
		}
		next[addr] = st
		if st.up {
			live = append(live, addr)
		}
	}
	s.candidate, s.state = candidates, next
	changed := !slices.Equal(s.live, live)
	prev := s.live
	if changed {
		s.live = live
	}
	listeners := slices.Clone(s.listeners)
	m, label := s.metrics, s.label
	s.mu.Unlock()
	if changed {
		m.GaugeSet(metricPeers, float64(len(live)))
		slog.Info("bnbloc shard: peer set changed",
			"event", "bnbloc.shard.peers_changed", "service", label, "self", s.self, "from", prev, "to", live)
	}
	for _, fn := range listeners {
		fn(changed)
	}
}

// Leave takes this replica out of its own view at once (it is stopping):
// every key it owned is forwarded to its new owner from now, and the
// listeners release the keys' state (their streams reopen there). The other
// replicas drop it when its /readyz turns 503.
func (s *Set) Leave() {
	if s == nil {
		return
	}
	s.leaving.Store(true)
	s.refresh(context.Background(), false)
}

// OnRefresh registers fn to run after every refresh, with changed reporting
// whether the live view moved. Services release the state of keys they no
// longer own on every call, not only on a change: a forwarded request served
// locally while the views disagreed may have left state (an open stream) for
// a key this replica does not own, and that must move to the real owner once
// the sender's view converges. fn runs on the refresh goroutine.
func (s *Set) OnRefresh(fn func(changed bool)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

// Self is this replica's own address ("" for a single node or a count-only
// set).
func (s *Set) Self() string {
	if s == nil {
		return ""
	}
	return s.self
}

// Peers returns the live view, sorted.
func (s *Set) Peers() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.live)
}

// Size is the number of live replicas, at least 1 (this one is serving).
func (s *Set) Size() int {
	if s == nil {
		return 1
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return max(1, len(s.live))
}

// Owner returns the live peer that owns key. "" means this replica serves it:
// a single node, or an empty view (nothing reachable yet; serving locally is
// the only option).
func (s *Set) Owner(key string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	owner := Owner(s.live, key)
	if owner == s.self {
		return ""
	}
	return owner
}

// Owns reports whether this replica serves key (Owner(key) == "").
func (s *Set) Owns(key string) bool { return s.Owner(key) == "" }

// Owner is rendezvous hashing: the peer with the highest score for key, ties
// broken by the lexically smaller address. It depends only on the peer set,
// never its order, so every replica with the same view picks the same owner,
// and removing a peer reassigns only that peer's keys. "" for no peers.
func Owner(peers []string, key string) string {
	var best string
	var bestScore uint64
	for _, p := range peers {
		sc := score(p, key)
		if best == "" || sc > bestScore || (sc == bestScore && p < best) {
			best, bestScore = p, sc
		}
	}
	return best
}

// score hashes (peer, key) with FNV-1a and a 64-bit finalizer (MurmurHash3
// fmix64), so peers differing in one character still score independently.
func score(peer, key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(peer))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(key))
	x := h.Sum64()
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

// SelfAuto as <prefix>_SELF takes this host's own address: its first
// non-loopback unicast address (a container's, where no downward API gives
// the pod IP: Docker Compose with replicas). DNS peer names only.
const SelfAuto = "auto"

// localAddress is this host's first non-loopback unicast address.
func localAddress() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("shard: own address (auto): %w", err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
			return n.IP.String(), nil
		}
	}
	return "", errors.New("shard: own address (auto): no non-loopback address")
}
