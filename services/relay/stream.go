package relay

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wsconn"
)

// pairTimeout bounds how long a lone first peer parks waiting for its partner.
// A half-open stream whose peer never arrives (random/abandoned sessionKey) is
// purged after this so it cannot leak the FD + map entry indefinitely.
const pairTimeout = 45 * time.Second

// streamPair holds the first peer waiting for a partner on a session key.
type streamPair struct {
	conn net.Conn
	done chan struct{}
}

// handleStream relays raw bytes between the two peers that connect to the same
// session key. The transport is a standard RFC 6455 WebSocket so an L7 reverse
// proxy can forward it; the connection is adapted to net.Conn (internal/wsconn)
// so the pairing and piping below are unchanged. The first connection parks
// until the second arrives, then bytes are piped bidirectionally. The relay
// never inspects or logs payloads.
func (s *Service) handleStream(w http.ResponseWriter, r *http.Request) {
	sessionKey := r.PathValue("sessionKey")
	if sessionKey == "" {
		http.Error(w, "session key required", http.StatusBadRequest)
		return
	}
	conn, err := wsconn.Upgrade(w, r)
	if err != nil {
		// wsconn.Upgrade already wrote the HTTP error.
		return
	}

	partner, self := s.matchStream(sessionKey, conn)
	if partner == nil {
		// We are the first peer; park until a partner pipes us, our own request
		// context ends (client disconnected), or the pairing deadline fires. The
		// last two purge the half-open session so a peer that never arrives can't
		// leak this FD + the map entry.
		timer := time.NewTimer(pairTimeout)
		defer timer.Stop()
		select {
		case <-self.done:
			// A partner matched and finished piping (the conn is already
			// closed), or the key moved to another replica (release closed it).
		case <-r.Context().Done():
			s.cleanupPending(sessionKey, self)
		case <-timer.C:
			s.cleanupPending(sessionKey, self)
		}
		return
	}
	// We are the second peer; pipe both directions and signal the first to end.
	pipe(conn, partner.conn)
	close(partner.done)
}

// matchStream registers conn as the pending peer for a session key, or returns
// the already-pending peer to pair with. When it parks conn (partner==nil), it
// also returns the freshly registered streamPair so the caller can wait on it
// and clean it up if no partner arrives.
func (s *Service) matchStream(sessionKey string, conn net.Conn) (partner, self *streamPair) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if pending, ok := s.pendingStreams[sessionKey]; ok {
		delete(s.pendingStreams, sessionKey)
		return pending, nil
	}
	sp := &streamPair{conn: conn, done: make(chan struct{})}
	s.pendingStreams[sessionKey] = sp
	return nil, sp
}

// cleanupPending purges a half-open first peer that never got a partner: it
// removes the map entry and closes the dangling conn to release the FD, but
// ONLY if the entry still refers to this exact pair. If a partner already
// matched (and deleted the entry), it now owns piping this conn, so we must not
// close it out from under an active transfer.
func (s *Service) cleanupPending(sessionKey string, self *streamPair) {
	s.streamMu.Lock()
	pending, ok := s.pendingStreams[sessionKey]
	mine := ok && pending == self
	if mine {
		delete(s.pendingStreams, sessionKey)
	}
	s.streamMu.Unlock()
	if mine {
		_ = self.conn.Close()
	}
}

// pipe copies bytes in both directions until either side closes.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(a, b); _ = a.Close() }()
	go func() { defer wg.Done(); _, _ = io.Copy(b, a); _ = b.Close() }()
	wg.Wait()
}
