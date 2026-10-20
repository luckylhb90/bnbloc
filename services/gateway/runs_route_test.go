package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// R10: agent run records are served by activity (the agent_jobs owner) in all
// three scopes; the tenant-wide listing keeps the manage_org gate of every
// /v1/tenant/* path, and the member scopes are plain reads.
func TestAgentRunRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/rooms/r1/jobs":          "act",
		"/v1/rooms/r1/jobs/job-1":    "act",
		"/v1/rooms/jobs/jobs":        "act", // a room named "jobs"
		"/v1/my/jobs":                "act",
		"/v1/tenant/jobs":            "act",
		"/v1/tenant/jobs/tool-calls": "act",
		"/v1/rooms/r1/agent-jobs":    "act",
		"/v1/tenant/members":         "share",
		"/v1/tenant/team/agents":     "act",
		"/v1/rooms/jobs/members":     "room",
		"/v1/my/notification-prefs":  "rt",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"/v1/tenant/jobs", "/v1/tenant/jobs/tool-calls"} {
		if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, path)); p != rbac.PermManageOrg {
			t.Errorf("GET %s needs %s, want manage_org", path, p)
		}
	}
	for _, path := range []string{"/v1/my/jobs", "/v1/rooms/r1/jobs", "/v1/rooms/r1/jobs/job-1"} {
		if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, path)); p != rbac.PermRead {
			t.Errorf("GET %s needs %s, want read", path, p)
		}
	}
}
