import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type AgentTarget } from "@/lib/api";
import { desktop, type DesktopSettings } from "@/lib/desktop";
import { sidecarsPending, sidecarTargets } from "@/lib/sidecarState";
import { sidecarAgents, useSidecarAgents } from "@/lib/useSidecarAgents";

const LS_URL = "bnbloc.localAgentUrl";
const LS_TOKEN = "bnbloc.localAgentToken";
const LS_TARGET = "bnbloc.agentTarget";

export interface LocalAgent {
  url: string;
  token: string;
  targets: AgentTarget[];
  target: string; // selected agent target id (core-qualified "<core>/<rawid>")
  core: string; // core of the selected target (derived from the target list)
  detected: boolean | null;
  // coreStatus is the gateway's view of every registered core and whether it
  // resolved a reachable endpoint (from GET /v1/cores). It is the production-safe
  // availability signal, unlike the dev-gated discover probe.
  coreStatus: { name: string; available: boolean; builtin: boolean }[];
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

// useLocalAgent centralizes local-agent-core discovery, target listing, the
// selected target, and persisted manual address — shared by the composer's
// agent picker and the run-agent dialog. A saved manual URL wins over discovery.
// Desktop-managed sidecar targets come from the shared live sidecar store, so
// they appear as soon as a sidecar finishes starting (no one-shot scan).
export function useLocalAgent(active: boolean): LocalAgent {
  const [url, setUrl] = useState(() => localStorage.getItem(LS_URL) || "");
  const [token, setToken] = useState(() => localStorage.getItem(LS_TOKEN) || "");
  const [gatewayTargets, setGatewayTargets] = useState<AgentTarget[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [settings, setSettings] = useState<DesktopSettings | null>(null);
  const [target, setTargetState] = useState(
    () => localStorage.getItem(LS_TARGET) || "",
  );
  const [coreStatus, setCoreStatus] = useState<
    { name: string; available: boolean; builtin: boolean }[]
  >([]);
  const [devDiscovery, setDevDiscovery] = useState(false);
  const sidecars = useSidecarAgents(active);

  const setTarget = useCallback((id: string) => {
    setTargetState(id);
    if (id) localStorage.setItem(LS_TARGET, id);
    else localStorage.removeItem(LS_TARGET);
  }, []);

  const load = useCallback(async () => {
    setLoaded(false);
    let u = localStorage.getItem(LS_URL) || "";
    let t = localStorage.getItem(LS_TOKEN) || "";

    // GET /v1/cores is the production-safe availability truth (unlike the
    // dev-gated discover probe): it reports every registered core and whether
    // the gateway resolved a reachable endpoint for it.
    try {
      const cs = await api.cores();
      setCoreStatus(cs.cores || []);
    } catch {
      setCoreStatus([]);
    }

    // The dev discovery probe doubles as the dev-mode signal: it answers only
    // when the gateway runs BNBLOC_DEV_DISCOVERY (404 in production). Only in
    // that mode is a manual local core address sent; otherwise any stored
    // address is ignored, never forwarded.
    let dev = false;
    try {
      const d = await api.localAgentDiscover();
      dev = true;
      if (!u && d.found && d.localAgentUrl) {
        u = d.localAgentUrl;
        t = d.localAgentToken || "";
        setUrl(u);
        setToken(t);
        localStorage.setItem(LS_URL, u);
        if (t) localStorage.setItem(LS_TOKEN, t);
      }
    } catch {
      dev = false;
    }
    setDevDiscovery(dev);
    if (!dev) {
      u = "";
      t = "";
    }

    try {
      const res = await api.localAgentTargets(u || undefined, t || undefined);
      setGatewayTargets(res.targets || []);
    } catch {
      // Transient failure: no gateway targets right now (the desktop shell
      // still lists its managed sidecars); the saved choice stands.
      setGatewayTargets([]);
    }
    // In the desktop shell, respect the "Agent sources" panel (getSettings is
    // null in a plain browser, where nothing is filtered).
    setSettings(await desktop.getSettings());
    setLoaded(true);
  }, []);

  useEffect(() => {
    if (active) void load();
  }, [active, load]);

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
  useEffect(() => {
    if (!loaded) return;
    const saved = localStorage.getItem(LS_TARGET) || "";
    if (pending && !targets.some((x) => x.id === saved)) return;
    setTarget(targets.find((x) => x.id === saved)?.id || defaultId || targets[0]?.id || "");
  }, [loaded, pending, targets, defaultId, setTarget]);

  const reload = useCallback(() => {
    void load();
    sidecarAgents.refresh();
  }, [load]);

  const save = useCallback(() => {
    if (url) localStorage.setItem(LS_URL, url);
    else localStorage.removeItem(LS_URL);
    if (token) localStorage.setItem(LS_TOKEN, token);
    else localStorage.removeItem(LS_TOKEN);
    reload();
  }, [url, token, reload]);

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
    [devAddress, url, token, targets, target, core, detected, coreStatus, anyCoreAvailable, devDiscovery, isRemoteGateway, setTarget, save, reload],
  );
}
