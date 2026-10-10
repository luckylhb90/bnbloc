import { ArrowRightIcon, CheckCircleIcon, XCircleIcon } from "@phosphor-icons/react";
import type { ComponentValidationStep } from "@/lib/api";

export function ComponentValidationTimeline({ steps }: { readonly steps: readonly ComponentValidationStep[] }) {
  return (
    <div className="flex flex-col gap-1.5 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-3">
      {steps.map((step) => (
        <div key={step.id} className="animate-in flex items-start gap-2.5">
          <span className={stepIconClass(step.state)}>{stepIcon(step.state)}</span>
          <div className="min-w-0">
            <div className={step.state === "failed" ? "text-sm font-medium text-[var(--color-danger)]" : "text-sm font-medium text-[var(--color-fg)]"}>{step.label}</div>
            <div className="text-xs leading-relaxed text-[var(--color-fg-subtle)]">{step.detail}</div>
          </div>
        </div>
      ))}
    </div>
  );
}

function stepIconClass(state: ComponentValidationStep["state"]): string {
  return state === "failed" ? "mt-0.5 grid size-5 shrink-0 place-items-center text-[var(--color-danger)]" : state === "pending" ? "mt-0.5 grid size-5 shrink-0 place-items-center text-[var(--color-fg-subtle)]" : "mt-0.5 grid size-5 shrink-0 place-items-center text-[var(--color-accent)]";
}

function stepIcon(state: ComponentValidationStep["state"]) {
  if (state === "done") return <CheckCircleIcon size={16} weight="fill" />;
  if (state === "failed") return <XCircleIcon size={16} weight="fill" />;
  return <ArrowRightIcon size={14} className={state === "running" ? "pulse-dot" : undefined} />;
}
