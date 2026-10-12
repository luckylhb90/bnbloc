// Package relay implements the bnbloc-relay service: a byte-stream relay that
// pairs two peers on a session key (GET /relay/stream/{sessionKey}). Payloads
// are never inspected or logged.
package relay

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/shard"
)

// Service holds the relay handler dependencies.
type Service struct {
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

// New constructs a relay Service.
func New() *Service {
	return &Service{pendingStreams: map[string]*streamPair{}}
}

// SessionKey is a relay request's ownership key: the stream route's session
// key.
func SessionKey(r *http.Request) (string, bool) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/relay/stream/")
	if !ok {
		return "", false
	}
	key, err := url.PathUnescape(rest)
	if err != nil || strings.TrimSpace(key) == "" || strings.Contains(key, "/") {
		return "", false
	}
	return "relay:" + key, true
}

// release closes the first peer still parked for its partner on a session
// key this replica no longer owns (after every peer-set refresh), so it
// reconnects to the key's new owner where its partner will meet it. An
// established pipe is left alone: both halves are already here and no longer
// depend on ownership.
func (s *Service) release(bool) {
	owned := func(key string) bool { return s.Peers.Owns("relay:" + key) }
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
	if len(parked) > 0 {
		log.Printf("bnbloc-relay: ownership moved: closed %d parked stream(s) now owned by another replica", len(parked))
	}
}

// Handler builds the HTTP mux for the relay service.
//
// The relay lives under the top-level /relay/ prefix (not /v1/relay/) so a
// single-domain reverse proxy routes it with a distinct, non-nested prefix
// and never confuses it with the gateway's /v1/ surface. Its client is the
// desktop bridge's code sync (services/bridge/sync), not a versioned API
// caller.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /relay/stream/{sessionKey}", s.handleStream)
	s.Peers.OnRefresh(s.release)
	return shard.Forward(s.Peers, SessionKey, mux)
}
