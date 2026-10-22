// Test accounts (docs/prd/2026-10-22-multi-account.md §2.3): svc:<name>
// identities with a chosen organization role, which a super admin creates on
// the platform console's "Test accounts" page. The gateway already has both
// halves, an organization member with a role and an API token for the
// subject; this module only does them in the right order and reads the
// result back. No React, no DOM.

import type { APITokenCreated, APITokenView } from "./wire.gen.ts";
import { ServiceSubjectPrefix } from "./wire.gen.ts";
import type { TenantMember } from "./apiModels.ts";

/** The roles a test account can be given. Never super admin: that is configuration. */
export const TEST_ACCOUNT_ROLES = ["viewer", "member", "org_admin"] as const;
export type TestAccountRole = (typeof TEST_ACCOUNT_ROLES)[number];

/** What the gateway accepts after the prefix: lowercase letters, digits, . _ - */
const NAME = /^[a-z0-9][a-z0-9._-]{0,62}$/;

/**
 * The subject a typed name makes (svc:<name>), or null when the gateway would
 * refuse it. A typed "svc:" prefix and surrounding space are tolerated.
 */
export function testAccountSubject(typed: string): string | null {
  let name = typed.trim().toLowerCase();
  if (name.startsWith(ServiceSubjectPrefix)) name = name.slice(ServiceSubjectPrefix.length);
  return NAME.test(name) ? ServiceSubjectPrefix + name : null;
}

export interface TestAccountRow {
  readonly subject: string;
  readonly role: TenantMember["role"];
  /** API tokens of this subject that can still sign in. */
  readonly activeTokens: number;
  readonly lastUsedAtMs: number;
  /** The ids of every token of this subject (active or not), to revoke. */
  readonly tokenIds: readonly string[];
}

/** The test accounts of the tenant: its svc: members, with their tokens. */
export function testAccountRows(members: readonly TenantMember[], tokens: readonly APITokenView[]): TestAccountRow[] {
  return members
    .filter((m) => m.userId.startsWith(ServiceSubjectPrefix))
    .map((m) => {
      const own = tokens.filter((t) => t.subject === m.userId);
      return {
        subject: m.userId,
        role: m.role,
        activeTokens: own.filter((t) => t.active).length,
        lastUsedAtMs: own.reduce((latest, t) => Math.max(latest, t.lastUsedAtMs ?? 0), 0),
        tokenIds: own.map((t) => t.id),
      };
    })
    .sort((a, b) => a.subject.localeCompare(b.subject));
}

export interface TestAccountApi {
  addTenantMember: (userId: string, role: TestAccountRole) => Promise<unknown>;
  removeTenantMember: (userId: string) => Promise<unknown>;
  createApiToken: (body: { subject: string; label: string; ttlDays: number }) => Promise<APITokenCreated>;
  revokeApiToken: (id: string) => Promise<unknown>;
}

/**
 * Creates a test account: the member first, with its role, then the token. A
 * create that fails after the member was added leaves a member with no token
 * (listed, and a token can be issued for it), never a token for a subject
 * that would sign in with the wrong role: a subject with no membership is
 * provisioned as a plain member on its first sign-in.
 */
export async function createTestAccount(
  api: Pick<TestAccountApi, "addTenantMember" | "createApiToken">,
  subject: string,
  role: TestAccountRole,
  ttlDays: number,
): Promise<APITokenCreated> {
  await api.addTenantMember(subject, role);
  return api.createApiToken({ subject, label: `Test account: ${role}`, ttlDays });
}

/** Removes a test account: every token revoked, then the member. */
export async function removeTestAccount(
  api: Pick<TestAccountApi, "removeTenantMember" | "revokeApiToken">,
  row: Pick<TestAccountRow, "subject" | "tokenIds" | "activeTokens">,
  tokens: readonly APITokenView[],
): Promise<void> {
  const active = new Set(tokens.filter((t) => t.active).map((t) => t.id));
  for (const id of row.tokenIds) if (active.has(id)) await api.revokeApiToken(id);
  await api.removeTenantMember(row.subject);
}
