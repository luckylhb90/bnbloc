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
import type { SandboxStatus, ShellEnvReload } from "./sandboxStatus";
import { isNotBound, snapshotOf, type HeldAccount } from "./desktopAccounts";
import { StorageKey, readStored, writeStored } from "./storageKeys.ts";
import { readVault, readVaultRev } from "./vault";
import { boundAccount } from "./windowBinding";

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
  /** The account's own gateway, subject and tenant: read-only here (they are
   *  the account, not settings; the shell ignores them on save). */
  gatewayUrl: string;
  owner: string;
  tenant: string;
  cores: CoreConfig[];
  defaultAgent: DefaultAgent;
  disabledProviders: string[];
  /** The model the member chose as the default of an agent, by "core/provider".
   *  Absent or empty = the agent's own default. */
  defaultModels: Record<string, string>;
  syncSecret: string;
  /** Single-host mode: this one bridge drives every member's agent (local
   *  multi-user testing). Off in a real deployment. */
  serveAllOwners: boolean;
  /** Optional static bearer for gateway calls, for enterprise-IdP (remote)
   *  gateways that reject the bridge's dev login. Empty keeps the default
   *  owner-login path. Mirrors BNBLOC_GATEWAY_TOKEN. */
  gatewayToken: string;
}

/** One account's state in the shell (src-tauri/src/runtime.rs `AccountStatus`). */
export interface AccountStatus {
  gatewayUrl: string;
  subject: string;
  /** "active"; "retrying" (no session yet, or the gateway is down); "signedOut" (its credential was refused). */
  session: "active" | "retrying" | "signedOut";
  /** Whether it serves agents from this computer at all (a test account never does) and whether it does now. */
  canServe: boolean;
  serving: boolean;
  bridge: BridgeState;
  runningTurns: number;
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
  const raw = w.__TAURI__?.core?.invoke;
  const listen = w.__TAURI__?.event?.listen;
  if (!raw || !listen) return null;
  // A command that raced this window's binding to its account waits for the
  // binding and goes again (once); every other answer is the caller's.
  const invoke: InvokeFn = async (cmd, args, options) => {
    try {
      return await raw(cmd, args, options);
    } catch (e) {
      if (!isNotBound(e)) throw e;
      await Promise.race([binding, new Promise<void>((resolve) => setTimeout(resolve, BINDING_WAIT_MS))]);
      return raw(cmd, args, options);
    }
  };
  return { invoke, listen };
}

// The binding of this window to its account (syncAccounts): a command that
// found the window unbound waits for it, for a while, and goes again. Pending
// until the first sync has bound the window.
const BINDING_WAIT_MS = 10_000;
let bindingDone: () => void = () => {};
let binding: Promise<void> = new Promise((resolve) => {
  bindingDone = resolve;
});

