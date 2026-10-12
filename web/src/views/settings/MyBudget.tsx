import { useEffect, useState } from "react";
import { ApiError } from "@/lib/api";
import { budgetsApi } from "@/lib/budgetsApi";
import { describeLimits, periodLabel, type MyBudget as MyBudgetData } from "@/lib/budgets";
import { compactNumber, formatUsd } from "@/lib/format";
import { Card, Skeleton } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { BudgetBar } from "@/views/team/BudgetBar";

// MyBudget shows the member's monthly usage budget (set by an org admin) and
// how much of it their agents used this month. Runs other members borrow on
// the member's agents count here too.
export function MyBudget() {
  const [data, setData] = useState<MyBudgetData | null>(null);
  const [error, setError] = useState("");
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    budgetsApi
      .mine()
      .then(setData)
      .catch((e) => {
        // A deployment without budgets answers 501: nothing to show.
        if (e instanceof ApiError && e.status === 501) setUnavailable(true);
        else setError(e instanceof Error ? e.message : "Could not load your usage budget");
      });
  }, []);

  if (unavailable) return null;
  const budget = data?.budget ?? null;

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold">Usage budget</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          {data ? periodLabel(data.period) : "This month"} · usage your agents reported, including runs others borrowed.
        </p>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      {!data && !error && <Skeleton className="h-20" />}
      {data && (
        <Card className="flex flex-col gap-3 px-4 py-3">
          {budget ? (
            <>
              <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
                <span className="font-medium">{describeLimits(budget)}</span>
                <span className="text-xs text-[var(--color-fg-muted)]">
                  Warns at {budget.warnAtPercent}%{budget.hardStop ? " · agents stop at 100%" : ""}
                </span>
              </div>
              <BudgetBar view={budget} />
            </>
          ) : (
            <p className="text-sm text-[var(--color-fg-muted)]">
              No monthly budget is set for you. Your agents used{" "}
              <span className="tabular">{compactNumber(data.burn.tokens)}</span> tokens
              {data.burn.cost > 0 ? ` and ${formatUsd(data.burn.cost)}` : ""} this month.
            </p>
          )}
          {data.blocked && (
            <p className="text-sm text-[var(--color-danger)]">
              {data.blockedBy === "tenant"
                ? "The organization's monthly budget is reached: agent runs are stopped until next month or until an admin raises it."
                : "Your monthly budget is reached: your agents do not run until next month or until an admin raises it."}
            </p>
          )}
        </Card>
      )}
    </section>
  );
}
