import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ArrowClockwiseIcon,
  StopIcon,
  PlayIcon,
  TrashIcon,
  StarIcon,
  DownloadSimpleIcon,
} from "@phosphor-icons/react";
import {
  desktop,
  type BridgeStatus,
  type BridgeState,
  type DesktopSettings,
  type CoreConfig,
} from "@/lib/desktop";
import { setGatewayBase, inDesktopShell } from "@/lib/config";
import { api, type Core, type ReleaseFile } from "@/lib/api";
import { Button } from "@/ui/Button";
import { Input, Field } from "@/ui/Input";
import { Card, Badge, Dot } from "@/ui/Card";
import { Switch } from "@/ui/Segment";
import { PageHeader } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { cn } from "@/lib/cn";
import { useDesktopDownload } from "@/lib/useDesktopDownload";
import { isCore, sidecarCoreView, sidecarProviders, type SidecarCoreView } from "@/lib/sidecarState";
import { sidecarAgents, useSidecarAgents } from "@/lib/useSidecarAgents";
import { CoreHeader, coreStatus } from "@/views/agents/MyAgents";
import { ManagedSidecars } from "@/views/agents/ManagedSidecars";
import { AgentCoreEmptyState } from "@/views/AgentCoreEmptyState";
import { DesktopUpdatePanel } from "@/views/DesktopUpdates";
import { ReleaseDownloadButton } from "@/views/ReleaseDownloadButton";
import { PlatformAgentsHint } from "@/views/PlatformAgentsHint";
import { EmailInput } from "@/views/UserName";

// Agents is the single page for everything about the agents this workspace can
// drive. It replaces the former split between the read-only "Agent cores" page
// and the desktop-only "Local bridge" panel:
//
//   - Available agents (all clients): the cores the gateway knows about, each
//     core's providers, availability, and installing a manifest extension. Read
//     from /v1/cores + /v1/local-agent/targets, so a browser sees them too.
//   - Local bridge (desktop shell only): supervise + configure THIS machine's
//     bridge — status/start-stop, gateway URL/token, owner, single-host mode,
//     and per-provider enable/disable + default. These write local desktop
//     settings, which do not exist in a plain browser, so the browser shows a
//     "get the desktop app" note instead.

const STATE_LABEL: Record<BridgeState, string> = {
  stopped: "Stopped",
  starting: "Starting",
  running: "Running",
  failed: "Failed",
};

function stateTone(s: BridgeState): "accent" | "neutral" | "danger" | "warning" {
  if (s === "running") return "accent";
  if (s === "failed") return "danger";
  if (s === "starting") return "warning";
  return "neutral";
}

const EMPTY: DesktopSettings = {
  gatewayUrl: "",
  owner: "",
  tenant: "",
  cores: [],
  defaultAgent: { core: "", provider: "" },
  disabledProviders: [],
  syncSecret: "",
  serveAllOwners: false,
  gatewayToken: "",
  sandboxMode: "borrowed",
};

