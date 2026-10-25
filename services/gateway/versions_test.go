package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/buildinfo"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// fakeVersionBackend answers GET /version as service, rotating through the
// given (instance, commit) replicas per request like a Service VIP would.
func fakeVersionBackend(t *testing.T, service string, replicas ...[2]string) *httptest.Server {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		rep := replicas[int(n.Add(1)-1)%len(replicas)]
		_ = json.NewEncoder(w).Encode(wire.ServiceVersion{Service: service, Instance: rep[0], Commit: rep[1]})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlatformVersions(t *testing.T) {
	old := buildinfo.Commit
	t.Cleanup(func() { buildinfo.Commit = old })
	buildinfo.Commit = "abc1234def"

	gw, srv := tokenGateway(t)
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	gw.Backends.Room = fakeVersionBackend(t, "bnbloc-room", [2]string{"room-a", "abc1234def"}, [2]string{"room-b", "abc1234"}).URL
	gw.Backends.Activity = fakeVersionBackend(t, "bnbloc-activity", [2]string{"act-a", "abc1234def"}).URL
	gw.Backends.Realtime = fakeVersionBackend(t, "bnbloc-realtime", [2]string{"rt-a", "abc1234def"}).URL
	gw.Backends.ShareAgent = fakeVersionBackend(t, "bnbloc-shareagent", [2]string{"sa-a", "abc1234def"}).URL
	gw.Backends.Pulse = fakeVersionBackend(t, "bnbloc-pulse", [2]string{"pu-a", "abc1234def"}).URL
	gw.Backends.Relay = ""
	gw.WebBuild = &wire.WebBundleVersion{Version: "0.1.14", Commit: "abc1234"}

	if code, _ := call(t, http.MethodGet, srv.URL+"/v1/platform/versions", sessionFor(gw, "alice", string(rbac.RoleMember)), nil); code != http.StatusForbidden {
		t.Fatalf("member: status %d, want 403", code)
	}
	root := sessionFor(gw, "root", string(rbac.RoleSuperAdmin))
	code, body := call(t, http.MethodGet, srv.URL+"/v1/platform/versions", root, nil)
	var got wire.DeploymentVersions
	if code != http.StatusOK || json.Unmarshal(body, &got) != nil {
		t.Fatalf("status %d body %s", code, body)
	}
	if !got.Consistent || len(got.Services) != 7 {
		t.Fatalf("want 7 consistent instances (gateway + 2 room replicas + 4), got %s", body)
	}

	// A skewed replica and an unreachable service are both reported.
	gw.Backends.Room = fakeVersionBackend(t, "bnbloc-room", [2]string{"room-a", "abc1234def"}, [2]string{"room-b", "0ld0ld0"}).URL
	gw.Backends.Relay = gone.URL
	code, body = call(t, http.MethodGet, srv.URL+"/v1/platform/versions", root, nil)
	got = wire.DeploymentVersions{}
	if code != http.StatusOK || json.Unmarshal(body, &got) != nil {
		t.Fatalf("status %d body %s", code, body)
	}
	all := strings.Join(got.Problems, "\n")
	if got.Consistent || !strings.Contains(all, "bnbloc-room@room-b") || !strings.Contains(all, "bnbloc-relay unreachable") {
		t.Fatalf("skew/unreachable not reported: %s", body)
	}
}

// A backend that answers slowly costs one round trip, not one per sample: the
// samples run together (the Deployment card of the platform console waits for
// the slowest backend, and a backend 0.5 s away took 2.5 s to sample in turn).
func TestSampleVersionsRunsTogether(t *testing.T) {
	const delay = 300 * time.Millisecond
	var inflight, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := inflight.Add(1)
		for {
			p := peak.Load()
			if now <= p || peak.CompareAndSwap(p, now) {
				break
			}
		}
		time.Sleep(delay)
		inflight.Add(-1)
		_ = json.NewEncoder(w).Encode(wire.ServiceVersion{Service: "bnbloc-room", Instance: "room-a", Commit: "abc1234def"})
	}))
	t.Cleanup(srv.Close)
	start := time.Now()
	got, err := sampleVersions(context.Background(), srv.URL+"/version")
	took := time.Since(start)
	if err != nil || len(got) != 1 {
		t.Fatalf("sampled %+v, %v", got, err)
	}
	if took >= 3*delay {
		t.Fatalf("sampling took %v for %d samples of %v each: they ran one after another", took, versionSamples, delay)
	}
	if peak.Load() < int64(versionSamples) {
		t.Fatalf("at most %d of %d samples were in flight together", peak.Load(), versionSamples)
	}
}

// Replicas are listed in the order the samples were taken, whichever answered
// first, so a re-read of an unchanged deployment reads the same.
func TestSampleVersionsKeepsSampleOrder(t *testing.T) {
	srv := fakeVersionBackend(t, "bnbloc-room", [2]string{"room-a", "c1"}, [2]string{"room-b", "c1"})
	got, err := sampleVersions(context.Background(), srv.URL+"/version")
	if err != nil || len(got) != 2 {
		t.Fatalf("sampled %+v, %v", got, err)
	}
	// Distinct instances, each once, whatever order the replicas answered in.
	if got[0].Instance == got[1].Instance {
		t.Fatalf("an instance was listed twice: %+v", got)
	}
}

// A failed probe says how far it got, so the Deployment card can tell a
// replica that does not accept connections from one that accepts and does not
// answer.
func TestVersionProbeNamesThePhaseOfAFailure(t *testing.T) {
	// Accepts the connection and never answers.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { silent.Close() })
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = fetchVersion(ctx, "http://"+silent.Addr().String()+"/version")
	if err == nil || !strings.Contains(err.Error(), "connected to "+silent.Addr().String()+" but it did not answer") {
		t.Fatalf("a replica that accepts and does not answer: %v", err)
	}

	// Nothing listens: the connection is refused, at a named address.
	gone, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := gone.Addr().String()
	gone.Close()
	_, err = fetchVersion(context.Background(), "http://"+addr+"/version")
	if err == nil || !strings.Contains(err.Error(), "could not connect to "+addr) {
		t.Fatalf("a replica that refuses: %v", err)
	}
}
