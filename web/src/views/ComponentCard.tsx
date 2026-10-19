import { useState } from "react";
import { ShieldCheckIcon, TrashIcon } from "@phosphor-icons/react";
import type { ComponentRollout, MemberComponent, PlatformComponent } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { Segment } from "@/ui/Segment";
import { AdoptionBar, ArtifactFacts, ChipGroup, FactBlock, KindIcon, MCPFacts, PreviewerFacts, RolloutBadge, SettingsFacts, TrustBlock, memberMeta } from "@/views/componentFacts";
import { PlatformComponentSettings } from "@/views/PlatformComponentSettings";
import { ofScope } from "@/lib/componentSettings";
import { componentKindLabel } from "@/lib/componentKinds";
import { exactTime } from "@/lib/format";

const ROLLOUT_SEGMENTS = [
  { value: "auto", label: "Auto" },
  { value: "optin", label: "Opt-in" },
  { value: "off", label: "Off" },
] satisfies readonly { readonly value: ComponentRollout; readonly label: string }[];

type AdminCardProps = {
  readonly component: PlatformComponent;
  readonly mode: "admin";
  readonly busy?: boolean;
  readonly onSetRollout?: (component: PlatformComponent, rollout: ComponentRollout) => void;
  readonly onUninstall?: (component: PlatformComponent) => void;
  readonly onRotateSecret?: (component: PlatformComponent) => void;
  readonly onSendTest?: (component: PlatformComponent) => void;
};

type MemberCardProps = {
  readonly component: MemberComponent;
  readonly mode: "member";
  readonly busy?: boolean;
  readonly onInstall?: (component: MemberComponent) => void;
  readonly onRemove?: (component: MemberComponent) => void;
};

export function ComponentCard(props: AdminCardProps | MemberCardProps) {
  if (props.mode === "admin") {
    return (
      <AdminComponentCard
        component={props.component}
        busy={Boolean(props.busy)}
        onSetRollout={props.onSetRollout}
        onUninstall={props.onUninstall}
        onRotateSecret={props.onRotateSecret}
        onSendTest={props.onSendTest}
      />
    );
  }
  return (
    <MemberComponentCard
      component={props.component}
      busy={Boolean(props.busy)}
      onInstall={props.onInstall}
      onRemove={props.onRemove}
    />
  );
}

function AdminComponentCard({
  component,
  busy,
  onSetRollout,
  onUninstall,
  onRotateSecret,
  onSendTest,
}: {
  readonly component: PlatformComponent;
  readonly busy: boolean;
  readonly onSetRollout?: (component: PlatformComponent, rollout: ComponentRollout) => void;
  readonly onUninstall?: (component: PlatformComponent) => void;
  readonly onRotateSecret?: (component: PlatformComponent) => void;
  readonly onSendTest?: (component: PlatformComponent) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  return (
    <Card
      className={cn(
        "animate-in p-4 transition-opacity",
        component.rollout === "off" && "opacity-70",
        !component.trust.signed && "border-[var(--color-danger)]/60 bg-[var(--color-danger)]/5",
      )}
      id={`component-${component.name}`}
    >
      <ComponentHeader component={component} />
      <AdoptionBar component={component} />
      <div className="mt-4 grid gap-3 md:grid-cols-2">
        <TrustBlock trust={component.trust} />
        <FactBlock label="Publisher" value={component.publisher || "unknown publisher"} />
        <FactBlock label="Kind" value={componentKindLabel(component.kind)} />
        <FactBlock label="Runtime" value={component.runtime} />
        {component.protocol && <FactBlock label="Protocol" value={`${component.protocol.min}-${component.protocol.max}`} />}
        {component.installedAtMs && <FactBlock label="Installed" value={exactTime(component.installedAtMs)} />}
      </div>
      <ChipGroup label="Capabilities" values={component.capabilities ?? []} empty="No capabilities recorded." />
      <ChipGroup label="Hooks" values={component.hooks ?? []} empty="No event hooks declared." />
      <ChipGroup
        label="Declared network permissions"
        values={component.permissions?.network ?? []}
        empty="No network permissions declared."
        warning
      />
      {(component.permissions?.host?.length ?? 0) > 0 && (
        <ChipGroup
          label="Desktop host API (acts as the member)"
          values={component.permissions?.host ?? []}
          empty=""
          warning
        />
      )}
      <PreviewerFacts component={component} />
      <MCPFacts component={component} />
      <SettingsFacts component={component} />
      <ArtifactFacts component={component} />
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-border)] pt-4">
        <div className="min-w-[15rem] flex-1">
          <Segment
            options={ROLLOUT_SEGMENTS}
            value={component.rollout}
            onChange={(rollout) => onSetRollout?.(component, rollout)}
          />
        </div>
        {component.kind === "notifier" && (
          <div className="flex gap-2">
            <Button variant="outline" size="sm" disabled={busy} onClick={() => onSendTest?.(component)}>
              Send test notification
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => onRotateSecret?.(component)}>
              Rotate secret
            </Button>
          </div>
        )}
        {ofScope(component.settings, "platform").length > 0 && (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => setSettingsOpen(true)}>
            Settings
          </Button>
        )}
        <Button variant="danger" size="sm" disabled={busy} onClick={() => setConfirming(true)}>
          <TrashIcon size={14} />
          {busy ? "Working…" : "Uninstall"}
        </Button>
      </div>
      {settingsOpen && <PlatformComponentSettings component={component} onClose={() => setSettingsOpen(false)} />}
      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => onUninstall?.(component)}
        title={`Uninstall ${component.name}?`}
        description="This removes the package from the registry and withdraws it from every member catalog."
        confirmLabel="Uninstall component"
        danger
      />
    </Card>
  );
}

