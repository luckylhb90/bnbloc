package sync

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runSync runs e.Start in the background under a child of ctx and returns a
// channel closed when Start has returned. Start's exit saves the sync index
// into cfg.Dir, so the test must not remove that dir (t.TempDir cleanup) or
// reuse it with a new engine until then: the registered cleanup stops the
// engine and waits for it, and callers wait on the channel between phases.
// (Sleeping instead raced the index save and failed cleanup intermittently.)
func runSync(t *testing.T, ctx context.Context, e *goSync, cfg SyncConfig) <-chan struct{} {
	t.Helper()
	ctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.Start(ctx, cfg)
	}()
	t.Cleanup(func() {
		stop()
		waitStopped(t, done)
	})
	return done
}

// waitStopped waits for engines started with runSync to return.
func waitStopped(t *testing.T, dones ...<-chan struct{}) {
	t.Helper()
	for _, d := range dones {
		select {
		case <-d:
		case <-time.After(10 * time.Second):
			t.Error("sync engine did not stop")
		}
	}
}

// startPair wires two gosync engines over an in-memory pipe (stands in for the
// relay raw stream) against two temp dirs, returning a stop func.
func startPair(t *testing.T, dirA, dirB string) func() {
	t.Helper()
	ca, cb := loopbackPair(t)
	ea := &goSync{}
	eb := &goSync{}
	ctx, cancel := context.WithCancel(context.Background())
	da := runSync(t, ctx, ea, SyncConfig{Dir: dirA, Conn: ca, SelfID: "A", PeerID: "B"})
	db := runSync(t, ctx, eb, SyncConfig{Dir: dirB, Conn: cb, SelfID: "B", PeerID: "A"})
	return func() {
		cancel()
		ca.Close()
		cb.Close()
		waitStopped(t, da, db)
	}
}

// loopbackPair returns two connected buffered TCP conns over loopback. Unlike
// net.Pipe (synchronous, unbuffered), this matches the relay's buffered stream
// and does not stall a writer waiting on a reader under CPU contention.
func loopbackPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c net.Conn
		e error
	}
	ch := make(chan res, 1)
	go func() { c, e := ln.Accept(); ch <- res{c, e} }()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.e != nil {
		t.Fatal(r.e)
	}
	return client, r.c
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, path, want string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && string(b) == want {
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return false
}

func TestSyncInitialAndIncremental(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	// Pre-existing file in A before sync starts (initial reconcile).
	write(t, dirA, "readme.md", "hello\nworld")
	stop := startPair(t, dirA, dirB)
	defer stop()

	if !waitFor(t, filepath.Join(dirB, "readme.md"), "hello\nworld", 5*time.Second) {
		t.Fatal("initial file did not sync A->B")
	}
	// Incremental: new file in B propagates to A.
	write(t, dirB, "src/app.go", "package main")
	if !waitFor(t, filepath.Join(dirA, "src/app.go"), "package main", 5*time.Second) {
		t.Fatal("incremental file did not sync B->A")
	}
}

func TestSyncCleanConcurrentMerge(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	write(t, dirA, "f.txt", "l1\nl2\nl3")
	stop := startPair(t, dirA, dirB)
	defer stop()
	// Converge the base first.
	if !waitFor(t, filepath.Join(dirB, "f.txt"), "l1\nl2\nl3", 5*time.Second) {
		t.Fatal("base did not converge")
	}
	stop() // pause syncing to create a true concurrent edit

	// Non-overlapping edits on each side.
	write(t, dirA, "f.txt", "l1-A\nl2\nl3")
	write(t, dirB, "f.txt", "l1\nl2\nl3-B")

	stop2 := startPair(t, dirA, dirB)
	defer stop2()
	want := "l1-A\nl2\nl3-B"
	if !waitFor(t, filepath.Join(dirA, "f.txt"), want, 6*time.Second) &&
		!waitFor(t, filepath.Join(dirB, "f.txt"), want, 6*time.Second) {
		a, _ := os.ReadFile(filepath.Join(dirA, "f.txt"))
		b, _ := os.ReadFile(filepath.Join(dirB, "f.txt"))
		t.Fatalf("clean 3-way merge did not converge: A=%q B=%q", a, b)
	}
}

func TestSyncConflictPreservesBothVersions(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	write(t, dirA, "f.txt", "base")
	var conflicts []ConflictEvent
	var cmu sync.Mutex
	onConf := func(e ConflictEvent) { cmu.Lock(); conflicts = append(conflicts, e); cmu.Unlock() }
	ca, cb := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ea := &goSync{}
	eb := &goSync{}
	// A is the merge authority (lower id); it resolves conflicts and preserves
	// the peer's version as a .CONFLICT copy on its own side.
	da := runSync(t, ctx, ea, SyncConfig{Dir: dirA, Conn: ca, SelfID: "A", PeerID: "B", OnConflict: onConf})
	db := runSync(t, ctx, eb, SyncConfig{Dir: dirB, Conn: cb, SelfID: "B", PeerID: "A"})
	if !waitFor(t, filepath.Join(dirB, "f.txt"), "base", 5*time.Second) {
		t.Fatal("base did not converge")
	}
	cancel()
	ca.Close()
	cb.Close()
	waitStopped(t, da, db)

	// Overlapping edits to the SAME line -> unmergeable.
	write(t, dirA, "f.txt", "AAA")
	write(t, dirB, "f.txt", "BBB")

	cmu.Lock()
	conflicts = nil
	cmu.Unlock()
	ca2, cb2 := net.Pipe()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ea2 := &goSync{}
	eb2 := &goSync{}
	runSync(t, ctx2, ea2, SyncConfig{Dir: dirA, Conn: ca2, SelfID: "A", PeerID: "B", OnConflict: onConf})
	runSync(t, ctx2, eb2, SyncConfig{Dir: dirB, Conn: cb2, SelfID: "B", PeerID: "A"})

	// The authority (A) preserves B's version as a .CONFLICT copy in its own dir.
	deadline := time.Now().Add(6 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(dirA)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "f.txt.CONFLICT") {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("conflict did not produce a .CONFLICT copy on the authority (data would be lost)")
	}
	// Both converge to the authority's version; the peer's version is preserved
	// in the .CONFLICT copy, so no edit is silently lost.
	if a, _ := os.ReadFile(filepath.Join(dirA, "f.txt")); string(a) != "AAA" {
		t.Fatalf("authority's own version was overwritten: %q", a)
	}
	cmu.Lock()
	n := len(conflicts)
	cmu.Unlock()
	if n == 0 {
		t.Fatal("no ConflictEvent surfaced")
	}
}
