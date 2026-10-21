// The agent sandbox on this computer (desktop shell only): the local VM's
// status as its Components settings dialog shows it. What it does (the
// mode, the environment, the private hosts) is the sandbox component's own
// settings, set in that dialog's form. Shapes mirror the shell's Tauri
// commands (apps/desktop/src-tauri/src/internal_ext/sandbox.rs).

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
  isolation?: string;
  machine?: SandboxMachine;
  lastError?: string;
  timings: { sidecarReadyMs?: number; prewarmMs?: number };
  handedToBridge: boolean;
  privateHosts: PrivateHostsStatus;
}

export type PrivateHostsState = "none" | "unsupported" | "inactive" | "waiting" | "applying" | "ready" | "error";

// The private hosts the member's own sandboxed runs reach
// (docs/prd/2026-10-21-sandbox-private-hosts.md): the organization's and,
// when it lets members allow their own, the member's.
export interface PrivateHostsStatus {
  hosts: string[];
  // The member may allow a host of their own (an agent reply's card offers it).
  memberMayAllow: boolean;
  state: PrivateHostsState;
  message: string;
  // The hosts the member's own VM opens now.
  applied: string[];
}

// privateHostsLine is the Private hosts status line (empty when no host is
// listed).
export function privateHostsLine(p: PrivateHostsStatus): { tone: SandboxTone; text: string } | null {
  if (p.hosts.length === 0) return null;
  switch (p.state) {
    case "none":
      return null;
    case "ready":
      return { tone: "ok", text: p.message };
    case "waiting":
    case "applying":
      return { tone: "busy", text: p.message };
    case "error":
      return { tone: "error", text: p.message };
    case "unsupported":
    case "inactive":
      return { tone: "muted", text: p.message };
  }
}

// mayAllowHosts: the member may allow a private host of their own on this
// computer: the sandbox's settings let them and its runtime can open one.
export function mayAllowHosts(s: SandboxStatus): boolean {
  return s.installed && s.privateHosts.memberMayAllow && s.privateHosts.state !== "unsupported";
}

// validPrivateHost is the one rule for a host the sandbox may open to the
// member's own runs: the Go owner's (sandbox/protocol.ValidPrivateHost), as
// a copy pinned by sandbox/protocol/testdata/private_hosts.json (the desktop
// holds another). A lower-case DNS name of at least two labels or an IP
// literal; never a wildcard, URL, port, localhost, or a loopback,
// link-local, unspecified, broadcast or multicast address. A proposal's
// host is held to it before the card offers it.
export function validPrivateHost(host: string): boolean {
  if (host.includes(":")) return validIPv6(host);
  const v4 = ipv4(host);
  if (v4) return validIPv4(v4);
  const labels = host.split(".");
  if (host.length > 253 || labels.length < 2) return false;
  if (!labels.every((l) => l.length > 0 && l.length <= 63 && /^[a-z0-9-]+$/.test(l) && !l.startsWith("-") && !l.endsWith("-"))) return false;
  // A last label of digits only is a malformed address, and localhost is
  // the guest itself.
  const last = labels[labels.length - 1];
  return !/^[0-9]+$/.test(last) && last !== "localhost";
}

// ipv4 is a dotted-quad address's octets (as Go's netip reads one: four
// decimal octets, no leading zero), else null.
function ipv4(host: string): number[] | null {
  const parts = host.split(".");
  if (parts.length !== 4 || !parts.every((p) => /^(0|[1-9][0-9]{0,2})$/.test(p))) return null;
  const octets = parts.map(Number);
  return octets.every((o) => o <= 255) ? octets : null;
}

function validIPv4([a, b, c, d]: number[]): boolean {
  const loopback = a === 127;
  const linkLocal = a === 169 && b === 254;
  const unspecified = a === 0 && b === 0 && c === 0 && d === 0;
  const multicast = a >= 224 && a <= 239;
  const broadcast = a === 255 && b === 255 && c === 255 && d === 255;
  return !(loopback || linkLocal || unspecified || multicast || broadcast);
}

// validIPv6 reads an IPv6 literal through the URL parser (which refuses a
// zone and normalizes an embedded IPv4 part to hex), then holds it to the
// rule; an IPv4-mapped one is held to IPv4's.
function validIPv6(host: string): boolean {
  let normalized: string;
  try {
    normalized = new URL(`http://[${host}]/`).hostname.slice(1, -1);
  } catch {
    return false;
  }
  const halves = normalized.split("::");
  const groups = (s: string) => (s === "" ? [] : s.split(":").map((g) => parseInt(g, 16)));
  const head = groups(halves[0]);
  const tail = halves.length > 1 ? groups(halves[1]) : [];
  const words = [...head, ...Array<number>(8 - head.length - tail.length).fill(0), ...tail];
  if (words.length !== 8 || words.some((w) => Number.isNaN(w))) return false;
  if (words.slice(0, 5).every((w) => w === 0) && words[5] === 0xffff) {
    return validIPv4([words[6] >> 8, words[6] & 0xff, words[7] >> 8, words[7] & 0xff]);
  }
  const unspecified = words.every((w) => w === 0);
  const loopback = words.slice(0, 7).every((w) => w === 0) && words[7] === 1;
  const linkLocal = (words[0] & 0xffc0) === 0xfe80;
  const multicast = (words[0] & 0xff00) === 0xff00;
  return !(unspecified || loopback || linkLocal || multicast);
}

function seconds(ms: number): string {
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

// The tone of the status line.
export type SandboxTone = "muted" | "busy" | "ok" | "error";

// statusLine renders the sandbox's one status line.
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

// What "Reload shell environment" (Agents → Local bridge) answers (the shell's `shell_env_reload`,
// apps/desktop/src-tauri/src/shell_env.rs): how many variables agents now
// get from the member's login shell, and why none when the shell failed.
// Never a name or a value.
export interface ShellEnvReload {
  imported: number;
  error?: string;
}

// shellEnvReloadText is the toast after a reload; ok=false when the shell
// failed and the agents kept the app's own environment.
export function shellEnvReloadText(r: ShellEnvReload): { ok: boolean; text: string } {
  if (r.error) return { ok: false, text: `Could not read your shell environment (${r.error}). Agents restarted without it.` };
  const n = r.imported;
  return { ok: true, text: `Agents restarted with ${n} variable${n === 1 ? "" : "s"} from your shell.` };
}