// The bearer of the account this window shows (the updater's call as it).
function sessionToken(): string | null {
  if (typeof window === "undefined") return null;
  return boundAccount(readVault())?.record.token ?? null;
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

// pluginInvoke calls one of the shell's plugin commands (skills, panels); a failure reads as
// its message.
async function pluginInvoke(command: string, args: Record<string, unknown>): Promise<unknown> {
  const t = tauri();
  if (!t) throw new Error("this is only available in the desktop app");
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

  // Hand the shell the accounts this app holds and bind this window to its
  // own. The shell reconciles its account runtimes to the list (an account
  // missing from it stops serving and its sign-in on the gateway ends) and
  // renews each from its own session family, never from the web's token
  // again; the window's commands are then that account's. Called on every
  // change of what the web holds. Signed out (an empty list) it stops them
  // all.
  async syncAccounts(held: readonly HeldAccount[], window: HeldAccount | null, revision: number): Promise<void> {
    const t = tauri();
    if (!t) return;
    const accounts = snapshotOf(held);
    const mine = window ? snapshotOf([window])[0] : undefined;
    try {
      await t.invoke("accounts_sync", { revision, accounts });
      if (mine) await t.invoke("window_bind", { gatewayUrl: mine.gatewayUrl, subject: mine.subject });
    } catch {
      /* the next change of the vault hands it again */
    }
    bindingDone();
  },

  // Open a shell window bound to an account (gateway and subject), or, with
  // none, one to sign in another account. The shell binds the window to the
  // account before its page loads; an account that is already shown in a window
  // is focused instead. False when the shell refused.
  async openAccountWindow(gatewayUrl: string | null, subject: string | null): Promise<boolean> {
    const t = tauri();
    if (!t) return false;
    try {
      await t.invoke("open_account_window", { gatewayUrl, subject });
      return true;
    } catch {
      return false;
    }
  },

  // Forget what this computer keeps of an account that left the vault: its
  // settings, its member state (component values, sign-ins to tools, sandbox
  // disks). The shell refuses an account that is still signed in here.
  async forgetAccountData(gatewayUrl: string, subject: string): Promise<void> {
    const t = tauri();
    if (!t) return;
    await t.invoke("account_forget_data", { gatewayUrl, subject }).catch((e: unknown) => {
      throw new Error(e instanceof Error ? e.message : String(e));
    });
  },

  // Bring forward the app window that already shows an account, when it is
  // not this one: the app keeps one window per account. True when there is
  // one. Always false outside the shell.
  async focusAccountElsewhere(gatewayUrl: string, subject: string): Promise<boolean> {
    const t = tauri();
    if (!t) return false;
    return ((await t.invoke("account_focus_elsewhere", { gatewayUrl, subject }).catch(() => false)) as boolean) === true;
  },

  // Close this window (a sign-in that found its account open elsewhere).
  async closeThisWindow(): Promise<void> {
    await tauri()?.invoke("window_close").catch(() => {});
  },

  // Every account's state beside its name (the switcher): session, whether
  // it serves agents from this computer, its bridge and running turns.
  async accountsStatus(): Promise<AccountStatus[]> {
    const t = tauri();
    if (!t) return [];
    return (await t.invoke("accounts_status").catch(() => [])) as AccountStatus[];
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
    return (await pluginInvoke("skills_provider_state", { plugin })) as SkillsState;
  },

  async saveSkillsSettings(plugin: string, settings: SkillsSettings): Promise<SkillsState> {
    return (await pluginInvoke("skills_provider_settings", { plugin, settings })) as SkillsState;
  },

  // One call of a plugin's panel, made by the desktop with the bearer it keeps
  // (docs/architecture/plugin-panels.md): the plugin's status and JSON answer.
  async panelCall(plugin: string, method: string, path: string, body?: unknown): Promise<{ status: number; body: unknown }> {
    return (await pluginInvoke("plugin_panel_call", { plugin, method, path, body: body ?? null })) as { status: number; body: unknown };
  },

  // One skill packed by its provider: the tar.gz and the hash of the skill.
  async packSkill(plugin: string, name: string): Promise<{ hash: string; archive: Uint8Array }> {
    const raw = (await pluginInvoke("skills_provider_pack", { plugin, name })) as ArrayBuffer;
    return unframePackedSkill(new Uint8Array(raw));
  },

  // The folder picker for a skills source; null when cancelled.
  async pickSkillsFolder(): Promise<string | null> {
    return (await pluginInvoke("skills_pick_folder", {})) as string | null;
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

  // The agent sandbox on this computer (its Components settings dialog;
  // what it does is its component settings). Null outside the shell.
  async sandboxStatus(): Promise<SandboxStatus | null> {
    const t = tauri();
    if (!t) return null;
    return (await t.invoke("sandbox_status")) as SandboxStatus;
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

// syncDesktopAccounts hands the shell every account of the vault that can
// still sign in, and binds this window to the one it shows
// (desktop.syncAccounts).
export async function syncDesktopAccounts(): Promise<void> {
  if (!desktop.available()) return;
  const vault = readVault();
  const held = vault.filter((a) => !a.signedOut).map((a) => ({ gateway: a.gateway, record: a.record }));
  const mine = boundAccount(vault);
  await desktop.syncAccounts(held, mine && !mine.signedOut ? { gateway: mine.gateway, record: mine.record } : null, readVaultRev());
}

// openExternal opens a URL for the user regardless of environment: in the
// desktop shell it hands off to the OS browser via IPC (the webview swallows
// <a target="_blank">); in a plain browser it opens a new tab. Use this for
// every external link instead of a bare anchor so links work in the shell too.
export async function openExternal(url: string): Promise<void> {
  if (await desktop.openUrl(url)) return;
  window.open(url, "_blank", "noopener,noreferrer");
}

// Desktop SSO hand-off nonce. The desktop opens SSO in the system browser with
// a fresh random nonce in the callback URL; the gateway binds the one-time
// code it returns (via the bnbloc://sso deep link) to that nonce. Only the app
// window that started the flow holds the nonce, so a deep link crafted from
// someone else's login cannot sign this app in (login CSRF), and a window
// never takes the code of a sign-in another window started (the shell gives
// the link to every app window; only the one with a nonce in its own
// sessionStorage acts). A copy in localStorage is for a link that arrives
// after the app was restarted. Single use, short-lived.
const SSO_NONCE_KEY = StorageKey.ssoNonce;
const SSO_NONCE_TTL_MS = 10 * 60 * 1000;

export function beginDesktopSso(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  const nonce = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  const stored = JSON.stringify({ nonce, at: Date.now() });
  writeStored(SSO_NONCE_KEY, stored, "session");
  writeStored(SSO_NONCE_KEY, stored);
  return nonce;
}

export function takeDesktopSsoNonce(): string | null {
  const raw = readStored(SSO_NONCE_KEY, "session") || readStored(SSO_NONCE_KEY) || null;
  writeStored(SSO_NONCE_KEY, "", "session");
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
