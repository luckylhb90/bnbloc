import { cn } from "@/lib/cn";

export function Card({
  className,
  interactive,
  ...props
}: React.HTMLAttributes<HTMLDivElement> & { interactive?: boolean }) {
  return (
    <div
      className={cn(
        "rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]",
        interactive &&
          "tactile cursor-pointer hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-2)]",
        className,
      )}
      {...props}
    />
  );
}

export function Badge({
  className,
  tone = "neutral",
  ...props
}: React.HTMLAttributes<HTMLSpanElement> & {
  tone?: "neutral" | "accent" | "danger" | "warning";
}) {
  const tones = {
    neutral:
      "bg-[var(--color-surface-2)] text-[var(--color-fg-muted)] border-[var(--color-border)]",
    accent:
      "bg-[var(--color-accent-soft)] text-[var(--color-accent)] border-[var(--color-accent)]/30",
    danger: "bg-[var(--color-danger)]/15 text-[var(--color-danger)] border-[var(--color-danger)]/30",
    warning:
      "bg-[var(--color-warning)]/15 text-[var(--color-warning)] border-[var(--color-warning)]/30",
  };
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium",
        tones[tone],
        className,
      )}
      {...props}
    />
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("skeleton rounded-[var(--radius)]", className)} />;
}

// Dot is a status light: it pulses (something is live) unless still.
export function Dot({ tone = "accent", still = false }: { tone?: "accent" | "muted" | "danger"; still?: boolean }) {
  const c = {
    accent: "bg-[var(--color-accent)]",
    muted: "bg-[var(--color-fg-subtle)]",
    danger: "bg-[var(--color-danger)]",
  }[tone];
  return <span className={cn("inline-block size-2 shrink-0 rounded-full", !still && "pulse-dot", c)} />;
}

// EmptyCard is a dashed placeholder card: a title and a hint.
export function EmptyCard({ title, hint }: { readonly title: string; readonly hint: string }) {
  return (
    <Card className="border-dashed px-4 py-8 text-center">
      <p className="text-sm font-medium text-[var(--color-fg)]">{title}</p>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{hint}</p>
    </Card>
  );
}

// StatTile is one figure of a dashboard row: a label, the value and an
// optional note beside it.
export function StatTile({ label, value, sub }: { readonly label: string; readonly value: string; readonly sub?: string }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3">
      <div className="text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">{label}</div>
      <div className="mt-1 flex items-baseline gap-1">
        <span className="text-lg font-semibold tabular">{value}</span>
        {sub && <span className="text-xs text-[var(--color-fg-subtle)]">{sub}</span>}
      </div>
    </div>
  );
}
