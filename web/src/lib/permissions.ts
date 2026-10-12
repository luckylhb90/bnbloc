// Organization permissions, answered exactly as the gateway answers them
// (internal/rbac: Can, Normalize). The grants and the role an unknown role
// acts as are generated from rbac (OrgRolePermissions, OrgRoleUnknown in
// wire.gen.ts), so the UI never offers what the gateway refuses (web H8): a
// viewer gets no room creation and no composer.
//
// Room roles are a different thing: lib/roomRoles.ts.

import { OrgRolePermissions, OrgRoleUnknown, type OrgPermission } from "./wire.gen.ts";

export type { OrgPermission, OrgRole } from "./wire.gen.ts";

// can reports whether the session's org role holds perm. A session without
// a role holds the grants of the empty role; an unrecognized role those of
// OrgRoleUnknown (least privilege).
export function can(role: string | null | undefined, perm: OrgPermission): boolean {
  const table = OrgRolePermissions as Readonly<Record<string, readonly OrgPermission[]>>;
  const grants = table[(role ?? "").trim()] ?? table[OrgRoleUnknown];
  return grants.includes(perm);
}
