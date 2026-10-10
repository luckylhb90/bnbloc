package wsconn

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Dial and Upgrade complete a real RFC 6455 handshake and carry a bidirectional
// byte stream: what the client writes the server reads, and vice versa. This
// guards the frame codec (masking, length encoding) that both the relay server
// and the bridge client share.
func TestDialUpgradeRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		// Echo one message back with a prefix so the test sees both directions.
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		if _, err := conn.Write(append([]byte("echo:"), buf[:n]...)); err != nil {
			t.Errorf("server write: %v", err)
		}
	}))
	defer srv.Close()

	conn, err := Dial(srv.URL+"/relay/stream/sess", 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	buf := make([]byte, 1024)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if got := string(buf[:n]); got != "echo:ping" {
		t.Fatalf("client received %q, want echo:ping", got)
	}
}

// A payload larger than a single Read buffer is reassembled without loss across
// successive Reads (the codec must carry the frame remainder).
func TestLargePayloadReassembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		big := make([]byte, 200000) // exceeds the 16-bit frame length boundary
		for i := range big {
			big[i] = byte(i)
		}
		if _, err := conn.Write(big); err != nil {
			t.Errorf("server write: %v", err)
		}
	}))
	defer srv.Close()

	conn, err := Dial(srv.URL+"/relay/stream/sess", 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, 0, 200000)
	buf := make([]byte, 4096)
	for len(got) < 200000 {
		n, err := conn.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if err != nil {
			t.Fatalf("read after %d bytes: %v", len(got), err)
		}
	}
	if len(got) != 200000 {
		t.Fatalf("reassembled %d bytes, want 200000", len(got))
	}
	for i := range got {
		if got[i] != byte(i) {
			t.Fatalf("byte %d = %d, want %d", i, got[i], byte(i))
		}
	}
}

// A TLS/wss upstream is rejected rather than silently dialed plaintext: the
// caller terminates TLS at a proxy and dials the http upstream behind it.
func TestDialRejectsTLSUpstream(t *testing.T) {
	if _, err := Dial("https://relay.example.com/relay/stream/x", time.Second); err == nil {
		t.Fatal("expected TLS upstream to be rejected")
	}
}

// A non-WebSocket request is rejected by Upgrade (426 Upgrade Required), not
// hijacked or served as a normal response.
func TestUpgradeRejectsPlainRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Upgrade(w, r); err == nil {
			t.Error("expected upgrade to reject a plain request")
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", resp.StatusCode)
	}
}

func echoTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		_, _ = conn.Write(append([]byte("tls:"), buf[:n]...))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The production edge serves the relay over https on the gateway origin: Dial
// must speak wss with certificate verification (trust injected per call, not
// a global insecure switch).
func TestDialTLSRoundTrip(t *testing.T) {
	srv := echoTLSServer(t)
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	for _, base := range []string{srv.URL, "wss" + strings.TrimPrefix(srv.URL, "https")} {
		conn, err := Dial(base+"/relay/stream/sess", 2*time.Second, WithTLSConfig(&tls.Config{RootCAs: pool}))
		if err != nil {
			t.Fatalf("dial %s: %v", base, err)
		}
		if _, err := conn.Write([]byte("hi")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if err != nil || string(buf[:n]) != "tls:hi" {
			t.Fatalf("read = %q, %v; want tls:hi", buf[:n], err)
		}
		conn.Close()
	}
}

// Without trust for the server's certificate the dial fails: verification is
// on by default.
func TestDialTLSVerifiesCertificate(t *testing.T) {
	srv := echoTLSServer(t)
	if _, err := Dial(srv.URL+"/relay/stream/sess", 2*time.Second); err == nil {
		t.Fatal("dial to an untrusted certificate must fail")
	}
	if _, err := Dial("ftp://relay/x", time.Second); err == nil {
		t.Fatal("unsupported scheme must be rejected")
	}
}
