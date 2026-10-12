import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import { api, type AgentTarget, type Core } from "@/lib/api";
import { desktop, type DesktopSettings } from "@/lib/desktop";
import { sidecarsPending, sidecarTargets } from "@/lib/sidecarState";
import { sidecarAgents, useSidecarAgents } from "@/lib/useSidecarAgents";

const LS_URL = "bnbloc.localAgentUrl";
const LS_TOKEN = "bnbloc.localAgentToken";
const LS_TARGET = "bnbloc.agentTarget";

export interface LocalAgent {
  url: string;
  token: string;
  targets: readonly AgentTarget[];
  target: string; // selected agent target id (core-qualified "<core>/<rawid>")
  core: string; // core of the selected target (derived from the target list)
  detected: boolean | null;
  // coreStatus is the gateway's view of every registered core and whether it
  // resolved a reachable endpoint (from GET /v1/cores). It is the production-safe
  // availability signal, unlike the dev-gated discover probe.
  coreStatus: readonly Core[];
  // anyCoreAvailable is true when the gateway reports at least one reachable
  // core, so the UI can distinguish "no core connected" from "core up, no agents
  // enabled yet".
  anyCoreAvailable: boolean;
  // devDiscovery is true only when the gateway runs the dev-only discovery
  // surface (BNBLOC_DEV_DISCOVERY): then, and only then, a caller-supplied
  // local core address/token may be sent (the gateway answers 403 otherwise).
  // The manual-address UI is shown only in this mode.
  devDiscovery: boolean;
  // The caller-supplied core address to send with run/targets calls: the
  // manual/discovered address in dev-discovery mode, empty otherwise.
  devAddress: { readonly localAgentUrl?: string; readonly localAgentToken?: string };
  // isRemoteGateway is true when Path A ("Run a local agent", which makes the
  // gateway dial the caller's daemon) cannot work. Desktop-managed sidecars are
  // driven by the bridge after a room exists; they are not reachable from a
  // remote gateway's /v1/local-agent/run request.
  isRemoteGateway: boolean;
  setUrl: (v: string) => void;
  setToken: (v: string) => void;
  setTarget: (id: string) => void;
  save: () => void; // persist url/token then reload targets
  reload: () => void;
}

// The local-agent state is one module store (web M5): every useLocalAgent
// (room view, my rooms, the run dialog, the people chat) reads the same
// answer, one load serves them all, and a selection made in one is the
// selection of every other.
interface LocalAgentState {
  readonly url: string;
  readonly token: string;
  readonly gatewayTargets: readonly AgentTarget[];
  readonly loaded: boolean;
  readonly settings: DesktopSettings | null;
  readonly target: string;
  readonly coreStatus: readonly Core[];
  readonly devDiscovery: boolean;
}

let state: LocalAgentState = {
  url: readStored(LS_URL),
  token: readStored(LS_TOKEN),
  gatewayTargets: [],
  loaded: false,
  settings: null,
  target: readStored(LS_TARGET),
  coreStatus: [],
  devDiscovery: false,
};
let loading: Promise<void> | null = null;
const listeners = new Set<() => void>();

function readStored(key: string): string {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

function writeStored(key: string, value: string): void {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    /* storage unavailable: the in-memory value still applies */
  }
}

