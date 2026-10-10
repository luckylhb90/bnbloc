import { clientLabel, groupDevices, otherSessionCount, type MySession } from "./devices.ts";

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
  const a = JSON.stringify(actual);
  const b = JSON.stringify(expected);
  if (a !== b) throw new Error(`expected ${b}, got ${a}`);
}

function s(id: string, client: string, lastUsedAtMs: number, extra: Partial<MySession> = {}): MySession {
  return { id, client, createdAtMs: 1, lastUsedAtMs, expiresAtMs: 9, current: false, ...extra };
}

test("clientLabel names each surface and labels bridges as the desktop agent", () => {
  equal(
    ["web", "desktop", "cli", "bridge", "", "other"].map(clientLabel),
    ["Browser", "Desktop app", "Command line", "Desktop agent", "Unknown client", "other"],
  );
});

test("groupDevices nests forks under their device, current first, then most recent", () => {
  const list = [
    s("old", "web", 10),
    s("agent-of-desk", "bridge", 50, { parentId: "desk" }),
    s("desk", "desktop", 40),
    s("me", "web", 5, { current: true }),
    s("orphan", "bridge", 30, { parentId: "gone" }),
  ];
  const groups = groupDevices(list);
  equal(
    groups.map((g) => [g.session.id, g.agents.map((a) => a.id)]),
    [
      ["me", []],
      ["desk", ["agent-of-desk"]],
      ["orphan", []],
      ["old", []],
    ],
  );
});

test("otherSessionCount spares the current session and its own agent", () => {
  const list = [
    s("me", "desktop", 5, { current: true }),
    s("my-agent", "bridge", 5, { parentId: "me" }),
    s("laptop", "web", 5),
    s("laptop-agent", "bridge", 5, { parentId: "laptop" }),
  ];
  equal(otherSessionCount(list), 2);
  equal(otherSessionCount([s("me", "web", 1, { current: true })]), 0);
});
