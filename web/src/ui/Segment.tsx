import { cn } from "@/lib/cn";

export interface SegmentOption<T extends string> {
  value: T;
  label: string;
  hint?: string;
}

// Segment is a row of exclusive choices. size "sm" is the compact form for
// a toolbar (a feed's header).
export function Segment<T extends string>({
  options,
  value,
  onChange,
  className,
  size = "md",
  ariaLabel,
}: {
  options: readonly SegmentOption<T>[];
  value: T;
  onChange: (v: T) => void;
  className?: string;
  size?: "md" | "sm";
  ariaLabel?: string;
}) {
  const sm = size === "sm";
  return (
    <div
      className={cn(
        "grid rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)]",
        sm ? "gap-0.5 p-0.5" : "gap-1 p-1",
        className,
      )}
      style={{ gridTemplateColumns: `repeat(${options.length}, minmax(0,1fr))` }}
      role="tablist"
      aria-label={ariaLabel}
    >
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="tab"
            aria-selected={active}
            title={o.label}
            onClick={() => onChange(o.value)}
            className={cn(
              "tactile min-w-0 truncate rounded-[calc(var(--radius)-0.25rem)] text-center focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60",
              sm ? "px-2 py-0.5 text-xs" : "px-2 py-2 text-sm sm:px-3",
              active
                ? "bg-[var(--color-elevated)] text-[var(--color-fg)] shadow-[var(--shadow-pop)]"
                : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
            )}
          >
            <span className="font-medium">{o.label}</span>
            {o.hint && (
              <span className="ml-1 text-[var(--color-fg-subtle)]">· {o.hint}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

export function Switch({
  checked,
  onChange,
  label,
  ariaLabel,
  disabled = false,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: string;
  ariaLabel?: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className="tactile inline-flex items-center gap-2 disabled:cursor-not-allowed disabled:opacity-60"
    >
      <span
        className={cn(
          "relative h-5 w-9 rounded-full transition-colors",
          checked ? "bg-[var(--color-accent)]" : "bg-[var(--color-border-strong)]",
        )}
      >
        <span
          className={cn(
            "absolute top-0.5 left-0.5 size-4 rounded-full bg-white transition-transform",
            checked && "translate-x-4",
          )}
        />
      </span>
      {label && <span className="text-sm text-[var(--color-fg-muted)]">{label}</span>}
    </button>
  );
}
