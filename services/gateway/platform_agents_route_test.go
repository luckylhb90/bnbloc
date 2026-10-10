package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// Platform agents (docs/prd/2026-10-10-zero-install-onboarding.md §2): every
// route lives on shareagent; the policy keeps /v1/platform/*'s
// manage_platform gate, the member list is a read, and adding/removing a
// room's platform agent is a member write (a viewer cannot).
func TestPlatformAgentRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	for _, path := range []string{
		"/v1/platform/platform-agents",
		"/v1/platform/platform-agents/usage",
		"/v1/platform-agents",
		"/v1/rooms/r1/platform-agents",
		"/v1/rooms/r1/platform-agents/codex",
	} {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != "share" {
			t.Errorf("route(%s) = %q, want shareagent", path, got)
		}
	}
	for _, c := range []struct {
		method, path string
		want         rbac.Permission
	}{
		{http.MethodGet, "/v1/platform/platform-agents", rbac.PermManagePlatform},
		{http.MethodPut, "/v1/platform/platform-agents", rbac.PermManagePlatform},
		{http.MethodGet, "/v1/platform/platform-agents/usage", rbac.PermManagePlatform},
		{http.MethodGet, "/v1/platform-agents", rbac.PermRead},
		{http.MethodGet, "/v1/rooms/r1/platform-agents", rbac.PermRead},
		{http.MethodPost, "/v1/rooms/r1/platform-agents", rbac.PermDriveAgent},
		{http.MethodDelete, "/v1/rooms/r1/platform-agents/codex", rbac.PermDriveAgent},
	} {
		if got := requiredPermission(c.method, mustParseAPIPath(t, c.method, c.path)); got != c.want {
			t.Errorf("%s %s needs %s, want %s", c.method, c.path, got, c.want)
		}
	}
	if !mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/platform-agents").roomScoped() {
		t.Error("a room's platform agents must be room-scoped (membership + room tenant)")
	}
}
