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

export function Dot({ tone = "accent" }: { tone?: "accent" | "muted" | "danger" }) {
  const c = {
    accent: "bg-[var(--color-accent)]",
    muted: "bg-[var(--color-fg-subtle)]",
    danger: "bg-[var(--color-danger)]",
  }[tone];
  return <span className={cn("inline-block size-2 rounded-full pulse-dot", c)} />;
}
