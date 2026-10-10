package provisioning

import (
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func newResolver() *Resolver {
	r := New(store.NewMemoryTenant())
	r.Now = func() int64 { return 1 }
	return r
}

// The first user of a tenant is promoted to org_admin so an org always has an
// administrator; subsequent users are plain members.
func TestFirstUserBecomesOrgAdmin(t *testing.T) {
	r := newResolver()
	_, role1 := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	if role1 != string(store.OrgRoleAdmin) {
		t.Fatalf("first user role = %q, want org_admin", role1)
	}
	_, role2 := r.Resolve(auth.Identity{Subject: "bob", Email: "bob@acme.com"}, "default")
	if role2 != string(store.OrgRoleMember) {
		t.Fatalf("second user role = %q, want member", role2)
	}
}

// Two users from the same email domain converge on one tenant; a different
// domain gets its own tenant. This is the SSO org boundary.
func TestEmailDomainMapsToTenant(t *testing.T) {
	r := newResolver()
	tA, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	tB, _ := r.Resolve(auth.Identity{Subject: "bob", Email: "bob@acme.com"}, "default")
	if tA != tB {
		t.Fatalf("same domain must map to same tenant: %q vs %q", tA, tB)
	}
	tC, _ := r.Resolve(auth.Identity{Subject: "carol", Email: "carol@other.com"}, "default")
	if tC == tA {
		t.Fatalf("different domain must map to a different tenant, both %q", tC)
	}
}

// An explicit identity.Tenant (IdP/SAML issuer mapping) wins over email domain.
func TestExplicitTenantWins(t *testing.T) {
	r := newResolver()
	tid, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com", Tenant: "org-explicit"}, "default")
	if tid != "org-explicit" {
		t.Fatalf("explicit tenant = %q, want org-explicit", tid)
	}
}

// An existing member keeps their role on re-login (no silent demotion/promote).
func TestExistingMemberKeepsRole(t *testing.T) {
	r := newResolver()
	tid, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	// Alice is org_admin (first user). Re-login must keep org_admin.
	_, role := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	if role != string(store.OrgRoleAdmin) {
		t.Fatalf("re-login role = %q, want org_admin preserved", role)
	}
	// A registered issuer mapping resolves later logins to the same tenant.
	tid2, ok := r.Tenants.TenantForIssuer(context.Background(), "acme.com")
	if !ok || tid2 != tid {
		t.Fatalf("issuer mapping = %q,%v want %q", tid2, ok, tid)
	}
}

// With AutoProvision disabled, an unknown user is not provisioned and gets the
// least-privilege viewer role in the default tenant.
func TestProvisioningDisabled(t *testing.T) {
	r := newResolver()
	r.AutoProvision = false
	tid, role := r.Resolve(auth.Identity{Subject: "stranger", Email: "x@nope.com"}, "default")
	if tid != "default" {
		t.Fatalf("no-provision tenant = %q, want default", tid)
	}
	if role != string(store.OrgRoleViewer) {
		t.Fatalf("no-provision role = %q, want viewer", role)
	}
}

// A configured super-admin resolves to the super_admin role, taking precedence
// over any tenant membership. This is the platform-role escalation path: it is
// config-driven, not a stored membership, so it must win over the org role a
// user would otherwise get.
func TestSuperAdminRoleFromConfig(t *testing.T) {
	r := newResolver()
	r.SuperAdmins = map[string]bool{"root": true}
	// Seed root as a plain member first so we prove precedence over membership.
	_ = r.Tenants.PutMember(context.Background(), store.TenantMember{
		TenantID: "default", UserID: "root", Role: store.OrgRoleMember, JoinedAtMs: 1,
	})
	_, role := r.Resolve(auth.Identity{Subject: "root"}, "default")
	if role != string(store.OrgRoleSuperAdmin) {
		t.Fatalf("configured super-admin role = %q, want super_admin", role)
	}
}

// A user NOT in the super-admin set never gets super_admin, even as the first
// user of a tenant (they become org_admin, not super_admin). Negative control
// for the precedence rule above.
func TestNonSuperAdminNeverSuper(t *testing.T) {
	r := newResolver()
	r.SuperAdmins = map[string]bool{"root": true}
	_, role := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	if role == string(store.OrgRoleSuperAdmin) {
		t.Fatalf("non-configured user must never be super_admin, got %q", role)
	}
}

// After an admin approves a join request, the user has a membership row. On the
// next login — with no IdP tenant and no issuer mapping (the dev/no-SSO case) —
// provisioning must route them to that tenant, not the default. This is what
// makes "request to join -> approved" actually take effect at login.
func TestApprovedMembershipRoutesLogin(t *testing.T) {
	r := newResolver()
	// Simulate approval: the admin created a membership in "acme" for dave.
	_ = r.Tenants.PutTenant(context.Background(), store.Tenant{
		TenantID: "acme", Name: "Acme", Status: store.TenantActive,
	})
	_ = r.Tenants.PutMember(context.Background(), store.TenantMember{
		TenantID: "acme", UserID: "dave", Role: store.OrgRoleMember, JoinedAtMs: 100,
	})
	// dave logs in with no email/tenant (dev verifier): must land in acme.
	tid, role := r.Resolve(auth.Identity{Subject: "dave"}, "default")
	if tid != "acme" {
		t.Fatalf("approved member login tenant = %q, want acme", tid)
	}
	if role != string(store.OrgRoleMember) {
		t.Fatalf("approved member role = %q, want member", role)
	}
}

// A user with no membership and no SSO mapping still lands in the default
// tenant (negative control: routing only kicks in for actual members).
func TestNoMembershipFallsBackToDefault(t *testing.T) {
	r := newResolver()
	r.AutoProvision = false // don't auto-create; isolate the routing fallback
	tid, _ := r.Resolve(auth.Identity{Subject: "nobody"}, "default")
	if tid != "default" {
		t.Fatalf("no-membership login tenant = %q, want default", tid)
	}
}

// A platform super-admin can be configured by EMAIL (the natural operator
// choice) even though the OIDC subject is an opaque id — the original bug was
// matching only on subject, so an email-configured admin logged in as a plain
// user. Matching is also case-insensitive on email and still works by subject.
func TestSuperAdminMatchesEmailOrSubject(t *testing.T) {
	r := newResolver()
	// Configured exactly as main.go does: raw + lowercased for each entry.
	r.SuperAdmins = map[string]bool{}
	for _, a := range []string{"jane.d@example.com", "u-subject-42"} {
		r.SuperAdmins[a] = true
		r.SuperAdmins[toLower(a)] = true
	}

	// Email match, opaque subject (the production scenario).
	_, role := r.Resolve(auth.Identity{Subject: "9f3c1a", Email: "jane.d@example.com"}, "default")
	if role != string(store.OrgRoleSuperAdmin) {
		t.Fatalf("email-configured admin got role %q, want super_admin", role)
	}
	// Case-insensitive email.
	_, role = r.Resolve(auth.Identity{Subject: "abc", Email: "Jane.D@Example.com"}, "default")
	if role != string(store.OrgRoleSuperAdmin) {
		t.Fatalf("mixed-case email admin got role %q, want super_admin", role)
	}
	// Subject match still works.
	_, role = r.Resolve(auth.Identity{Subject: "u-subject-42", Email: "someone@x.com"}, "default")
	if role != string(store.OrgRoleSuperAdmin) {
		t.Fatalf("subject-configured admin got role %q, want super_admin", role)
	}
	// A non-admin stays non-admin.
	_, role = r.Resolve(auth.Identity{Subject: "zzz", Email: "nobody@example.com"}, "default")
	if role == string(store.OrgRoleSuperAdmin) {
		t.Fatal("non-admin must not be super_admin")
	}
}

func toLower(s string) string { return strings.ToLower(s) }
