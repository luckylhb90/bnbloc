import { useCallback, useEffect, useRef, useState } from "react";
import { expertBadge } from "@/lib/contentComponents";
import {
  ArrowClockwiseIcon,
  ArrowLeftIcon,
  ChatTextIcon,
  ListChecksIcon,
  RobotIcon,
  XIcon,
} from "@phosphor-icons/react";
import { ApiError } from "@/lib/api";
import {
  isFailedRun,
  isLiveJob,
  mergeRunPages,
  runAgentLabel,
  runDurationMs,
  runPhaseSteps,
  type AgentRun,
  type AgentRunAgent,
  type AgentRunsReply,
  type RunListQuery,
  type RunStatusFilter,
} from "@/lib/agentRuns";
import { agentRunsApi } from "@/lib/agentRunsApi";
import { cn } from "@/lib/cn";
import { exactTime, formatDuration, formatUsd, relativeTime } from "@/lib/format";
import { formatTokens } from "@/lib/teamDashboard";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { Button } from "@/ui/Button";
import { Badge, Skeleton } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Input } from "@/ui/Input";
import { EmptyState } from "@/ui/Page";
import { Segment } from "@/ui/Segment";
import { AGENT_JOB_FAILURE_LABELS, AGENT_JOB_STATUS_LABELS } from "@/views/room/agentJobLabels";
import { AgentHandle, UserName } from "@/views/UserName";
import { useDisplayNames } from "@/lib/useDisplayName";

const PAGE = 30;
const REFRESH_MS = 30_000;

const STATUSES = [
  { value: "" as const, label: "All" },
  { value: "active" as const, label: "Active" },
  { value: "failed" as const, label: "Failed" },
  { value: "replied" as const, label: "Replied" },
];

// An agent the browser is narrowed to (the Team page's per-agent link).
export interface RunAgentFilter {
  readonly owner: string;
  readonly core: string;
  readonly provider: string;
}

function statusText(r: AgentRun): string {
  const label = (AGENT_JOB_STATUS_LABELS as Record<string, string>)[r.status] ?? r.status.replace(/_/g, " ");
  return label.charAt(0).toUpperCase() + label.slice(1);
}

function failureText(r: AgentRun): string {
  const reason = r.failureReason ? AGENT_JOB_FAILURE_LABELS[r.failureReason] ?? r.failureReason.replace(/_/g, " ") : "";
  if (r.error && reason) return `${reason}: ${r.error}`;
  return r.error || reason;
}

function StatusBadge({ run }: { readonly run: AgentRun }) {
  const tone = isFailedRun(run) ? "danger" : isLiveJob(run) ? "accent" : run.status === "cancelled" ? "warning" : "neutral";
  return <Badge tone={tone}>{statusText(run)}</Badge>;
}

function usageText(r: AgentRun): string {
  const u = r.usage;
  if (!u || (u.inputTokens === undefined && u.outputTokens === undefined && u.costUsd === undefined)) return "";
  const parts: string[] = [];
  if (u.inputTokens !== undefined || u.outputTokens !== undefined) parts.push(formatTokens(u.inputTokens, u.outputTokens));
  if (u.costUsd !== undefined) parts.push(formatUsd(u.costUsd));
  return parts.join(" · ");
}

