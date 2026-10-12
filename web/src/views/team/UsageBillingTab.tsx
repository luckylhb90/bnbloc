import { useCallback, useEffect, useState } from "react";
import { DownloadSimpleIcon, ReceiptIcon } from "@phosphor-icons/react";
import { api, ApiError } from "@/lib/api";
import {
  currentMonth,
  formatTokens,
  type BorrowUsageReport,
  type UsageAs,
  type UsageGroupBy,
} from "@/lib/teamDashboard";
import { formatUsd } from "@/lib/format";
import { Button } from "@/ui/Button";
import { Skeleton } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Input } from "@/ui/Input";
import { EmptyState } from "@/ui/Page";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { QuotaRequestsPanel } from "@/views/team/QuotaRequestsPanel";
import { UserName } from "@/views/UserName";

const GROUPS = [
  { value: "day" as const, label: "By day" },
  { value: "month" as const, label: "By month" },
];

// UsageBillingTab is borrow billing: who borrowed whose agents and how often
// (and at what cost, when reported), for a month, grouped by day or month,
// with CSV export; plus the quota requests addressed to the member and those
// they made. Org admins also get the whole-organization report.
export function UsageBillingTab({
  canSeeTeam,
  onOpenRoom,
}: {
  readonly canSeeTeam: boolean;
  readonly onOpenRoom: (id: string) => void;
}) {
  const toast = useToast();
  const [month, setMonth] = useState(currentMonth());
  const [groupBy, setGroupBy] = useState<UsageGroupBy>("day");
  const [as, setAs] = useState<UsageAs>("owner");
  const [report, setReport] = useState<BorrowUsageReport | null>(null);
  const [error, setError] = useState("");
  const [downloading, setDownloading] = useState(false);

  const perspectives = [
    { value: "owner" as const, label: "My agents" },
    { value: "borrower" as const, label: "My borrows" },
    ...(canSeeTeam ? [{ value: "tenant" as const, label: "Organization" }] : []),
  ];

  const load = useCallback(() => {
    setReport(null);
    api
      .borrowUsageReport({ as, groupBy, month })
      .then((r) => {
        setReport(r);
        setError("");
      })
      .catch((e) => setError(e instanceof ApiError ? e.message : "Could not load the usage report"));
  }, [as, groupBy, month]);
  useEffect(load, [load]);

  const download = async () => {
    setDownloading(true);
    try {
      await api.downloadBorrowUsageCsv({ as, groupBy, month });
    } catch (e) {
      toast.error(e instanceof ApiError ? `CSV export failed: ${e.message}` : "CSV export failed");
    } finally {
      setDownloading(false);
    }
  };

  const hasCost = report?.totalCost !== undefined;
  const hasTokens = report?.totalInputTokens !== undefined || report?.totalOutputTokens !== undefined;

  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="month"
            aria-label="Month"
            value={month}
            max={currentMonth()}
            onChange={(e) => e.target.value && setMonth(e.target.value)}
            className="h-9 w-40"
          />
          <Segment options={GROUPS} value={groupBy} onChange={setGroupBy} className="w-52" />
          <Segment options={perspectives} value={as} onChange={setAs} className={canSeeTeam ? "w-80" : "w-56"} />
          <Button
            variant="outline"
            size="sm"
            className="ml-auto"
            onClick={() => void download()}
            disabled={downloading || !report || report.rows.length === 0}
          >
            <DownloadSimpleIcon size={14} /> {downloading ? "Exporting…" : "CSV"}
          </Button>
        </div>

        {error && <ErrorBox>{error}</ErrorBox>}
        {!report && !error && <Skeleton className="h-32" />}

        {report && report.rows.length === 0 && (
          <EmptyState
            icon={<ReceiptIcon size={26} />}
            title="No borrowed runs this month"
            hint={
              as === "borrower"
                ? "Runs you drive on other members' agents appear here."
                : "Runs other members drive on your authorized agents appear here."
            }
          />
        )}

        {report && report.rows.length > 0 && (
          <>
            <div className="overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)]">
              <table className="w-full min-w-[36rem] text-sm">
                <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
                  <tr>
                    <th className="px-3 py-2">{groupBy === "day" ? "Day" : "Month"}</th>
                    <th className="px-3 py-2">Owner</th>
                    <th className="px-3 py-2">Borrower</th>
                    <th className="px-3 py-2">Agent</th>
                    <th className="px-3 py-2">Room</th>
                    <th className="px-3 py-2 text-right">Runs</th>
                    {hasTokens && <th className="px-3 py-2 text-right">Tokens</th>}
                    {hasCost && <th className="px-3 py-2 text-right">Cost</th>}
                  </tr>
                </thead>
                <tbody>
                  {report.rows.map((r) => (
                    <tr
                      key={`${r.period}|${r.owner}|${r.borrower}|${r.provider}|${r.roomId}`}
                      className="border-t border-[var(--color-border)]"
                    >
                      <td className="px-3 py-2 tabular text-[var(--color-fg-muted)]">{r.period}</td>
                      <td className="px-3 py-2">
                        <UserName id={r.owner} />
                      </td>
                      <td className="px-3 py-2">
                        <UserName id={r.borrower} />
                      </td>
                      <td className="px-3 py-2">{r.provider || "—"}</td>
                      <td className="px-3 py-2">
                        <button
                          type="button"
                          className="tactile text-[var(--color-accent)] hover:underline"
                          onClick={() => onOpenRoom(r.roomId)}
                          title={`Open room ${r.roomId}`}
                        >
                          {r.roomId.slice(0, 8)}
                        </button>
                      </td>
                      <td className="px-3 py-2 text-right tabular">{r.runs}</td>
                      {hasTokens && (
                        <td className="px-3 py-2 text-right tabular">{formatTokens(r.inputTokens, r.outputTokens)}</td>
                      )}
                      {hasCost && <td className="px-3 py-2 text-right tabular">{formatUsd(r.cost)}</td>}
                    </tr>
                  ))}
                </tbody>
                <tfoot>
                  <tr className="border-t border-[var(--color-border-strong)] font-medium">
                    <td className="px-3 py-2" colSpan={5}>
                      Total
                    </td>
                    <td className="px-3 py-2 text-right tabular">{report.totalRuns}</td>
                    {hasTokens && (
                      <td className="px-3 py-2 text-right tabular">
                        {formatTokens(report.totalInputTokens, report.totalOutputTokens)}
                      </td>
                    )}
                    {hasCost && <td className="px-3 py-2 text-right tabular">{formatUsd(report.totalCost)}</td>}
                  </tr>
                </tfoot>
              </table>
            </div>
            <p className="text-xs text-[var(--color-fg-subtle)]">
              {hasCost || hasTokens
                ? "Tokens and cost sum the runs whose agent reported them; runs without reported usage count toward runs only."
                : "No token usage or cost was reported for these runs."}
              {report.truncated && " The report hit its row limit — narrow the month to see everything."}
            </p>
          </>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
          Quota requests to you
        </h2>
        <QuotaRequestsPanel role="owner" emptyText="No one has asked you for more runs." onChanged={load} />
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
          Your quota requests
        </h2>
        <QuotaRequestsPanel
          role="requester"
          emptyText="When a borrow hits an owner's run limit you can ask for more from the room."
        />
      </section>
    </div>
  );
}
