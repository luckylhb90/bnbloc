import {
  describeDays,
  describeEffective,
  describeStatus,
  fieldsFromPolicy,
  policyFromFields,
  samePolicy,
  type RetentionPolicy,
  type TenantRetentionView,
} from "./retention.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
const assert = {
  equal(actual: unknown, expected: unknown): void {
    if (actual !== expected) throw new Error(`expected ${String(expected)}, got ${String(actual)}`);
  },
  deepEqual(actual: unknown, expected: unknown): void {
    if (JSON.stringify(actual) !== JSON.stringify(expected)) {
      throw new Error(`expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
    }
  },
};

const limits = { minDays: 31, maxDays: 36500 };
const empty: RetentionPolicy = { messagesDays: null, artifactVersionsDays: null, auditDays: null, agentJobsDays: null };

test("tenant fields: null uses the default, 0 keeps forever, N is days", () => {
  const f = fieldsFromPolicy({ ...empty, messagesDays: 0, auditDays: 90 }, "tenant");
  assert.deepEqual(f.messages, { mode: "forever", days: "" });
  assert.deepEqual(f.audit, { mode: "days", days: "90" });
  assert.deepEqual(f.agentJobs, { mode: "default", days: "" });
});

test("platform fields: null keeps forever", () => {
  const f = fieldsFromPolicy({ ...empty, artifactVersionsDays: 365 }, "platform");
  assert.deepEqual(f.messages, { mode: "forever", days: "" });
  assert.deepEqual(f.artifactVersions, { mode: "days", days: "365" });
});

test("policyFromFields round-trips each layer", () => {
  const tenant = { ...empty, messagesDays: 0, auditDays: 400 };
  assert.deepEqual(policyFromFields(fieldsFromPolicy(tenant, "tenant"), "tenant", limits), { policy: tenant });
  const platform = { ...empty, agentJobsDays: 90 };
  assert.deepEqual(policyFromFields(fieldsFromPolicy(platform, "platform"), "platform", limits), { policy: platform });
});

test("policyFromFields refuses days outside the server's limits", () => {
  const f = fieldsFromPolicy(empty, "tenant");
  for (const days of ["30", "36501", "4.5", "", "abc"]) {
    const r = policyFromFields({ ...f, messages: { mode: "days", days } }, "tenant", limits);
    assert.equal("error" in r && r.error.startsWith("Room messages:"), true);
  }
  const ok = policyFromFields({ ...f, messages: { mode: "days", days: " 31 " } }, "tenant", limits);
  assert.deepEqual(ok, { policy: { ...empty, messagesDays: 31 } });
});

test("the platform layer has no default to fall back to", () => {
  const f = fieldsFromPolicy(empty, "tenant");
  assert.equal("error" in policyFromFields(f, "platform", limits), true);
});

test("samePolicy compares every kind", () => {
  assert.equal(samePolicy(empty, { ...empty }), true);
  assert.equal(samePolicy(empty, { ...empty, auditDays: 0 }), false);
});

test("descriptions", () => {
  assert.equal(describeDays(null), "Kept forever");
  assert.equal(describeDays(90), "Deleted 90 days after it was created");
  const view: TenantRetentionView = {
    tenantId: "acme",
    override: { ...empty, auditDays: 0 },
    default: { ...empty, messagesDays: 365, auditDays: 400 },
    effective: { ...empty, messagesDays: 365 },
    limits,
  };
  assert.equal(describeEffective(view, "messages"), "Deleted 365 days after it was created (platform default).");
  assert.equal(describeEffective(view, "audit"), "Kept forever (your setting).");
  assert.equal(describeEffective(view, "agentJobs"), "Kept forever (platform default).");
});

test("describeStatus", () => {
  const now = Date.UTC(2026, 9, 11, 12, 0, 0);
  assert.equal(describeStatus({ lastRunAtMs: 0, lastBlobGcAtMs: 0, lastDeleted: null }, now), "The cleanup has not run yet.");
  assert.equal(
    describeStatus({ lastRunAtMs: now - 2 * 3600_000, lastBlobGcAtMs: 0, lastDeleted: { room_messages: 1204, blobs: 3, attachments: 0 } }, now),
    "The last cleanup ran 2 hours ago and removed 3 stored files, 1,204 messages.",
  );
  assert.equal(
    describeStatus({ lastRunAtMs: now - 5 * 60_000, lastBlobGcAtMs: 0, lastDeleted: {} }, now),
    "The last cleanup ran 5 minutes ago and found nothing to remove.",
  );
});
