package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// F6/F7: message tasks and global search route to realtime (the room
// feed's owner). The prompt library is gone (prompt packs are content
// components the gateway serves): its paths route nowhere.
func TestTeamFeatureRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/prompts":                  "",
		"/v1/prompts/pr_1/use":         "",
		"/v1/search":                   "rt",
		"/v1/rooms/r1/tasks":           "rt",
		"/v1/rooms/r1/tasks/12":        "rt",
		"/v1/rooms/tasks-team/members": "room",
		"/v1/tenant/members":           "share",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	if p := requiredPermission(http.MethodDelete, mustParseAPIPath(t, http.MethodDelete, "/v1/tenant/members/u1")); p != rbac.PermManageOrg {
		t.Errorf("DELETE tenant/members needs %s, want manage_org", p)
	}
	if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, "/v1/search")); p != rbac.PermRead {
		t.Errorf("GET search needs %s, want read", p)
	}
}
