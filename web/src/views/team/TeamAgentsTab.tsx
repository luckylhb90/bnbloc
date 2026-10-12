import { useCallback, useEffect, useState } from "react";
import { ArrowClockwiseIcon, CpuIcon, ListChecksIcon } from "@phosphor-icons/react";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatUsd, relativeTime } from "@/lib/format";
import {
  TEAM_REFRESH_MS,
  formatTokens,
  summarizeTeam,
  type TeamAgent,
  type TeamAgentsReply,
  type TeamJob,
  type TeamRange,
  type TeamScope,
} from "@/lib/teamDashboard";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { EmptyState } from "@/ui/Page";
import { Segment } from "@/ui/Segment";
import { AGENT_JOB_FAILURE_LABELS, AGENT_JOB_STATUS_LABELS } from "@/views/room/agentJobLabels";
import { AgentHandle, UserName, useAgentHandle } from "@/views/UserName";

const RANGES = [
  { value: "today" as const, label: "Today" },
  { value: "7d" as const, label: "7 days" },
];

const SCOPES = [
  { value: "tenant" as const, label: "Team" },
  { value: "self" as const, label: "Mine" },
];

// jobStatusLabel reuses the room's job vocabulary, capitalized for a cell.
function jobStatusLabel(status: string): string {
  const label = (AGENT_JOB_STATUS_LABELS as Record<string, string>)[status] ?? status.replace(/_/g, " ");
  return label.charAt(0).toUpperCase() + label.slice(1);
}

function OnlineDot({ online }: { readonly online: boolean }) {
  return (
    <span
      role="img"
      aria-label={online ? "Online" : "Offline"}
      title={online ? "Online" : "Offline"}
      className={cn(
        "inline-block size-2 shrink-0 rounded-full",
        online ? "bg-[var(--color-accent)] pulse-dot" : "bg-[var(--color-fg-subtle)]",
      )}
    />
  );
}

// JobLink renders a job as a click-through to its room.
function JobLink({ job, onOpenRoom }: { readonly job: TeamJob; readonly onOpenRoom: (id: string) => void }) {
  return (
    <button
      type="button"
      onClick={() => onOpenRoom(job.roomId)}
      className="tactile min-w-0 truncate text-left text-[var(--color-accent)] hover:underline"
      title={`Open ${job.roomName || job.roomId}`}
    >
      {job.roomName || job.roomId.slice(0, 8)}
    </button>
  );
}

function statusLine(a: TeamAgent): string {
  if (a.currentJob) return jobStatusLabel(a.currentJob.status);
  if (a.online) return "Idle";
  return a.lastSeenAtMs ? `Offline · seen ${relativeTime(a.lastSeenAtMs)}` : "Offline";
}

function lastJobLine(j: TeamJob): string {
  const at = j.finishedAtMs || j.createdAtMs;
  const reason = j.failureReason
    ? ` (${AGENT_JOB_FAILURE_LABELS[j.failureReason] ?? j.failureReason.replace(/_/g, " ")})`
    : "";
  return `${jobStatusLabel(j.status)}${reason} · ${relativeTime(at)}`;
}

function tokens(a: TeamAgent): string {
  return a.usage ? formatTokens(a.usage.inputTokens, a.usage.outputTokens) : "—";
}

function costCell(a: TeamAgent): string {
  const c = formatUsd(a.usage?.costUsd);
  return a.usage?.shared && a.usage.costUsd !== undefined ? `${c}*` : c;
}

