package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// R12: usage budgets route to activity; the admin set keeps the manage_org
// gate of /v1/tenant/*, a member's own budget is a read. R8: the borrow
// reservation is a shared-agents write (shareagent, rate-limited).
func TestBudgetAndReserveRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	for path, want := range map[string]string{
		"/v1/tenant/budgets": "act",
		"/v1/my/budget":      "act",
		"/v1/rooms/r1/shared-agents/shared-agent:c:codex/authorization/reserve": "share",
	} {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	if p := requiredPermission(http.MethodPut, mustParseAPIPath(t, http.MethodPut, "/v1/tenant/budgets")); p != rbac.PermManageOrg {
		t.Errorf("PUT tenant/budgets needs %s, want manage_org", p)
	}
	if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, "/v1/my/budget")); p != rbac.PermRead {
		t.Errorf("GET my/budget needs %s, want read", p)
	}
	reserve := mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/shared-agents/shared-agent:c:codex/authorization/reserve")
	if !rateLimited(http.MethodPost, reserve) {
		t.Error("borrow reservation must be rate-limited like the other borrow writes")
	}
}
