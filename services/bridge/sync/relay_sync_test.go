package sync

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	relaysvc "github.com/luckylhb90/bnbloc/services/relay"
)

// startRelay boots a real bnbloc-relay on a loopback port and returns its base URL.
func startRelay(t *testing.T) string {
	t.Helper()
	svc := relaysvc.New()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: svc.Handler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return "http://" + ln.Addr().String()
}

// dialBoth pairs two peers on the relay for one session key.
func dialBoth(t *testing.T, base, key string) (net.Conn, net.Conn) {
	t.Helper()
	type res struct {
		c net.Conn
		e error
	}
	ch := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() { c, e := DialRelayStream(base, key, 5*time.Second); ch <- res{c, e} }()
	}
	var conns []net.Conn
	for i := 0; i < 2; i++ {
		r := <-ch
		if r.e != nil {
			t.Fatalf("relay dial: %v", r.e)
		}
		conns = append(conns, r.c)
	}
	return conns[0], conns[1]
}

// TestSyncOverRealRelay proves the engine converges over the ACTUAL relay raw
// stream transport (not an in-memory pipe): initial reconcile, incremental
// propagation, and a concurrent-edit three-way merge — end to end.
func TestSyncOverRealRelay(t *testing.T) {
	base := startRelay(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	write(t, dirA, "readme.md", "shared\nproject")

	connA, connB := dialBoth(t, base, "sync:room-1")
	ctx, cancel := context.WithCancel(context.Background())
	// Stop engines before t.TempDir cleanup so no goroutine writes the sidecar
	// concurrently with RemoveAll.
	t.Cleanup(cancel)
	ea := &goSync{}
	eb := &goSync{}
	runSync(t, ctx, ea, SyncConfig{Dir: dirA, Conn: connA, SelfID: "alice", PeerID: "bob"})
	runSync(t, ctx, eb, SyncConfig{Dir: dirB, Conn: connB, SelfID: "bob", PeerID: "alice"})

	// Initial reconcile A->B over the relay.
	if !waitFor(t, filepath.Join(dirB, "readme.md"), "shared\nproject", 6*time.Second) {
		t.Fatal("initial file did not sync over the relay")
	}
	// Incremental B->A over the relay.
	write(t, dirB, "src/main.go", "package main")
	if !waitFor(t, filepath.Join(dirA, "src/main.go"), "package main", 6*time.Second) {
		t.Fatal("incremental file did not sync over the relay")
	}
}

// TestSyncMergeOverRealRelay proves a clean concurrent three-way merge converges
// over the real relay transport.
func TestSyncMergeOverRealRelay(t *testing.T) {
	base := startRelay(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	write(t, dirA, "f.txt", "l1\nl2\nl3")

	run := func(ctx context.Context) (<-chan struct{}, <-chan struct{}) {
		ca, cb := dialBoth(t, base, "sync:merge-room")
		da := runSync(t, ctx, &goSync{}, SyncConfig{Dir: dirA, Conn: ca, SelfID: "alice", PeerID: "bob"})
		db := runSync(t, ctx, &goSync{}, SyncConfig{Dir: dirB, Conn: cb, SelfID: "bob", PeerID: "alice"})
		return da, db
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	d1a, d1b := run(ctx1)
	if !waitFor(t, filepath.Join(dirB, "f.txt"), "l1\nl2\nl3", 6*time.Second) {
		cancel1()
		t.Fatal("base did not converge over relay")
	}
	cancel1()
	waitStopped(t, d1a, d1b)

	// Concurrent non-overlapping edits.
	write(t, dirA, "f.txt", "l1-alice\nl2\nl3")
	write(t, dirB, "f.txt", "l1\nl2\nl3-bob")

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	run(ctx2)
	want := "l1-alice\nl2\nl3-bob"
	okA := waitFor(t, filepath.Join(dirA, "f.txt"), want, 8*time.Second)
	okB := waitFor(t, filepath.Join(dirB, "f.txt"), want, 8*time.Second)
	if !okA || !okB {
		a, _ := os.ReadFile(filepath.Join(dirA, "f.txt"))
		b, _ := os.ReadFile(filepath.Join(dirB, "f.txt"))
		t.Fatalf("3-way merge did not converge over relay: A=%q B=%q", a, b)
	}
}
