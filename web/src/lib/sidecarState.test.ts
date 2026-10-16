import type { AgentTarget } from "./apiModels.ts";
import type { InternalAdapterStatus, SidecarAuthStatus } from "./desktop.ts";
import {
  AUTH_PENDING_MS,
  SETTLED_MS,
  STATUS_BACKOFF_MS,
  STATUS_FAST_MS,
  TARGETS_RETRY_MS,
  FAST_TICKS,
  authDue,
  authPhase,
  canSignIn,
  isSettled,
  mergeEntry,
  pollDelayMs,
  runKey,
  shouldAutoSignIn,
  signedOutCores,
  sidecarCoreView,
  sidecarProviders,
  sidecarTargets,
  sidecarsPending,
  targetsDue,
  type SidecarEntry,
} from "./sidecarState.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function status(patch: Partial<InternalAdapterStatus> = {}): InternalAdapterStatus {
  return { kind: "core", name: "samplecore", state: "running", message: "", pid: 11, localUrl: "http://127.0.0.1:7100", ...patch };
}

const TARGET: AgentTarget = { id: "samplecore/codex", provider: "codex", core: "samplecore" };
const SIGNED_IN: SidecarAuthStatus = { supported: true, authenticated: true, user: "alice", checkedAtMs: 5 };
const SIGNED_OUT: SidecarAuthStatus = { supported: true, authenticated: false, user: "", checkedAtMs: 5 };
const CHECKING: SidecarAuthStatus = { supported: true, authenticated: false, user: "", checkedAtMs: 0 };
const NO_AUTH: SidecarAuthStatus = { supported: false, authenticated: false, user: "", checkedAtMs: 0 };

function listed(e: SidecarEntry, targets: AgentTarget[], at = 1000, error = ""): SidecarEntry {
  return { ...e, targets: { targets, loaded: error === "" || e.targets.loaded, error, fetchedAtMs: at, runKey: runKey(e.status) } };
}

function testStartingToRunningTransitionRelistsTargets(): void {
  const starting = mergeEntry(undefined, status({ state: "starting", pid: null }));
  assert(!targetsDue(starting, 0), "a sidecar that is not running is never asked for targets");
  const running = mergeEntry(starting, status());
  assert(targetsDue(running, 0), "starting → running re-lists targets immediately");
  const ready = listed(running, [TARGET]);
  assert(!targetsDue(ready, 1000 + TARGETS_RETRY_MS), "a settled listing is not re-fetched on the fast cadence");
  assert(targetsDue(ready, 1000 + SETTLED_MS), "a settled listing refreshes on the slow cadence");
  const restarted = mergeEntry(ready, status({ pid: 12 }));
  assert(restarted.targets.targets.length === 0 && targetsDue(restarted, 1001), "a restart (new pid) forgets the old run and re-lists");
  assert(mergeEntry(ready, status()).targets.targets.length === 1, "the same run keeps its targets across status polls");
}

function testFailedOrEmptyListingsRetryOnTheFastCadence(): void {
  const running = mergeEntry(undefined, status());
  const failed = listed(running, [], 1000, "list targets samplecore: status 500");
  assert(!failed.targets.loaded && failed.targets.error !== "", "an error is recorded, not mistaken for an empty list");
  assert(!targetsDue(failed, 1000 + TARGETS_RETRY_MS - 1), "a failed listing waits for the retry spacing");
  assert(targetsDue(failed, 1000 + TARGETS_RETRY_MS), "a failed listing is retried");
  const empty = listed(running, []);
  assert(targetsDue(empty, 1000 + TARGETS_RETRY_MS), "an empty listing is retried on the fast cadence");
}

function testPollDelayIsFastWhileSettlingAndSlowOnceStable(): void {
  const starting = mergeEntry(undefined, status({ state: "downloading", pid: null }));
  assert(pollDelayMs([starting], 0) === STATUS_FAST_MS, "a sidecar that is not running polls fast");
  assert(pollDelayMs([starting], FAST_TICKS) === STATUS_BACKOFF_MS, "a long unsettled streak backs off");
  const loading = mergeEntry(undefined, status());
  assert(pollDelayMs([loading], 0) === STATUS_FAST_MS, "a running sidecar with no targets yet polls fast");
  const stable = { ...listed(loading, [TARGET]), auth: NO_AUTH };
  assert(isSettled(stable) && pollDelayMs([stable], 0) === SETTLED_MS, "a running sidecar with targets settles");
  assert(pollDelayMs([], 0) === SETTLED_MS, "no sidecars: slow poll");
  const plugin = mergeEntry(undefined, status({ kind: "plugin", name: "audit" }));
  assert(isSettled(plugin), "a running plugin is settled without targets");
  assert(!isSettled({ ...stable, auth: SIGNED_OUT }), "a signed-out sidecar is not settled");
  assert(!isSettled({ ...stable, auth: CHECKING }), "a sidecar still checking sign-in is not settled");
}

