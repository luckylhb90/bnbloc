package sync

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wsconn"
)

// DialRelayStream connects to the bnbloc-relay byte-stream for sessionKey and
// returns a bidirectional net.Conn to the peer that connects with the same key.
//
// The transport is a standard RFC 6455 WebSocket (internal/wsconn) so the dial
// traverses an L7 reverse proxy, letting the relay share a domain with the
// gateway. relayBaseURL is the relay's base: plain http locally (e.g.
// http://relay:8085), or the production https origin whose edge routes
// /relay/* to the relay — dialed as wss with system-root verification.
func DialRelayStream(relayBaseURL, sessionKey string, timeout time.Duration) (net.Conn, error) {
	base, err := url.Parse(strings.TrimRight(relayBaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("relay url: %w", err)
	}
	base.Path += "/relay/stream/" + url.PathEscape(sessionKey)
	return wsconn.Dial(base.String(), timeout)
}