export function Agents() {
  const shell = inDesktopShell();
  const toast = useToast();
  const desktopDownload = useDesktopDownload();

  // --- Available agents (all clients) ---
  const [cores, setCores] = useState<Core[] | null>(null);
  const [gatewayProviders, setGatewayProviders] = useState<Record<string, string[]>>({});
  // Desktop-managed sidecars: live, shared state (re-lists agents as soon as a
  // core finishes starting; see useSidecarAgents).
  const sidecars = useSidecarAgents(shell);
  const adapterStatus = useMemo(() => sidecars.entries.map((e) => e.status), [sidecars.entries]);
  const [scan, setScan] = useState<"loading" | "ok" | "error">("loading");
  const [knownCores, setKnownCores] = useState<string[]>([]);

  const scanAgents = useCallback(async () => {
    setScan("loading");
    try {
      const cs = await api.cores();
      setCores(cs.cores);
      setKnownCores(
        cs.cores.map((c) => c.name).filter((n, i, all) => n && all.indexOf(n) === i),
      );
      const res = await api.localAgentTargets();
      const byCore: Record<string, string[]> = {};
      for (const t of res.targets || []) {
        const list = (byCore[t.core] ||= []);
        if (!list.includes(t.provider)) list.push(t.provider);
      }
      if (!shell) {
        // Browser: the member's own bridge-advertised agents (same rows the
        // desktop app renders — one data source, one layout).
        const mine = await api.myAgents().catch(() => ({ agents: [] as { core: string; provider: string }[] }));
        for (const a of mine.agents || []) {
          const list = (byCore[a.core] ||= []);
          if (!list.includes(a.provider)) list.push(a.provider);
        }
      }
      setGatewayProviders(byCore);
      setScan("ok");
    } catch {
      setCores([]);
      setGatewayProviders({});
      setScan("error");
    }
  }, [shell]);

  useEffect(() => {
    void scanAgents();
  }, [scanAgents]);

  const rescan = () => {
    void scanAgents();
    sidecarAgents.refresh();
  };

  // Gateway-listed providers plus what the running managed sidecars list.
  const providers = useMemo(() => {
    const merged: Record<string, string[]> = {};
    for (const src of [gatewayProviders, sidecarProviders(sidecars.entries)]) {
      for (const [core, list] of Object.entries(src)) {
        const into = (merged[core] ||= []);
        for (const p of list) if (!into.includes(p)) into.push(p);
      }
    }
    return merged;
  }, [gatewayProviders, sidecars.entries]);
  const sidecarView = (name: string): SidecarCoreView | null =>
    sidecarCoreView(sidecars.entries.find((e) => e.status.name === name && isCore(e.status)));

  // --- Local bridge (desktop shell only) ---
  const [status, setStatus] = useState<BridgeStatus | null>(null);
  const [settings, setSettings] = useState<DesktopSettings>(EMPTY);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!shell) return;
    desktop.getSettings().then((s) => s && setSettings(s));
    desktop.status().then((s) => s && setStatus(s));
    let unlisten = () => {};
    desktop.onStatus((s) => setStatus(s)).then((fn) => (unlisten = fn));
    return () => unlisten();
  }, [shell]);

  const update = (patch: Partial<DesktopSettings>) => {
    setSettings((s) => ({ ...s, ...patch }));
    setDirty(true);
  };
  // Config edits are keyed by core NAME, not array index, because the rendered
  // list is the union of gateway-detected cores and locally-configured ones —
  // a detected core may have no settings entry yet. updateCore upserts: patch
  // the matching entry, or append one (so editing a detected-only core's
  // connection creates its local config on first touch).
  const updateCore = (name: string, patch: Partial<CoreConfig>) => {
    const cores = settings.cores.some((c) => c.name === name)
      ? settings.cores.map((c) => (c.name === name ? { ...c, ...patch } : c))
      : [...settings.cores, { name, kind: "local", ...patch }];
    update({ cores });
  };
  const addCore = () => update({ cores: [...settings.cores, { name: "", kind: "local" }] });
  const removeCore = (name: string) => {
    const patch: Partial<DesktopSettings> = {
      cores: settings.cores.filter((c) => c.name !== name),
    };
    if (name) {
      if (settings.defaultAgent.core === name) {
        patch.defaultAgent = { core: "", provider: "" };
      }
      const prefix = `${name}/`;
      const kept = settings.disabledProviders.filter((k) => !k.startsWith(prefix));
      if (kept.length !== settings.disabledProviders.length) {
        patch.disabledProviders = kept;
      }
    }
    update(patch);
  };
  const isDisabled = (core: string, provider: string) =>
    settings.disabledProviders.includes(`${core}/${provider}`);
  const isDefault = (core: string, provider: string) =>
    settings.defaultAgent.core === core && settings.defaultAgent.provider === provider;
  const setDefault = (core: string, provider: string) =>
    update({ defaultAgent: { core, provider } });
  const toggleProvider = (core: string, provider: string) => {
    const key = `${core}/${provider}`;
    const disabling = !isDisabled(core, provider);
    const next = disabling
      ? [...settings.disabledProviders, key]
      : settings.disabledProviders.filter((k) => k !== key);
    const patch: Partial<DesktopSettings> = { disabledProviders: next };
    if (disabling && isDefault(core, provider)) {
      patch.defaultAgent = { core: "", provider: "" };
    }
    update(patch);
  };

  const save = async () => {
    setSaving(true);
    try {
      await desktop.setSettings(settings);
      if (settings.gatewayUrl) setGatewayBase(settings.gatewayUrl);
      setDirty(false);
      toast.success("Settings saved");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  const state = status?.state ?? "stopped";
  // "No agent core detected" must reflect cores THIS client can actually
  // drive. The gateway's available flag only covers endpoint cores it can
  // reach itself; a managed sidecar is driven locally through the
  // shell and reports available:false remotely — by design. So in the desktop
  // shell, a running managed sidecar counts as a usable core.
  const runningSidecar = shell && adapterStatus.some((s) => s.state === "running");
  const availableCores = (cores || []).filter(
    (c) => c.available || (runningSidecar && c.runtime === "sidecar"),
  );
  // The cores to render: the union of what the gateway detected (authoritative
  // online list) and what is configured locally (a manually-added core the
  // gateway has not resolved yet). Detected order first, then any local-only
  // extras. Each carries the detected meta + its local config (if any).
  const displayCores: {
    name: string;
    available: boolean;
    requiresDesktop: boolean;
    config: CoreConfig;
  }[] = [];
  {
    const seen = new Set<string>();
    for (const d of cores || []) {
      seen.add(d.name);
      const config = settings.cores.find((c) => c.name === d.name) ?? { name: d.name, kind: "local" };
      displayCores.push({
        name: d.name,
        available: d.available,
        requiresDesktop: !!d.requiresDesktop || d.runtime === "sidecar",
        config,
      });
    }
    for (const c of settings.cores) {
      if (c.name && !seen.has(c.name)) {
        displayCores.push({ name: c.name, available: false, requiresDesktop: false, config: c });
      } else if (!c.name) {
        // A freshly "+ Add a core" row with no name yet: always show it so the
        // user can type its name/connection.
        displayCores.push({ name: "", available: false, requiresDesktop: false, config: c });
      }
    }
  }

  return (
    <>
      <PageHeader
        title="Agents"
        subtitle={
          shell
            ? "The agents this workspace can drive, and the local cores that host them"
            : "The agents available in this workspace"
        }
      />

      <div className="p-6">
        <div className="mx-auto flex w-full max-w-[720px] flex-col gap-2">
          <SectionLabel>Available agents</SectionLabel>

          {scan === "loading" && (
            <p className="text-xs text-[var(--color-fg-subtle)]">Scanning for agent cores…</p>
          )}
          {scan === "error" && (
            <Banner tone="warning">
              <p className="text-[var(--color-warning)]">Couldn’t reach the gateway to list cores.</p>
              <Button size="sm" variant="outline" className="mt-2" onClick={rescan}>
                <ArrowClockwiseIcon size={14} /> Re-scan
              </Button>
            </Banner>
          )}
          {shell && adapterStatus.length > 0 && adapterStatus.some((a) => a.state !== "running") && (
            <Banner tone="warning">
              <p className="font-medium text-[var(--color-fg)]">
                Agent core not running
              </p>
              <p className="mt-1">
                {adapterStatus.find((a) => a.state !== "running")?.message ||
                  "A managed core is not running."}{" "}
                {(() => {
                  const m = adapterStatus.find((a) => a.state !== "running")?.message || "";
                  if (m.includes("401") || m.includes("Unauthorized"))
                    return "Sign out and sign in again (the session expired), then re-scan.";
                  if (m.includes("Download"))
                    return "Download the update when the admin configures it, then re-scan.";
                  return "It restarts automatically; this page updates on its own.";
                })()}
              </p>
            </Banner>
          )}
          {scan === "ok" && availableCores.length === 0 && (
            <Banner tone="warning">
              <p className="font-medium text-[var(--color-fg)]">No agent core available</p>
              <p className="mt-1">
                A core is the service that runs your agents. Cores are installed by a platform
                admin — nothing is installed for this instance yet.
              </p>
              <ul className="mt-1 list-disc pl-4">
                <li>
                  members: check{" "}
                  <a href="#/components" className="text-[var(--color-accent)] hover:underline">
                    Components
                  </a>{" "}
                  for optional ones, or ask your admin to install one
                </li>
                <li>
                  admins: install from Platform → Components or with{" "}
                  <span className="font-mono">bnbloc cores install</span>
                </li>
              </ul>
              <p className="mt-1">You can still chat and borrow other members’ agents meanwhile.</p>
              <PlatformAgentsHint className="mt-2" />
            </Banner>
          )}

          {/* Both surfaces render the same CoreGroup layout. Providers come
              from the same data (desktop: local core scan; browser: the
              member's bridge advertisement via my/agents) — no separate
              panels, no separate logic. */}
          {displayCores.map((d, i) => (
            <CoreGroup
              key={d.name || `new:${i}`}
              core={d.config}
              available={d.available}
              requiresDesktop={d.requiresDesktop}
              inDesktop={shell}
              providers={providers[d.name] || []}
              isDefault={(p) => isDefault(d.name, p)}
              isDisabled={(p) => isDisabled(d.name, p)}
              onSetDefault={(p) => setDefault(d.name, p)}
              onToggle={(p) => toggleProvider(d.name, p)}
              onChange={(patch) => updateCore(d.name, patch)}
              onRemove={() => removeCore(d.name)}
              canRemove={shell}
              knownCores={knownCores}
              onRescan={rescan}
              desktopDownload={desktopDownload}
              sidecar={shell ? sidecarView(d.name) : null}
            />
          ))}

          <div className="flex items-center justify-between border-t border-dashed border-[var(--color-border)] pt-3 text-xs text-[var(--color-fg-subtle)]">
            <div className="flex items-center gap-3">
              {shell && (
                <button onClick={addCore} className="hover:text-[var(--color-fg)]">
                  + Add a core
                </button>
              )}
            </div>
            <Button size="sm" variant="outline" onClick={rescan}>
              <ArrowClockwiseIcon size={14} /> Re-scan
            </Button>
          </div>
          <datalist id="known-cores">
            {knownCores.map((n) => (
              <option key={n} value={n} />
            ))}
          </datalist>

          <SectionLabel className="mt-6">Local bridge</SectionLabel>

          {shell ? (
            <>
              <DesktopUpdatePanel />

              <Card className="p-4">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2.5">
                    <Dot tone={state === "running" ? "accent" : state === "failed" ? "danger" : "muted"} />
                    <span className="font-medium">{STATE_LABEL[state]}</span>
                    <Badge tone={stateTone(state)}>{status?.pid ? `pid ${status.pid}` : "no process"}</Badge>
                    {typeof status?.lastExitCode === "number" && state !== "running" && (
                      <Badge tone="neutral">exit {status.lastExitCode}</Badge>
                    )}
                  </div>
                  <div className="flex items-center gap-2">
                    {state === "running" || state === "starting" ? (
                      <>
                        <Button size="sm" variant="outline" onClick={() => desktop.start()}>
                          <ArrowClockwiseIcon size={15} /> Restart
                        </Button>
                        <Button size="sm" variant="danger" onClick={() => desktop.stop()}>
                          <StopIcon size={15} /> Stop
                        </Button>
                      </>
                    ) : (
                      <Button
                        size="sm"
                        variant="primary"
                        onClick={() =>
                          void desktop
                            .start()
                            .catch((e) =>
                              toast.error(e instanceof Error ? e.message : "Could not start the bridge"),
                            )
                        }
                      >
                        <PlayIcon size={15} weight="fill" /> Start
                      </Button>
                    )}
                  </div>
                </div>
                {status && status.logsTail.length > 0 && (
                  <pre className="tabular mt-4 max-h-52 overflow-auto rounded-[var(--radius)] bg-[var(--color-bg)] p-3 text-xs leading-relaxed text-[var(--color-fg-muted)]">
                    {status.logsTail.join("\n")}
                  </pre>
                )}
              </Card>

              <ManagedSidecars entries={sidecars.entries} />

              <Card className="p-4">
                <div className="flex flex-col gap-4">
                  <Field label="Gateway URL" hint="The bnbloc control-plane address, e.g. https://bnbloc.example.com">
                    <Input
                      value={settings.gatewayUrl}
                      placeholder="https://bnbloc.example.com"
                      onChange={(e) => update({ gatewayUrl: e.target.value })}
                    />
                  </Field>
                  <Field
                    label="Gateway token"
                    hint="Optional. For a remote gateway using enterprise sign-in (SSO/OIDC) that rejects the default login. Leave empty for local/dev."
                  >
                    <Input
                      type="password"
                      value={settings.gatewayToken}
                      placeholder="paste an access token"
                      onChange={(e) => update({ gatewayToken: e.target.value })}
                    />
                  </Field>
                  <Field label="Owner" hint="The member this client acts for">
                    <EmailInput
                      value={settings.owner}
                      placeholder="your-subject"
                      onChange={(e) => update({ owner: e.target.value })}
                    />
                  </Field>
                  <Field
                    label="Single-host mode"
                    hint="Let this one bridge drive every member's agent from your local core — for local multi-user testing against a dev gateway only. Production gateways refuse it; there each member runs their own bridge."
                  >
                    <Switch
                      checked={settings.serveAllOwners}
                      onChange={(v) => update({ serveAllOwners: v })}
                      label={settings.serveAllOwners ? "On" : "Off"}
                    />
                  </Field>
                  <div className="flex items-center justify-end gap-2 pt-1">
                    <Button variant="primary" onClick={save} disabled={!dirty || saving}>
                      {saving ? "Saving…" : "Save & apply"}
                    </Button>
                  </div>
                </div>
              </Card>
            </>
          ) : (
            <Banner>
              The bridge that connects your own machine’s agents runs in the{" "}
              <span className="font-medium text-[var(--color-fg)]">bnbloc desktop app</span>. Install
              it and point it at this gateway to bring your own agent, then @-message it in a room.
              {/* Once, here or in the no-core banner above. */}
              {!(scan === "ok" && availableCores.length === 0) && <PlatformAgentsHint className="mt-2" />}
              <div className="mt-3">
                {desktopDownload.file ? (
                  <ReleaseDownloadButton file={desktopDownload.file} label="Download desktop app" />
                ) : (
                  <Button size="sm" variant="primary" disabled>
                    <DownloadSimpleIcon size={14} />
                    {desktopDownload.loading ? "Finding download…" : "Download not configured"}
                  </Button>
                )}
                {!desktopDownload.file && !desktopDownload.loading && (
                  <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">
                    Download not configured — ask your admin.
                  </p>
                )}
              </div>
            </Banner>
          )}
        </div>
      </div>
    </>
  );
}

function SectionLabel({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("mt-2 flex items-center gap-3", className)}>
      <span className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
        {children}
      </span>
      <span className="h-px flex-1 bg-[var(--color-border)]" />
    </div>
  );
}


