package relay

import (
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wsconn"
)

// Two peers connecting to the same stream session exchange bytes end-to-end
// over the WebSocket transport, in both directions.
func TestStreamRelayPipesBytes(t *testing.T) {
	svc := New()
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
