import { useCallback, useSyncExternalStore } from "react";
import { inDesktopShell } from "@/lib/config";
import { desktop } from "@/lib/desktop";
import {
  EMPTY_SNAPSHOT,
  authPhase,
  authDue,
  entryKey,
  isCore,
  isSettled,
  mergeEntry,
  pollDelayMs,
  runKey,
  shouldAutoSignIn,
  targetsDue,
  type SidecarEntry,
  type SidecarSnapshot,
} from "@/lib/sidecarState";
import { errorReason } from "./errors.ts";

// One app-wide poller for the desktop's managed sidecars, shared by every view
// that lists local agents (Agents page, composer agent picker, run dialog) so
// they all see the same freshness and never run parallel polls. It runs only
// while something is subscribed, and only in the desktop shell.
//
// Each tick reads the shell's sidecar statuses (cheap, in-memory), then — per
// the pure rules in sidecarState — re-lists a core's agents when it (re)starts
// or its last listing failed/was empty, and re-checks its sign-in state. A
// core that reports signed out gets its sign-in started once per app session.

let snapshot: SidecarSnapshot = EMPTY_SNAPSHOT;
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setTimeout> | undefined;
let ticking = false;
let unsettledStreak = 0;
const inflightTargets = new Set<string>();
const inflightAuth = new Set<string>();

const SIGN_IN_KEY = "bnbloc.sidecarSignInStarted";

// Auto sign-in happens at most once per app session per sidecar, surviving a
// webview reload (sessionStorage) but not an app restart.
function signInStarted(name: string): boolean {
  try {
    return (JSON.parse(sessionStorage.getItem(SIGN_IN_KEY) || "[]") as string[]).includes(name);
  } catch {
    return false;
  }
}

function markSignInStarted(name: string): void {
  try {
    const started = JSON.parse(sessionStorage.getItem(SIGN_IN_KEY) || "[]") as string[];
    if (!started.includes(name)) sessionStorage.setItem(SIGN_IN_KEY, JSON.stringify([...started, name]));
  } catch {
    // sessionStorage unavailable: the in-memory signIn state still prevents repeats.
  }
}

function emit(next: SidecarSnapshot): void {
  snapshot = next;
  for (const l of listeners) l();
}

function update(key: string, fn: (e: SidecarEntry) => SidecarEntry): void {
  emit({ ...snapshot, entries: snapshot.entries.map((e) => (entryKey(e.status) === key ? fn(e) : e)) });
}

function message(e: unknown): string {
  return errorReason(e) || "unknown error";
}

async function loadTargets(e: SidecarEntry): Promise<void> {
  const key = entryKey(e.status);
  if (inflightTargets.has(key)) return;
  inflightTargets.add(key);
  const rk = runKey(e.status);
  try {
    const res = await desktop.internalAdapterTargets(e.status.name);
    update(key, (cur) =>
      runKey(cur.status) !== rk
        ? cur
        : { ...cur, targets: { targets: res.targets || [], loaded: true, error: "", fetchedAtMs: Date.now(), runKey: rk } },
    );
  } catch (err) {
    // Keep what this run already listed; show the error and retry.
    update(key, (cur) =>
      runKey(cur.status) !== rk
        ? cur
        : {
            ...cur,
            targets: {
              ...cur.targets,
              targets: cur.targets.runKey === rk ? cur.targets.targets : [],
              error: message(err),
              fetchedAtMs: Date.now(),
              runKey: rk,
            },
          },
    );
  } finally {
    inflightTargets.delete(key);
  }
}

