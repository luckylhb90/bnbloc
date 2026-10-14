import {
  currentMonth,
  formatTokens,
  monthBounds,
  sortQuotaRequests,
  summarizeTeam,
  teamAgentsPath,
  usageReportPath,
  type QuotaRequest,
  type TeamAgent,
} from "./teamDashboard.ts";

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
  ok(value: unknown): void {
    if (!value) throw new Error(`expected a truthy value, got ${String(value)}`);
  },
};

test("teamAgentsPath picks the scope's path and carries range + tz", () => {
  assert.equal(teamAgentsPath("self", "today", -480), "/v1/team/agents?range=today&tzOffsetMin=-480");
  assert.equal(teamAgentsPath("tenant", "7d", 0), "/v1/tenant/team/agents?range=7d&tzOffsetMin=0");
});

test("monthBounds covers a local calendar month and rejects junk", () => {
  const b = monthBounds("2026-02");
  if (!b) throw new Error("monthBounds(2026-02) = null");
  assert.equal(b.fromMs, new Date(2026, 1, 1).getTime());
  assert.equal(b.toMs, new Date(2026, 2, 1).getTime());
  assert.equal(monthBounds("2026-13"), null);
  assert.equal(monthBounds("feb"), null);
  assert.equal(currentMonth(new Date(2026, 9, 8)), "2026-10");
});

test("usageReportPath scopes owner/borrower to the caller and tenant to /v1/tenant", () => {
  const own = usageReportPath({ as: "owner", groupBy: "day", month: "2026-10" }, 0, "csv");
  assert.ok(own?.startsWith("/v1/borrow/usage-report?"));
  const params = new URLSearchParams(own!.split("?")[1]);
  assert.equal(params.get("as"), "owner");
  assert.equal(params.get("format"), "csv");
  assert.equal(params.get("groupBy"), "day");
  const org = usageReportPath({ as: "tenant", groupBy: "month", month: "2026-10" }, 0);
  assert.ok(org?.startsWith("/v1/tenant/borrow/usage-report?"));
  assert.equal(new URLSearchParams(org!.split("?")[1]).get("as"), null);
  assert.equal(usageReportPath({ as: "owner", groupBy: "day", month: "" }, 0), null);
});

test("summarizeTeam counts a shared usage total once", () => {
  const base = { turns: 1, failedTurns: 0, borrowedRuns: 1, online: true } as const;
  const agents: TeamAgent[] = [
    { ...base, owner: "alice", core: "a", provider: "codex", usage: { reportedTurns: 1, inputTokens: 1, outputTokens: 1, costUsd: 2, shared: true } },
    { ...base, owner: "alice", core: "b", provider: "codex", usage: { reportedTurns: 1, inputTokens: 1, outputTokens: 1, costUsd: 2, shared: true } },
    { ...base, owner: "bob", core: "a", provider: "claude", online: false, currentJob: { jobId: "j", roomId: "r", requester: "bob", status: "running", createdAtMs: 1 } },
  ];
  const s = summarizeTeam(agents);
  assert.deepEqual(s, { online: 2, busy: 1, turns: 3, borrowedRuns: 3, costUsd: 2 });
  assert.equal(summarizeTeam([]).costUsd, undefined);
});

test("sortQuotaRequests puts pending first, newest first", () => {
  const q = (id: string, status: QuotaRequest["status"], at: number): QuotaRequest => ({
    requestId: id, roomId: "r", owner: "o", requester: "q", provider: "p", kind: "quota", status, createdAtMs: at,
  });
  const out = sortQuotaRequests([q("a", "approved", 30), q("b", "pending", 10), q("c", "pending", 20)]);
  assert.deepEqual(out.map((x) => x.requestId), ["c", "b", "a"]);
});

test("formatTokens shows reported usage and never fabricates zero", () => {
  assert.equal(formatTokens(1200, 34), "1.2k in / 34 out");
  assert.equal(formatTokens(undefined, undefined), "—");
  assert.equal(formatTokens(5, undefined), "5 in / — out");
});
