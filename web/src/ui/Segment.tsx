import { cn } from "@/lib/cn";

export interface SegmentOption<T extends string> {
  value: T;
  label: string;
  hint?: string;
}

export function Segment<T extends string>({
  options,
  value,
  onChange,
  className,
}: {
  options: readonly SegmentOption<T>[];
  value: T;
  onChange: (v: T) => void;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "grid gap-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-1",
        className,
      )}
      style={{ gridTemplateColumns: `repeat(${options.length}, minmax(0,1fr))` }}
      role="tablist"
    >
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            role="tab"
            aria-selected={active}
            onClick={() => onChange(o.value)}
            className={cn(
              "tactile rounded-[calc(var(--radius)-0.25rem)] px-3 py-2 text-sm text-center",
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
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: string;
  ariaLabel?: string;
}) {
  return (
    <button
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      onClick={() => onChange(!checked)}
      className="tactile inline-flex items-center gap-2"
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
