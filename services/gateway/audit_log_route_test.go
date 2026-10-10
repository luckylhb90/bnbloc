package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// R13: the audit log is served by shareagent; the tenant view needs
// manage_org (org admins, super-admins) and the cross-tenant platform view
// manage_platform (super-admins only). A plain member reaches neither.
func TestAuditLogRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := []struct {
		path string
		perm rbac.Permission
	}{
		{"/v1/tenant/audit-log", rbac.PermManageOrg},
		{"/v1/platform/audit-log", rbac.PermManagePlatform},
	}
	for _, c := range cases {
		p := mustParseAPIPath(t, http.MethodGet, c.path)
		if got := s.route(p); got != "share" {
			t.Errorf("route(%s) = %q, want shareagent", c.path, got)
		}
		if perm := requiredPermission(http.MethodGet, p); perm != c.perm {
			t.Errorf("permission(%s) = %s, want %s", c.path, perm, c.perm)
		}
	}
	if rbac.Can(string(rbac.RoleMember), rbac.PermManageOrg) || rbac.Can(string(rbac.RoleViewer), rbac.PermManageOrg) {
		t.Fatal("a member may read the tenant audit log")
	}
	if rbac.Can(string(rbac.RoleAdmin), rbac.PermManagePlatform) {
		t.Fatal("an org admin may read the cross-tenant audit log")
	}
	if !rbac.Can(string(rbac.RoleAdmin), rbac.PermManageOrg) || !rbac.Can(string(rbac.RoleSuperAdmin), rbac.PermManagePlatform) {
		t.Fatal("admins lost access to their audit log")
	}
}

// Session revocations land in the audit trail with the family's tenant:
// logout, and the refresh-token reuse that revokes a family.
func TestSessionRevocationsAudited(t *testing.T) {
	gw, srv, now := refreshGateway(t)
	audit := store.NewMemoryExtensionAudit()
	gw.ExtAudit = audit

	g := loginGrant(t, srv.URL)
	if resp, _ := postJSON(t, srv.URL+"/v1/auth/logout", map[string]string{"refreshToken": g.RefreshToken}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	// A second logout revokes nothing and records nothing.
	_, _ = postJSON(t, srv.URL+"/v1/auth/logout", map[string]string{"refreshToken": g.RefreshToken})

	g1 := loginGrant(t, srv.URL)
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", g1.RefreshToken); c != http.StatusOK {
		t.Fatalf("rotate = %d", c)
	}
	*now = now.Add(refreshReuseGrace + time.Second)
	_, _ = refreshWith(t, srv.URL, "/v1/auth/refresh", g1.RefreshToken)

	recs, _ := audit.ExtAuditRecords(context.Background(), wire.AuditKindSession, 10, 0)
	actions := map[string]int{}
	for _, r := range recs {
		actions[r.Action]++
		if r.TenantID == "" || r.Actor == "" || r.Name == "" {
			t.Errorf("session audit row missing tenant/actor/family: %+v", r)
		}
	}
	if actions["logout"] != 1 || actions["reuse_revoked"] != 1 {
		t.Fatalf("session audit actions = %v, want one logout and one reuse_revoked", actions)
	}
}
