package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
	if !got.Consistent || len(got.Services) != 6 {
		t.Fatalf("want 6 consistent instances (gateway + 2 room replicas + 3), got %s", body)
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
