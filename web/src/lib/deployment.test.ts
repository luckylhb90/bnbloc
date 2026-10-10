import { deploymentRows, shortCommit } from "./deployment.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function equalJSON(actual: unknown, expected: unknown): void {
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) throw new Error(`expected ${b}, got ${a}`);
}

test("groups instances per service and surfaces a skewed replica's commit", () => {
  const rows = deploymentRows({
    consistent: false,
    services: [
      { service: "bnbloc-gateway", commit: "abc1234def0123456", instance: "gw-1" },
      { service: "bnbloc-room", commit: "abc1234def0123456", instance: "room-a" },
      { service: "bnbloc-room", commit: "0ld0ld0", instance: "room-b" },
      { service: "bnbloc-room", commit: "", instance: "room-c" },
    ],
  });
  equalJSON(rows, [
    { service: "gateway", instances: ["gw-1"], commits: ["abc1234def01"] },
    { service: "room", instances: ["room-a", "room-b", "room-c"], commits: ["abc1234def01", "0ld0ld0", "unknown"] },
  ]);
  equalJSON(shortCommit(""), "unknown");
});
