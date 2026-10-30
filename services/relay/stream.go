package relay

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/wsconn"
)

// pairTimeout bounds how long a lone first peer parks waiting for its partner.
// A half-open stream whose peer never arrives (random/abandoned sessionKey) is
// purged after this so it cannot leak the FD + map entry indefinitely.
const pairTimeout = 45 * time.Second

// joinDialTimeout bounds the dial to the replica holding the first half.
const joinDialTimeout = 5 * time.Second

// streamPair holds the first peer waiting for a partner on a session key.
// token, with more than one replica, is the secret another replica's half
// joins it with; holder is its rendezvous record.
type streamPair struct {
	conn   net.Conn
	done   chan struct{}
	token  string
	holder string
}

// handleStream relays raw bytes between the two peers that connect to the same
// session key. The transport is a standard RFC 6455 WebSocket so an L7 reverse
// proxy can forward it; the connection is adapted to net.Conn (internal/wsconn)
// so the pairing and piping below are unchanged. The first connection parks
// until the second arrives, then bytes are piped bidirectionally. The relay
// never inspects or logs payloads.
//
// With more than one replica the halves may reach different ones: the first
// records its replica at the rendezvous; the second, landing elsewhere,
// finds it there and joins it over /internal/relay/join, piping its own
// connection to that one.
func (s *Service) handleStream(w http.ResponseWriter, r *http.Request) {
	sessionKey := r.PathValue("sessionKey")
	if sessionKey == "" {
		httpx.Error(w, "session key required", http.StatusBadRequest)
		return
	}
	conn, err := wsconn.Upgrade(w, r)
	if err != nil {
		// wsconn.Upgrade already wrote the HTTP error.
		return
	}
	for attempt := 0; ; attempt++ {
		partner, self, elsewhere := s.matchStream(sessionKey, conn)
		switch {
		case partner != nil:
			// We are the second peer; pipe both directions and signal the
			// first to end.
			s.leave(sessionKey, partner)
			pipe(conn, partner.conn)
			close(partner.done)
			return
		case self != nil:
			s.park(r, sessionKey, self)
			return
		}
		// The first half is parked on another replica: join it there.
		remote, err := s.join(sessionKey, elsewhere)
		if err == nil {
			pipe(conn, remote)
			return
		}
		// Its replica is gone (a stale record): clear it and meet again.
		log.Printf("bnbloc-relay: joining a session's first half failed: %v", err)
		_ = s.Meet.Leave(sessionKey, elsewhere)
		if attempt == 1 {
			_ = conn.Close()
			return
		}
	}
}

// park waits for a partner to pipe the first peer, its own request context
// to end (client disconnected), or the pairing deadline; the last two purge
// the half-open session so a peer that never arrives can't leak this FD and
// the map entry.
func (s *Service) park(r *http.Request, sessionKey string, self *streamPair) {
	timer := time.NewTimer(pairTimeout)
	defer timer.Stop()
	select {
	case <-self.done:
		// A partner matched and finished piping (the conn is already closed).
	case <-r.Context().Done():
		s.cleanupPending(sessionKey, self)
	case <-timer.C:
		s.cleanupPending(sessionKey, self)
	}
}

// matchStream pairs conn with a peer parked here on the session key
// (partner), or parks it (self), or reports the rendezvous record of the
// replica the first half is parked on (elsewhere).
func (s *Service) matchStream(sessionKey string, conn net.Conn) (partner, self *streamPair, elsewhere string) {
	s.streamMu.Lock()
	if pending, ok := s.pendingStreams[sessionKey]; ok {
		delete(s.pendingStreams, sessionKey)
		s.streamMu.Unlock()
		return pending, nil, ""
	}
	s.streamMu.Unlock()
	sp := &streamPair{conn: conn, done: make(chan struct{})}
	if s.Meet != nil {
		sp.token = randomToken()
		sp.holder = s.Self + " " + sp.token
		for range 2 {
			at, err := s.Meet.Meet(sessionKey, sp.holder, pairTimeout)
			if err != nil {
				// Without the rendezvous a half can still meet a partner
				// that reaches this replica.
				log.Printf("bnbloc-relay: rendezvous unavailable (pairing on this replica only): %v", err)
				break
			}
			if at == sp.holder {
				break
			}
			if !strings.HasPrefix(at, s.Self+" ") {
				return nil, nil, at
			}
			s.streamMu.Lock()
			_, parkedHere := s.pendingStreams[sessionKey]
			s.streamMu.Unlock()
			if parkedHere {
				break // paired below
			}
			_ = s.Meet.Leave(sessionKey, at) // a record of this replica's that outlived its half
		}
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if pending, ok := s.pendingStreams[sessionKey]; ok {
		// The first half parked here meanwhile.
		delete(s.pendingStreams, sessionKey)
		if sp.holder != "" {
			_ = s.Meet.Leave(sessionKey, sp.holder)
		}
		return pending, nil, ""
	}
	s.pendingStreams[sessionKey] = sp
	return nil, sp, ""
}

// join dials the replica the record names and joins the half parked there.
func (s *Service) join(sessionKey, record string) (net.Conn, error) {
	addr, token, _ := strings.Cut(record, " ")
	return wsconn.Dial("http://"+addr+"/internal/relay/join/"+url.PathEscape(sessionKey)+"?t="+url.QueryEscape(token), joinDialTimeout)
}

// handleJoin pipes another replica's half to the first half parked here,
// when the token is the one the parked half recorded.
func (s *Service) handleJoin(w http.ResponseWriter, r *http.Request) {
	sessionKey, token := r.PathValue("sessionKey"), r.URL.Query().Get("t")
	s.streamMu.Lock()
	pending, ok := s.pendingStreams[sessionKey]
	if ok && token != "" && pending.token == token {
		delete(s.pendingStreams, sessionKey)
	} else {
		ok = false
	}
	s.streamMu.Unlock()
	if !ok {
		httpx.Error(w, "no such parked session", http.StatusNotFound)
		return
	}
	s.leave(sessionKey, pending)
	conn, err := wsconn.Upgrade(w, r)
	if err != nil {
		_ = pending.conn.Close()
		close(pending.done)
		return
	}
	pipe(conn, pending.conn)
	close(pending.done)
}

// leave clears a paired half's rendezvous record.
func (s *Service) leave(sessionKey string, sp *streamPair) {
	if s.Meet != nil && sp.holder != "" {
		_ = s.Meet.Leave(sessionKey, sp.holder)
	}
}

// cleanupPending purges a half-open first peer that never got a partner: it
// removes the map entry and its rendezvous record and closes the dangling
// conn to release the FD, but ONLY if the entry still refers to this exact
// pair. If a partner already matched (and deleted the entry), it now owns
// piping this conn, so we must not close it out from under an active
// transfer.
func (s *Service) cleanupPending(sessionKey string, self *streamPair) {
	s.streamMu.Lock()
	pending, ok := s.pendingStreams[sessionKey]
	mine := ok && pending == self
	if mine {
		delete(s.pendingStreams, sessionKey)
	}
	s.streamMu.Unlock()
	if mine {
		s.leave(sessionKey, self)
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

func randomToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
