package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// Display names (docs/prd/2026-10-10-display-names-and-guided-onboarding.md
// §1): the member's own profile and the batch lookup route to the room
// service, and setting one's own name is a read-level action (a viewer may
// choose a display name too).
func TestProfileRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/my/profile"},
		{http.MethodPut, "/v1/my/profile"},
		{http.MethodGet, "/v1/users/profiles"},
		// Reading preferences (docs/prd/2026-10-21-feed-by-request.md).
		{http.MethodGet, "/v1/my/preferences"},
		{http.MethodPut, "/v1/my/preferences"},
	} {
		p := mustParseAPIPath(t, c.method, c.path)
		if got := s.route(p); got != "room" {
			t.Fatalf("%s %s routed to %q, want room", c.method, c.path, got)
		}
		if perm := requiredPermission(c.method, p); perm != rbac.PermRead {
			t.Fatalf("%s %s needs %s, want read", c.method, c.path, perm)
		}
		if rateLimited(c.method, p) {
			t.Fatalf("%s %s is rate limited; it is not cost-bearing", c.method, c.path)
		}
	}
	for _, path := range []string{"/v1/my/profile/x", "/v1/my/preferences/x"} {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != "" {
			t.Fatalf("%s routed to %q", path, got)
		}
	}
}
