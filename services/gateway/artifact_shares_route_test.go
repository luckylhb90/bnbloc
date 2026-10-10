package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// The org-admin public-link list routes to the room service (the owner of
// artifacts and their links) behind manage_org; member share routes are
// ordinary room sub-resources.
func TestArtifactShareRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	p := mustParseAPIPath(t, http.MethodGet, "/v1/tenant/shares")
	if got := s.route(p); got != "room" {
		t.Fatalf("tenant shares route = %q, want room", got)
	}
	if perm := requiredPermission(http.MethodGet, p); perm != rbac.PermManageOrg {
		t.Fatalf("GET tenant/shares needs %s, want manage_org", perm)
	}
	member := mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/artifacts/a1/shares")
	if got := s.route(member); got != "room" || !member.roomScoped() {
		t.Fatalf("member share route = %q scoped=%v", got, member.roomScoped())
	}
	if !mustParseAPIPath(t, http.MethodDelete, "/v1/rooms/r1/artifacts/a1/shares/s1").isArtifactShareRevoke(http.MethodDelete) {
		t.Fatal("revoke path not recognized")
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/rooms/r1/artifacts/a1/shares/s1"},
		{http.MethodDelete, "/v1/rooms/r1/artifacts/a1/shares"},
		{http.MethodDelete, "/v1/rooms/r1/artifacts/a1/versions/1"},
		{http.MethodDelete, "/v1/rooms/r1/members/a1/shares/s1"},
	} {
		if mustParseAPIPath(t, c.method, c.path).isArtifactShareRevoke(c.method) {
			t.Fatalf("%s %s treated as a share revoke", c.method, c.path)
		}
	}
}

// An org admin reaches the share revoke of a room they are not a member of
// (the room service re-checks the org role); a member does not, and the
// exemption covers no other room route.
func TestProxyOrgAdminShareRevokeExemption(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Method+" "+r.URL.Path)
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	gw.Rooms = func(string, string) []string { return nil }
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	do := func(role, method, path string) int {
		tok := gw.Signer.Issue(auth.Claims{Subject: "ada", Tenant: "acme", Role: role, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	const revoke = "/v1/rooms/room-x/artifacts/art_1/shares/abc"
	if code := do(string(rbac.RoleAdmin), http.MethodDelete, revoke); code != http.StatusOK {
		t.Fatalf("org admin revoke = %d, want proxied", code)
	}
	if code := do(string(rbac.RoleMember), http.MethodDelete, revoke); code != http.StatusForbidden {
		t.Fatalf("non-member member revoke = %d, want 403", code)
	}
	if code := do(string(rbac.RoleAdmin), http.MethodGet, "/v1/rooms/room-x/artifacts/art_1/shares"); code != http.StatusForbidden {
		t.Fatalf("org admin share list of a foreign room = %d, want 403", code)
	}
}
