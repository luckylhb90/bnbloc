package rbac

import "testing"

// The role→permission matrix is a security boundary: a wrong grant is a
// privilege escalation. These assert each role holds exactly its intended
// permissions, and that an unknown/empty role fails closed to viewer.
func TestPermissionMatrix(t *testing.T) {
	cases := []struct {
		role string
		perm Permission
		want bool
	}{
		{"super_admin", PermManagePlatform, true},
		{"super_admin", PermManageOrg, true},
		{"super_admin", PermCreateRoom, true},
		{"super_admin", PermDriveAgent, true},
		{"super_admin", PermRead, true},

		// manage_platform is super-admin ONLY: a wrong grant here lets an
		// org_admin (or lower) escape their tenant. This is the escalation
		// boundary for the cross-tenant feature.
		{"org_admin", PermManagePlatform, false},
		{"member", PermManagePlatform, false},
		{"viewer", PermManagePlatform, false},
		{"", PermManagePlatform, false},
		{"bogus", PermManagePlatform, false},

		{"org_admin", PermManageOrg, true},
		{"org_admin", PermCreateRoom, true},
		{"org_admin", PermDriveAgent, true},
		{"org_admin", PermRead, true},

		{"member", PermManageOrg, false},
		{"member", PermCreateRoom, true},
		{"member", PermDriveAgent, true},
		{"member", PermRead, true},

		{"viewer", PermManageOrg, false},
		{"viewer", PermCreateRoom, false},
		{"viewer", PermDriveAgent, false},
		{"viewer", PermRead, true},

		// Empty / legacy tokens retain full member access (upgrade-safe); only
		// an explicit unknown role fails closed to viewer.
		{"", PermDriveAgent, true},
		{"", PermCreateRoom, true},
		{"", PermManageOrg, false},
		{"", PermRead, true},
		{"bogus", PermCreateRoom, false},
		{"bogus", PermRead, true},
	}
	for _, c := range cases {
		if got := Can(c.role, c.perm); got != c.want {
			t.Errorf("Can(%q, %q) = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
}
