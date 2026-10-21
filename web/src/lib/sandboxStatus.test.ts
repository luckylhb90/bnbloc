import { canRestartVm, privateHostsLine, shellEnvReloadText, statusLine, type SandboxStatus } from "./sandboxStatus.ts";

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
  privateHosts: { hosts: [], state: "none", message: "", applied: [] },
};

test("private hosts: no line without hosts, the shell's message otherwise", () => {
  equal(privateHostsLine(base.privateHosts), null);
  const listed = { ...base.privateHosts, hosts: [{ host: "npm.internal.example", addedAtMs: 1 }] };
  equal(privateHostsLine({ ...listed, state: "waiting", message: "Applies when your agent's current run ends." }), {
    tone: "busy",
    text: "Applies when your agent's current run ends.",
  });
  equal(privateHostsLine({ ...listed, state: "error", message: "did not start" })?.tone, "error");
  equal(privateHostsLine({ ...listed, state: "ready", message: "open" })?.tone, "ok");
});

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

test("a shell environment reload reports the count or why none", () => {
  equal(shellEnvReloadText({ imported: 42 }), { ok: true, text: "Agents restarted with 42 variables from your shell." });
  equal(shellEnvReloadText({ imported: 1 }).text, "Agents restarted with 1 variable from your shell.");
  equal(shellEnvReloadText({ imported: 0, error: "/bin/zsh timed out after 4s" }), {
    ok: false,
    text: "Could not read your shell environment (/bin/zsh timed out after 4s). Agents restarted without it.",
  });
});
