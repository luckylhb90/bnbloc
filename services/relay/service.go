// Package relay implements the bnbloc-relay service: server-authoritative ICE
// configuration, a Trickle ICE rendezvous channel for candidate exchange, and
// a byte-stream relay that pairs two peers on a session key. Payloads are
// never inspected or logged.
package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/shard"
)

// ICEConfig is the server-authoritative STUN/relay configuration handed to
// peers before they attempt a direct link.
type ICEConfig struct {
	StunServers    []string `json:"stunServers"`
	RelayEndpoints []string `json:"relayEndpoints"`
}

// Rendezvous quotas bound the memory an unauthenticated caller can make the
// relay hold. The rendezvous endpoints have no auth (pairing is proven inside
// the stream), so without these a client could exhaust memory with many random
// session keys or an unbounded candidate queue.
const (
	// maxRendezvousSessions caps concurrently tracked session keys. A publish
	// that would create a new session beyond this is refused until stale
	// sessions age out.
	maxRendezvousSessions = 10000
	// maxCandidatesPerPeer caps queued candidate blobs per peer in one session.
	// Trickle ICE needs only a handful; this is generous headroom.
	maxCandidatesPerPeer = 64
	// maxCandidateBytes caps a single candidate body. ICE candidates are short
	// strings; this stops a large-body memory amplification.
	maxCandidateBytes = 8 << 10
	// rendezvousTTL purges a session with no activity, releasing its memory even
	// if the peers never complete or poll.
	rendezvousTTL = 2 * time.Minute
)

// Service holds the relay handler dependencies.
type Service struct {
	Config ICEConfig

	mu         sync.Mutex
	rendezvous map[string]*pairing // sessionKey -> pairing

	// streamMu guards pendingStreams: the first peer parked on each stream
	// session key, waiting for its partner. Per-Service (not package-global) so
	// independent relay instances never pair each other's connections.
	streamMu       sync.Mutex
	pendingStreams map[string]*streamPair

	// Peers is the scale-out replica set (BNBLOC_RELAY_PEERS): both halves of
	// a pairing must meet on one replica, so every session-keyed request is
	// forwarded to the session key's owner. Nil is a single node.
	Peers *shard.Set
}

// New constructs a relay Service with the given ICE configuration.
func New(cfg ICEConfig) *Service {
	return &Service{Config: cfg, rendezvous: map[string]*pairing{}, pendingStreams: map[string]*streamPair{}}
}

// SessionKey is a relay request's ownership key: the session key of the
// rendezvous and stream routes (the ICE config is served anywhere).
func SessionKey(r *http.Request) (string, bool) {
	for _, prefix := range []string{"/relay/rendezvous/", "/relay/stream/"} {
		if rest, ok := strings.CutPrefix(r.URL.EscapedPath(), prefix); ok {
			key, err := url.PathUnescape(rest)
			if err != nil || strings.TrimSpace(key) == "" || strings.Contains(key, "/") {
				return "", false
			}
			return "relay:" + key, true
		}
	}
	return "", false
}

// release drops the pairing state of session keys this replica no longer
// owns (after every peer-set refresh): queued rendezvous candidates are
// discarded and a first peer still parked for its partner is closed, so it
// reconnects to the key's new owner where its partner will meet it. An
// established pipe is left alone: both halves are already here and no longer
// depend on ownership.
func (s *Service) release(bool) {
	owned := func(key string) bool { return s.Peers.Owns("relay:" + key) }
	s.mu.Lock()
	dropped := 0
	for key := range s.rendezvous {
		if !owned(key) {
			delete(s.rendezvous, key)
			dropped++
		}
	}
	s.mu.Unlock()
	var parked []*streamPair
	s.streamMu.Lock()
	for key, sp := range s.pendingStreams {
		if !owned(key) {
			delete(s.pendingStreams, key)
			parked = append(parked, sp)
		}
	}
	s.streamMu.Unlock()
	for _, sp := range parked {
		// Removed from the map first, so no partner can match it any more:
		// closing done here is the only close. The WebSocket close handshake
		// may wait on the peer, so it never runs on the refresh goroutine.
		close(sp.done)
		go func() { _ = sp.conn.Close() }()
	}
	if dropped > 0 || len(parked) > 0 {
		log.Printf("bnbloc-relay: ownership moved: dropped %d rendezvous session(s) and %d parked stream(s) now owned by another replica", dropped, len(parked))
	}
}

