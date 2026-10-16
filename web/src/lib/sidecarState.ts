// Pure state + scheduling rules for the desktop's managed sidecars (the cores
// the desktop app supervises locally). The live store (useSidecarAgents)
// polls the shell and applies these rules; keeping them pure makes the
// "when do we re-scan / sign in / call it ready" decisions testable.
//
// Product-neutral: nothing here knows any sidecar by name. Sign-in uses the
// OPTIONAL sidecar contract GET /v1/auth/status; a sidecar without it reports
// supported=false and is never prompted.
import type { AgentTarget } from "./apiModels.ts";
import type { InternalAdapterStatus, SidecarAuthStatus } from "./desktop.ts";

/** Status poll while something is still settling (starting, loading, signing in). */
export const STATUS_FAST_MS = 2000;
/** Status poll once a long settle keeps failing (e.g. a core waiting on a stale listener). */
export const STATUS_BACKOFF_MS = 10_000;
/** Fast ticks before backing off to STATUS_BACKOFF_MS. */
export const FAST_TICKS = 30;
/** Status poll, and targets/auth refresh, once everything is settled. */
export const SETTLED_MS = 30_000;
/** Retry spacing for a target listing that failed or came back empty. */
export const TARGETS_RETRY_MS = 5000;
/** Auth re-check spacing while checking or waiting for a sign-in to finish. */
export const AUTH_PENDING_MS = 3000;

export interface SidecarTargets {
  readonly targets: readonly AgentTarget[];
  /** A listing succeeded at least once for this run. */
  readonly loaded: boolean;
  /** The last listing's error ("" when it succeeded). */
  readonly error: string;
  readonly fetchedAtMs: number;
  /** The sidecar run these targets belong to (see runKey). */
  readonly runKey: string;
}

export type SignInState = "idle" | "opening" | "waiting" | "error";

export interface SidecarEntry {
  readonly status: InternalAdapterStatus;
  readonly targets: SidecarTargets;
  /** null = not checked yet for this run. */
  readonly auth: SidecarAuthStatus | null;
  readonly authCheckedAtMs: number;
  readonly signIn: SignInState;
  readonly signInError: string;
}

export interface SidecarSnapshot {
  /** The first status read finished. */
  readonly loaded: boolean;
  readonly entries: readonly SidecarEntry[];
}

export const EMPTY_SNAPSHOT: SidecarSnapshot = { loaded: false, entries: [] };

export function entryKey(s: InternalAdapterStatus): string {
  return `${s.kind ?? "core"}:${s.name}`;
}

export function isCore(s: InternalAdapterStatus): boolean {
  return (s.kind ?? "core") === "core";
}

/** Identity of one running instance: a restart (new pid / url) is a new run
 *  whose targets and sign-in state must be re-read. "" when not running. */
export function runKey(s: InternalAdapterStatus): string {
  return s.state === "running" ? `${s.pid ?? "-"}@${s.localUrl ?? ""}` : "";
}

/** Fold a fresh status into the previous entry: same run keeps what was
 *  learned; a new run (or a stop) starts over. */
export function mergeEntry(prev: SidecarEntry | undefined, status: InternalAdapterStatus): SidecarEntry {
  if (prev && runKey(prev.status) === runKey(status)) return { ...prev, status };
  return {
    status,
    targets: { targets: [], loaded: false, error: "", fetchedAtMs: 0, runKey: "" },
    auth: null,
    authCheckedAtMs: 0,
    signIn: prev?.signIn === "waiting" || prev?.signIn === "opening" ? prev.signIn : "idle",
    signInError: "",
  };
}

/** The sidecar's sign-in phase. "checking" covers both "not asked yet" and
 *  the sidecar's own first check still running (checkedAtMs 0). */
export function authPhase(auth: SidecarAuthStatus | null): "unknown" | "unsupported" | "checking" | "signedIn" | "signedOut" {
  if (!auth) return "unknown";
  if (!auth.supported) return "unsupported";
  if (auth.authenticated) return "signedIn";
  return auth.checkedAtMs > 0 ? "signedOut" : "checking";
}

function running(e: SidecarEntry): boolean {
  return isCore(e.status) && e.status.state === "running";
}

/** A running core's target listing is due now. */
export function targetsDue(e: SidecarEntry, now: number): boolean {
  if (!running(e)) return false;
  const t = e.targets;
  if (t.runKey !== runKey(e.status)) return true; // new run (e.g. starting → running)
  const unsettled = !t.loaded || t.error !== "" || t.targets.length === 0;
  return now - t.fetchedAtMs >= (unsettled ? TARGETS_RETRY_MS : SETTLED_MS);
}

