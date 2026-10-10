// Shared Agents-page primitives (file-length split from Agents.tsx): core
// status vocabulary + header, exported for the agents/* component modules.
import { cn } from "@/lib/cn";
import { Badge } from "@/ui/Card";

export // A core's real status, distinct from the gateway's coarse "available" (which
// only means an endpoint address resolved, not that a daemon answered):
//   - "ready": the daemon responded and exposed at least one agent (provider);
//   - "starting": an address resolved but no agents came back yet — the daemon
//     is likely not running, so we guide the user to start it and re-scan;
//   - "offline": no address resolved at all (nothing to reach).
type CoreStatus = "ready" | "starting" | "offline";

export function coreStatus(available: boolean, providerCount: number): CoreStatus {
  if (providerCount > 0) return "ready";
  return available ? "starting" : "offline";
}

const STATUS_LABEL: Record<CoreStatus, string> = {
  ready: "✓ ready",
  starting: "not started",
  offline: "offline",
};

export function CoreHeader({
  name,
  status,
  label,
  builtin,
  right,
}: {
  name: string;
  status: CoreStatus;
  // A more precise label than the status vocabulary (e.g. a managed
  // sidecar's "starting…" / "sign-in required").
  label?: string;
  builtin: boolean;
  right?: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2">
      <span className="font-medium text-sm">{name || "unnamed"}</span>
      <span
        className={cn(
          "text-xs font-medium",
          status === "ready" ? "text-[var(--color-accent)]" : "text-[var(--color-fg-subtle)]",
        )}
      >
        {label || STATUS_LABEL[status]}
      </span>
      <Badge tone={builtin ? "neutral" : "accent"}>{builtin ? "built-in" : "extension"}</Badge>
      <div className="flex-1" />
      {right}
    </div>
  );
}