function Banner({
  children,
  tone,
}: {
  children: React.ReactNode;
  tone?: "warning" | "neutral";
}) {
  return (
    <div
      className={cn(
        "rounded-[var(--radius)] border p-3 text-xs text-[var(--color-fg-muted)]",
        tone === "warning"
          ? "border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10"
          : "border-[var(--color-border)] bg-[var(--color-surface-2)]",
      )}
    >
      {children}
    </div>
  );
}

function CoreGroup({
  core,
  available,
  requiresDesktop,
  inDesktop,
  providers,
  isDefault,
  isDisabled,
  onSetDefault,
  onToggle,
  onChange,
  onRemove,
  canRemove,
  knownCores,
  onRescan,
  desktopDownload,
  sidecar,
}: {
  core: CoreConfig;
  available: boolean;
  requiresDesktop: boolean;
  inDesktop: boolean;
  providers: string[];
  isDefault: (provider: string) => boolean;
  isDisabled: (provider: string) => boolean;
  onSetDefault: (provider: string) => void;
  onToggle: (provider: string) => void;
  onChange: (patch: Partial<CoreConfig>) => void;
  onRemove: () => void;
  canRemove: boolean;
  knownCores: string[];
  onRescan: () => void;
  desktopDownload: { readonly url: string | null; readonly file: ReleaseFile | null; readonly loading: boolean };
  // Live state of the desktop-managed sidecar serving this core (desktop
  // shell only): replaces the coarse gateway-derived status.
  sidecar: SidecarCoreView | null;
}) {
  // Agents listed by another source outrank a sidecar still starting.
  const live = sidecar && (providers.length === 0 || sidecar.status === "ready") ? sidecar : null;
  const status = live?.status ?? coreStatus(available, providers.length);
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] overflow-hidden">
      <CoreHeader
        name={core.name}
        status={status}
        label={live?.label}
        right={
          <Button size="icon" variant="ghost" aria-label="Remove core" disabled={!canRemove} onClick={onRemove}>
            <TrashIcon size={16} />
          </Button>
        }
      />
      {sidecar?.error && providers.length > 0 && (
        <p className="border-b border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-fg-subtle)]">
          {sidecar.error}
        </p>
      )}
      {providers.length > 0 ? (
        <div>
          {providers.map((p) => (
            <ProviderRow
              key={p}
              provider={p}
              core={core.name}
              isDefault={isDefault(p)}
              disabled={isDisabled(p)}
              onSetDefault={() => onSetDefault(p)}
              onToggle={() => onToggle(p)}
            />
          ))}
        </div>
      ) : (
        <AgentCoreEmptyState
          status={status}
          requiresDesktop={requiresDesktop}
          inDesktop={inDesktop}
          onRescan={onRescan}
          desktopDownload={{ file: desktopDownload.file, loading: desktopDownload.loading }}
          detail={live?.detail}
          error={sidecar?.error}
        />
      )}
      <details className="border-t border-[var(--color-border)]">
        <summary className="cursor-pointer px-3 py-2 text-xs text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]">
          Connection (advanced)
        </summary>
        <div className="flex flex-col gap-3 px-3 pb-3">
          <Field label="Name" hint="installed core name">
            <Input
              list="known-cores"
              value={core.name}
              placeholder="local-core"
              onChange={(e) => onChange({ name: e.target.value })}
            />
            {core.name && !knownCores.includes(core.name) && (
              <span className="mt-1 block text-xs text-[var(--color-fg-subtle)]">not loaded yet</span>
            )}
          </Field>
          <Field label="URL" hint="empty = auto-discover">
            <Input
              value={core.url ?? ""}
              placeholder="http://127.0.0.1:<port>"
              onChange={(e) => onChange({ url: e.target.value })}
            />
          </Field>
          <Field label="Token" hint="empty = auto-discover">
            <Input type="password" value={core.token ?? ""} onChange={(e) => onChange({ token: e.target.value })} />
          </Field>
        </div>
      </details>
    </div>
  );
}

