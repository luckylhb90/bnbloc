import { pendingUpdates, pendingUpdatesNote, runningTurnsWarning } from "./pendingUpdates.ts";

function eq(actual: unknown, expected: unknown, what: string): void {
  const a = JSON.stringify(actual);
  const e = JSON.stringify(expected);
  if (a !== e) throw new Error(`${what}: got ${a}, want ${e}`);
}

// Only statuses with a waiting update are listed, by name.
eq(
  pendingUpdates([
    { name: "nimbus", pendingUpdate: { version: "0.4.8", waitingFor: 2 } },
    { name: "acp", pendingUpdate: null },
    { name: "sandbox", pendingUpdate: { version: "0.1.3", waitingFor: 2 } },
    { name: "plain" },
  ]),
  [
    { name: "nimbus", version: "0.4.8", waitingFor: 2 },
    { name: "sandbox", version: "0.1.3", waitingFor: 2 },
  ],
  "waiting updates",
);

eq(pendingUpdatesNote([]), "", "nothing waits");
eq(
  pendingUpdatesNote([{ name: "nimbus", version: "0.4.8", waitingFor: 1 }]),
  "nimbus 0.4.8 is ready — waiting for 1 running task to finish, then it applies on its own.",
  "one update, one task",
);
eq(
  pendingUpdatesNote([
    { name: "acp", version: "0.2.5", waitingFor: 3 },
    { name: "nimbus", version: "0.4.8", waitingFor: 3 },
  ]),
  "acp 0.2.5, nimbus 0.4.8 are ready — waiting for 3 running tasks to finish, then they apply on their own.",
  "two updates",
);
eq(
  pendingUpdatesNote([{ name: "nimbus", version: "0.4.8", waitingFor: 0 }]),
  "nimbus 0.4.8 is ready and will apply in a moment.",
  "idle now",
);

eq(runningTurnsWarning(0), null, "no warning when idle");
eq(
  runningTurnsWarning(1),
  "Your agents are working on 1 running task. Updating now restarts them and stops it midway; their work so far stays in the room and the workspace.",
  "one task",
);
if (!runningTurnsWarning(2)?.includes("2 running tasks")) throw new Error("two tasks");