// RunsBrowser lists agent run records for one scope, newest first, with a
// status filter, cursor paging, and a detail drawer. `load` fetches one page;
// `onJump` opens a run's request or reply message in its room.
export function RunsBrowser({
  load,
  showRoom,
  memberFilter,
  agent,
  onClearAgent,
  onJump,
  emptyHint,
}: {
  readonly load: (q: RunListQuery) => Promise<AgentRunsReply>;
  // List rows name the room (the cross-room views).
  readonly showRoom: boolean;
  // Offer the tenant view's member filter.
  readonly memberFilter?: boolean;
  readonly agent?: RunAgentFilter | null;
  readonly onClearAgent?: () => void;
  readonly onJump: (run: AgentRun, seq: number) => void;
  readonly emptyHint: string;
}) {
  const [status, setStatus] = useState<RunStatusFilter>("");
  const [member, setMember] = useState("");
  const [appliedMember, setAppliedMember] = useState("");
  const [runs, setRuns] = useState<AgentRun[] | null>(null);
  const [next, setNext] = useState<string | undefined>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [selected, setSelected] = useState<AgentRun | null>(null);
  const paged = useRef(false);

  const query = useCallback(
    (before?: string): RunListQuery => ({
      status,
      member: appliedMember,
      owner: agent?.owner,
      core: agent?.core,
      provider: agent?.provider,
      before,
      limit: PAGE,
    }),
    [status, appliedMember, agent],
  );

  const refresh = useCallback(() => {
    setLoading(true);
    load(query())
      .then((r) => {
        paged.current = false;
        setRuns(r.runs);
        setNext(r.nextBefore);
        setError("");
      })
      .catch((e) => setError(e instanceof ApiError && e.message ? e.message : "Could not load runs"))
      .finally(() => setLoading(false));
  }, [load, query]);

  useEffect(() => {
    setRuns(null);
    setSelected(null);
    refresh();
  }, [refresh]);
  // Keep the first page fresh while the member watches; an older page they
  // paged to is not reset under them.
  useVisiblePoll(() => {
    if (!paged.current) refresh();
  }, REFRESH_MS);

  const loadMore = () => {
    if (!next) return;
    setLoading(true);
    load(query(next))
      .then((r) => {
        paged.current = true;
        setRuns((cur) => mergeRunPages(cur ?? [], r.runs));
        setNext(r.nextBefore);
      })
      .catch((e) => setError(e instanceof ApiError && e.message ? e.message : "Could not load more runs"))
      .finally(() => setLoading(false));
  };

  if (selected) {
    return <RunDetail run={selected} showRoom={showRoom} onBack={() => setSelected(null)} onJump={onJump} />;
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Segment options={STATUSES} value={status} onChange={setStatus} className="w-72" />
        {memberFilter && (
          <form
            className="flex items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault();
              setAppliedMember(member.trim());
            }}
          >
            <Input
              value={member}
              onChange={(e) => setMember(e.target.value)}
              onBlur={() => setAppliedMember(member.trim())}
              placeholder="Member"
              aria-label="Filter by member"
              className="h-9 w-40"
            />
          </form>
        )}
        {agent && (
          <Badge tone="accent" className="gap-1.5">
            <RobotIcon size={12} />
            <AgentHandle owner={agent.owner} provider={agent.provider} />
            {agent.core && <span className="opacity-70">{agent.core}</span>}
            {onClearAgent && (
              <button type="button" onClick={onClearAgent} aria-label="Show every agent" title="Show every agent" className="tactile">
                <XIcon size={12} />
              </button>
            )}
          </Badge>
        )}
        <Button variant="ghost" size="sm" className="ml-auto" onClick={refresh} disabled={loading} aria-label="Refresh runs">
          <ArrowClockwiseIcon size={14} className={cn(loading && "animate-spin")} /> Refresh
        </Button>
      </div>

      {error && <ErrorBox>{error}</ErrorBox>}

      {runs === null && !error && (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-14" />
          <Skeleton className="h-14" />
          <Skeleton className="h-14" />
        </div>
      )}

      {runs !== null && runs.length === 0 && (
        <EmptyState icon={<ListChecksIcon size={26} />} title="No agent runs" hint={emptyHint} />
      )}

      {runs !== null && runs.length > 0 && (
        <ul className="flex flex-col gap-1.5">
          {runs.map((r) => (
            <li key={r.jobId}>
              <RunRow run={r} showRoom={showRoom} onOpen={() => setSelected(r)} />
            </li>
          ))}
        </ul>
      )}

      {next && (
        <Button variant="secondary" size="sm" className="self-center" onClick={loadMore} disabled={loading}>
          Load older runs
        </Button>
      )}
    </div>
  );
}