// TeamAgentsTab is the read-only team agent dashboard: per member per agent,
// online state, what it is doing now, its last job, and turns/borrows/tokens
// /cost over the range. Org admins default to the whole team; members see
// their own agents. Refreshes every 30s while the page is visible.
export function TeamAgentsTab({
  canSeeTeam,
  onOpenRoom,
  onShowRuns,
}: {
  readonly canSeeTeam: boolean;
  readonly onOpenRoom: (id: string) => void;
  // Opens the agent's run records (R10).
  readonly onShowRuns?: (agent: { owner: string; core: string; provider: string }) => void;
}) {
  const [range, setRange] = useState<TeamRange>("today");
  const [scope, setScope] = useState<TeamScope>(canSeeTeam ? "tenant" : "self");
  const [data, setData] = useState<TeamAgentsReply | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const effectiveScope: TeamScope = canSeeTeam ? scope : "self";
  const load = useCallback(() => {
    setLoading(true);
    api
      .teamAgents(effectiveScope, range)
      .then((r) => {
        setData(r);
        setError("");
      })
      .catch((e) => setError(e instanceof ApiError ? e.message : "Could not load the team dashboard"))
      .finally(() => setLoading(false));
  }, [effectiveScope, range]);

  useEffect(() => {
    setData(null);
    load();
  }, [load]);
  useVisiblePoll(load, TEAM_REFRESH_MS);

  const agents = data?.agents ?? [];
  const sum = summarizeTeam(agents);
  const anyShared = agents.some((a) => a.usage?.shared && a.usage.costUsd !== undefined);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Segment options={RANGES} value={range} onChange={setRange} className="w-48" />
        {canSeeTeam && <Segment options={SCOPES} value={scope} onChange={setScope} className="w-40" />}
        <Button variant="ghost" size="sm" className="ml-auto" onClick={load} disabled={loading} aria-label="Refresh">
          <ArrowClockwiseIcon size={14} className={cn(loading && "animate-spin")} /> Refresh
        </Button>
      </div>

      {error && <ErrorBox>{error}</ErrorBox>}

      {data && agents.length > 0 && (
        <section className="grid grid-cols-2 gap-3 sm:grid-cols-5">
          <Stat label="Online" value={`${sum.online}/${agents.length}`} />
          <Stat label="Working now" value={String(sum.busy)} />
          <Stat label="Turns" value={String(sum.turns)} />
          <Stat label="Borrowed runs" value={String(sum.borrowedRuns)} />
          <Stat label="Reported cost" value={formatUsd(sum.costUsd)} />
        </section>
      )}

      {!data && !error && (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-12" />
          <Skeleton className="h-12" />
          <Skeleton className="h-12" />
        </div>
      )}

      {data && agents.length === 0 && (
        <EmptyState
          icon={<CpuIcon size={26} />}
          title={effectiveScope === "tenant" ? "No agents in this organization yet" : "You have no agents yet"}
          hint={
            effectiveScope === "tenant"
              ? "Agents appear here once members run the desktop app with a connected core."
              : "Agents appear here once your desktop app is running with a connected core."
          }
        />
      )}

      {agents.length > 0 && (
        <>
          {/* Wide screens: one table row per agent. */}
          <div className="hidden overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)] md:block">
            <table className="w-full text-sm">
              <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
                <tr>
                  <th className="px-3 py-2">Agent</th>
                  <th className="px-3 py-2">Now</th>
                  <th className="px-3 py-2">Last job</th>
                  <th className="px-3 py-2 text-right">Turns</th>
                  <th className="px-3 py-2 text-right">Borrowed</th>
                  <th className="px-3 py-2 text-right">Tokens</th>
                  <th className="px-3 py-2 text-right">Cost</th>
                </tr>
              </thead>
              <tbody>
                {agents.map((a) => (
                  <tr key={`${a.owner}/${a.core}/${a.provider}`} className="border-t border-[var(--color-border)] align-top">
                    <td className="px-3 py-2">
                      <div className="flex items-center gap-2">
                        <OnlineDot online={a.online} />
                        <AgentHandle owner={a.owner} provider={a.provider} className="font-medium text-[var(--color-fg)]" />
                        {a.core && <span className="text-xs text-[var(--color-fg-subtle)]">{a.core}</span>}
                        {onShowRuns && <RunsLink agent={a} onShowRuns={onShowRuns} />}
                      </div>
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex min-w-0 flex-col">
                        <span className={cn(a.currentJob ? "text-[var(--color-fg)]" : "text-[var(--color-fg-muted)]")}>
                          {statusLine(a)}
                        </span>
                        {a.currentJob && (
                          <span className="flex min-w-0 gap-1 text-xs text-[var(--color-fg-subtle)]">
                            in <JobLink job={a.currentJob} onOpenRoom={onOpenRoom} />
                            {a.currentJob.requester !== a.owner && (
                              <>
                                {" "}· for <UserName id={a.currentJob.requester} />
                              </>
                            )}
                          </span>
                        )}
                      </div>
                    </td>
                    <td className="px-3 py-2">
                      {a.lastJob ? (
                        <div className="flex min-w-0 flex-col">
                          <span className="text-[var(--color-fg-muted)]">{lastJobLine(a.lastJob)}</span>
                          <span className="flex min-w-0 gap-1 text-xs text-[var(--color-fg-subtle)]">
                            in <JobLink job={a.lastJob} onOpenRoom={onOpenRoom} />
                          </span>
                        </div>
                      ) : (
                        <span className="text-[var(--color-fg-subtle)]">—</span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-right tabular">
                      {a.turns}
                      {a.failedTurns > 0 && (
                        <span className="ml-1 text-xs text-[var(--color-danger)]" title="Failed or timed out">
                          ({a.failedTurns} failed)
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-right tabular">{a.borrowedRuns}</td>
                    <td className="px-3 py-2 text-right tabular text-[var(--color-fg-muted)]">{tokens(a)}</td>
                    <td className="px-3 py-2 text-right tabular">{costCell(a)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* Narrow screens: one card per agent. */}
          <div className="flex flex-col gap-2 md:hidden">
            {agents.map((a) => (
              <Card key={`${a.owner}/${a.core}/${a.provider}`} className="flex flex-col gap-2 p-3">
                <div className="flex items-center gap-2">
                  <OnlineDot online={a.online} />
                  <AgentHandle owner={a.owner} provider={a.provider} className="font-medium" />
                  {a.core && <span className="text-xs text-[var(--color-fg-subtle)]">{a.core}</span>}
                  {onShowRuns && <RunsLink agent={a} onShowRuns={onShowRuns} />}
                  {a.currentJob && (
                    <Badge tone="accent" className="ml-auto">
                      {jobStatusLabel(a.currentJob.status)}
                    </Badge>
                  )}
                </div>
                <div className="text-sm text-[var(--color-fg-muted)]">
                  {a.currentJob ? (
                    <span className="flex min-w-0 gap-1">
                      Working in <JobLink job={a.currentJob} onOpenRoom={onOpenRoom} />
                    </span>
                  ) : (
                    statusLine(a)
                  )}
                </div>
                {a.lastJob && (
                  <div className="flex min-w-0 gap-1 text-xs text-[var(--color-fg-subtle)]">
                    Last: {lastJobLine(a.lastJob)} in <JobLink job={a.lastJob} onOpenRoom={onOpenRoom} />
                  </div>
                )}
                <div className="grid grid-cols-3 gap-2 text-xs">
                  <MiniStat label="Turns" value={String(a.turns)} />
                  <MiniStat label="Borrowed" value={String(a.borrowedRuns)} />
                  <MiniStat label="Cost" value={costCell(a)} />
                </div>
              </Card>
            ))}
          </div>

          <p className="text-xs text-[var(--color-fg-subtle)]">
            Turns count replied jobs since {range === "today" ? "midnight" : "7 days ago"}. Tokens and cost appear only
            for agents whose core reports them.
            {anyShared && " * Cost shared by agents exposing the same provider under different cores."}
          </p>
        </>
      )}
    </div>
  );
}

// RunsLink opens one agent's run records; a failed turn in the range shows
// the link in the danger tone so failures are one click from their errors.
function RunsLink({
  agent,
  onShowRuns,
}: {
  readonly agent: TeamAgent;
  readonly onShowRuns: (agent: { owner: string; core: string; provider: string }) => void;
}) {
  const handle = useAgentHandle(agent.owner, agent.provider);
  return (
    <button
      type="button"
      onClick={() => onShowRuns({ owner: agent.owner, core: agent.core, provider: agent.provider })}
      className={cn(
        "tactile inline-flex items-center gap-1 text-xs hover:underline",
        agent.failedTurns > 0 ? "text-[var(--color-danger)]" : "text-[var(--color-accent)]",
      )}
      title={`Runs of ${handle}`}
      aria-label={`Runs of ${handle}`}
    >
      <ListChecksIcon size={12} /> Runs
    </button>
  );
}

function Stat({ label, value }: { readonly label: string; readonly value: string }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2">
      <div className="text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">{label}</div>
      <div className="mt-0.5 text-lg font-semibold tabular">{value}</div>
    </div>
  );
}

function MiniStat({ label, value }: { readonly label: string; readonly value: string }) {
  return (
    <div className="flex flex-col">
      <span className="text-[var(--color-fg-subtle)]">{label}</span>
      <span className="tabular text-[var(--color-fg)]">{value}</span>
    </div>
  );
}
