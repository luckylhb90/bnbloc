package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// Room tools: POST /v1/rooms/{id}/dispatch is the public door of realtime's
// internal dispatch route. It carries the chat rate limit and drive_agent,
// is room-scoped, and reaches realtime at /internal/v1/rooms/{id}/dispatch
// with the caller signed as X-Bnbloc-User.
func TestDispatchRouteRewritesToTheInternalRoute(t *testing.T) {
	const key = "dispatch-route-test-key"
	t.Setenv("BNBLOC_INTERNAL_KEY", key)
	var gotPath, gotUser string
	var verified bool
	backend := httptest.NewServer(svcauth.RequireSigned(key, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUser, verified = r.URL.Path, r.Header.Get("X-Bnbloc-User"), true
		_, _ = io.WriteString(w, `{"sequence":1,"jobId":"job_x","depth":1}`)
	})))
	defer backend.Close()
	gw := newGateway(t, "")
	gw.Backends.Realtime = backend.URL
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	post := func(room string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/"+room+"/dispatch", strings.NewReader(`{"parentJobId":"job_p"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post("room-1"); resp.StatusCode != http.StatusOK || !verified || gotPath != "/internal/v1/rooms/room-1/dispatch" || gotUser != "alice" {
		t.Fatalf("dispatch = %d; backend saw path %q user %q verified %v", resp.StatusCode, gotPath, gotUser, verified)
	}
	if resp := post("room-other"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dispatch outside the token's rooms = %d; want 403", resp.StatusCode)
	}

	p := mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/dispatch")
	if !rateLimited(http.MethodPost, p) {
		t.Error("a dispatch must be rate-limited like chat")
	}
	if perm := requiredPermission(http.MethodPost, p); perm != rbac.PermDriveAgent {
		t.Errorf("dispatch needs %s, want drive_agent", perm)
	}
	if gw.route(mustParseAPIPath(t, http.MethodGet, "/v1/rooms/r1/dispatch")) != backend.URL {
		t.Error("dispatch must route to realtime")
	}
	// Only the exact POST is rewritten: a deeper path stays a /v1 path the
	// backend does not serve.
	if mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/dispatch/x").isRoomDispatch(http.MethodPost) {
		t.Error("only POST /v1/rooms/{id}/dispatch is the dispatch door")
	}
}
