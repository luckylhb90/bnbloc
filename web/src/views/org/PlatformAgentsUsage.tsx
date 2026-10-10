import type { ReactNode } from "react";
import { CaretDownIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { compactNumber, exactTime, relativeTime } from "@/lib/format";
import { platformAgentsApi } from "@/lib/platformAgentsApi";
import { roomLabel, runModelLabel, usageSummary, usageTiles, type PlatformUsageTotals } from "@/lib/platformUsage";
import { formatCost } from "@/lib/teamDashboard";
import { useQuery } from "@/lib/useQuery";
import { Button } from "@/ui/Button";
import { ErrorBox } from "@/ui/ErrorBox";
import { UserName } from "@/views/UserName";

// PlatformAgentsUsagePanel is the platform agents' usage dashboard (PRD
// 2026-10-10 v2 §2): today's and this month's totals, then expandable top
// members, top rooms and recent runs. Cost is what the bridges report (0 for
// unpriced/trial models).
export function PlatformAgentsUsagePanel() {
  const { subject } = useAuth();
  const query = useQuery(subject ? `platform-agents-usage:${subject.toLowerCase()}` : null, platformAgentsApi.usage, {
    staleMs: 30_000,
  });
  const usage = query.data;
  return (
    <div className="flex flex-col gap-3 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3 text-sm">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Usage</h3>
        <Button variant="ghost" size="sm" onClick={query.refetch} disabled={query.loading}>
          Refresh
        </Button>
      </div>
      {query.error && !usage ? (
        <ErrorBox>{query.error instanceof Error ? query.error.message : "Could not load usage"}</ErrorBox>
      ) : !usage ? (
        <p className="text-xs text-[var(--color-fg-subtle)]">Loading usage…</p>
      ) : (
        <>
          {usage.truncated && (
            <p className="text-xs text-[var(--color-warning)]">
              There are more runs than the dashboard reads at once: the figures cover the most recent runs only.
            </p>
          )}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Period title="Today" totals={usage.today} />
            <Period title="This month" totals={usage.month} />
          </div>
          <Expandable title="Top members" hint="this month" count={usage.topMembers.length}>
            <Table
              head={["Member", "Runs", "Tokens", "Cost"]}
              rows={usage.topMembers.map((m) => [<UserName key="m" id={m.memberId} />, compactNumber(m.runs), compactNumber(m.tokens), formatCost(m.cost)])}
              empty="No runs this month."
            />
          </Expandable>
          <Expandable title="Top rooms" hint="this month" count={usage.topRooms.length}>
            <Table
              head={["Room", "Organization", "Runs", "Tokens", "Cost"]}
              rows={usage.topRooms.map((r) => [roomLabel(r), r.tenantId, compactNumber(r.runs), compactNumber(r.tokens), formatCost(r.cost)])}
              empty="No runs this month."
            />
          </Expandable>
          <Expandable title="Recent runs" hint="last 20" count={usage.recent.length}>
            <Table
              head={["When", "Member", "Room", "Agent", "Host", "Model", "Cost"]}
              rows={usage.recent.map((r) => [
                <span key="t" title={exactTime(r.atMs)}>
                  {relativeTime(r.atMs)}
                </span>,
                <UserName key="m" id={r.memberId} />,
                roomLabel(r),
                r.agent,
                <UserName key="h" id={r.hostUserId} />,
                runModelLabel(r.model),
                formatCost(r.cost),
              ])}
              empty="No runs yet."
            />
          </Expandable>
        </>
      )}
    </div>
  );
}

function Period({ title, totals }: { readonly title: string; readonly totals: PlatformUsageTotals }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-2" aria-label={`${title}: ${usageSummary(totals)}`}>
      <p className="text-xs font-medium text-[var(--color-fg-muted)]">{title}</p>
      <dl className="mt-1 grid grid-cols-5 gap-2">
        {usageTiles(totals).map((t) => (
          <div key={t.label} className="min-w-0">
            <dt className="truncate text-[10px] uppercase tracking-wide text-[var(--color-fg-subtle)]">{t.label}</dt>
            <dd className="tabular truncate text-sm font-semibold text-[var(--color-fg)]">{t.value}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function Expandable({
  title,
  hint,
  count,
  children,
}: {
  readonly title: string;
  readonly hint: string;
  readonly count: number;
  readonly children: ReactNode;
}) {
  return (
    <details className="group rounded-[var(--radius)] border border-[var(--color-border)]">
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-sm text-[var(--color-fg)] [&::-webkit-details-marker]:hidden">
        <span className="font-medium">{title}</span>
        <span className="text-xs text-[var(--color-fg-subtle)]">
          {hint} · {count}
        </span>
        <CaretDownIcon size={12} className="ml-auto shrink-0 transition-transform group-open:rotate-180" />
      </summary>
      <div className="border-t border-[var(--color-border)]">{children}</div>
    </details>
  );
}

function Table({ head, rows, empty }: { readonly head: readonly string[]; readonly rows: readonly ReactNode[][]; readonly empty: string }) {
  if (rows.length === 0) return <p className="px-3 py-2 text-xs text-[var(--color-fg-subtle)]">{empty}</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs">
        <thead className="text-[var(--color-fg-subtle)]">
          <tr>
            {head.map((h) => (
              <th key={h} className="whitespace-nowrap px-3 py-1.5 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-[var(--color-border)] text-[var(--color-fg-muted)]">
          {rows.map((cells, i) => (
            <tr key={i}>
              {cells.map((c, j) => (
                <td key={j} className="whitespace-nowrap px-3 py-1.5 tabular">
                  {c}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
