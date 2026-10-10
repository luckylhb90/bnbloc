import { cn } from "@/lib/cn";
import { budgetTone, describeBurn, type BudgetView } from "@/lib/budgets";

// BudgetBar is a budget's burn this month: a bar coloured by the warning
// threshold, with the burn against each limit underneath.
export function BudgetBar({ view, className }: { readonly view: BudgetView; readonly className?: string }) {
  const tone = budgetTone(view);
  const width = Math.min(100, Math.max(0, view.percent));
  return (
    <div className={cn("flex flex-col gap-1", className)}>
      <div
        className="h-2 w-full overflow-hidden rounded-full bg-[var(--color-surface-2)]"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={width}
        aria-label="Budget used"
      >
        <div
          className={cn(
            "h-full rounded-full transition-[width]",
            tone === "over"
              ? "bg-[var(--color-danger)]"
              : tone === "warn"
                ? "bg-[var(--color-warning)]"
                : "bg-[var(--color-accent)]",
          )}
          style={{ width: `${width}%` }}
        />
      </div>
      <div className="flex flex-wrap justify-between gap-x-3 text-xs text-[var(--color-fg-muted)]">
        <span className="tabular">{describeBurn(view)}</span>
        <span className={cn("tabular", tone === "over" && "text-[var(--color-danger)]")}>{view.percent}% used</span>
      </div>
    </div>
  );
}
