import { useState } from "react";
import { CaretRightIcon } from "@phosphor-icons/react";
import type { TurnMetrics } from "@/lib/api";
import { compactNumber } from "@/lib/format";
import { cn } from "@/lib/cn";

export function ThinkingDisclosure({
  thinking,
  metrics,
}: {
  readonly thinking: string;
  readonly metrics?: TurnMetrics;
}) {
  const [open, setOpen] = useState(false);
  const tokens = metrics && metrics.thinkingTokens > 0 ? compactNumber(metrics.thinkingTokens) : "";
  const label = tokens ? `Thought for ${tokens} tokens` : "Thinking";
  return (
    <div className="mb-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)]/60">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="tactile flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-xs font-medium text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
      >
        <CaretRightIcon
          size={12}
          className={cn("shrink-0 transition-transform duration-150", open && "rotate-90")}
        />
        <span>{label}</span>
      </button>
      {open && (
        <pre className="max-h-80 overflow-auto border-t border-[var(--color-border)] px-2.5 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap text-[var(--color-fg-subtle)] [overflow-wrap:anywhere]">
          {thinking}
        </pre>
      )}
    </div>
  );
}
