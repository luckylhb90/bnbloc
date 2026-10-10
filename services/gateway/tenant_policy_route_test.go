package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// The member tenant-policy read routes to shareagent (the tenant registry)
// as a plain read; changing it stays behind the manage_org tenant settings.
// The old sandbox-policy path no longer routes (clean cutover).
func TestTenantPolicyRoute(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	p := mustParseAPIPath(t, http.MethodGet, "/v1/my/tenant-policy")
	if got := s.route(p); got != "share" {
		t.Fatalf("route = %q, want share", got)
	}
	if perm := requiredPermission(http.MethodGet, p); perm != rbac.PermRead {
		t.Fatalf("GET tenant-policy needs %s, want read", perm)
	}
	if perm := requiredPermission(http.MethodPut, mustParseAPIPath(t, http.MethodPut, "/v1/tenant/settings")); perm != rbac.PermManageOrg {
		t.Fatalf("PUT tenant/settings needs %s, want manage_org", perm)
	}
	for _, path := range []string{"/v1/my/tenant-policy/x", "/v1/my/sandbox-policy"} {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != "" {
			t.Fatalf("%s routed to %q", path, got)
		}
	}
}