function ProviderRow({
  provider,
  core,
  isDefault,
  disabled,
  onSetDefault,
  onToggle,
}: {
  provider: string;
  core: string;
  isDefault: boolean;
  disabled: boolean;
  onSetDefault: () => void;
  onToggle: () => void;
}) {
  return (
    <div
      className={cn(
        "flex items-center gap-3 border-b border-[var(--color-border)] px-3 py-2 last:border-b-0",
        disabled && "opacity-50",
      )}
    >
      <div className="flex-1">
        <div className="text-sm font-medium">{provider}</div>
        <div className="tabular text-xs text-[var(--color-fg-subtle)]">{`${core}/${provider}`}</div>
      </div>
      {isDefault && !disabled && (
        <span className="rounded-full bg-[var(--color-accent)] px-1.5 text-[0.6rem] font-semibold text-[#04140f]">
          Default
        </span>
      )}
      <button
        onClick={onSetDefault}
        disabled={disabled}
        aria-label="Set as default"
        title="Set as default agent"
        className={cn(
          "tactile",
          isDefault ? "text-[var(--color-accent)]" : "text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]",
        )}
      >
        <StarIcon size={15} weight={isDefault ? "fill" : "regular"} />
      </button>
      <Switch checked={!disabled} onChange={() => onToggle()} label="" ariaLabel={`Enable ${core}/${provider}`} />
    </div>
  );
}
