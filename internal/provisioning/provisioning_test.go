package provisioning

import (
	"context"
	"errors"
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
	_, role1, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	if role1 != string(store.OrgRoleAdmin) {
		t.Fatalf("first user role = %q, want org_admin", role1)
	}
	_, role2, _ := r.Resolve(auth.Identity{Subject: "bob", Email: "bob@acme.com"}, "default")
	if role2 != string(store.OrgRoleMember) {
		t.Fatalf("second user role = %q, want member", role2)
	}
}

// Two users from the same email domain converge on one tenant; a different
// domain gets its own tenant. This is the SSO org boundary.
func TestEmailDomainMapsToTenant(t *testing.T) {
	r := newResolver()
	tA, _, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	tB, _, _ := r.Resolve(auth.Identity{Subject: "bob", Email: "bob@acme.com"}, "default")
	if tA != tB {
		t.Fatalf("same domain must map to same tenant: %q vs %q", tA, tB)
	}
	tC, _, _ := r.Resolve(auth.Identity{Subject: "carol", Email: "carol@other.com"}, "default")
	if tC == tA {
		t.Fatalf("different domain must map to a different tenant, both %q", tC)
	}
}

// An explicit identity.Tenant (IdP/SAML issuer mapping) wins over email domain.
func TestExplicitTenantWins(t *testing.T) {
	r := newResolver()
	tid, _, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com", Tenant: "org-explicit"}, "default")
	if tid != "org-explicit" {
		t.Fatalf("explicit tenant = %q, want org-explicit", tid)
	}
}

// An existing member keeps their role on re-login (no silent demotion/promote).
func TestExistingMemberKeepsRole(t *testing.T) {
	r := newResolver()
	tid, _, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	// Alice is org_admin (first user). Re-login must keep org_admin.
	_, role, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
	if role != string(store.OrgRoleAdmin) {
		t.Fatalf("re-login role = %q, want org_admin preserved", role)
	}
	// A registered issuer mapping resolves later logins to the same tenant.
	tid2, ok, _ := r.Tenants.TenantForIssuer(context.Background(), "acme.com")
	if !ok || tid2 != tid {
		t.Fatalf("issuer mapping = %q,%v want %q", tid2, ok, tid)
	}
}

// With AutoProvision disabled, an unknown user is not provisioned and gets the
// least-privilege viewer role in the default tenant.
func TestProvisioningDisabled(t *testing.T) {
	r := newResolver()
	r.AutoProvision = false
	tid, role, _ := r.Resolve(auth.Identity{Subject: "stranger", Email: "x@nope.com"}, "default")
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
	_, role, _ := r.Resolve(auth.Identity{Subject: "root"}, "default")
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
	_, role, _ := r.Resolve(auth.Identity{Subject: "alice", Email: "alice@acme.com"}, "default")
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
	tid, role, _ := r.Resolve(auth.Identity{Subject: "dave"}, "default")
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
	tid, _, _ := r.Resolve(auth.Identity{Subject: "nobody"}, "default")
	if tid != "default" {
		t.Fatalf("no-membership login tenant = %q, want default", tid)
	}
}

// A platform super-admin can be configured by EMAIL (the natural operator
// choice) even though the OIDC subject is an opaque id — the original bug was
// matching only on subject, so an email-configured admin logged in as a plain
// user. Matching is case-insensitive on email and exact on subject (subjects
// are case-sensitive).
func TestSuperAdminMatchesEmailOrSubject(t *testing.T) {
	r := newResolver()
	// Configured exactly as main.go does: each entry as written.
	r.SuperAdmins = map[string]bool{"Jane.D@example.com": true, "u-subject-42": true}

	// Email match, opaque subject (the production scenario).
	_, role, _ := r.Resolve(auth.Identity{Subject: "9f3c1a", Email: "jane.d@example.com"}, "default")
	if role != string(store.OrgRoleSuperAdmin) {
		t.Fatalf("email-configured admin got role %q, want super_admin", role)
	}
	// Case-insensitive email.
	_, role, _ = r.Resolve(auth.Identity{Subject: "abc", Email: "Jane.D@Example.com"}, "default")
	if role != string(store.OrgRoleSuperAdmin) {
		t.Fatalf("mixed-case email admin got role %q, want super_admin", role)
	}
	// Subject match still works.
	_, role, _ = r.Resolve(auth.Identity{Subject: "u-subject-42", Email: "someone@x.com"}, "default")
	if role != string(store.OrgRoleSuperAdmin) {
		t.Fatalf("subject-configured admin got role %q, want super_admin", role)
	}
	// A subject differing only in case is another identity.
	_, role, _ = r.Resolve(auth.Identity{Subject: "U-Subject-42", Email: "someone@x.com"}, "default")
	if role == string(store.OrgRoleSuperAdmin) {
		t.Fatal("a case variant of a configured subject must not be super_admin")
	}
	// A non-admin stays non-admin.
	_, role, _ = r.Resolve(auth.Identity{Subject: "zzz", Email: "nobody@example.com"}, "default")
	if role == string(store.OrgRoleSuperAdmin) {
		t.Fatal("non-admin must not be super_admin")
	}
}