function RunRow({ run, showRoom, onOpen }: { readonly run: AgentRun; readonly showRoom: boolean; readonly onOpen: () => void }) {
  const failed = isFailedRun(run);
  const dur = runDurationMs(run);
  const usage = usageText(run);
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cn(
        "tactile flex w-full min-w-0 flex-col gap-1 rounded-[var(--radius)] border px-3 py-2 text-left",
        failed
          ? "border-[var(--color-danger)]/40 bg-[var(--color-danger)]/5 hover:bg-[var(--color-danger)]/10"
          : "border-[var(--color-border)] hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-2)]",
      )}
    >
      <div className="flex min-w-0 items-center gap-2 text-sm">
        <StatusBadge run={run} />
        <span className="min-w-0 truncate font-medium text-[var(--color-fg)]">
          {run.expert && <>expert {expertBadge({ name: run.expert.name || run.expert.id, version: run.expert.version })} on </>}
          {!run.expert && run.roomExpert && <>room expert {expertBadge({ name: run.roomExpert.name || run.roomExpert.id, version: run.roomExpert.version })} on </>}
          <RunAgent agent={run.agent} />
        </span>
        {run.borrowed && (
          <Badge>
            borrowed by <UserName id={run.requester} />
          </Badge>
        )}
        {run.dispatchedBy && <Badge>dispatched by an agent</Badge>}
        <span className="ml-auto shrink-0 text-xs text-[var(--color-fg-subtle)] tabular" title={exactTime(run.phases.queuedAtMs)}>
          {relativeTime(run.phases.queuedAtMs)}
        </span>
      </div>
      {failed && failureText(run) && (
        <p className="line-clamp-2 text-xs text-[var(--color-danger)]">{failureText(run)}</p>
      )}
      <p className="min-w-0 truncate text-xs text-[var(--color-fg-muted)]">
        {run.redacted ? <span className="italic">Request text hidden (not a member of this room)</span> : run.request.text || "—"}
      </p>
      <div className="flex min-w-0 flex-wrap gap-x-3 text-xs text-[var(--color-fg-subtle)] tabular">
        {showRoom && <span className="truncate">in {run.roomName || run.roomId.slice(0, 8)}</span>}
        {!run.borrowed && (
          <span>
            by <UserName id={run.requester} />
          </span>
        )}
        {dur !== undefined && <span>{formatDuration(dur)}</span>}
        {run.reply?.count ? <span>{run.reply.count} repl{run.reply.count === 1 ? "y" : "ies"}</span> : null}
        {usage && <span>{usage}</span>}
      </div>
    </button>
  );
}