function testAuthPhasesAndSignInGate(): void {
  assert(authPhase(null) === "unknown", "unchecked");
  assert(authPhase(NO_AUTH) === "unsupported", "404 → no sign-in concept");
  assert(authPhase(CHECKING) === "checking", "checkedAtMs 0 → the sidecar's first check is still running");
  assert(authPhase(SIGNED_OUT) === "signedOut" && authPhase(SIGNED_IN) === "signedIn", "settled phases");

  const base = listed(mergeEntry(undefined, status()), [TARGET]);
  assert(!canSignIn({ ...base, auth: NO_AUTH }), "no sign-in button without the auth endpoint");
  assert(!canSignIn({ ...base, auth: CHECKING }), "no sign-in button while the sidecar is still checking");
  assert(!canSignIn({ ...base, auth: SIGNED_IN }), "no sign-in button when signed in");
  assert(canSignIn({ ...base, auth: SIGNED_OUT }), "sign-in button only when signed out");

  assert(shouldAutoSignIn({ ...base, auth: SIGNED_OUT }, false), "signed out → start sign-in once");
  assert(!shouldAutoSignIn({ ...base, auth: SIGNED_OUT }, true), "never auto-open twice in one sign-out episode");
  assert(signedOutCores([{ ...base, auth: SIGNED_OUT }]).length === 1, "a running core that is signed out waits on the member");
  assert(signedOutCores([base]).length === 0, "a core not known to be signed out does not");
  assert(!shouldAutoSignIn({ ...base, auth: CHECKING }, false), "never auto-open while checking");
  assert(!shouldAutoSignIn({ ...base, auth: SIGNED_OUT, signIn: "waiting" }, false), "never while a sign-in is in progress");
}

function testAuthChecksAreScheduledByPhase(): void {
  const base = { ...listed(mergeEntry(undefined, status()), [TARGET]), authCheckedAtMs: 1000 };
  assert(authDue({ ...base, auth: null, authCheckedAtMs: 0 }, 1), "an unchecked sidecar is asked right away");
  assert(!authDue({ ...base, auth: NO_AUTH }, 1000 + SETTLED_MS * 10), "a sidecar without the endpoint is never re-asked in this run");
  assert(authDue({ ...base, auth: SIGNED_OUT }, 1000 + AUTH_PENDING_MS), "signed out: re-check every few seconds until it flips");
  assert(authDue({ ...base, auth: CHECKING }, 1000 + AUTH_PENDING_MS), "checking: retry shortly");
  assert(!authDue({ ...base, auth: SIGNED_IN }, 1000 + AUTH_PENDING_MS), "signed in: slow refresh only");
  assert(authDue({ ...base, auth: SIGNED_IN }, 1000 + SETTLED_MS), "signed in: slow refresh");
  assert(!authDue(mergeEntry(undefined, status({ state: "waiting", pid: null })), 1e9), "not running: never asked");
}

function testCoreViewShowsThePreciseState(): void {
  const running = mergeEntry(undefined, status());
  assert(sidecarCoreView(undefined) === null, "not a managed core");
  const loading = sidecarCoreView(running);
  assert(loading?.status === "starting" && loading.label === "starting…", "running but targets loading = starting…, not offline");
  const failed = sidecarCoreView(listed(running, [], 1, "timeout"));
  assert(failed?.label === "starting…" && failed.error.includes("timeout"), "a listing error is surfaced inline");
  assert(sidecarCoreView(listed(running, [TARGET]))?.status === "ready", "targets → ready");
  assert(sidecarCoreView({ ...listed(running, []), auth: SIGNED_OUT })?.label === "sign-in required", "signed out, no agents");
  const waiting = sidecarCoreView(mergeEntry(undefined, status({ state: "waiting", pid: null, message: "port busy" })));
  assert(waiting?.status === "offline" && waiting.detail === "port busy", "not running shows the manager's message");
}

function testTargetsProvidersAndPending(): void {
  const ready = listed(mergeEntry(undefined, status()), [TARGET, { ...TARGET, id: "samplecore/codex-2" }]);
  const stopped = listed(mergeEntry(undefined, status({ name: "other" })), [{ ...TARGET, core: "other" }]);
  const down = { ...stopped, status: { ...stopped.status, state: "failed" } };
  assert(sidecarTargets([ready, down]).length === 2, "only running cores contribute targets");
  assert(JSON.stringify(sidecarProviders([ready])) === JSON.stringify({ samplecore: ["codex"] }), "providers dedupe per core");
  assert(sidecarsPending([mergeEntry(undefined, status())]), "running with no listing yet is pending");
  assert(sidecarsPending([mergeEntry(undefined, status({ state: "downloading" }))]), "a starting core is pending");
  assert(!sidecarsPending([ready, down]), "listed and failed cores are not pending");
}

testStartingToRunningTransitionRelistsTargets();
testFailedOrEmptyListingsRetryOnTheFastCadence();
testPollDelayIsFastWhileSettlingAndSlowOnceStable();
testAuthPhasesAndSignInGate();
testAuthChecksAreScheduledByPhase();
testCoreViewShowsThePreciseState();
testTargetsProvidersAndPending();
