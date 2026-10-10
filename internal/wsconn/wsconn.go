// Package wsconn adapts an RFC 6455 WebSocket to a net.Conn carrying an opaque
// bidirectional byte stream in binary messages.
//
// It exists so the relay's byte-stream endpoint uses a standard WebSocket
// upgrade (which an L7 reverse proxy such as nginx recognizes and forwards)
// while both the relay server and the bridge client keep speaking plain
// net.Conn — the relay's pairing/piping and the bridge's sync engine are
// unchanged. The framing is handled by github.com/coder/websocket rather than
// hand-rolled, so masking, fragmentation, control frames, and close handshakes
// are a maintained dependency's concern.
package wsconn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

// Upgrade performs the RFC 6455 server handshake and returns a net.Conn whose
// Read/Write carry the peer byte stream as binary messages. On failure it
// writes an HTTP error and returns the error.
//
// Origin verification is disabled: the relay's only client is the non-browser
// bridge (which sends no Origin), and stream authorization is enforced by the
// sync-layer HMAC over the room id, not by Origin. The relay never inspects
// payloads.
func Upgrade(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote an HTTP error response.
		return nil, err
	}
	// A relay stream can be arbitrarily long-lived and large; the default read
	// limit (32 KiB per message) is far too small for file-sync payloads.
	c.SetReadLimit(-1)
	// The net.Conn lifetime is the connection's; a background context lets it
	// live until either side closes.
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}

// DialOption customizes Dial.
type DialOption func(*dialConfig)

type dialConfig struct {
	tlsConfig *tls.Config
}

// WithTLSConfig sets the TLS client configuration for an https/wss dial — e.g.
// a RootCAs pool for a private CA or a test server. Without it the system
// roots verify the server and SNI is the URL host. Verification is never
// disabled by this package.
func WithTLSConfig(cfg *tls.Config) DialOption {
	return func(c *dialConfig) { c.tlsConfig = cfg }
}

// Dial opens a WebSocket to a relay stream URL and returns a net.Conn carrying
// the peer byte stream. rawURL may be http/ws (local, plaintext) or https/wss
// (the production edge, which routes /relay/* on the gateway origin): https is
// dialed as wss with full certificate verification against the system roots
// (or WithTLSConfig's), SNI = the URL host. timeout bounds the handshake only;
// the returned stream lives until either side closes.
func Dial(rawURL string, timeout time.Duration, opts ...DialOption) (net.Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	var cfg dialConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	var dialOpts *websocket.DialOptions
	switch u.Scheme {
	case "http", "ws":
	case "https", "wss":
		if cfg.tlsConfig != nil {
			// No http.Client.Timeout: the handshake is bounded by ctx below and
			// the upgraded stream must outlive it.
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = cfg.tlsConfig.Clone()
			dialOpts = &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}}
		}
	default:
		return nil, fmt.Errorf("dial relay: unsupported scheme %q (want http, https, ws, or wss)", u.Scheme)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, _, err := websocket.Dial(ctx, u.String(), dialOpts)
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	c.SetReadLimit(-1)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}
