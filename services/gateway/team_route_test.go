package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// F2/F6/F7: the prompt library, message tasks and global search route to
// realtime (the room feed's owner); the org-admin prompt moderation path is a
// /v1/tenant/* route and keeps its manage_org gate.
func TestTeamFeatureRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/prompts":                  "rt",
		"/v1/prompts/pr_1":             "rt",
		"/v1/prompts/pr_1/use":         "rt",
		"/v1/search":                   "rt",
		"/v1/rooms/r1/tasks":           "rt",
		"/v1/rooms/r1/tasks/12":        "rt",
		"/v1/rooms/tasks-team/members": "room",
		"/v1/tenant/prompts/pr_1":      "rt",
		"/v1/tenant/members":           "share",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	if p := requiredPermission(http.MethodDelete, mustParseAPIPath(t, http.MethodDelete, "/v1/tenant/prompts/pr_1")); p != rbac.PermManageOrg {
		t.Errorf("DELETE tenant/prompts needs %s, want manage_org", p)
	}
	if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, "/v1/search")); p != rbac.PermRead {
		t.Errorf("GET search needs %s, want read", p)
	}
}
