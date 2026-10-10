package relay

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wsconn"
)

// ICE config is served to peers.
func TestICEConfig(t *testing.T) {
	svc := New(ICEConfig{StunServers: []string{"stun:stun.corp:3478"}, RelayEndpoints: []string{"wss://relay.corp"}})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	resp := mustGet(t, srv.URL+"/relay/ice-config")
	defer resp.Body.Close()
	var cfg ICEConfig
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	if len(cfg.StunServers) != 1 || cfg.StunServers[0] != "stun:stun.corp:3478" {
		t.Fatalf("ice config = %+v", cfg)
	}
}

// Rendezvous relays a candidate from one peer to the other, but not back to the
// author.
func TestRendezvousExchange(t *testing.T) {
	svc := New(ICEConfig{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	pub, _ := json.Marshal(candidateBody{PeerID: "peer-a", Candidate: "cand-a"})
	resp := mustPost(t, srv.URL+"/relay/rendezvous/sess-1", pub)
	resp.Body.Close()

	// peer-b polls and receives peer-a's candidate.
	rb := mustGet(t, srv.URL+"/relay/rendezvous/sess-1?peerId=peer-b")
	defer rb.Body.Close()
	var out struct {
		Candidates []string `json:"candidates"`
	}
	_ = json.NewDecoder(rb.Body).Decode(&out)
	if len(out.Candidates) != 1 || out.Candidates[0] != "cand-a" {
		t.Fatalf("peer-b candidates = %v, want [cand-a]", out.Candidates)
	}

	// peer-a polling its own session sees nothing (not its own candidate).
	ra := mustGet(t, srv.URL+"/relay/rendezvous/sess-1?peerId=peer-a")
	defer ra.Body.Close()
	var out2 struct {
		Candidates []string `json:"candidates"`
	}
	_ = json.NewDecoder(ra.Body).Decode(&out2)
	if len(out2.Candidates) != 0 {
		t.Fatalf("peer-a should not receive its own candidate, got %v", out2.Candidates)
	}
}

// Two peers connecting to the same stream session exchange bytes end-to-end
// over the WebSocket transport, in both directions.
func TestStreamRelayPipesBytes(t *testing.T) {
	svc := New(ICEConfig{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	dial := func() net.Conn {
		c, err := wsconn.Dial(srv.URL+"/relay/stream/sess-1", 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return c
	}

	a := dial()
	defer a.Close()
	b := dial()
	defer b.Close()

	// a -> b
	if _, err := a.Write([]byte("hello-from-a")); err != nil {
		t.Fatalf("a write: %v", err)
	}
	buf := make([]byte, 64)
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := b.Read(buf)
	if err != nil {
		t.Fatalf("b read: %v", err)
	}
	if string(buf[:n]) != "hello-from-a" {
		t.Fatalf("b received %q, want hello-from-a", buf[:n])
	}

	// b -> a (verify the reverse direction pipes too)
	if _, err := b.Write([]byte("reply-from-b")); err != nil {
		t.Fatalf("b write: %v", err)
	}
	_ = a.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = a.Read(buf)
	if err != nil {
		t.Fatalf("a read: %v", err)
	}
	if string(buf[:n]) != "reply-from-b" {
		t.Fatalf("a received %q, want reply-from-b", buf[:n])
	}
}

// The per-peer candidate queue is capped: past the cap a publish is refused
// with 429 rather than growing memory without bound.
func TestRendezvousCandidateQueueCap(t *testing.T) {
	svc := New(ICEConfig{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	body, _ := json.Marshal(candidateBody{PeerID: "flooder", Candidate: "cand"})
	var lastStatus int
	for i := 0; i < maxCandidatesPerPeer+5; i++ {
		resp := mustPost(t, srv.URL+"/relay/rendezvous/sess-flood", body)
		lastStatus = resp.StatusCode
		resp.Body.Close()
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("past the queue cap, want 429, got %d", lastStatus)
	}
}

// An oversized candidate body is refused rather than buffered.
func TestRendezvousCandidateTooLarge(t *testing.T) {
	svc := New(ICEConfig{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	huge := make([]byte, maxCandidateBytes+1)
	for i := range huge {
		huge[i] = 'x'
	}
	body, _ := json.Marshal(candidateBody{PeerID: "p", Candidate: string(huge)})
	resp := mustPost(t, srv.URL+"/relay/rendezvous/sess-big", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized candidate: want 413/400, got %d", resp.StatusCode)
	}
}

// A stale session is purged after its TTL so its memory is reclaimed even if
// the peers never complete.
func TestRendezvousStalePurge(t *testing.T) {
	svc := New(ICEConfig{})
	body, _ := json.Marshal(candidateBody{PeerID: "p", Candidate: "c"})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	mustPost(t, srv.URL+"/relay/rendezvous/stale-1", body).Body.Close()

	svc.mu.Lock()
	if len(svc.rendezvous) != 1 {
		svc.mu.Unlock()
		t.Fatalf("expected 1 session, got %d", len(svc.rendezvous))
	}
	// Age the session past the TTL, then trigger a purge via a new session.
	svc.rendezvous["stale-1"].lastSeen = time.Now().Add(-rendezvousTTL - time.Second)
	svc.mu.Unlock()

	mustPost(t, srv.URL+"/relay/rendezvous/fresh-1", body).Body.Close()

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if _, ok := svc.rendezvous["stale-1"]; ok {
		t.Fatal("stale session was not purged")
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func mustPost(t *testing.T, url string, body []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// Two relay instances never pair each other's stream peers: pending stream
// state is per-Service. It used to be a package-level map shared by every
// Service in the process, so a peer parked on one instance could be piped to a
// peer that connected to an unrelated instance with the same session key.
func TestStreamPendingIsPerService(t *testing.T) {
	a := httptest.NewServer(New(ICEConfig{}).Handler())
	defer a.Close()
	b := httptest.NewServer(New(ICEConfig{}).Handler())
	defer b.Close()

	first, err := wsconn.Dial(a.URL+"/relay/stream/shared-key", 2*time.Second)
	if err != nil {
		t.Fatalf("dial a: %v", err)
	}
	defer first.Close()
	second, err := wsconn.Dial(b.URL+"/relay/stream/shared-key", 2*time.Second)
	if err != nil {
		t.Fatalf("dial b: %v", err)
	}
	defer second.Close()

	if _, err := second.Write([]byte("cross-instance")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = first.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 64)
	if n, err := first.Read(buf); err == nil && n > 0 {
		t.Fatalf("peer on instance A received %q from instance B", buf[:n])
	}
}
