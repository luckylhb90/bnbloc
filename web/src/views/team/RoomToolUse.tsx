import { useState } from "react";
import { refusalShare, toolUseLabel } from "@/lib/agentRuns";
import { agentRunsApi } from "@/lib/agentRunsApi";
import { useAuth } from "@/lib/auth";
import { errorText } from "@/lib/errors";
import { compactNumber, exactTime } from "@/lib/format";
import { queryKey, useQuery } from "@/lib/useQuery";
import { ErrorBox } from "@/ui/ErrorBox";
import { Segment } from "@/ui/Segment";

const RANGES = [
  { value: "7" as const, label: "7 days" },
  { value: "30" as const, label: "30 days" },
];

// RoomToolUse is the organization's room tool use for org admins
// (docs/prd/2026-10-20-fewer-sharper-room-tools.md §2.3): how often the
// agents' runs called each room tool (and each action), and how many of
// those calls were refused or failed, over the last 7 or 30 days. The record
// keeps the tool, action and outcome only, never what the agent sent.
export function RoomToolUse() {
  const { subject, tenant } = useAuth();
  const [range, setRange] = useState<(typeof RANGES)[number]["value"]>("7");
  // Read only once opened: the read sums up to thousands of runs.
  const [open, setOpen] = useState(false);
  const query = useQuery(open ? queryKey(`room-tool-use:${range}`, { subject, tenant }) : null, () => agentRunsApi.toolUse(Number(range)), {
    staleMs: 60_000,
  });
  const use = query.data;
  return (
    <details
      className="group rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] text-sm"
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 [&::-webkit-details-marker]:hidden">
        <span className="font-medium">Room tool use</span>
        <span className="text-xs text-[var(--color-fg-subtle)]">calls and refusals per tool</span>
      </summary>
      <div className="flex flex-col gap-2 border-t border-[var(--color-border)] px-3 py-2">
        <Segment options={RANGES} value={range} onChange={setRange} className="w-48" />
        {query.error && !use ? (
          <ErrorBox>{errorText(query.error, "Could not load room tool use")}</ErrorBox>
        ) : !use ? (
          <p className="text-xs text-[var(--color-fg-subtle)]">Loading…</p>
        ) : (
          <>
            <p className="text-xs text-[var(--color-fg-subtle)]">
              {compactNumber(use.runs)} runs read.
              {use.truncated && use.oldestMs ? ` Only the most recent runs, since ${exactTime(use.oldestMs)}.` : ""}
              {use.dropped ? ` ${compactNumber(use.dropped)} calls beyond a run's record are not counted.` : ""}
            </p>
            {use.tools.length === 0 ? (
              <p className="text-xs text-[var(--color-fg-subtle)]">No room tool calls in this range.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-xs">
                  <thead className="text-[var(--color-fg-subtle)]">
                    <tr>
                      {["Tool", "Calls", "Refused", "Errors", "Not ok"].map((h) => (
                        <th key={h} className="whitespace-nowrap px-2 py-1.5 font-medium">
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-[var(--color-border)] text-[var(--color-fg-muted)]">
                    {use.tools.map((row) => (
                      <tr key={toolUseLabel(row)}>
                        <td className="whitespace-nowrap px-2 py-1.5 font-mono text-[var(--color-fg)]">{toolUseLabel(row)}</td>
                        <td className="tabular px-2 py-1.5">{compactNumber(row.calls)}</td>
                        <td className="tabular px-2 py-1.5">{compactNumber(row.refused)}</td>
                        <td className="tabular px-2 py-1.5">{compactNumber(row.errors)}</td>
                        <td className="tabular px-2 py-1.5">{refusalShare(row)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </details>
  );
}
