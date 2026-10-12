import { can } from "./permissions.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// The rows of internal/rbac's TestPermissionMatrix: the web answers alike
// (the grants are generated from rbac.Can, the fallback from rbac.Normalize).
const rows: [string, string, boolean][] = [
  ["super_admin", "manage_platform", true],
  ["org_admin", "manage_platform", false],
  ["member", "manage_platform", false],
  ["viewer", "manage_platform", false],
  ["", "manage_platform", false],
  ["bogus", "manage_platform", false],
  ["org_admin", "manage_org", true],
  ["member", "manage_org", false],
  ["member", "create_room", true],
  ["member", "drive_agent", true],
  ["viewer", "create_room", false],
  ["viewer", "drive_agent", false],
  ["viewer", "read", true],
  ["", "drive_agent", true],
  ["", "manage_org", false],
  ["bogus", "create_room", false],
  ["bogus", "read", true],
  [" member ", "create_room", true],
];
for (const [role, perm, want] of rows) {
  assert(can(role, perm as Parameters<typeof can>[1]) === want, `can(${JSON.stringify(role)}, ${perm}) != ${want}`);
}
assert(can(null, "read") && can(undefined, "create_room"), "no role is the empty role");
