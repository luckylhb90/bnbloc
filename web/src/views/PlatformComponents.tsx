import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowClockwiseIcon, PackageIcon } from "@phosphor-icons/react";
import { api, type ComponentKind, type ComponentRollout, type PlatformComponent } from "@/lib/api";
import { describeTestResult } from "@/lib/notificationPrefs";
import { useStage } from "@/lib/capability";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { ComponentCard } from "@/views/ComponentCard";
import { ComponentUploadFlow } from "@/views/ComponentUploadFlow";
import { TrustedKeysPanel } from "@/views/PlatformTrustedKeys";

type KindFilter = "all" | ComponentKind;

const FILTERS = [
  { value: "all", label: "All" },
  { value: "core", label: "Cores" },
  { value: "plugin", label: "Plugins" },
  { value: "auth-provider", label: "Auth" },
  { value: "notifier", label: "Notifiers" },
  { value: "sandbox", label: "Sandboxes" },
  { value: "previewer", label: "Previewers" },
  { value: "mcp", label: "MCP tools" },
] satisfies readonly { readonly value: KindFilter; readonly label: string }[];

export function PlatformComponents() {
  const toast = useToast();
  const shellRef = useRef<HTMLElement | null>(null);
  const stage = useStage(shellRef);
  const [components, setComponents] = useState<readonly PlatformComponent[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busyComponent, setBusyComponent] = useState<string | null>(null);
  const [filter, setFilter] = useState<KindFilter>("all");
  // A notifier's signing secret, shown once after install or rotation.
  const [revealed, setRevealed] = useState<{ readonly name: string; readonly secret: string } | null>(null);

  const load = useCallback(async () => {
    setLoadError(null);
    try {
      const result = await api.listPlatformComponents();
      setComponents(result.components ?? []);
    } catch (error: unknown) {
      setLoadError(error instanceof Error ? error.message : "failed to load components");
      setComponents([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const counts = useMemo(() => summarizeComponents(components ?? []), [components]);
  const shown = useMemo(
    () => (components ?? []).filter((component) => filter === "all" || component.kind === filter),
    [components, filter],
  );

  const setRollout = async (component: PlatformComponent, rollout: ComponentRollout) => {
    if (component.rollout === rollout) return;
    setBusyComponent(component.name);
    setLoadError(null);
    try {
      await api.setComponentRollout(component, rollout);
      toast.success(`${component.name} ${rolloutCopy(rollout)}`);
      await load();
    } catch (error: unknown) {
      setLoadError(error instanceof Error ? error.message : "failed to set rollout");
    } finally {
      setBusyComponent(null);
    }
  };

  const rotateSecret = async (component: PlatformComponent) => {
    setBusyComponent(component.name);
    setLoadError(null);
    try {
      const { secret } = await api.rotateComponentSecret(component);
      setRevealed({ name: component.name, secret });
      toast.success(`New signing secret for ${component.name}`);
    } catch (error: unknown) {
      setLoadError(error instanceof Error ? error.message : "failed to rotate the secret");
    } finally {
      setBusyComponent(null);
    }
  };

  const sendTest = async (component: PlatformComponent) => {
    setBusyComponent(component.name);
    try {
      const r = describeTestResult(await api.testComponent(component));
      if (r.ok) toast.success(r.message);
      else toast.error(r.message);
    } catch (error: unknown) {
      toast.error(error instanceof Error ? error.message : "Could not send a test notification");
    } finally {
      setBusyComponent(null);
    }
  };

  const uninstall = async (component: PlatformComponent) => {
    setBusyComponent(component.name);
    setLoadError(null);
    try {
      await api.uninstallPlatformComponent(component);
      toast.success(`${component.name} uninstalled`);
      await load();
    } catch (error: unknown) {
      setLoadError(error instanceof Error ? error.message : "failed to uninstall component");
    } finally {
      setBusyComponent(null);
    }
  };

  return (
    <section ref={shellRef} className="coarse-targets flex flex-col gap-4">
      <Card className="p-4">
        <div className="flex items-start gap-3">
          <div className="grid size-10 shrink-0 place-items-center rounded-[var(--radius)] bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
            <PackageIcon size={20} weight="fill" />
          </div>
          <div className="min-w-0 flex-1">
            <h2 className="text-base font-semibold text-[var(--color-fg)]">Components Workbench</h2>
            <p className="mt-1 text-sm leading-relaxed text-[var(--color-fg-muted)]">
              Publish signed core, plugin, auth-provider, notifier, sandbox, previewer and MCP tool packages, review trust and runtime facts, and choose who receives each component.
            </p>
          </div>
          <div className="hidden shrink-0 gap-6 pl-5 sm:flex">
            <HeadStat label="Components" value={counts.total} />
            <HeadStat label="Cores / Plugins / Auth / Notifiers / Sandboxes / Previewers / MCP tools" value={`${counts.core} / ${counts.plugin} / ${counts["auth-provider"]} / ${counts.notifier} / ${counts.sandbox} / ${counts.previewer} / ${counts.mcp}`} />
            <HeadStat label="Auto-installed" value={counts.auto} />
            <HeadStat label="Members" value={counts.members > 0 ? counts.members : "—"} />
          </div>
        </div>
      </Card>

      {loadError && <ErrorBox>{loadError}</ErrorBox>}
      {revealed && <SecretReveal name={revealed.name} secret={revealed.secret} onDismiss={() => setRevealed(null)} />}

      <div className={stage === "wide" ? "grid grid-cols-[minmax(0,1fr)_22rem] gap-4" : "flex flex-col gap-4"}>
        <div className="flex min-w-0 flex-col gap-4">
          <ComponentUploadFlow
            onPublished={async (name, secret) => {
              if (secret) setRevealed({ name, secret });
              await load();
              window.setTimeout(() => document.querySelector(`#component-${CSS.escape(name)}`)?.scrollIntoView({ behavior: "smooth", block: "center" }), 100);
            }}
          />
          <PublishedComponents
            components={components}
            shown={shown}
            filter={filter}
            setFilter={setFilter}
            busyComponent={busyComponent}
            refresh={() => void load()}
            setRollout={(component, rollout) => void setRollout(component, rollout)}
            uninstall={(component) => void uninstall(component)}
            rotateSecret={(component) => void rotateSecret(component)}
            sendTest={(component) => void sendTest(component)}
          />
        </div>
        <aside className="flex min-w-0 flex-col gap-4">
          <TrustedKeysPanel />
        </aside>
      </div>
    </section>
  );
}

function PublishedComponents({
  components,
  shown,
  filter,
  setFilter,
  busyComponent,
  refresh,
  setRollout,
  uninstall,
  rotateSecret,
  sendTest,
}: {
  readonly components: readonly PlatformComponent[] | null;
  readonly shown: readonly PlatformComponent[];
  readonly filter: KindFilter;
  readonly setFilter: (filter: KindFilter) => void;
  readonly busyComponent: string | null;
  readonly refresh: () => void;
  readonly setRollout: (component: PlatformComponent, rollout: ComponentRollout) => void;
  readonly uninstall: (component: PlatformComponent) => void;
  readonly rotateSecret: (component: PlatformComponent) => void;
  readonly sendTest: (component: PlatformComponent) => void;
}) {
  const list = components ?? [];
  const counts = summarizeComponents(list);
  return (
    <div>
      <SectionHeading title="Published components" subtitle="Rollout changes apply immediately. Off components stay visible here for staging but are hidden from members." />
      <div className="mt-3 flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Segment
            options={FILTERS.map((option) => ({ ...option, label: `${option.label} ${option.value === "all" ? counts.total : counts[option.value]}` }))}
            value={filter}
            onChange={setFilter}
          />
          <Button variant="ghost" size="sm" onClick={refresh}>
            <ArrowClockwiseIcon size={14} /> Refresh
          </Button>
        </div>
        {components === null ? (
          <EmptyCard title="Loading components…" hint="Fetching the platform registry." />
        ) : shown.length === 0 ? (
          <EmptyCard title="No components published." hint="Upload a signed component package to add a core or plugin." />
        ) : (
          shown.map((component) => (
            <ComponentCard
              key={`${component.kind}:${component.name}`}
              mode="admin"
              component={component}
              busy={busyComponent === component.name}
              onSetRollout={setRollout}
              onUninstall={uninstall}
              onRotateSecret={rotateSecret}
              onSendTest={sendTest}
            />
          ))
        )}
      </div>
    </div>
  );
}

function summarizeComponents(components: readonly PlatformComponent[]) {
  const ofKind = (kind: ComponentKind) => components.filter((component) => component.kind === kind).length;
  const auto = components.filter((component) => component.rollout === "auto").length;
  const members = Math.max(0, ...components.map((component) => component.adoption.totalMembers ?? component.totalMembers ?? 0));
  return {
    total: components.length,
    core: ofKind("core"),
    plugin: ofKind("plugin"),
    "auth-provider": ofKind("auth-provider"),
    notifier: ofKind("notifier"),
    sandbox: ofKind("sandbox"),
    previewer: ofKind("previewer"),
    mcp: ofKind("mcp"),
    auto,
    members,
  };
}

// SecretReveal shows a notifier's signing secret once (after install or
// rotation) with a copy button; it is never retrievable again.
function SecretReveal({ name, secret, onDismiss }: { readonly name: string; readonly secret: string; readonly onDismiss: () => void }) {
  const toast = useToast();
  return (
    <Card className="flex flex-col gap-2 border-[var(--color-warning)]/50 px-4 py-3">
      <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
        Signing secret for {name} — copy it now, it is not shown again
      </span>
      <code className="break-all font-mono text-xs">{secret}</code>
      <p className="text-xs text-[var(--color-fg-subtle)]">Configure it on the notifier endpoint so it can verify deliveries.</p>
      <div className="flex gap-2">
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            void navigator.clipboard?.writeText(secret).then(
              () => toast.success("Secret copied"),
              () => toast.error("Could not copy the secret"),
            );
          }}
        >
          Copy
        </Button>
        <Button size="sm" variant="ghost" onClick={onDismiss}>
          Done
        </Button>
      </div>
    </Card>
  );
}

function rolloutCopy(rollout: ComponentRollout): string {
  if (rollout === "auto") return "now auto-installs for everyone";
  if (rollout === "optin") return "is now opt-in for members";
  return "is now registered only";
}

function HeadStat({ label, value }: { readonly label: string; readonly value: string | number }) {
  return (
    <div>
      <div className="text-[0.65rem] font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">{label}</div>
      <div className="tabular mt-0.5 text-lg font-semibold text-[var(--color-fg)]">{value}</div>
    </div>
  );
}

function SectionHeading({ title, subtitle }: { readonly title: string; readonly subtitle: string }) {
  return (
    <div>
      <h3 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">{title}</h3>
      <p className="mt-1 text-sm leading-relaxed text-[var(--color-fg-muted)]">{subtitle}</p>
    </div>
  );
}

function EmptyCard({ title, hint }: { readonly title: string; readonly hint: string }) {
  return (
    <Card className="border-dashed px-4 py-8 text-center">
      <p className="text-sm font-medium text-[var(--color-fg)]">{title}</p>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{hint}</p>
    </Card>
  );
}
