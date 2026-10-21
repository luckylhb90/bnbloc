// Desktop shell bridge (Tauri IPC), feature-detected so the same bundle runs
// unchanged in a plain browser. When not in the shell, every call is a no-op /
// unavailable and the UI hides shell-only surfaces (see inDesktopShell()).
//
// We reach Tauri through its injected globals rather than the @tauri-apps/api
// npm package, so the web package needs no Tauri build dependency and the
// browser build stays dependency-free.

import { inDesktopShell } from "./config";
import { api, type AgentTarget, type UpdatePolicy } from "./api";
import type { ComponentSettingsUpdate, DesktopComponentSettings } from "./componentSettings";
import { ApiError } from "./errors.ts";
import { unframePackedSkill, type SkillsProvider, type SkillsSettings, type SkillsState } from "./localSkills";
import type { PrivateHost, SandboxMode, SandboxStatus, ShellEnvReload } from "./sandboxStatus";
import { SESSION_KEY, parseSessionRecord, type SessionRecord } from "./sessionGrant";
import { StorageKey, readStored, writeStored } from "./storageKeys.ts";

export type BridgeState = "stopped" | "starting" | "running" | "failed";

export interface BridgeStatus {
  state: BridgeState;
  pid?: number;
  lastExitCode?: number;
  logsTail: string[];
}

/** One configured agent source (daemon backend). Empty url/token rely on the
 *  bridge's per-source auto-discovery. `kind` is "local" today (extension point
 *  for future remote sources). */
export interface CoreConfig {
  name: string;
  kind: string;
  url?: string;
  token?: string;
}

/** The global default agent, used when a room chat names no provider. */
export interface DefaultAgent {
  core: string;
  provider: string;
}

export interface DesktopSettings {
  gatewayUrl: string;
  owner: string;
  /** Active tenant/workspace; bridge switches to it before subscribing. */
  tenant: string;
  cores: CoreConfig[];
  defaultAgent: DefaultAgent;
  disabledProviders: string[];
  syncSecret: string;
  /** Single-host mode: this one bridge drives every member's agent (local
   *  multi-user testing). Off in a real deployment. */
  serveAllOwners: boolean;
  /** Optional static bearer for gateway calls, for enterprise-IdP (remote)
   *  gateways that reject the bridge's dev login. Empty keeps the default
   *  owner-login path. Mirrors BNBLOC_GATEWAY_TOKEN. */
  gatewayToken: string;
  /** Which of this member's agent runs execute their commands in the local
   *  sandbox VM. Mirrors BNBLOC_SANDBOX_MODE. */
  sandboxMode: SandboxMode;
  /** Environment variable names (or PREFIX* patterns) passed into the
   *  sandboxed runs of this member's own agents; names only. Shell-owned:
   *  changed through setSandboxEnv. Mirrors BNBLOC_SANDBOX_ENV. */
  sandboxEnv: string[];
  /** Private hosts this member's own sandboxed runs may reach. Shell-owned:
   *  changed through addSandboxPrivateHost / removeSandboxPrivateHost. */
  sandboxPrivateHosts: PrivateHost[];
}

export interface AppInfo {
  version: string;
  platform: string;
  inShell: boolean;
}

export interface InternalAdapterStatus {
  /** "core" (drives agents), "plugin" (event hooks only) or "sandbox" (Sandbox Protocol runtime). */
  kind?: "core" | "plugin" | "sandbox";
  name: string;
  state: string;
  message: string;
  pid?: number | null;
  version?: string | null;
  localUrl?: string | null;
  /** A newer version that waits for running agent turns to finish; the
   *  component keeps running meanwhile (live turns PRD). */
  pendingUpdate?: { version: string; waitingFor: number } | null;
}

/** A sidecar's sign-in state (optional sidecar contract GET /v1/auth/status).
 *  `supported` false = the sidecar has no sign-in concept: never offer it. */
export interface SidecarAuthStatus {
  supported: boolean;
  authenticated: boolean;
  user: string;
  checkedAtMs: number;
}

export type DesktopUpdateCheck = { available: boolean; version?: string; notes?: string; forced: boolean };
export type DesktopUpdateProgress = { downloaded: number; total?: number };

