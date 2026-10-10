import { useRef, type KeyboardEvent } from "react";
import { RobotIcon, UsersIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";

export type SendMode = "agent" | "people";

const OPTIONS: readonly { readonly mode: SendMode; readonly label: string; readonly hint: string }[] = [
  { mode: "agent", label: "Agent", hint: "Send to the agent — it replies" },
  { mode: "people", label: "People", hint: "Send to people — the agent reads it but won't reply" },
];

// SendModeToggle is the composer's "To: Agent | People" segment: an explicit
// two-option radio group (arrow keys move between options, roving tabindex).
// Icon-only on a narrow composer, labelled on a wide one; the tooltip and
// accessible name always say what each mode does.
export function SendModeToggle({ value, onChange }: { value: SendMode; onChange: (mode: SendMode) => void }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    const at = OPTIONS.findIndex((o) => o.mode === value);
    const next =
      e.key === "Home" ? 0 : e.key === "End" ? OPTIONS.length - 1 : (at + (e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 1) + OPTIONS.length) % OPTIONS.length;
    onChange(OPTIONS[next].mode);
    refs.current[next]?.focus();
  };
  return (
    <div
      role="radiogroup"
      aria-label="Send to"
      onKeyDown={onKeyDown}
      className="flex shrink-0 items-center rounded-[var(--radius)] bg-[var(--color-surface-2)] p-0.5"
    >
      {OPTIONS.map((o, i) => {
        const active = o.mode === value;
        const Icon = o.mode === "agent" ? RobotIcon : UsersIcon;
        return (
          <button
            key={o.mode}
            ref={(el) => {
              refs.current[i] = el;
            }}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={o.hint}
            title={o.hint}
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(o.mode)}
            className={cn(
              "tactile flex h-9 min-w-9 items-center justify-center gap-1.5 rounded-[calc(var(--radius)-2px)] px-2 text-xs font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60",
              active
                ? "bg-[var(--color-elevated)] text-[var(--color-fg)] shadow-sm"
                : "text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]",
            )}
          >
            <Icon size={18} weight={active ? "fill" : "regular"} />
            <span className="hidden @md:inline">{o.label}</span>
          </button>
        );
      })}
    </div>
  );
}