function setState(patch: Partial<LocalAgentState>): void {
  state = { ...state, ...patch };
  for (const l of listeners) l();
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

function setTarget(id: string): void {
  if (id === state.target) return;
  writeStored(LS_TARGET, id);
  setState({ target: id });
}

// load reads the cores, the dev-discovery signal and the targets once for
// every subscriber (concurrent callers share the in-flight load).
function load(): Promise<void> {
  if (loading) return loading;
  loading = (async () => {
    setState({ loaded: false });
    let u = state.url;
    let t = state.token;
    // GET /v1/cores is the production-safe availability truth (unlike the
    // dev-gated discover probe): it reports every registered core and
    // whether the gateway resolved a reachable endpoint for it.
    const coreStatus = await api
      .cores()
      .then((cs) => cs.cores)
      .catch(() => []);
    // The dev discovery probe doubles as the dev-mode signal: it answers
    // only when the gateway runs BNBLOC_DEV_DISCOVERY (404 in production).
    // Only in that mode is a manual local core address sent; otherwise any
    // stored address is ignored, never forwarded.
    let dev = false;
    try {
      const d = await api.localAgentDiscover();
      dev = true;
      if (!u && d.found && d.localAgentUrl) {
        u = d.localAgentUrl;
        t = d.localAgentToken || "";
        writeStored(LS_URL, u);
        writeStored(LS_TOKEN, t);
      }
    } catch {
      dev = false;
    }
    // Transient failure: no gateway targets right now (the desktop shell
    // still lists its managed sidecars); the saved choice stands.
    const gatewayTargets = await api
      .localAgentTargets(dev ? u || undefined : undefined, dev ? t || undefined : undefined)
      .then((res) => res.targets)
      .catch(() => []);
    // In the desktop shell, respect the "Agent sources" panel (getSettings
    // is null in a plain browser, where nothing is filtered).
    const settings = await desktop.getSettings();
    setState({ url: u, token: t, coreStatus, devDiscovery: dev, gatewayTargets, settings, loaded: true });
  })().finally(() => {
    loading = null;
  });
  return loading;
}

function reload(): void {
  void load();
  sidecarAgents.refresh();
}

// forgetLocalAgent drops the loaded answer at sign-out: the next member's
// first useLocalAgent loads their own cores and targets.
export function forgetLocalAgent(): void {
  setState({ gatewayTargets: [], coreStatus: [], settings: null, devDiscovery: false, loaded: false });
}

// useLocalAgent is local-agent-core discovery, target listing, the selected
// target and the persisted manual address — shared by the composer's agent
// picker and the run-agent dialog. A saved manual URL wins over discovery.
// Desktop-managed sidecar targets come from the shared live sidecar store, so
// they appear as soon as a sidecar finishes starting (no one-shot scan).
export function useLocalAgent(active: boolean): LocalAgent {
  const { url, token, gatewayTargets, loaded, settings, target, coreStatus, devDiscovery } = useSyncExternalStore(subscribe, () => state, () => state);
  const sidecars = useSidecarAgents(active);
  useEffect(() => {
    if (active && !state.loaded) void load();
  }, [active]);
  const setUrl = useCallback((v: string) => setState({ url: v }), []);
  const setToken = useCallback((v: string) => setState({ token: v }), []);

  // A provider the user turned off must not be selectable here (the bridge
  // drops messages to it), and the ★ default seeds the initial selection.
  const managed = useMemo(() => sidecarTargets(sidecars.entries), [sidecars.entries]);
  const pending = sidecarsPending(sidecars.entries);
  const { targets, defaultId } = useMemo(() => {
    let list = [...gatewayTargets, ...managed];
    let defaultId = "";
    if (settings) {
      const disabled = new Set(settings.disabledProviders);
      list = list.filter((x) => !disabled.has(`${x.core}/${x.provider}`));
      defaultId =
        list.find(
          (x) => x.core === settings.defaultAgent.core && x.provider === settings.defaultAgent.provider,
        )?.id || "";
    }
    return { targets: list, defaultId };
  }, [gatewayTargets, managed, settings]);

  // Selection precedence: a still-available saved target wins (respects an
  // in-session switch), else the panel's ★ default, else the first target. A
  // saved target that no longer exists is cleared, never kept as a phantom
  // selection — except while a managed core is still starting, when it may
  // simply not be listed yet.
  // Only an active instance judges the selection: an inactive one sees no
  // managed sidecar targets and would clear a saved one.
  useEffect(() => {
    if (!active || !loaded) return;
    const saved = state.target;
    if (pending && !targets.some((x) => x.id === saved)) return;
    setTarget(targets.find((x) => x.id === saved)?.id || defaultId || targets[0]?.id || "");
  }, [active, loaded, pending, targets, defaultId]);

  const save = useCallback(() => {
    writeStored(LS_URL, state.url);
    writeStored(LS_TOKEN, state.token);
    reload();
  }, []);

  // The selected target's core, derived from the current target list. The id is
  // core-qualified ("<core>/<rawid>"), so this mirrors what the gateway splits.
  const core = targets.find((x) => x.id === target)?.core || "";
  const anyCoreAvailable = coreStatus.some((c) => c.available);
  // Detected = we can actually run an agent: a target came back, a manual URL
  // is set, or the gateway reports a reachable core. This does not depend on
  // the dev-only discover probe (the prod dead-end).
  const detected = loaded ? targets.length > 0 || (devDiscovery && !!url) || anyCoreAvailable : null;
  // Remote for the gateway-dialed run path when no gateway-side core and no
  // manual local URL are available. Managed sidecar targets may still be usable
  // from room chat through the desktop bridge.
  const isRemoteGateway = detected !== null && !anyCoreAvailable && !(devDiscovery && url);

  const devAddress = useMemo(
    () => (devDiscovery && url ? { localAgentUrl: url, ...(token ? { localAgentToken: token } : {}) } : {}),
    [devDiscovery, url, token],
  );

  // One stable object per actual change, so consumers can depend on it.
  return useMemo(
    () => ({
      devAddress,
      url,
      token,
      targets,
      target,
      core,
      detected,
      coreStatus,
      anyCoreAvailable,
      devDiscovery,
      isRemoteGateway,
      setUrl,
      setToken,
      setTarget,
      save,
      reload,
    }),
    [devAddress, url, token, targets, target, core, detected, coreStatus, anyCoreAvailable, devDiscovery, isRemoteGateway, setUrl, setToken, save],
  );
}