// args is a raw body (a Uint8Array) for commands that take bytes; options
// carry that call's headers.
type InvokeFn = (
  cmd: string,
  args?: Record<string, unknown> | Uint8Array,
  options?: { headers?: Record<string, string> },
) => Promise<unknown>;
type ListenFn = (
  event: string,
  handler: (e: { payload: unknown }) => void,
) => Promise<() => void>;

/** The shell's IPC (null outside the desktop shell). */
export function tauri(): { invoke: InvokeFn; listen: ListenFn } | null {
  if (!inDesktopShell()) return null;
  const w = window as unknown as {
    __TAURI__?: {
      core?: { invoke?: InvokeFn };
      event?: { listen?: ListenFn };
    };
  };
  const invoke = w.__TAURI__?.core?.invoke;
  const listen = w.__TAURI__?.event?.listen;
  if (!invoke || !listen) return null;
  return { invoke, listen };
}

// Reads the web session for shell IPC (sidecar adapter, bridge refresh
// token, updater). The session persists in localStorage (see auth.tsx).
function storedSession(): SessionRecord | null {
  if (typeof window === "undefined") return null;
  try {
    return parseSessionRecord(readStored(SESSION_KEY) || null);
  } catch {
    return null;
  }
}

function sessionToken(): string | null {
  return storedSession()?.token ?? null;
}

// popOutWindow asks the shell to open one of its auxiliary windows (a
// torn-out preview, the people chat). Every window of the app shares one
// origin and so one localStorage: the new window boots signed in. False
// when not in the shell or the shell refused (the caller keeps its in-app
// surface).
async function popOutWindow(command: string, args: Record<string, string>): Promise<boolean> {
  const t = tauri();
  if (!t) return false;
  try {
    await t.invoke(command, args);
    return true;
  } catch {
    return false;
  }
}

// skillsInvoke calls one of the shell's skills commands; a failure reads as
// its message.
async function skillsInvoke(command: string, args: Record<string, unknown>): Promise<unknown> {
  const t = tauri();
  if (!t) throw new Error("local skills are only available in the desktop app");
  return t.invoke(command, args).catch((e: unknown) => {
    throw new Error(e instanceof Error ? e.message : String(e));
  });
}