async function loadAuth(e: SidecarEntry): Promise<void> {
  const key = entryKey(e.status);
  if (inflightAuth.has(key)) return;
  inflightAuth.add(key);
  const rk = runKey(e.status);
  try {
    const auth = await desktop.internalAdapterAuthStatus(e.status.name);
    const before = snapshot.entries.find((x) => entryKey(x.status) === key);
    const signedIn = authPhase(auth) === "signedIn";
    const wasPending = !!before && ["checking", "signedOut"].includes(authPhase(before.auth));
    const signedInNow = signedIn && wasPending;
    update(key, (cur) =>
      runKey(cur.status) !== rk
        ? cur
        : {
            ...cur,
            auth,
            authCheckedAtMs: Date.now(),
            signIn: signedIn ? "idle" : cur.signIn,
            signInError: signedIn ? "" : cur.signInError,
          },
    );
    const cur = snapshot.entries.find((x) => entryKey(x.status) === key);
    if (!cur || runKey(cur.status) !== rk) return;
    // Just signed in: the agent list may have changed — re-list now.
    if (signedInNow) void loadTargets(cur);
    if (shouldAutoSignIn(cur, signInStarted(cur.status.name))) void signIn(cur.status.name);
  } catch {
    update(key, (cur) => (runKey(cur.status) !== rk ? cur : { ...cur, authCheckedAtMs: Date.now() }));
  } finally {
    inflightAuth.delete(key);
  }
}

/** Open the sidecar's sign-in (its browser flow); auth is then re-checked
 *  every few seconds until it reports signed in. */
async function signIn(name: string): Promise<void> {
  const entry = snapshot.entries.find((e) => e.status.name === name && isCore(e.status));
  if (!entry) return;
  const k = entryKey(entry.status);
  markSignInStarted(name);
  update(k, (cur) => ({ ...cur, signIn: "opening", signInError: "" }));
  try {
    await desktop.internalAdapterLogin(name);
    update(k, (cur) => ({ ...cur, signIn: "waiting", authCheckedAtMs: 0 }));
  } catch (err) {
    update(k, (cur) => ({ ...cur, signIn: "error", signInError: message(err) }));
  }
  schedule(0);
}

async function tick(): Promise<void> {
  if (ticking) return;
  ticking = true;
  try {
    const statuses = await desktop.internalAdapterStatus();
    const prev = new Map(snapshot.entries.map((e) => [entryKey(e.status), e]));
    emit({ loaded: true, entries: statuses.map((s) => mergeEntry(prev.get(entryKey(s)), s)) });
    const now = Date.now();
    for (const e of snapshot.entries) {
      if (targetsDue(e, now)) void loadTargets(e);
      if (authDue(e, now)) void loadAuth(e);
    }
  } finally {
    ticking = false;
    unsettledStreak = snapshot.entries.every(isSettled) ? 0 : unsettledStreak + 1;
    schedule(pollDelayMs(snapshot.entries, unsettledStreak));
  }
}

function schedule(delay: number): void {
  if (timer !== undefined) clearTimeout(timer);
  timer = undefined;
  if (listeners.size === 0) return;
  timer = setTimeout(() => void tick(), delay);
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  if (listeners.size === 1) schedule(0);
  return () => {
    listeners.delete(l);
    if (listeners.size === 0) schedule(0); // clears the timer
  };
}

/** Force an immediate re-read of statuses, targets and sign-in state. */
function refresh(): void {
  emit({
    ...snapshot,
    entries: snapshot.entries.map((e) => ({
      ...e,
      targets: { ...e.targets, fetchedAtMs: 0 },
      authCheckedAtMs: 0,
    })),
  });
  unsettledStreak = 0;
  if (listeners.size > 0) schedule(0);
}

export const sidecarAgents = { refresh, signIn };

const noopSubscribe = () => () => {};
const emptySnapshot = () => EMPTY_SNAPSHOT;
const currentSnapshot = () => snapshot;

/** The managed sidecars' live state. `enabled` false (or outside the desktop
 *  shell) returns the empty snapshot and does not poll. */
export function useSidecarAgents(enabled = true): SidecarSnapshot {
  const on = enabled && inDesktopShell();
  const sub = useCallback((l: () => void) => (on ? subscribe(l) : noopSubscribe()), [on]);
  return useSyncExternalStore(sub, on ? currentSnapshot : emptySnapshot);
}
