import { CpuIcon, EyeIcon, PackageIcon, PlugsConnectedIcon, ShieldCheckIcon, WarningCircleIcon, WrenchIcon } from "@phosphor-icons/react";
import type { ComponentRollout, MemberComponent, PlatformComponent } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Badge } from "@/ui/Card";

export function KindIcon({ kind }: { readonly kind: string }) {
  const Icon = kind === "core" ? CpuIcon : kind === "plugin" ? PlugsConnectedIcon : kind === "previewer" ? EyeIcon : kind === "mcp" ? WrenchIcon : PackageIcon;
  return (
    <div className="grid size-9 shrink-0 place-items-center rounded-[var(--radius)] bg-[var(--color-surface-2)] text-[var(--color-accent)]">
      <Icon size={18} weight={kind === "core" ? "regular" : "fill"} />
    </div>
  );
}

export function RolloutBadge({ rollout }: { readonly rollout: ComponentRollout }) {
  switch (rollout) {
    case "auto":
      return <Badge tone="accent">Auto-install</Badge>;
    case "optin":
      return <Badge tone="neutral">Opt-in</Badge>;
    case "off":
      return <Badge tone="warning">Off</Badge>;
  }
}

export function AdoptionBar({ component }: { readonly component: PlatformComponent }) {
  const total = component.adoption.totalMembers ?? 0;
  const installs = component.adoption.memberInstalls;
  const pct = adoptionPercent(component.rollout, installs, total);
  return (
    <div className="mt-4">
      <div className="flex items-center justify-between text-xs text-[var(--color-fg-subtle)]">
        <span>{adoptionLabel(component.rollout, installs, total)}</span>
        {pct !== null && <span className="tabular">{pct}%</span>}
      </div>
      <div className="mt-1 h-1 overflow-hidden rounded-full bg-[var(--color-surface-2)]">
        <div
          className={cn("h-full rounded-full transition-all", component.rollout === "auto" ? "bg-[var(--color-accent)]" : "bg-[var(--color-fg-subtle)]")}
          style={{ width: `${pct ?? 0}%` }}
        />
      </div>
    </div>
  );
}

export function TrustBlock({ trust }: { readonly trust: PlatformComponent["trust"] }) {
  return (
    <div className={cn("rounded-[var(--radius)] border p-3", trust.signed ? "border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)]" : "border-[var(--color-danger)]/40 bg-[var(--color-danger)]/10")}>
      <div className="flex items-center gap-2">
        {trust.signed ? <ShieldCheckIcon size={16} weight="fill" className="text-[var(--color-accent)]" /> : <WarningCircleIcon size={16} weight="fill" className="text-[var(--color-danger)]" />}
        <span className={cn("text-sm font-semibold", trust.signed ? "text-[var(--color-accent)]" : "text-[var(--color-danger)]")}>{trust.signed ? "Trusted signature" : "Unsigned or untrusted"}</span>
      </div>
      <p className="mt-2 break-all text-xs text-[var(--color-fg-muted)]">Key: {trust.keyId || "missing"}</p>
      {trust.contentHash && <p className="mt-1 break-all text-xs text-[var(--color-fg-subtle)]">Hash: {trust.contentHash}</p>}
    </div>
  );
}

export function FactBlock({ label, value }: { readonly label: string; readonly value: string }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
      <div className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">{label}</div>
      <div className="mt-1 break-words text-sm text-[var(--color-fg)]">{value}</div>
    </div>
  );
}

export function ChipGroup({ label, values, empty, warning, compact }: { readonly label: string; readonly values: readonly string[]; readonly empty?: string; readonly warning?: boolean; readonly compact?: boolean }) {
  return (
    <div className={compact ? "mt-3" : "mt-4"}>
      <div className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">{label}</div>
      <div className="mt-2 flex flex-wrap gap-2">
        {values.length > 0 ? values.map((value) => <Badge key={value} tone={warning ? "warning" : "neutral"} className="break-all">{value}</Badge>) : <span className="text-xs text-[var(--color-fg-subtle)]">{empty}</span>}
      </div>
    </div>
  );
}

