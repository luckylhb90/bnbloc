import { canRestartVm, statusLine, type SandboxStatus } from "./sandboxStatus.ts";

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
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

const base: SandboxStatus = {
  installed: true,
  name: "vm-runtime",
  version: "0.1.0",
  state: "ready",
  message: "VM up",
  mode: "borrowed",
  env: [],
  timings: { sidecarReadyMs: 120, prewarmMs: 900 },
  handedToBridge: true,
  isolation: "microvm",
  machine: { name: "bnbloc", state: "running", image: "img:1", bootMs: 512 },
};

test("not installed points at Components", () => {
  const line = statusLine({ ...base, installed: false, state: "notInstalled", machine: undefined });
  equal(line.tone, "muted");
  equal(line.text.includes("Components"), true);
});

test("ready shows the VM boot time", () => {
  equal(statusLine(base), { tone: "ok", text: "Ready: VM up (boot 512 ms)." });
  equal(
    statusLine({ ...base, machine: { ...base.machine!, bootMs: 1500, lastOp: "start", lastOpMs: 190 } }).text,
    "Ready: VM up (boot 1.5 s, last restart 190 ms).",
  );
});

test("starting and error carry the shell message", () => {
  equal(statusLine({ ...base, state: "starting", message: "Preparing the VM image" }), {
    tone: "busy",
    text: "Starting: Preparing the VM image…",
  });
  equal(statusLine({ ...base, state: "error", message: "image_pull_failed: offline" }), {
    tone: "error",
    text: "Error: image_pull_failed: offline",
  });
});

test("restart is offered only for a running runtime outside mode off", () => {
  equal(canRestartVm(base), true);
  equal(canRestartVm({ ...base, mode: "off", state: "off" }), false);
  equal(canRestartVm({ ...base, state: "starting" }), false);
  equal(canRestartVm({ ...base, state: "error", machine: undefined }), true);
  equal(canRestartVm({ ...base, state: "error", isolation: undefined }), false);
  equal(canRestartVm({ ...base, installed: false, state: "notInstalled" }), false);
});