export const desktop = {
  available: (): boolean => tauri() !== null,

  // Tear the artifact out into its own OS window (desktop shell only).
  popOutPreview: (roomId: string, artifactId: string): Promise<boolean> =>
    popOutWindow("pop_out_preview", { roomId, artifactId }),

  // Open the room's people chat in its own OS window (desktop shell only).
  popOutPeopleChat: (roomId: string): Promise<boolean> => popOutWindow("pop_out_people_chat", { roomId }),
  // A running job's live view (the page an agent works on) in its own
  // window: #/live/<roomId>/<jobId>.
  popOutLive: (roomId: string, jobId: string): Promise<boolean> => popOutWindow("pop_out_live", { roomId, jobId }),

  async status(): Promise<BridgeStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("bridge_status")) as BridgeStatus;
  },

  // Start the bridge; a running one is restarted (the shell stops the
  // current run first).
  async start(): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("bridge_start");
  },

  async stop(): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("bridge_stop");
  },

  async getSettings(): Promise<DesktopSettings | null> {
    const t = tauri();
    if (!t) return null;
    // The shell returns the complete current settings shape (every field
    // defaulted on the Rust side).
    return (await t.invoke("get_settings")) as DesktopSettings;
  },

  async setSettings(settings: DesktopSettings): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("set_settings", { settings });
  },

  // Hand the current session to the shell: the sidecar adapter gets the
  // bearer, the bridge supervisor the refresh token (the bridge renews its own
  // session from it). Called on every token swap so the shell always holds the
  // newest refresh token. Signed out (no stored session) it sends empty
  // tokens, which tells the shell to stop handing a refresh token on.
  async refreshInternalAdapter(gatewayUrl: string): Promise<void> {
    const t = tauri();
    if (!t) return;
    const session = storedSession();
    await t
      .invoke("internal_adapter_session", {
        gatewayUrl,
        sessionToken: session?.token ?? "",
        refreshToken: session?.refreshToken ?? "",
      })
      .catch(() => {});
  },

  async internalAdapterStatus(): Promise<InternalAdapterStatus[]> {
    const t = tauri();
    if (!t) return [];
    return (await t.invoke("internal_adapter_status").catch(() => [])) as InternalAdapterStatus[];
  },

  // Apply component updates that wait for running agent turns now.
  async applyPendingUpdates(): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("internal_adapter_apply_updates");
  },

  // How many agent turns this machine's bridge drives now (0 outside the
  // shell or when unknown).
  async bridgeRunningTurns(): Promise<number> {
    const t = tauri();
    if (!t) return 0;
    return ((await t.invoke("bridge_running_turns").catch(() => 0)) as number) ?? 0;
  },

  // Rejects when the sidecar cannot list its agents: callers surface the
  // error and retry instead of mistaking a failure for "no agents".
  async internalAdapterTargets(name: string): Promise<{ targets: AgentTarget[] }> {
    const t = tauri();
    if (!t) return { targets: [] };
    return (await t.invoke("internal_adapter_targets", { name }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as { targets: AgentTarget[] };
  },

  async internalAdapterAuthStatus(name: string): Promise<SidecarAuthStatus> {
    const t = tauri();
    if (!t) return { supported: false, authenticated: false, user: "", checkedAtMs: 0 };
    return (await t.invoke("internal_adapter_auth_status", { name }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SidecarAuthStatus;
  },

  async internalAdapterLogin(name: string): Promise<boolean> {
    const t = tauri();
    if (!t) return false;
    await t.invoke("internal_adapter_login", { name });
    return true;
  },

  // The installed components with settings on this computer (Settings →
  // Components): every MCP tool and every plugin that declares settings,
  // with the member's own values (a secret's never), the platform's and the
  // organization's as set or not, and what keeps each in needs setup.
  // Nothing here reaches the gateway. Null outside the shell.
  async componentSettings(): Promise<DesktopComponentSettings[] | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("component_settings").catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as DesktopComponentSettings[];
  },

  // Store the member's own values of a component on this computer (an
  // empty secret keeps the stored one); a plugin restarts with them, an MCP
  // tool's next session reads them.
  async saveComponentSettings(kind: string, name: string, update: ComponentSettingsUpdate): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("component_settings_save", { kind, name, values: update.values, clear: update.clear ?? [] }).catch((e: unknown) => {
      // A refused value reads as the settings routes' refusal: the reason
      // and the setting it names (the shell answers {error, field}).
      if (e && typeof e === "object" && "error" in e && typeof e.error === "string") {
        throw "field" in e && typeof e.field === "string" ? new ApiError(400, e.error, e.field) : new Error(e.error);
      }
      throw new Error(e instanceof Error ? e.message : String(e));
    });
  },

  // Local skills (the skills panel a provider's card opens): the installed
  // plugins that provide skills and each one's `bnbloc.skills.provider/v1`
  // API, called through the shell on loopback. Nothing here reaches the
  // gateway.
  async skillsProviders(): Promise<SkillsProvider[]> {
    const t = tauri();
    if (!t) return [];
    return (await t.invoke("skills_providers")) as SkillsProvider[];
  },

  async skillsProviderState(plugin: string): Promise<SkillsState> {
    return (await skillsInvoke("skills_provider_state", { plugin })) as SkillsState;
  },

  async saveSkillsSettings(plugin: string, settings: SkillsSettings): Promise<SkillsState> {
    return (await skillsInvoke("skills_provider_settings", { plugin, settings })) as SkillsState;
  },

  // One skill packed by its provider: the tar.gz and the hash of the skill.
  async packSkill(plugin: string, name: string): Promise<{ hash: string; archive: Uint8Array }> {
    const raw = (await skillsInvoke("skills_provider_pack", { plugin, name })) as ArrayBuffer;
    return unframePackedSkill(new Uint8Array(raw));
  },

  // The folder picker for a skills source; null when cancelled.
  async pickSkillsFolder(): Promise<string | null> {
    return (await skillsInvoke("skills_pick_folder", {})) as string | null;
  },

  // Sign in to an MCP tool that takes an OAuth sign-in: its sign-in page
  // opens in the browser and this resolves once it is done (the tokens stay
  // on this computer).
  async signInMcpTool(tool: string): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("mcp_tool_sign_in", { tool }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    });
  },

  async signOutMcpTool(tool: string): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("mcp_tool_sign_out", { tool }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    });
  },

  // The local agent sandbox (Settings → Agent sandbox). Null outside the shell.
  async sandboxStatus(): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_status")) as SandboxStatus;
  },

  async setSandboxMode(mode: SandboxMode): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_mode_set", { mode }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SandboxStatus;
  },

  // The names passed into the sandboxed runs of the member's own agents
  // (refused whole when one is not a valid name; the error says which).
  async setSandboxEnv(names: string[]): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_env_set", { names }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SandboxStatus;
  },

  // The private hosts the member's own sandboxed runs may reach (refused
  // when the host is not one; the error says why). roomId: the room whose
  // agent reply proposed it.
  async addSandboxPrivateHost(host: string, roomId?: string): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_private_host_add", { host, roomId }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SandboxStatus;
  },

  async removeSandboxPrivateHost(host: string): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_private_host_remove", { host }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SandboxStatus;
  },

  // Recreate the member's own VM with their hosts (addresses resolved
  // again), when no run of theirs is going on there.
  async applySandboxPrivateHosts(): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_private_hosts_apply").catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SandboxStatus;
  },

  async restartSandboxVm(): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_restart_vm").catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SandboxStatus;
  },

  // Read the member's login-shell environment again and restart the bridge
  // and the sidecars with it (a newly exported key reaches the agents
  // without quitting the app).
  async reloadShellEnv(): Promise<ShellEnvReload | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("shell_env_reload").catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as ShellEnvReload;
  },

  async appInfo(): Promise<AppInfo | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("app_info")) as AppInfo;
  },

  async updatePolicy(): Promise<UpdatePolicy | null> {
    const t = tauri();
    if (!t) return null;
    return api.getUpdatePolicy();
  },

  async checkUpdate(): Promise<DesktopUpdateCheck | null> {
    const t = tauri();
    if (!t) return null;
    const policy = await api.getUpdatePolicy();
    if (!policy.enabled) return { available: false, forced: false };
    const token = sessionToken();
    if (!token) return null;
    return (await t.invoke("check_update", { sessionToken: token })) as DesktopUpdateCheck;
  },

  async installUpdate(): Promise<boolean> {
    const t = tauri();
    if (!t) return false;
    const policy = await api.getUpdatePolicy();
    if (!policy.enabled) return false;
    const token = sessionToken();
    if (!token) return false;
    return (await t.invoke("install_update", { sessionToken: token })) as boolean;
  },

  // Open an external URL in the OS default browser. Needed because the bundled
  // webview swallows <a target="_blank">. No-op outside the desktop shell (the
  // caller falls back to window.open there).
  async openUrl(url: string): Promise<boolean> {
    const t = tauri();
    if (!t) return false;
    await t.invoke("open_url", { url });
    return true;
  },

  // Subscribe to live status pushes from the supervisor. Returns an unlisten
  // function; a no-op unsubscribe in a browser.
  async onStatus(handler: (s: BridgeStatus) => void): Promise<() => void> {
    const t = tauri();
    if (!t) return () => {};
    return t.listen("bridge://status", (e) => handler(e.payload as BridgeStatus));
  },

  async onUpdateProgress(handler: (progress: DesktopUpdateProgress) => void): Promise<() => void> {
    const t = tauri();
    if (!t) return () => {};
    return t.listen("updater://progress", (e) => handler(e.payload as DesktopUpdateProgress));
  },
};