function MemberComponentCard({
  component,
  busy,
  onInstall,
  onRemove,
}: {
  readonly component: MemberComponent;
  readonly busy: boolean;
  readonly onInstall?: (component: MemberComponent) => void;
  readonly onRemove?: (component: MemberComponent) => void;
}) {
  return (
    <Card className="animate-in flex flex-wrap items-start gap-3.5 p-4">
      <KindIcon kind={component.kind} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-[0.95rem] font-semibold text-[var(--color-fg)]">{component.name}</h3>
          <Badge tone="neutral" className="tabular">v{component.version}</Badge>
          {component.managed && <Badge tone="accent">Auto-installed</Badge>}
          {!component.managed && component.installed && <Badge tone="neutral">You installed this</Badge>}
        </div>
        <p className="mt-1 text-sm leading-relaxed text-[var(--color-fg-muted)]">
          {component.description || "No description provided."}
        </p>
        <p className="mt-1.5 text-xs text-[var(--color-fg-subtle)]">{memberMeta(component)}</p>
        {component.hooks && component.hooks.length > 0 && (
          <ChipGroup label="Hooks" values={component.hooks} compact />
        )}
      </div>
      <div className="ml-auto flex shrink-0 items-center gap-2">
        {memberAction(component, busy, onInstall, onRemove)}
      </div>
    </Card>
  );
}

function ComponentHeader({ component }: { readonly component: PlatformComponent }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="flex min-w-0 gap-3">
        <KindIcon kind={component.kind} />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="truncate text-base font-semibold text-[var(--color-fg)]">{component.name}</h3>
            <Badge tone="neutral" className="tabular">v{component.version}</Badge>
            <Badge tone={component.kind === "plugin" ? "neutral" : "accent"}>{componentKindLabel(component.kind)}</Badge>
            {component.requiresDesktop && <Badge tone="warning">Desktop sidecar</Badge>}
            <RolloutBadge rollout={component.rollout} />
          </div>
          <p className="mt-1 text-sm leading-relaxed text-[var(--color-fg-muted)]">
            {component.description || "No description provided."}
          </p>
        </div>
      </div>
    </div>
  );
}

function memberAction(
  component: MemberComponent,
  busy: boolean,
  onInstall?: (component: MemberComponent) => void,
  onRemove?: (component: MemberComponent) => void,
) {
  if (busy) return <span className="text-xs text-[var(--color-fg-subtle)]">Working…</span>;
  if (component.managed) {
    return <span className="inline-flex items-center gap-1.5 text-xs text-[var(--color-fg-subtle)]"><ShieldCheckIcon size={13} className="text-[var(--color-accent)]" />Managed by your admin</span>;
  }
  if (component.installed) {
    return <Button size="sm" variant="outline" onClick={() => onRemove?.(component)}><TrashIcon size={14} />Remove</Button>;
  }
  return <Button size="sm" variant="primary" onClick={() => onInstall?.(component)}>Install</Button>;
}