// Handler builds the HTTP mux for the relay service.
//
// Relay endpoints live under the top-level /relay/ prefix (not /v1/relay/) so a
// single-domain reverse proxy routes them with a distinct, non-nested prefix
// and never confuses them with the gateway's /v1/ surface. The relay is not
// part of the versioned gateway API; its clients are the desktop bridge and the
// gateway's ICE config, not versioned application callers.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /relay/ice-config", s.handleICEConfig)
	mux.HandleFunc("POST /relay/rendezvous/{sessionKey}", s.handleRendezvousPublish)
	mux.HandleFunc("GET /relay/rendezvous/{sessionKey}", s.handleRendezvousPoll)
	mux.HandleFunc("GET /relay/stream/{sessionKey}", s.handleStream)
	s.Peers.OnRefresh(s.release)
	return shard.Forward(s.Peers, SessionKey, mux)
}

func (s *Service) handleICEConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Config)
}

// pairing holds one rendezvous session: the opaque candidate blobs each peer
// published for the other. Polling is the only delivery path (no long-poll).
type pairing struct {
	mu         sync.Mutex
	candidates map[string][][]byte // peerID -> queued candidate blobs for the OTHER peer
	lastSeen   time.Time           // for stale-session purging
}

func newPairing() *pairing {
	return &pairing{candidates: map[string][][]byte{}, lastSeen: time.Now()}
}

// pairingFor returns the pairing for a session key, creating it if absent. It
// returns ok=false when a new session would exceed maxRendezvousSessions (after
// purging stale ones), so the caller can refuse rather than grow unbounded.
func (s *Service) pairingFor(sessionKey string) (*pairing, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.rendezvous[sessionKey]; p != nil {
		p.lastSeen = time.Now()
		return p, true
	}
	s.purgeStaleLocked()
	if len(s.rendezvous) >= maxRendezvousSessions {
		return nil, false
	}
	p := newPairing()
	s.rendezvous[sessionKey] = p
	return p, true
}

// purgeStaleLocked drops sessions idle past rendezvousTTL. Caller holds s.mu.
func (s *Service) purgeStaleLocked() {
	cutoff := time.Now().Add(-rendezvousTTL)
	for key, p := range s.rendezvous {
		if p.lastSeen.Before(cutoff) {
			delete(s.rendezvous, key)
		}
	}
}

type candidateBody struct {
	PeerID    string `json:"peerId"`
	Candidate string `json:"candidate"` // opaque base64 blob; never inspected
}

// handleRendezvousPublish queues a candidate for delivery to the peer.
func (s *Service) handleRendezvousPublish(w http.ResponseWriter, r *http.Request) {
	sessionKey := r.PathValue("sessionKey")
	if strings.TrimSpace(sessionKey) == "" {
		httpx.Error(w, "session key required", http.StatusBadRequest)
		return
	}
	var body candidateBody
	// Cap the body so a single publish cannot amplify memory. The limit covers
	// the JSON envelope around a maxCandidateBytes candidate.
	if !httpx.DecodeJSON(w, r, maxCandidateBytes+512, &body) {
		return
	}
	if len(body.Candidate) > maxCandidateBytes {
		httpx.Error(w, "candidate too large", http.StatusRequestEntityTooLarge)
		return
	}
	p, ok := s.pairingFor(sessionKey)
	if !ok {
		obs.ServerError(w, "too many sessions", nil)
		httpx.Error(w, "too many sessions", http.StatusServiceUnavailable)
		return
	}
	p.mu.Lock()
	// The candidate is destined for the OTHER peer; key by publisher so the
	// poller drains everything not authored by itself. Enforce a per-peer queue
	// cap so a peer that publishes without the other ever polling cannot grow
	// the queue without bound.
	if len(p.candidates[body.PeerID]) >= maxCandidatesPerPeer {
		p.mu.Unlock()
		httpx.Error(w, "candidate queue full", http.StatusTooManyRequests)
		return
	}
	p.candidates[body.PeerID] = append(p.candidates[body.PeerID], []byte(body.Candidate))
	p.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// handleRendezvousPoll returns candidates published by the other peer.
func (s *Service) handleRendezvousPoll(w http.ResponseWriter, r *http.Request) {
	sessionKey := r.PathValue("sessionKey")
	self := r.URL.Query().Get("peerId")
	p, ok := s.pairingFor(sessionKey)
	if !ok {
		// At capacity and this is a new key: nothing queued for it anyway.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string][]string{"candidates": {}})
		return
	}
	p.mu.Lock()
	out := [][]byte{}
	for peerID, blobs := range p.candidates {
		if peerID == self {
			continue
		}
		out = append(out, blobs...)
		p.candidates[peerID] = nil
	}
	p.mu.Unlock()

	candidates := make([]string, len(out))
	for i, blob := range out {
		candidates[i] = string(blob)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"candidates": candidates})
}