// openExternal opens a URL for the user regardless of environment: in the
// desktop shell it hands off to the OS browser via IPC (the webview swallows
// <a target="_blank">); in a plain browser it opens a new tab. Use this for
// every external link instead of a bare anchor so links work in the shell too.
export async function openExternal(url: string): Promise<void> {
  if (await desktop.openUrl(url)) return;
  window.open(url, "_blank", "noopener,noreferrer");
}

// ensureBridgeRunning gives the desktop shell a zero-config bridge: the bridge
// is what consumes room chat and drives the local agent, so without it a room
// message reaches no runner. On first launch the persisted settings are empty
// (no owner), so nothing auto-starts. Seed the two fields the bridge actually
// requires — the gateway URL and the owner (the signed-in subject) — persist
// them, and let set_settings auto-start (with no core the bridge runs idle
// until cores are installed or configured). Idempotent: once an owner is set
// we leave settings alone, and if the bridge is already live we do nothing.
// No-op outside the shell.
//
// The bridge also FOLLOWS the UI's gateway: the UI gateway (switcher/login)
// is the source of truth for which deployment's rooms the user is looking at,
// so a mismatch would leave every chat on that gateway "waiting for bridge"
// with the supervised bridge pinned to another deployment. On a mismatch we
// persist the UI's gateway (and tenant) and let set_settings restart the
// bridge against the same gateway.
//
// The bridge's OWNER follows the signed-in subject too: chat jobs are queued
// for the message author's identity (an SSO login is "user@corp", a dev login
// is the typed string), and the bridge only claims jobs whose owner matches
// its own — an owner pinned to a previous identity left every chat on that
// gateway dead ("no bridge online"). The owner is the member this client acts
// for; the signed-in subject is exactly that.
export async function ensureBridgeRunning(subject: string, gatewayUrl: string, tenant?: string | null): Promise<void> {
  if (!desktop.available() || !subject.trim()) return;
  await desktop.refreshInternalAdapter(gatewayUrl);
  const desiredGateway = (gatewayUrl || "").trim().replace(/\/+$/, "");
  const desiredOwner = subject.trim();
  const current = await desktop.getSettings();
  if (current && current.owner.trim()) {
    const desiredTenant = (tenant || "").trim();
    const gatewayDrifted =
      desiredGateway !== "" && current.gatewayUrl.trim().replace(/\/+$/, "") !== desiredGateway;
    // Keep the supervised bridge scoped to the same tenant/workspace as the UI.
    // A null tenant (the SSO callback path before the claims restore) must not
    // persist an empty tenant over the configured one — that blanked the
    // bridge's scope and made agents look broken after an SSO sign-in.
    const tenantDrifted = desiredTenant !== "" && (current.tenant || "") !== desiredTenant;
    const ownerDrifted = current.owner.trim() !== desiredOwner;
    if (gatewayDrifted || tenantDrifted || ownerDrifted) {
      await desktop.setSettings({
        ...current,
        ...(gatewayDrifted ? { gatewayUrl: desiredGateway } : {}),
        ...(tenantDrifted ? { tenant: desiredTenant } : {}),
        ...(ownerDrifted ? { owner: desiredOwner } : {}),
      });
      return;
    }
    // Already configured: start only if not already running.
    const st = await desktop.status();
    if (current.gatewayUrl.trim() && st && st.state !== "running" && st.state !== "starting") {
      await desktop.start().catch(() => {});
    }
    return;
  }
  const seeded: DesktopSettings = {
    ...(current ?? {
      cores: [],
      defaultAgent: { core: "", provider: "" },
      disabledProviders: [],
      syncSecret: "",
      serveAllOwners: false,
      gatewayToken: "",
      sandboxMode: "borrowed",
      sandboxEnv: [],
      sandboxPrivateHosts: [],
      gatewayUrl: "",
      owner: "",
      tenant: "",
    }),
    owner: subject.trim(),
    tenant: (tenant || "").trim(),
    gatewayUrl: (current?.gatewayUrl.trim() || desiredGateway).replace(/\/+$/, ""),
  };
  await desktop.setSettings(seeded); // set_settings auto-starts when runnable
}

// Desktop SSO hand-off nonce. The desktop opens SSO in the system browser with
// a fresh random nonce in the callback URL; the gateway binds the one-time
// code it returns (via the bnbloc://sso deep link) to that nonce. Only the app
// that started the flow holds the nonce, so a deep link crafted from someone
// else's login cannot sign this app in (login CSRF). Single use, short-lived.
const SSO_NONCE_KEY = StorageKey.ssoNonce;
const SSO_NONCE_TTL_MS = 10 * 60 * 1000;

export function beginDesktopSso(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  const nonce = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  writeStored(SSO_NONCE_KEY, JSON.stringify({ nonce, at: Date.now() }));
  return nonce;
}

export function takeDesktopSsoNonce(): string | null {
  const raw = readStored(SSO_NONCE_KEY) || null;
  writeStored(SSO_NONCE_KEY, "");
  if (!raw) return null;
  try {
    const { nonce, at } = JSON.parse(raw) as { nonce?: string; at?: number };
    if (!nonce || typeof at !== "number" || Date.now() - at > SSO_NONCE_TTL_MS) return null;
    return nonce;
  } catch {
    return null;
  }
}
