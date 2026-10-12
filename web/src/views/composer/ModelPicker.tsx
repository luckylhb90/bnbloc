import { useRef, useState } from "react";
import { CaretDownIcon, SlidersHorizontalIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { useDismissOnOutside } from "@/ui/useDismissOnOutside";
import { prettyModel } from "@/lib/modelLabel";

// ModelPicker selects the model + reasoning effort for the effective agent.
// Mirrors AgentPicker's open/close, styling, and dropdown structure. Options are
// daemon-discovered (AgentTarget), never hardcoded. Empty model/reasoning means
// "inherit the agent's default"; the default row shows what that resolves to.
export function ModelPicker({
  models,
  reasonings,
  model,
  reasoning,
  defaultModel,
  defaultReasoning,
  onPickModel,
  onPickReasoning,
}: {
  models: string[];
  reasonings: string[];
  model: string;
  reasoning: string;
  defaultModel: string;
  defaultReasoning: string;
  onPickModel: (m: string) => void;
  onPickReasoning: (r: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useDismissOnOutside(open, ref, () => setOpen(false));

  // The label shows the effective selection: an explicit pick, else the agent's
  // discovered default (so the composer reflects the CLI config at a glance).
  const effModel = model || defaultModel;
  // Show the effective reasoning only when it is valid for the current model's
  // options (a config default like codex's "high" is meaningless for a model
  // whose ladder is ["none","on"], so it must not appear in the label).
  const effReasoning =
    reasoning ||
    (reasonings.includes(defaultReasoning) ? defaultReasoning : "");
  const modelLabel = effModel ? prettyModel(effModel) : "Default model";
  const reasoningLabel = effReasoning
    ? effReasoning.charAt(0).toUpperCase() + effReasoning.slice(1)
    : "";
  const label = reasoningLabel ? `${modelLabel} · ${reasoningLabel}` : modelLabel;

  const defaultRow = (
    activeWhenEmpty: string,
    onPick: () => void,
    suffix: string,
  ) => (
    <button
      onClick={onPick}
      className={cn(
        "flex w-full items-center gap-2 px-3 py-2 text-left text-sm",
        !activeWhenEmpty
          ? "bg-[var(--color-surface-2)] text-[var(--color-fg)]"
          : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
      )}
    >
      <span className="flex-1 font-medium text-[var(--color-fg)]">
        Default{suffix}
      </span>
      {!activeWhenEmpty && (
        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" />
      )}
    </button>
  );

  return (
    <div className="relative min-w-10 shrink-[20]" ref={ref}>
      {/* Icon-only on a narrow composer (the label is in the tooltip and the
          accessible name); labelled on a wide one. */}
      <button
        onClick={() => setOpen((o) => !o)}
        className={cn(
          "tactile flex h-10 min-w-10 max-w-full items-center justify-center gap-1.5 rounded-[var(--radius)] px-2 text-sm",
          "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
        )}
        title={`${label} — select model and reasoning effort`}
        aria-label={`Model: ${label}`}
        aria-haspopup="true"
        aria-expanded={open}
      >
        <SlidersHorizontalIcon size={18} className="shrink-0 text-[var(--color-accent)]" />
        <span className="hidden min-w-0 max-w-[12rem] truncate @md:inline">{label}</span>
        <CaretDownIcon size={12} className="hidden shrink-0 @md:block" />
      </button>

      {open && (
        <div className="animate-in absolute bottom-full right-0 z-20 mb-2 w-60 @md:left-0 @md:right-auto overflow-hidden rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]">
          {models.length > 0 && (
            <>
              <div className="border-b border-[var(--color-border)] px-3 py-2 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
                Model
              </div>
              <div className="max-h-56 overflow-y-auto py-1">
                {defaultRow(model, () => onPickModel(""), defaultModel ? ` · ${prettyModel(defaultModel)}` : "")}
                {models.map((m) => {
                  const active = m === model;
                  return (
                    <button
                      key={m}
                      onClick={() => onPickModel(m)}
                      className={cn(
                        "flex w-full items-center gap-2 px-3 py-2 text-left text-sm",
                        active
                          ? "bg-[var(--color-surface-2)] text-[var(--color-fg)]"
                          : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
                      )}
                    >
                      <span className="flex-1 font-medium text-[var(--color-fg)]">
                        {prettyModel(m)}
                      </span>
                      {active && (
                        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" />
                      )}
                    </button>
                  );
                })}
              </div>
            </>
          )}
          {reasonings.length > 0 && (
            <>
              <div className="border-t border-[var(--color-border)] px-3 py-2 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
                Reasoning
              </div>
              <div className="max-h-40 overflow-y-auto py-1">
                {defaultRow(reasoning, () => onPickReasoning(""), defaultReasoning ? ` · ${defaultReasoning}` : "")}
                {reasonings.map((lvl) => {
                  const active = lvl === reasoning;
                  return (
                    <button
                      key={lvl}
                      onClick={() => onPickReasoning(lvl)}
                      className={cn(
                        "flex w-full items-center gap-2 px-3 py-2 text-left text-sm capitalize",
                        active
                          ? "bg-[var(--color-surface-2)] text-[var(--color-fg)]"
                          : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
                      )}
                    >
                      <span className="flex-1">{lvl}</span>
                      {active && (
                        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" />
                      )}
                    </button>
                  );
                })}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}
