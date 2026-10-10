package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// R7: the search backfill routes to realtime (the room feed's owner) and is
// super-admin-only like every /v1/platform path.
func TestSearchReindexRoute(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	p := mustParseAPIPath(t, http.MethodPost, "/v1/platform/search/reindex")
	if got := s.route(p); got != "rt" {
		t.Fatalf("route = %q, want realtime", got)
	}
	if perm := requiredPermission(http.MethodPost, p); perm != rbac.PermManagePlatform {
		t.Fatalf("permission = %s, want manage_platform", perm)
	}
	if rbac.Can(string(rbac.RoleAdmin), rbac.PermManagePlatform) {
		t.Fatal("an org admin may reach a platform route")
	}
	// Other /v1/platform paths keep their owner.
	if got := s.route(mustParseAPIPath(t, http.MethodGet, "/v1/platform/tenants")); got != "share" {
		t.Fatalf("platform tenants route = %q, want shareagent", got)
	}
}
