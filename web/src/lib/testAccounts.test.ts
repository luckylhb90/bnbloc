import { createTestAccount, removeTestAccount, testAccountRows, testAccountSubject } from "./testAccounts.ts";
import type { TenantMember } from "./apiModels.ts";
import type { APITokenView } from "./wire.gen.ts";

const ok = (cond: boolean, msg: string) => {
  if (!cond) throw new Error(msg);
};

// A typed name becomes the subject the gateway accepts, or nothing.
ok(testAccountSubject("qa-viewer") === "svc:qa-viewer", "a bare name gets the prefix");
ok(testAccountSubject("  SVC:QA-Viewer ") === "svc:qa-viewer", "prefix, case and space are tolerated");
ok(testAccountSubject("") === null && testAccountSubject("svc:") === null, "an empty name is refused");
ok(testAccountSubject("-x") === null && testAccountSubject("a b") === null && testAccountSubject("a@b") === null, "characters the gateway refuses");
ok(testAccountSubject("a".repeat(63)) !== null && testAccountSubject("a".repeat(64)) === null, "the length bound is the gateway's");

// The page lists the svc: members with the state of their tokens.
const members: TenantMember[] = [
  { userId: "maya@corp", role: "org_admin", joinedAtMs: 1 },
  { userId: "svc:qa-viewer", role: "viewer", joinedAtMs: 2 },
  { userId: "svc:qa-admin", role: "org_admin", joinedAtMs: 3 },
];
const tok = (id: string, subject: string, active: boolean, lastUsedAtMs = 0): APITokenView => ({
  id, subject, label: "", createdBy: "x", createdAtMs: 1, expiresAtMs: 2, lastUsedAtMs, active,
});
const tokens = [tok("t1", "svc:qa-viewer", true, 50), tok("t2", "svc:qa-viewer", false, 90), tok("t3", "svc:other", true)];
const rows = testAccountRows(members, tokens);
ok(rows.map((r) => r.subject).join() === "svc:qa-admin,svc:qa-viewer", "only svc: members, sorted");
const viewer = rows[1]!;
ok(viewer.activeTokens === 1 && viewer.lastUsedAtMs === 90 && viewer.tokenIds.join() === "t1,t2", "tokens of the subject only");
ok(rows[0]!.activeTokens === 0, "an account with no token says so");

// Creating: the member first (with its role), the token second.
const calls: string[] = [];
const api = {
  addTenantMember: async (u: string, r: string) => void calls.push(`member ${u} ${r}`),
  createApiToken: async (b: { subject: string; label: string; ttlDays: number }) => {
    calls.push(`token ${b.subject} ${b.ttlDays}d`);
    return { ...tok("new", b.subject, true), token: "bnbloc_at_secret" };
  },
  removeTenantMember: async (u: string) => void calls.push(`remove ${u}`),
  revokeApiToken: async (id: string) => void calls.push(`revoke ${id}`),
};
const made = await createTestAccount(api, "svc:qa-viewer", "viewer", 30);
ok(calls.join("|") === "member svc:qa-viewer viewer|token svc:qa-viewer 30d", `order: ${calls.join("|")}`);
ok(made.token === "bnbloc_at_secret", "the token is returned to be shown once");

// A failed member add issues no token.
calls.length = 0;
let failed = false;
try {
  await createTestAccount({ ...api, addTenantMember: async () => Promise.reject(new Error("no")) }, "svc:x", "member", 7);
} catch {
  failed = true;
}
ok(failed && calls.length === 0, "no token is issued for a member that could not be added");

// Removing revokes the active tokens, then removes the member.
calls.length = 0;
await removeTestAccount(api, viewer, tokens);
ok(calls.join("|") === "revoke t1|remove svc:qa-viewer", `removal: ${calls.join("|")}`);
