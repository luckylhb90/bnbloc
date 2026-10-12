import {
  budgetTone,
  buildBudgetsUpdate,
  describeBurn,
  describeLimits,
  draftFromView,
  emptyDraft,
  limitsFromDraft,
  periodLabel,
} from "./budgets.ts";

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

test("limitsFromDraft validates like the server", () => {
  assert.equal(limitsFromDraft(emptyDraft()).error, "set a token or cost limit");
  assert.equal(limitsFromDraft({ ...emptyDraft(), tokens: "1.5" }).error, "tokens must be a whole number of at least 1");
  assert.equal(limitsFromDraft({ ...emptyDraft(), cost: "0" }).error, "cost must be greater than 0");
  assert.equal(limitsFromDraft({ ...emptyDraft(), tokens: "10", warnAtPercent: "100" }).error, "warn at must be between 1 and 99 percent");
  assert.deepEqual(limitsFromDraft({ ...emptyDraft(), tokens: " 5000 ", cost: "2.5", warnAtPercent: "", hardStop: true }).limits, {
    warnAtPercent: 80,
    hardStop: true,
    tokens: 5000,
    cost: 2.5,
  });
});

test("buildBudgetsUpdate names the failing row and refuses duplicates", () => {
  const ok = buildBudgetsUpdate({ ...emptyDraft(), cost: "100" }, [{ ...emptyDraft("alice"), tokens: "1000", warnAtPercent: "50" }]);
  assert.deepEqual(ok.update, {
    tenant: { warnAtPercent: 80, hardStop: false, cost: 100 },
    members: [{ userId: "alice", warnAtPercent: 50, hardStop: false, tokens: 1000 }],
  });
  assert.deepEqual(buildBudgetsUpdate(null, []).update, { tenant: null, members: [] });
  assert.equal(buildBudgetsUpdate(null, [{ ...emptyDraft("bob") }]).error, "bob: set a token or cost limit");
  assert.equal(
    buildBudgetsUpdate(null, [{ ...emptyDraft("bob"), tokens: "1" }, { ...emptyDraft("bob"), tokens: "2" }]).error,
    "bob has two budgets",
  );
  assert.equal(buildBudgetsUpdate(null, [{ ...emptyDraft(" "), tokens: "1" }]).error, "Every member budget needs a member");
});

test("draftFromView round-trips through limitsFromDraft", () => {
  const view = { userId: "alice", tokens: 1000, warnAtPercent: 60, hardStop: true, burn: { tokens: 0, cost: 0 }, percent: 0, reached: false };
  assert.deepEqual(limitsFromDraft(draftFromView(view)).limits, { warnAtPercent: 60, hardStop: true, tokens: 1000 });
});

test("budgetTone and descriptions", () => {
  assert.equal(budgetTone({ percent: 10, reached: false, warnAtPercent: 80 }), "ok");
  assert.equal(budgetTone({ percent: 85, reached: false, warnAtPercent: 80 }), "warn");
  assert.equal(budgetTone({ percent: 100, reached: true, warnAtPercent: 80 }), "over");
  const v = { tokens: 10000, cost: 10, warnAtPercent: 80, hardStop: false, burn: { tokens: 1200, cost: 1.5 } };
  assert.equal(describeBurn(v), "1.2k of 10k tokens · $1.50 of $10.00");
  assert.equal(describeBurn({ ...v, tokens: undefined }), "$1.50 of $10.00");
  assert.equal(describeLimits(v), "10k tokens · $10.00 / month");
  assert.equal(periodLabel("2026-10"), "October 2026 (UTC)");
});
