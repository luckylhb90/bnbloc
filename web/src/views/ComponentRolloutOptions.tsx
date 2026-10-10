import { CheckCircleIcon } from "@phosphor-icons/react";
import type { ComponentRollout } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Badge } from "@/ui/Card";

export const COMPONENT_ROLLOUT_OPTIONS = [
  {
    value: "auto",
    title: "Auto-install for everyone",
    description:
      "Every current and future member receives the component. Members cannot remove it from their catalog.",
    recommended: true,
  },
  {
    value: "optin",
    title: "Members opt in",
    description:
      "The component appears in Available to add. Only members with an install row receive artifacts or runtime access.",
  },
  {
    value: "off",
    title: "Register only",
    description:
      "Keep the package staged in the admin registry while hiding it from every member-facing catalog.",
  },
] satisfies readonly {
  readonly value: ComponentRollout;
  readonly title: string;
  readonly description: string;
  readonly recommended?: boolean;
}[];

export function ComponentRolloutOptions({
  value,
  onChange,
  disabled,
}: {
  readonly value: ComponentRollout;
  readonly onChange: (value: ComponentRollout) => void;
  readonly disabled?: boolean;
}) {
  return (
    <div className="grid gap-2" role="radiogroup" aria-label="Rollout policy">
      {COMPONENT_ROLLOUT_OPTIONS.map((option) => {
        const selected = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={selected}
            disabled={disabled}
            onClick={() => onChange(option.value)}
            className={cn(
              "tactile flex items-start gap-3 rounded-[var(--radius)] border px-3.5 py-3 text-left",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60",
              selected
                ? "border-[var(--color-accent)]/45 bg-[var(--color-accent-soft)]"
                : "border-[var(--color-border)] bg-[var(--color-surface)] hover:border-[var(--color-border-strong)]",
              disabled && "pointer-events-none opacity-60",
            )}
          >
            <span
              className={cn(
                "mt-0.5 grid size-5 shrink-0 place-items-center rounded-full border",
                selected ? "border-[var(--color-accent)] text-[var(--color-accent)]" : "border-[var(--color-border-strong)]",
              )}
            >
              {selected && <CheckCircleIcon size={13} weight="fill" />}
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold text-[var(--color-fg)]">
                  {option.title}
                </span>
                {option.recommended && <Badge tone="accent">Recommended</Badge>}
              </span>
              <span className="mt-0.5 block text-xs leading-relaxed text-[var(--color-fg-muted)]">
                {option.description}
              </span>
            </span>
          </button>
        );
      })}
    </div>
  );
}
