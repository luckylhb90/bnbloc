package sync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSyncRejectsTraversalPath proves a peer cannot write outside the mirrored
// directory: a hand-crafted file frame with a traversal path is dropped, and no
// file lands above the sync root.
func TestSyncRejectsTraversalPath(t *testing.T) {
	base := startRelay(t)
	root := t.TempDir()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}

	connA, connB := dialBoth(t, base, "sync:traversal")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// B is a real engine; A is a hostile raw peer that sends a malicious frame.
	eb := &goSync{}
	runSync(t, ctx, eb, SyncConfig{Dir: dirB, Conn: connB, SelfID: "bob", PeerID: "alice"})

	enc := json.NewEncoder(connA)
	// Authenticate (empty token matches eb's empty token), then attack.
	_ = enc.Encode(frame{Type: "hello", Version: protocolVersion, Peer: "alice"})
	_ = enc.Encode(frame{
		Type:    "file",
		Path:    "../pwned.txt",
		Content: []byte("owned"),
		Mode:    0o644,
	})
	// Give the victim time to (not) apply it.
	time.Sleep(1 * time.Second)

	escaped := filepath.Join(root, "pwned.txt")
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("traversal write escaped the sync root: %s exists", escaped)
	}
	if _, err := os.Stat(filepath.Join(dirB, "pwned.txt")); err == nil {
		t.Fatal("traversal path was normalized into the root instead of being rejected")
	}
}

// TestSyncAuthTokenMismatchAborts proves a peer presenting the wrong room token
// never receives the victim's manifest and the victim's engine ends in error.
func TestSyncAuthTokenMismatchAborts(t *testing.T) {
	base := startRelay(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	write(t, dirB, "secret.txt", "top secret")

	connA, connB := dialBoth(t, base, "sync:auth")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errc := make(chan error, 1)
	eb := &goSync{}
	go func() {
		errc <- eb.Start(ctx, SyncConfig{Dir: dirB, Conn: connB, SelfID: "bob", PeerID: "alice", AuthToken: "correct-secret"})
	}()

	// Attacker sends a HELLO with the wrong token.
	enc := json.NewEncoder(connA)
	_ = enc.Encode(frame{Type: "hello", Version: protocolVersion, Peer: "alice", Token: "wrong-secret"})

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("engine accepted a peer with a mismatched auth token")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not abort on auth token mismatch")
	}
	// The victim never sent its manifest, so the attacker learned no file paths.
	if _, err := os.Stat(filepath.Join(dirA, "secret.txt")); err == nil {
		t.Fatal("attacker received a file despite auth failure")
	}
}

// TestSyncAuthTokenMatchSyncs proves two peers sharing the correct token pair and
// converge over the real relay.
func TestSyncAuthTokenMatchSyncs(t *testing.T) {
	base := startRelay(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	write(t, dirA, "shared.txt", "hello")

	connA, connB := dialBoth(t, base, "sync:auth-ok")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ea := &goSync{}
	eb := &goSync{}
	runSync(t, ctx, ea, SyncConfig{Dir: dirA, Conn: connA, SelfID: "alice", PeerID: "bob", AuthToken: "shared-secret"})
	runSync(t, ctx, eb, SyncConfig{Dir: dirB, Conn: connB, SelfID: "bob", PeerID: "alice", AuthToken: "shared-secret"})

	if !waitFor(t, filepath.Join(dirB, "shared.txt"), "hello", 6*time.Second) {
		t.Fatal("authenticated peers did not sync")
	}
}

// TestSyncSkipsOversizeAndIgnoredIncoming proves an oversized file frame and an
// ignored-path frame from a peer are both dropped.
func TestSyncSkipsOversizeAndIgnoredIncoming(t *testing.T) {
	base := startRelay(t)
	dirB := t.TempDir()

	connA, connB := dialBoth(t, base, "sync:caps")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eb := &goSync{}
	runSync(t, ctx, eb, SyncConfig{Dir: dirB, Conn: connB, SelfID: "bob", PeerID: "alice"})

	enc := json.NewEncoder(connA)
	_ = enc.Encode(frame{Type: "hello", Version: protocolVersion, Peer: "alice"})
	// Ignored path (inside .git) must not be written.
	_ = enc.Encode(frame{Type: "file", Path: ".git/config", Content: []byte("[core]"), Mode: 0o644})
	// Oversized file must not be written.
	big := make([]byte, maxFileBytes+1)
	_ = enc.Encode(frame{Type: "file", Path: "big.bin", Content: big, Mode: 0o644})
	time.Sleep(1 * time.Second)

	if _, err := os.Stat(filepath.Join(dirB, ".git/config")); err == nil {
		t.Fatal("ignored incoming path was written")
	}
	if _, err := os.Stat(filepath.Join(dirB, "big.bin")); err == nil {
		t.Fatal("oversized incoming file was written")
	}
}
