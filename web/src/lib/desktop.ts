// Desktop shell bridge (Tauri IPC), feature-detected so the same bundle runs
// unchanged in a plain browser. When not in the shell, every call is a no-op /
// unavailable and the UI hides shell-only surfaces (see inDesktopShell()).
//
// We reach Tauri through its injected globals rather than the @tauri-apps/api
// npm package, so the web package needs no Tauri build dependency and the
// browser build stays dependency-free.

import { inDesktopShell } from "./config";
import { api, type AgentTarget, type UpdatePolicy } from "./api";
import type { McpToolSettings } from "./mcpTools";
import type { PluginLocalSettings } from "./pluginSettings";
import type { SandboxMode, SandboxStatus } from "./sandboxStatus";
import { SESSION_KEY, parseSessionRecord, type SessionRecord } from "./sessionGrant";

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

type InvokeFn = (cmd: string, args?: Record<string, unknown>) => Promise<unknown>;
type ListenFn = (
  event: string,
  handler: (e: { payload: unknown }) => void,
) => Promise<() => void>;

function tauri(): { invoke: InvokeFn; listen: ListenFn } | null {
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
    return parseSessionRecord(window.localStorage.getItem(SESSION_KEY));
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

export const desktop = {
  available: (): boolean => tauri() !== null,

  // Tear the artifact out into its own OS window (desktop shell only).
  popOutPreview: (roomId: string, artifactId: string): Promise<boolean> =>
    popOutWindow("pop_out_preview", { roomId, artifactId }),

  // Open the room's people chat in its own OS window (desktop shell only).
  popOutPeopleChat: (roomId: string): Promise<boolean> => popOutWindow("pop_out_people_chat", { roomId }),

  async status(): Promise<BridgeStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("bridge_status")) as BridgeStatus;
  },

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

  async restart(): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("bridge_restart");
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

  // Installed plugins with desktop-local settings (e.g. a personal bot).
  // Secret values never come back; nothing here reaches the gateway.
  async pluginLocalSettings(): Promise<PluginLocalSettings[]> {
    const t = tauri();
    if (!t) return [];
    return (await t.invoke("plugin_local_settings").catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as PluginLocalSettings[];
  },

  // Store a plugin's settings on this machine (an empty secret keeps the
  // stored one); the shell restarts the plugin with them.
  async savePluginLocalSettings(plugin: string, values: Record<string, string>): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("plugin_local_settings_save", { plugin, values }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    });
  },

  async clearPluginLocalSettings(plugin: string): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("plugin_local_settings_clear", { plugin }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    });
  },

  // Installed MCP tools with this computer's state (Settings → Tools): the
  // stdio binary's download state and the member's own credentials. Secret
  // values never come back; nothing here reaches the gateway. Null outside
  // the shell.
  async mcpToolSettings(): Promise<McpToolSettings[] | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("mcp_tool_settings").catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as McpToolSettings[];
  },

  // Store the member's credentials for an MCP tool on this computer (an
  // empty secret keeps the stored one); the bridge reads them per session.
  async saveMcpToolSettings(tool: string, values: Record<string, string>): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("mcp_tool_settings_save", { tool, values }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    });
  },

  async clearMcpToolSettings(tool: string): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("mcp_tool_settings_clear", { tool }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    });
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

  async restartSandboxVm(): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_restart_vm").catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    })) as SandboxStatus;
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
const SSO_NONCE_KEY = "bnbloc.ssoNonce";
const SSO_NONCE_TTL_MS = 10 * 60 * 1000;

export function beginDesktopSso(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  const nonce = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  localStorage.setItem(SSO_NONCE_KEY, JSON.stringify({ nonce, at: Date.now() }));
  return nonce;
}

export function takeDesktopSsoNonce(): string | null {
  const raw = localStorage.getItem(SSO_NONCE_KEY);
  localStorage.removeItem(SSO_NONCE_KEY);
  if (!raw) return null;
  try {
    const { nonce, at } = JSON.parse(raw) as { nonce?: string; at?: number };
    if (!nonce || typeof at !== "number" || Date.now() - at > SSO_NONCE_TTL_MS) return null;
    return nonce;
  } catch {
    return null;
  }
}
