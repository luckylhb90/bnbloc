package shareagenthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// sessionsFixture: tenant "default" with admin root + members ann, bob; each
// has an active refresh family landed in the default tenant, plus a
// super-admin family there.
func sessionsFixture(t *testing.T) (*Service, *store.MemoryRefreshToken) {
	t.Helper()
	svc, _ := newService(t, "")
	tenants := store.NewMemoryTenant()
	for _, m := range []store.TenantMember{
		{TenantID: tenant.Default, UserID: "root", Role: store.OrgRoleAdmin, JoinedAtMs: 1},
		{TenantID: tenant.Default, UserID: "ann", Role: store.OrgRoleMember, JoinedAtMs: 1},
		{TenantID: tenant.Default, UserID: "bob", Role: store.OrgRoleMember, JoinedAtMs: 1},
	} {
		_ = tenants.PutMember(dctx(), m)
	}
	_ = tenants.PutTenant(dctx(), store.Tenant{TenantID: "acme", Name: "Acme", Status: store.TenantActive})
	svc.Tenants = tenants
	refresh := store.NewMemoryRefreshToken()
	for _, f := range []store.RefreshFamily{
		{FamilyID: "ann-1", Subject: "ann", TenantID: tenant.Default, Role: "member"},
		{FamilyID: "ann-2", Subject: "ann", TenantID: "acme", Role: "member"},
		{FamilyID: "bob-1", Subject: "bob", TenantID: "acme", Role: "member"},
		{FamilyID: "sa-1", Subject: "boss", TenantID: "acme", Role: "super_admin"},
	} {
		f.CurrentJTI, f.ExpiresAtMs = f.FamilyID+"-j", 1<<62
		if err := refresh.CreateRefreshFamily(dctx(), f); err != nil {
			t.Fatal(err)
		}
	}
	svc.RefreshTokens = refresh
	return svc, refresh
}

func activeFamilies(t *testing.T, s *store.MemoryRefreshToken, subject string) int {
	t.Helper()
	fams, err := s.ListRefreshFamilies(dctx(), subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(fams)
}

// The admin "sign out member" revokes every family of that member only, and
// only for a member of the admin's tenant.
func TestAdminRevokesMemberSessions(t *testing.T) {
	svc, refresh := sessionsFixture(t)
	h := svc.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, tenantReq(http.MethodDelete, "/v1/tenant/members/ann/sessions", "root", ""))
	var out struct {
		Revoked int `json:"revoked"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || out.Revoked != 2 {
		t.Fatalf("revoke = %d %s; want 200 with 2 revoked", w.Code, w.Body)
	}
	if activeFamilies(t, refresh, "ann") != 0 || activeFamilies(t, refresh, "bob") != 1 {
		t.Fatal("revocation hit the wrong subject")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, tenantReq(http.MethodDelete, "/v1/tenant/members/stranger/sessions", "root", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("non-member = %d, want 404", w.Code)
	}
}

// Removing a member revokes their refresh families (other members untouched).
func TestRemoveMemberRevokesSessions(t *testing.T) {
	svc, refresh := sessionsFixture(t)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, tenantReq(http.MethodDelete, "/v1/tenant/members/bob", "root", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", w.Code, w.Body)
	}
	if activeFamilies(t, refresh, "bob") != 0 {
		t.Fatal("removed member still has an active refresh family")
	}
	if activeFamilies(t, refresh, "ann") != 2 {
		t.Fatal("removal revoked another member's sessions")
	}
}

// Suspending a tenant revokes the sessions that landed in it, sparing
// platform super-admins.
func TestSuspendTenantRevokesSessions(t *testing.T) {
	svc, refresh := sessionsFixture(t)
	w := httptest.NewRecorder()
	svc.Handler().ServeHTTP(w, tenantReq(http.MethodPut, "/v1/platform/tenants/acme/status", "boss", `{"status":"suspended"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("suspend = %d %s", w.Code, w.Body)
	}
	if activeFamilies(t, refresh, "bob") != 0 {
		t.Fatal("suspended tenant's member keeps a session")
	}
	if activeFamilies(t, refresh, "ann") != 1 {
		t.Fatal("ann's default-tenant session must survive acme's suspension")
	}
	if activeFamilies(t, refresh, "boss") != 1 {
		t.Fatal("super-admin session revoked by suspension")
	}
}
