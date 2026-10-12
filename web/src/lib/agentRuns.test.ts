import {
  isFailedRun,
  mergeRunPages,
  runDurationMs,
  runPhaseSteps,
  runsPath,
  statusParam,
  type AgentRun,
} from "./agentRuns.ts";

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

function run(over: Partial<AgentRun>): AgentRun {
  return {
    jobId: "j1",
    roomId: "r1",
    requester: "bob",
    status: "replied",
    request: { messageSeq: 3 },
    agent: { owner: "alice", core: "c1", provider: "codex" },
    phases: { queuedAtMs: 1000 },
    ...over,
  };
}

test("runsPath builds each scope's path with only the filters set", () => {
  assert.equal(runsPath("room", {}, "a b"), "/v1/rooms/a%20b/jobs");
  assert.equal(runsPath("self", { status: "failed", limit: 25 }), "/v1/my/jobs?status=failed%2Ctimed_out&limit=25");
  assert.equal(
    runsPath("tenant", { member: " bob ", owner: "alice", core: "c1", provider: "codex", before: "5:j" }),
    "/v1/tenant/jobs?member=bob&owner=alice&core=c1&provider=codex&before=5%3Aj",
  );
  // member is a tenant-only filter
  assert.equal(runsPath("self", { member: "bob" }), "/v1/my/jobs");
  assert.equal(runsPath("tenant", { fromMs: 10, toMs: 20 }), "/v1/tenant/jobs?from=10&to=20");
});

test("statusParam folds timed out into failed", () => {
  assert.equal(statusParam("failed"), "failed,timed_out");
  assert.equal(statusParam("active"), "active");
  assert.equal(statusParam(""), "");
});

test("runPhaseSteps keeps recorded phases in order with the gap since the previous", () => {
  const steps = runPhaseSteps(
    run({ phases: { queuedAtMs: 1000, claimedAtMs: 1200, startedAtMs: 1500, firstReplyAtMs: 4500, finishedAtMs: 5000 } }),
  );
  assert.deepEqual(
    steps.map((s) => [s.id, s.sincePrevMs ?? null]),
    [
      ["queued", null],
      ["claimed", 200],
      ["started", 300],
      ["firstReply", 3000],
      ["finished", 500],
    ],
  );
  assert.equal(steps[4].label, "Replied");
  const failed = runPhaseSteps(run({ status: "timed_out", phases: { queuedAtMs: 1000, finishedAtMs: 9000 } }));
  assert.deepEqual(failed.map((s) => s.label), ["Queued", "Timed out"]);
  // A live run has no finish step even if a retried attempt left one behind.
  const live = runPhaseSteps(run({ status: "running", phases: { queuedAtMs: 1000, startedAtMs: 1100, finishedAtMs: 1050 } }));
  assert.deepEqual(live.map((s) => s.id), ["queued", "started"]);
});

test("runDurationMs runs to finish, or to now while live", () => {
  assert.equal(runDurationMs(run({ phases: { queuedAtMs: 1000, finishedAtMs: 4000 } })), 3000);
  assert.equal(runDurationMs(run({ status: "queued", phases: { queuedAtMs: 1000 } }), 1600), 600);
  assert.equal(runDurationMs(run({ phases: { queuedAtMs: 1000 } })), undefined);
});

test("isFailedRun covers both failure statuses", () => {
  assert.equal(isFailedRun({ status: "failed" }), true);
  assert.equal(isFailedRun({ status: "timed_out" }), true);
  assert.equal(isFailedRun({ status: "cancelled" }), false);
});

test("mergeRunPages appends older runs without duplicates", () => {
  const merged = mergeRunPages([run({ jobId: "a" }), run({ jobId: "b" })], [run({ jobId: "b" }), run({ jobId: "c" })]);
  assert.deepEqual(merged.map((r) => r.jobId), ["a", "b", "c"]);
});