export function ArtifactFacts({ component }: { readonly component: PlatformComponent }) {
  const entries = Object.entries(component.artifacts ?? {});
  if (entries.length === 0 && !component.sidecar) return null;
  return (
    <div className="mt-4 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
      <div className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Artifacts and runtime</div>
      {component.sidecar && <p className="mt-1 break-all text-xs text-[var(--color-fg-muted)]">Sidecar: {component.sidecar.localUrl}{component.sidecar.listenEnv ? ` via ${component.sidecar.listenEnv}` : ""}</p>}
      <div className="mt-2 grid gap-1">
        {entries.map(([platform, artifact]) => (
          <p key={platform} className="break-all text-xs text-[var(--color-fg-subtle)]">
            <span className="tabular text-[var(--color-fg-muted)]">{platform}</span> · sha256 {artifact.sha256}
          </p>
        ))}
      </div>
    </div>
  );
}

// PreviewerFacts: what a previewer component renders (its match), where it
// ranks, and what it may do (capabilities, toolbar). It never has network
// access, so there is no permission list to review.
export function PreviewerFacts({ component }: { readonly component: PlatformComponent }) {
  const p = component.previewer;
  if (!p) return null;
  const match = [...(p.match.mimeTypes ?? []), ...(p.match.extensions ?? [])];
  return (
    <div className="mt-4 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
      <div className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Previewer</div>
      <p className="mt-1 break-all text-xs text-[var(--color-fg-muted)]">
        Entry {p.entry} · rank {p.rank} · sandboxed frame, no network
      </p>
      <ChipGroup label="Opens" values={match} empty="Nothing declared." compact />
      <ChipGroup label="Previewer capabilities" values={p.capabilities ?? []} empty="None declared." compact />
      {(p.toolbar?.length ?? 0) > 0 && <ChipGroup label="Toolbar" values={(p.toolbar ?? []).map((t) => `${t.label} (${t.kind})`)} compact />}
    </div>
  );
}

// MCPFacts: what an MCP tool offers and needs: its tools, transport, the
// variables it takes and where their values come from.
export function MCPFacts({ component }: { readonly component: PlatformComponent }) {
  const m = component.mcp;
  if (!m) return null;
  const transport = component.runtime === "endpoint" ? `http · ${component.endpoint?.baseUrl ?? ""}` : "stdio · spawned by the agent per session";
  const credentials = m.credentials === "org" ? "org admins configure one connector per organization" : m.credentials === "member" ? "each member sets them on their desktop" : "none";
  return (
    <div className="mt-4 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
      <div className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">MCP tool</div>
      <p className="mt-1 break-all text-xs text-[var(--color-fg-muted)]">{transport}</p>
      <ChipGroup label="Tools" values={m.tools.map((t) => t.name)} empty="None declared." compact />
      <ChipGroup label={`Variables (${credentials})`} values={(m.env ?? []).map((e) => `${e.name}${e.secret ? " (secret)" : ""}${e.required ? " *" : ""}`)} empty="None." compact />
    </div>
  );
}

export function memberMeta(component: MemberComponent): string {
  const facts = [
    component.kind,
    component.runtime,
    component.requiresDesktop ? "runs with the desktop app" : undefined,
    !component.installed && (component.memberInstalls ?? 0) > 0
      ? `${component.memberInstalls} member${component.memberInstalls === 1 ? " uses" : "s use"} it`
      : undefined,
  ];
  return facts.filter((fact): fact is string => Boolean(fact)).join(" · ");
}

function adoptionPercent(rollout: ComponentRollout, installs: number, total: number): number | null {
  if (rollout === "auto") return 100;
  if (rollout === "off") return 0;
  if (total <= 0) return null;
  return Math.min(100, Math.round((installs / total) * 100));
}

function adoptionLabel(rollout: ComponentRollout, installs: number, total: number): string {
  if (rollout === "auto") return total > 0 ? `All ${total} members get it` : "Auto-installed for every member";
  if (rollout === "off") return "Registered only — hidden from members";
  return total > 0 ? `${installs} of ${total} members installed` : `${installs} member${installs === 1 ? "" : "s"} installed`;
}
