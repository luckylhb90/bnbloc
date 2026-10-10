package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// F1/F3: the team dashboard routes to activity (both scopes), borrow billing
// to shareagent; every other /v1/tenant path keeps its backend.
func TestTeamAndBorrowRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/team/agents":                            "act",
		"/v1/tenant/team/agents":                     "act",
		"/v1/borrow/usage-report":                    "share",
		"/v1/tenant/borrow/usage-report":             "share",
		"/v1/borrow/quota-requests":                  "share",
		"/v1/borrow/quota-requests/qr_1/approve":     "share",
		"/v1/tenant/usage":                           "share",
		"/v1/tenant/audit":                           "share",
		"/v1/rooms/r1/shared-agents-usage":           "share",
		"/v1/rooms/r1/shared-agents/x/authorization": "share",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
}

// The tenant-wide views are org-admin only, for reads too; a member's own
// dashboard and report are plain reads, and deciding a quota request is a
// member (non-viewer) write.
func TestTeamAndBorrowPermissions(t *testing.T) {
	for _, path := range []string{"/v1/tenant/team/agents", "/v1/tenant/borrow/usage-report"} {
		if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, path)); p != rbac.PermManageOrg {
			t.Errorf("GET %s needs %s, want manage_org", path, p)
		}
	}
	for _, path := range []string{"/v1/team/agents", "/v1/borrow/usage-report", "/v1/borrow/quota-requests"} {
		if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, path)); p != rbac.PermRead {
			t.Errorf("GET %s needs %s, want read", path, p)
		}
	}
	if p := requiredPermission(http.MethodPost, mustParseAPIPath(t, http.MethodPost, "/v1/borrow/quota-requests/qr_1/approve")); p != rbac.PermDriveAgent {
		t.Errorf("approve needs %s, want drive_agent", p)
	}
}
