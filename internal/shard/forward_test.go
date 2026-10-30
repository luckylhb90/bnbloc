package shard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// replicas starts n httptest servers sharing one static peer list, each
// running Forward(set, key-from-path, app) where app reports who served the
// request and which identity headers it saw.
type testReplica struct {
	srv  *httptest.Server
	set  *Set
	addr string
}

func startReplicas(t *testing.T, n int, app func(self string) http.Handler) []*testReplica {
	t.Helper()
	reps := make([]*testReplica, n)
	addrs := make([]string, n)
	for i := range reps {
		srv := httptest.NewUnstartedServer(nil)
		reps[i] = &testReplica{srv: srv, addr: srv.Listener.Addr().String()}
		addrs[i] = reps[i].addr
	}
	alwaysUp := func(context.Context, string) bool { return true }
	for _, r := range reps {
		set, err := New(Options{Peers: strings.Join(addrs, ","), Self: r.addr, Probe: alwaysUp})
		if err != nil {
			t.Fatal(err)
		}
		set.Refresh(context.Background())
		r.set = set
		key := func(req *http.Request) (string, bool) {
			k, ok := strings.CutPrefix(req.URL.Path, "/k/")
			return k, ok
		}
		r.srv.Config.Handler = Forward(set, key, app(r.addr))
		r.srv.Start()
		t.Cleanup(r.srv.Close)
	}
	return reps
}

// keyOwnedBy finds a key the given replica owns.
func keyOwnedBy(t *testing.T, owner *testReplica) string {
	t.Helper()
	for i := 0; i < 10000; i++ {
		k := fmt.Sprintf("room:%d", i)
		if Owner(owner.set.Peers(), k) == owner.addr {
			return k
		}
	}
	t.Fatal("no key found")
	return ""
}

func whoami(self string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s|%s|%s", self, r.Header.Get("X-Bnbloc-User"), r.Header.Get("X-bnbloc-Sig"), r.Header.Get(ForwardedHeader))
	})
}

func TestForwardRoutesToOwnerInOneHop(t *testing.T) {
	reps := startReplicas(t, 3, whoami)
	owner := reps[1]
	key := keyOwnedBy(t, owner)
	for _, entry := range reps {
		req, _ := http.NewRequest(http.MethodGet, entry.srv.URL+"/k/"+key, nil)
		req.Header.Set("X-Bnbloc-User", "maya")
		req.Header.Set("X-bnbloc-Sig", "sig-from-gateway")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		parts := strings.Split(string(body), "|")
		if parts[0] != owner.addr {
			t.Fatalf("entry %s: served by %s, want owner %s", entry.addr, parts[0], owner.addr)
		}
		// The signed identity survives the hop untouched.
		if parts[1] != "maya" || parts[2] != "sig-from-gateway" {
			t.Fatalf("identity headers lost in the forward: %q", body)
		}
		wantMarker := entry.addr
		if entry == owner {
			wantMarker = "" // served directly, never forwarded
		}
		if parts[3] != wantMarker {
			t.Fatalf("forward marker = %q, want %q", parts[3], wantMarker)
		}
	}
	// Unkeyed requests are always served where they land.
	resp, err := http.Get(reps[0].srv.URL + "/unkeyed")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(string(body), reps[0].addr+"|") {
		t.Fatalf("unkeyed request served by %q", body)
	}
}

// A request already forwarded is served locally even when this replica's
// view names another owner (views briefly disagree): one hop, never a loop.
func TestForwardedRequestIsNeverForwardedAgain(t *testing.T) {
	reps := startReplicas(t, 2, whoami)
	key := keyOwnedBy(t, reps[1])
	req, _ := http.NewRequest(http.MethodGet, reps[0].srv.URL+"/k/"+key, nil)
	req.Header.Set(ForwardedHeader, "10.9.9.9:1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(string(body), reps[0].addr+"|") {
		t.Fatalf("a forwarded request was forwarded again: served by %q", body)
	}
}

// SSE streams pass through the forward live: the first event reaches the
// client while the owner's handler is still running.
func TestForwardStreamsSSE(t *testing.T) {
	release := make(chan struct{})
	sse := func(self string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: hello\ndata: %s\n\n", self)
			w.(http.Flusher).Flush()
			<-release // hold the stream open
		})
	}
	reps := startReplicas(t, 2, sse)
	defer close(release)
	key := keyOwnedBy(t, reps[1])
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reps[0].srv.URL+"/k/"+key, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			if got := strings.TrimPrefix(line, "data: "); got != reps[1].addr {
				t.Fatalf("stream served by %s, want owner %s", got, reps[1].addr)
			}
			return
		}
	}
	t.Fatalf("no event through the forward: %v", sc.Err())
}

// A dead owner answers 502 (the client retries; the next refresh drops it).
func TestForwardToDeadOwnerIs502(t *testing.T) {
	reps := startReplicas(t, 2, whoami)
	key := keyOwnedBy(t, reps[1])
	reps[1].srv.Close()
	resp, err := http.Get(reps[0].srv.URL + "/k/" + key)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
}

// A request whose owner cannot be reached is served again once the view has
// dropped that owner — by the key's new owner, its body intact — instead of
// failing: nothing of it had reached the dead one.
func TestForwardToDeadOwnerIsServedByTheNext(t *testing.T) {
	a, b := httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)
	addrA, addrB := a.Listener.Addr().String(), b.Listener.Addr().String()
	var bDown atomic.Bool
	probe := func(_ context.Context, addr string) bool { return !(addr == addrB && bDown.Load()) }
	set, err := New(Options{Peers: addrA + "," + addrB, Self: addrA, Probe: probe, Interval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	set.Refresh(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	set.Start(ctx)
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s:%s", addrA, body)
	})
	a.Config.Handler = Forward(set, func(r *http.Request) (string, bool) { return strings.CutPrefix(r.URL.Path, "/k/") }, echo)
	a.Start()
	defer a.Close()
	b.Start()
	var key string
	for i := 0; ; i++ {
		if key = fmt.Sprintf("room:%d", i); Owner([]string{addrA, addrB}, key) == addrB {
			break
		}
	}
	b.Close() // the owner dies
	bDown.Store(true)
	resp, err := http.Post(a.URL+"/k/"+key, "application/json", strings.NewReader(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != addrA+`:{"text":"hi"}` {
		t.Fatalf("status %d body %q, want served by the remaining replica with the body", resp.StatusCode, got)
	}
}
