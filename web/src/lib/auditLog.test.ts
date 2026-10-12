import { addDay, appendAuditPage, auditLogPath, auditOutcomeTone, EMPTY_AUDIT_FILTER } from "./auditLog.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function equal(actual: unknown, expected: unknown): void {
  if (actual !== expected) throw new Error(`expected ${String(expected)}, got ${String(actual)}`);
}

test("tenant scope reads the tenant route in the session's (acting) tenant", () => {
  equal(auditLogPath({ kind: "tenant" }, EMPTY_AUDIT_FILTER), "/v1/tenant/audit-log");
});

test("platform scope reads the platform route with an optional tenant filter", () => {
  equal(auditLogPath({ kind: "platform" }, EMPTY_AUDIT_FILTER), "/v1/platform/audit-log");
  equal(auditLogPath({ kind: "platform", tenant: "acme" }, EMPTY_AUDIT_FILTER, { limit: 50 }), "/v1/platform/audit-log?limit=50&tenant=acme");
});

test("filters map to query params; the inclusive to-day becomes an exclusive bound", () => {
  const path = auditLogPath(
    { kind: "platform" },
    { kind: "components", actor: " root ", outcome: "deny", from: "2026-10-01", to: "2026-10-31" },
    { cursor: "abc", csv: true, tzOffsetMin: -480 },
  );
  equal(
    path,
    "/v1/platform/audit-log?kind=core%2Cplugin%2Cnotifier%2Cauth-provider%2Cwebhook&actor=root&outcome=deny&from=2026-10-01&to=2026-11-01&tzOffsetMin=-480&cursor=abc&format=csv",
  );
  equal(addDay("2026-12-31"), "2027-01-01");
});

test("outcome tone and page append dedupe", () => {
  equal(auditOutcomeTone("deny"), "bad");
  equal(auditOutcomeTone("delivered"), "good");
  equal(auditOutcomeTone(undefined), "neutral");
  const a = { id: "extension:1", source: "extension" as const, kind: "member", name: "x", action: "add", atMs: 2 };
  const b = { ...a, id: "borrow:1", source: "borrow" as const };
  equal(appendAuditPage([a], [a, b]).length, 2);
});
