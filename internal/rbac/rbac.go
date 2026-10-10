// Package rbac defines the organization-level permission model layered over
// tenant membership. Roles come from the session token (auth.Claims.Role,
// resolved at login by provisioning). This package maps a role to the coarse
// permissions the gateway enforces; fine-grained room roles remain separate and
// are enforced by the room/shareagent services.
package rbac

import "strings"

// Permission is a coarse organization capability checked at the edge.
type Permission string

const (
	// PermManagePlatform covers cross-tenant platform administration (listing
	// all tenants, acting inside any tenant). Only super_admin holds it.
	PermManagePlatform Permission = "manage_platform"
	// PermManageOrg covers org administration (members, settings, audit,
	// quotas). super_admin and org_admin hold it.
	PermManageOrg Permission = "manage_org"
	// PermCreateRoom allows creating rooms and inviting members. Admins and
	// members hold it; viewers do not.
	PermCreateRoom Permission = "create_room"
	// PermDriveAgent allows driving/borrowing agents (cost-bearing). Admins and
	// members hold it; viewers are read-only.
	PermDriveAgent Permission = "drive_agent"
	// PermRead covers read-only access to rooms/feeds. Every role holds it.
	PermRead Permission = "read"
)

// Role is the org role string as it appears in the token (org_admin|member|
// viewer). An empty/unknown role is treated as the least-privileged viewer so a
// legacy token never gains more than read access.
type Role string

const (
	RoleSuperAdmin Role = "super_admin"
	RoleAdmin      Role = "org_admin"
	RoleMember     Role = "member"
	RoleViewer     Role = "viewer"
)

// Normalize maps role strings to a known role. An EMPTY role means a legacy /
// pre-RBAC token (minted before org roles, or the default tenant), which
// retains full member access so an upgrade never locks existing users out.
// An explicit but unknown role fails closed to viewer (least privilege).
func Normalize(s string) Role {
	t := strings.TrimSpace(s)
	if t == "" {
		return RoleMember
	}
	switch Role(t) {
	case RoleSuperAdmin:
		return RoleSuperAdmin
	case RoleAdmin:
		return RoleAdmin
	case RoleMember:
		return RoleMember
	case RoleViewer:
		return RoleViewer
	default:
		return RoleViewer
	}
}

// grants maps each role to the permissions it holds.
var grants = map[Role]map[Permission]bool{
	RoleSuperAdmin: {
		PermManagePlatform: true, PermManageOrg: true, PermCreateRoom: true, PermDriveAgent: true, PermRead: true,
	},
	RoleAdmin: {
		PermManageOrg: true, PermCreateRoom: true, PermDriveAgent: true, PermRead: true,
	},
	RoleMember: {
		PermCreateRoom: true, PermDriveAgent: true, PermRead: true,
	},
	RoleViewer: {
		PermRead: true,
	},
}

// Can reports whether a role holds a permission. An unknown role is normalized
// to viewer, so the check fails closed toward least privilege.
func Can(role string, p Permission) bool {
	return grants[Normalize(role)][p]
}
