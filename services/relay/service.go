// Package relay implements the bnbloc-relay service: a byte-stream relay that
// pairs two peers on a session key (GET /relay/stream/{sessionKey}). Payloads
// are never inspected or logged.
package relay

import (
	"net/http"
	"sync"
	"time"
)

// Rendezvous is where the two halves of a session meet when they reach
// different replicas: the first records its replica there, the second finds
// it and joins it (redisbus.Bus over the replicas' Redis). Nil: one
// replica, every half meets here.
type Rendezvous interface {
	// Meet records holder at key for ttl unless another holder is there,
	// and returns the holder at key now (holder itself when it was first).
	Meet(key, holder string, ttl time.Duration) (string, error)
	// Leave deletes key's record if it is still holder's.
	Leave(key, holder string) error
}

// Service holds the relay handler dependencies.
type Service struct {
	// streamMu guards pendingStreams: the first peer parked on each stream
	// session key, waiting for its partner. Per-Service (not package-global) so
	// independent relay instances never pair each other's connections.
	streamMu       sync.Mutex
	pendingStreams map[string]*streamPair

	// Meet, with more than one replica, pairs halves across replicas; Self
	// is this replica's address as the others reach it (host:port).
	Meet Rendezvous
	Self string
}

// New constructs a relay Service.
func New() *Service {
	return &Service{pendingStreams: map[string]*streamPair{}}
}

// Handler builds the HTTP mux for the relay service.
//
// The relay lives under the top-level /relay/ prefix (not /v1/relay/) so a
// single-domain reverse proxy routes it with a distinct, non-nested prefix
// and never confuses it with the gateway's /v1/ surface. Its client is the
// desktop bridge's code sync (services/bridge/sync), not a versioned API
// caller. /internal/relay/join is another replica's half joining one parked
// here; the edge never routes /internal.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /relay/stream/{sessionKey}", s.handleStream)
	mux.HandleFunc("GET /internal/relay/join/{sessionKey}", s.handleJoin)
	return mux
}
