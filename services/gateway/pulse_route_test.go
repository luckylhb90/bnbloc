package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// The organization board (executive Pulse) is served by the pulse service and
// needs its own grant, read_org_pulse. The rest of /v1/tenant/* keeps manage_org.
func TestPulseBoardRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Activity: "act", Pulse: "pulse"}}
	for _, path := range []string{"/v1/tenant/pulse/snapshot", "/v1/tenant/pulse/stream"} {
		p := mustParseAPIPath(t, http.MethodGet, path)
		if got := s.route(p); got != "pulse" {
			t.Errorf("route(%s) = %q, want pulse", path, got)
		}
		if perm := requiredPermission(http.MethodGet, p); perm != rbac.PermReadOrgPulse {
			t.Errorf("GET %s needs %s, want read_org_pulse", path, perm)
		}
	}
	// Nothing else under /v1/tenant changes its gate or its service.
	other := mustParseAPIPath(t, http.MethodGet, "/v1/tenant/jobs")
	if perm := requiredPermission(http.MethodGet, other); perm != rbac.PermManageOrg {
		t.Errorf("GET /v1/tenant/jobs needs %s, want manage_org", perm)
	}
	if got := s.route(other); got != "act" {
		t.Errorf("route(/v1/tenant/jobs) = %q, want act", got)
	}
}