// failingTenants fails the reads login provisioning makes and records every
// membership write.
type failingTenants struct {
	*store.MemoryTenant
	failIssuer, failMember, failCount bool
	writes                            int
}

var errDown = errors.New("db down")

func (f *failingTenants) TenantForIssuer(ctx context.Context, issuer string) (string, bool, error) {
	if f.failIssuer {
		return "", false, errDown
	}
	return f.MemoryTenant.TenantForIssuer(ctx, issuer)
}

func (f *failingTenants) GetMember(ctx context.Context, tid, uid string) (store.TenantMember, bool, error) {
	if f.failMember {
		return store.TenantMember{}, false, errDown
	}
	return f.MemoryTenant.GetMember(ctx, tid, uid)
}

func (f *failingTenants) MemberCount(ctx context.Context, tid string) (int, error) {
	if f.failCount {
		return 0, errDown
	}
	return f.MemoryTenant.MemberCount(ctx, tid)
}

func (f *failingTenants) PutMember(ctx context.Context, m store.TenantMember) error {
	f.writes++
	return f.MemoryTenant.PutMember(ctx, m)
}

func (f *failingTenants) EnsureMember(ctx context.Context, m store.TenantMember) (store.TenantMember, bool, error) {
	f.writes++
	return f.MemoryTenant.EnsureMember(ctx, m)
}

// A store read that fails at login fails the login and writes nothing: it is
// never read as "not a member" (which re-provisioned the subject and rewrote
// an org admin's role) or "no issuer mapping" (which routed the login to a
// fallback tenant).
func TestStoreFailureFailsLoginWithoutRoleChange(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		set  func(*failingTenants)
	}{
		{"membership read", func(f *failingTenants) { f.failMember = true }},
		{"member count", func(f *failingTenants) { f.failCount = true }},
		{"issuer mapping", func(f *failingTenants) { f.failIssuer = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &failingTenants{MemoryTenant: store.NewMemoryTenant()}
			_ = f.MemoryTenant.PutTenant(ctx, store.Tenant{TenantID: "acme", Name: "Acme", SSOIssuer: "acme.com"})
			r := New(f)
			r.Now = func() int64 { return 1 }
			tc.set(f)
			id := auth.Identity{Subject: "newcomer", Email: "newcomer@acme.com"}
			if tc.name == "member count" {
				id.Tenant = "acme" // reach the count: no issuer read, no member row
			}
			tid, role, err := r.Resolve(id, "default")
			if err == nil {
				t.Fatalf("Resolve = (%q, %q, nil), want an error", tid, role)
			}
			if f.writes != 0 {
				t.Fatalf("membership writes = %d, want 0", f.writes)
			}
		})
	}
}

// The first-login provisioning write is insert-only: a membership that
// appeared after the read keeps its role, and that stored role is returned.
func TestProvisioningNeverOverwritesARacingMembership(t *testing.T) {
	ctx := context.Background()
	tenants := &racingTenants{MemoryTenant: store.NewMemoryTenant()}
	r := New(tenants)
	r.Now = func() int64 { return 1 }
	tid, role, err := r.Resolve(auth.Identity{Subject: "maya", Tenant: "acme"}, "default")
	if err != nil || tid != "acme" {
		t.Fatalf("Resolve = %q, %v", tid, err)
	}
	if role != string(store.OrgRoleViewer) {
		t.Fatalf("role = %q, want the racing row's viewer role", role)
	}
	if m, _, _ := tenants.GetMember(ctx, "acme", "maya"); m.Role != store.OrgRoleViewer {
		t.Fatalf("stored role = %q, want viewer kept", m.Role)
	}
}

// racingTenants inserts a viewer membership for the subject right after the
// resolver's membership read, as a concurrent admin would.
type racingTenants struct {
	*store.MemoryTenant
	raced bool
}

func (r *racingTenants) MemberCount(ctx context.Context, tid string) (int, error) {
	if !r.raced {
		r.raced = true
		_ = r.MemoryTenant.PutMember(ctx, store.TenantMember{TenantID: tid, UserID: "maya", Role: store.OrgRoleViewer, JoinedAtMs: 1})
		return 0, nil
	}
	return r.MemoryTenant.MemberCount(ctx, tid)
}