/** A running core's sign-in state check is due now. */
export function authDue(e: SidecarEntry, now: number): boolean {
  if (!running(e)) return false;
  const phase = authPhase(e.auth);
  if (phase === "unsupported") return false; // no sign-in concept for this run
  if (e.authCheckedAtMs === 0) return true; // never asked (or a sign-in just started)
  const pending = phase === "unknown" || phase === "checking" || phase === "signedOut";
  return now - e.authCheckedAtMs >= (pending ? AUTH_PENDING_MS : SETTLED_MS);
}

/** Nothing about this sidecar is expected to change without outside action. */
export function isSettled(e: SidecarEntry): boolean {
  if (!isCore(e.status)) return e.status.state === "running";
  if (e.status.state !== "running") return false;
  const phase = authPhase(e.auth);
  if (phase === "unknown" || phase === "checking" || phase === "signedOut") return false;
  return e.targets.loaded && e.targets.error === "" && e.targets.targets.length > 0;
}

/** Next status poll: fast while anything settles (backing off after a long
 *  unsettled streak), slow once everything is settled. */
export function pollDelayMs(entries: readonly SidecarEntry[], unsettledStreak: number): number {
  if (entries.every(isSettled)) return SETTLED_MS;
  return unsettledStreak < FAST_TICKS ? STATUS_FAST_MS : STATUS_BACKOFF_MS;
}

/** Start the sidecar's sign-in on our own: only when it positively reports
 *  signed out, and at most once per sign-out episode (a core seen signed in
 *  ends the episode). */
export function shouldAutoSignIn(e: SidecarEntry, alreadyStarted: boolean): boolean {
  return !alreadyStarted && running(e) && authPhase(e.auth) === "signedOut" && e.signIn === "idle";
}

/** The cores waiting on the member to sign in, in the order listed: running,
 *  and positively signed out. */
export function signedOutCores(entries: readonly SidecarEntry[]): SidecarEntry[] {
  return entries.filter((e) => canSignIn(e));
}

/** Offer a manual "Sign in" button. */
export function canSignIn(e: SidecarEntry): boolean {
  return running(e) && authPhase(e.auth) === "signedOut";
}

/** Every agent target the running cores listed (core-qualified ids). */
export function sidecarTargets(entries: readonly SidecarEntry[]): AgentTarget[] {
  return entries.filter(running).flatMap((e) => [...e.targets.targets]);
}

/** Providers per core name, from the listed targets. */
export function sidecarProviders(entries: readonly SidecarEntry[]): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const e of entries) {
    if (!running(e)) continue;
    for (const t of e.targets.targets) {
      const list = (out[e.status.name] ||= []);
      if (!list.includes(t.provider)) list.push(t.provider);
    }
  }
  return out;
}

/** Some core is still on its way to listing agents: a saved selection that is
 *  not listed yet must not be cleared. Failed/stopped cores are not pending. */
export function sidecarsPending(entries: readonly SidecarEntry[]): boolean {
  return entries.some((e) => {
    if (!isCore(e.status)) return false;
    if (e.status.state !== "running") return e.status.state !== "failed" && e.status.state !== "stopped";
    return !e.targets.loaded;
  });
}

export type CoreCardStatus = "ready" | "starting" | "offline";

/** How the Agents page shows a desktop-managed core. */
export interface SidecarCoreView {
  readonly status: CoreCardStatus;
  readonly label: string;
  readonly detail: string;
  /** A quiet inline error (target listing failed; retrying). */
  readonly error: string;
}

export function sidecarCoreView(e: SidecarEntry | undefined): SidecarCoreView | null {
  if (!e) return null;
  const { status, targets } = e;
  const error = targets.error ? `Couldn’t list agents: ${targets.error} — retrying.` : "";
  if (status.state !== "running") {
    return { status: "offline", label: status.state || "offline", detail: status.message, error: "" };
  }
  if (targets.targets.length > 0) return { status: "ready", label: "✓ ready", detail: "", error };
  const phase = authPhase(e.auth);
  if (phase === "signedOut") {
    const detail =
      e.signIn === "waiting" || e.signIn === "opening"
        ? `Finish signing in to ${status.name} in your browser; this updates on its own.`
        : `Sign in to ${status.name} to list its agents.`;
    return { status: "starting", label: "sign-in required", detail, error };
  }
  if (!targets.loaded) {
    return { status: "starting", label: "starting…", detail: `${status.name} is running; loading its agents…`, error };
  }
  return { status: "starting", label: "no agents", detail: `${status.name} is running but reports no agents yet.`, error };
}
