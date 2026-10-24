import { cn } from "@/lib/cn";
import { compactNumber } from "@/lib/format";
import { contextLevel, type ContextUsage } from "@/lib/contextUsage";

// ContextMeter shows how full the addressed agent's conversation is: a small
// bar, and the percentage when there is room. It is "after the last reply" (it
// does not move while the agent works), says so in its tooltip, and is absent
// unless the agent reported both what it holds and its window: never "0%".
export function ContextMeter({ usage }: { usage: ContextUsage | null }) {
  if (!usage) return null;
  const level = contextLevel(usage.percent);
  const label = `Context after the last reply: ${compactNumber(usage.tokens)} of ${compactNumber(usage.window)} tokens (${usage.percent}%)${usage.model ? ` · ${usage.model}` : ""}`;
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className="flex h-10 shrink-0 items-center gap-1.5 px-1.5 text-xs tabular text-[var(--color-fg-muted)]"
    >
      <span className="h-1.5 w-8 overflow-hidden rounded-full bg-[var(--color-surface-2)]">
        <span
          className={cn(
            "block h-full rounded-full",
            level === "danger"
              ? "bg-[var(--color-danger)]"
              : level === "warn"
                ? "bg-[var(--color-warning)]"
                : "bg-[var(--color-accent)]",
          )}
          style={{ width: `${Math.max(usage.percent, 2)}%` }}
        />
      </span>
      <span className={cn("hidden @md:inline", level === "danger" && "text-[var(--color-danger)]", level === "warn" && "text-[var(--color-warning)]")}>
        {usage.percent}%
      </span>
    </span>
  );
}