// RunDetail is the run's drawer: phases with the time spent in each, the
// request and reply previews with jump-to-message, usage, and diagnostics.
// The full record (diagnostics, the reply's room sequence) comes from the
// room route; a viewer outside the room keeps the list row.
function RunDetail({
  run: initial,
  showRoom,
  onBack,
  onJump,
}: {
  readonly run: AgentRun;
  readonly showRoom: boolean;
  readonly onBack: () => void;
  readonly onJump: (run: AgentRun, seq: number) => void;
}) {
  const [run, setRun] = useState(initial);
  useEffect(() => {
    if (initial.redacted) return;
    let live = true;
    agentRunsApi
      .detail(initial.roomId, initial.jobId)
      .then((full) => live && setRun(full))
      .catch(() => {
        // Not a member of the run's room any more: the list row is all there is.
      });
    return () => {
      live = false;
    };
  }, [initial]);

  const steps = runPhaseSteps(run);
  const failed = isFailedRun(run);
  const usage = run.usage;
  return (
    <div className="flex flex-col gap-4 text-sm">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="sm" onClick={onBack} aria-label="Back to runs">
          <ArrowLeftIcon size={14} /> Runs
        </Button>
        <StatusBadge run={run} />
        <span className="min-w-0 truncate font-medium">
          <RunAgent agent={run.agent} />
        </span>
      </div>

      {(failed || run.status === "cancelled") && failureText(run) && <ErrorBox>{failureText(run)}</ErrorBox>}
      {run.warning && (
        <p className="rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 px-3 py-2 text-xs text-[var(--color-warning)]">
          {run.warning}
        </p>
      )}

      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
        {showRoom && (
          <>
            <dt className="text-[var(--color-fg-subtle)]">Room</dt>
            <dd className="truncate">{run.roomName || run.roomId}</dd>
          </>
        )}
        <dt className="text-[var(--color-fg-subtle)]">Requested by</dt>
        <dd>
          <UserName id={run.requester} />
          {run.borrowed && (
            <span className="text-[var(--color-fg-muted)]">
              {" "}
              (borrowed from <UserName id={run.agent.owner} />)
            </span>
          )}
        </dd>
        {run.expert && (
          <>
            <dt className="text-[var(--color-fg-subtle)]">Expert</dt>
            <dd className="truncate">
              {expertBadge({ name: run.expert.name || run.expert.id, version: run.expert.version })} on <RunAgent agent={run.agent} />
            </dd>
          </>
        )}
        {run.roomExpert && (
          <>
            <dt className="text-[var(--color-fg-subtle)]">Room expert</dt>
            <dd className="truncate">
              {expertBadge({ name: run.roomExpert.name || run.roomExpert.id, version: run.roomExpert.version })}
            </dd>
          </>
        )}
        {run.dispatchedBy && (
          <>
            <dt className="text-[var(--color-fg-subtle)]">Dispatched by</dt>
            <dd className="truncate">
              an agent running job <span className="font-mono">{run.dispatchedBy.jobId}</span>
              <span className="text-[var(--color-fg-muted)]"> · step {run.dispatchedBy.depth}</span>
            </dd>
          </>
        )}
        {run.agent.targetId && (
          <>
            <dt className="text-[var(--color-fg-subtle)]">Target</dt>
            <dd className="truncate font-mono">{run.agent.targetId}</dd>
          </>
        )}
        {(run.agent.model || usage?.model) && (
          <>
            <dt className="text-[var(--color-fg-subtle)]">Model</dt>
            <dd>
              {usage?.model || run.agent.model}
              {run.agent.reasoningEffort && <span className="text-[var(--color-fg-muted)]"> · {run.agent.reasoningEffort} effort</span>}
            </dd>
          </>
        )}
        {(run.attempt ?? 0) > 1 && (
          <>
            <dt className="text-[var(--color-fg-subtle)]">Attempts</dt>
            <dd className="tabular">{run.attempt}</dd>
          </>
        )}
        <dt className="text-[var(--color-fg-subtle)]">Usage</dt>
        <dd className="tabular">{usageText(run) || "Not reported"}</dd>
      </dl>

      <section>
        <h3 className="mb-2 text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">Timeline</h3>
        <ol className="flex flex-col gap-1">
          {steps.map((s) => (
            <li key={s.id} className="flex items-baseline gap-3 text-xs tabular">
              <span className="w-32 shrink-0 text-[var(--color-fg)]">{s.label}</span>
              <span className="text-[var(--color-fg-muted)]" title={exactTime(s.atMs)}>
                {new Date(s.atMs).toLocaleTimeString()}
              </span>
              {s.sincePrevMs !== undefined && <span className="text-[var(--color-fg-subtle)]">+{formatDuration(s.sincePrevMs)}</span>}
            </li>
          ))}
        </ol>
      </section>

      <section className="flex flex-col gap-2">
        <div className="flex items-center gap-2">
          <h3 className="text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">Request</h3>
          {run.request.messageSeq > 0 && (
            <Button variant="ghost" size="sm" className="ml-auto" onClick={() => onJump(run, run.request.messageSeq)}>
              <ChatTextIcon size={14} /> Jump to message
            </Button>
          )}
        </div>
        <p className="whitespace-pre-wrap break-words rounded-[var(--radius)] bg-[var(--color-surface-2)] px-3 py-2 text-xs text-[var(--color-fg-muted)]">
          {run.redacted ? "Hidden: you are not a member of this room." : run.request.text || "(attachments only)"}
          {(run.request.attachments ?? 0) > 0 && ` · ${run.request.attachments} attachment${run.request.attachments === 1 ? "" : "s"}`}
        </p>
      </section>

      {run.reply && (
        <section className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <h3 className="text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
              First reply{(run.reply.count ?? 0) > 1 ? ` of ${run.reply.count}` : ""}
            </h3>
            {run.reply.roomSeq ? (
              <Button variant="ghost" size="sm" className="ml-auto" onClick={() => onJump(run, run.reply?.roomSeq ?? 0)}>
                <ChatTextIcon size={14} /> Jump to reply
              </Button>
            ) : null}
          </div>
          <p className="whitespace-pre-wrap break-words rounded-[var(--radius)] bg-[var(--color-surface-2)] px-3 py-2 text-xs text-[var(--color-fg-muted)]">
            {run.redacted ? "Hidden: you are not a member of this room." : run.reply.text || "—"}
          </p>
        </section>
      )}

      {run.diagnostics && Object.keys(run.diagnostics).length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer text-[var(--color-fg-subtle)]">Diagnostics</summary>
          <pre className="mt-2 max-h-64 overflow-auto rounded-[var(--radius)] bg-[var(--color-surface-2)] p-3 font-mono text-[11px] text-[var(--color-fg-muted)]">
            {JSON.stringify(run.diagnostics, null, 2)}
          </pre>
        </details>
      )}
    </div>
  );
}

// RunAgent is the run's agent by its owner's display name.
function RunAgent({ agent }: { readonly agent: AgentRunAgent }) {
  const nameOf = useDisplayNames([agent.owner]);
  return <>{runAgentLabel(agent, nameOf)}</>;
}
