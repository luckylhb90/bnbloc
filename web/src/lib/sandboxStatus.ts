// Agent sandbox (desktop shell only): the member's mode and the local VM's
// status line. Shapes mirror the shell's Tauri commands
// (apps/desktop/src-tauri/src/internal_ext/sandbox.rs).

export type SandboxMode = "borrowed" | "all" | "off";

export type SandboxState = "notInstalled" | "installing" | "starting" | "ready" | "off" | "error";

export interface SandboxMachine {
  name: string;
  state: string;
  image: string;
  bootMs?: number;
  lastOp?: string;
  lastOpMs?: number;
  error?: string;
}

export interface SandboxStatus {
  installed: boolean;
  name?: string;
  version?: string;
  state: SandboxState;
  message: string;
  mode: SandboxMode;
  // The names passed into the sandboxed runs of the member's own agents.
  env: string[];
  isolation?: string;
  machine?: SandboxMachine;
  lastError?: string;
  timings: { sidecarReadyMs?: number; prewarmMs?: number };
  handedToBridge: boolean;
}

export const SANDBOX_MODES: readonly { value: SandboxMode; label: string; hint: string }[] = [
  {
    value: "borrowed",
    label: "Borrowed runs only",
    hint: "When another member runs your agent, its commands execute in the VM. Your own runs stay on this computer.",
  },
  { value: "all", label: "All my agent runs", hint: "Every command your agents run executes in the VM." },
  { value: "off", label: "Off", hint: "No VM runs. Every command runs directly on this computer." },
];

function seconds(ms: number): string {
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

// The tone of the status line.
export type SandboxTone = "muted" | "busy" | "ok" | "error";

// statusLine renders the one status line of the Agent sandbox section.
export function statusLine(s: SandboxStatus): { tone: SandboxTone; text: string } {
  switch (s.state) {
    case "notInstalled":
      return { tone: "muted", text: "Not installed. Turn on the sandbox component in Components (or ask your admin to install one)." };
    case "installing":
      return { tone: "busy", text: s.message || "Installing the sandbox runtime…" };
    case "starting":
      return { tone: "busy", text: `Starting: ${s.message || "starting the VM"}…` };
    case "off":
      return { tone: "muted", text: "Off. No VM is running." };
    case "ready": {
      const boot = s.machine?.bootMs ?? s.timings.prewarmMs;
      const lastRestart = s.machine?.lastOp === "start" && s.machine.lastOpMs ? `, last restart ${seconds(s.machine.lastOpMs)}` : "";
      return { tone: "ok", text: `Ready: VM up${boot ? ` (boot ${seconds(boot)}${lastRestart})` : ""}.` };
    }
    case "error":
      return { tone: "error", text: `Error: ${s.message || s.lastError || "the sandbox is not working"}` };
  }
}

// canRestartVm: the Restart VM button is offered once the runtime answered
// discover (isolation is known) and the member has not turned the sandbox
// off; after an error it recreates the VM.
export function canRestartVm(s: SandboxStatus): boolean {
  return s.installed && s.mode !== "off" && !!s.isolation && (s.state === "ready" || s.state === "error");
}
