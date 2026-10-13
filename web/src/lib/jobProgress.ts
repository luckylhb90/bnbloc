// A live job's progress (docs/prd/2026-10-13-live-turns.md §3): what the
// agent is doing while it works — when its turn started and its latest
// steps — relayed by the bridge as an ephemeral update of the job (never
// history, never fed back to an agent). The web keeps it only while the job
// is live. Pure helpers (no React, no DOM), unit-tested: folding updates
// into the room's map, dropping ended jobs, the elapsed time, the steps
// shown, and the plain note a quiet turn rotates through.

import { isLiveJob } from "./agentRuns.ts";
import { JobActivityKindValues, type AgentJob, type AgentJobProgress, type JobActivity } from "./wire.gen.ts";

/** One step of a running turn: a command run, a file read, reasoning, writing. */
export type JobStep = JobActivity;

/** A live job's progress: its turn's start and latest steps, and when the
 *  web last saw them change (receivedAtMs, its clock). */
export interface JobProgress {
  readonly jobId: string;
  readonly startedAtMs?: number;
  readonly steps: readonly JobStep[];
  readonly changedAtMs: number;
}

/** The steps a live job lists before its current one. */
export const SHOWN_STEPS = 5;
/** A turn is quiet after this long without a new step: notes rotate. */
export const QUIET_AFTER_MS = 20_000;
/** How long each quiet note shows. */
export const NOTE_EVERY_MS = 8_000;

const sameSteps = (a: readonly JobStep[], b: readonly JobStep[]) =>
  a.length === b.length && a.every((s, i) => s.kind === b[i]!.kind && s.title === b[i]!.title && s.atMs === b[i]!.atMs);

/** applyProgress folds an update into the room's map (by job id). An update
 *  repeating what the web already shows keeps its changedAtMs, so a quiet
 *  turn stays quiet however often the bridge relays it. */
export function applyProgress(
  cur: Readonly<Record<string, JobProgress>>,
  u: Pick<AgentJobProgress, "jobId" | "startedAtMs"> & { readonly activity?: readonly JobActivity[] },
  nowMs: number,
): Record<string, JobProgress> {
  if (!u.jobId) return { ...cur };
  const steps = (u.activity ?? []).filter((s) => s && (JobActivityKindValues as readonly string[]).includes(s.kind));
  const prev = cur[u.jobId];
  const changed = !prev || !sameSteps(prev.steps, steps) || prev.startedAtMs !== u.startedAtMs;
  return {
    ...cur,
    [u.jobId]: {
      jobId: u.jobId,
      ...(u.startedAtMs ? { startedAtMs: u.startedAtMs } : {}),
      steps,
      changedAtMs: changed ? nowMs : prev.changedAtMs,
    },
  };
}

/** pruneProgress drops the progress of every job that is no longer live
 *  (nothing is kept past the job). */
export function pruneProgress(
  cur: Readonly<Record<string, JobProgress>>,
  jobs: readonly Pick<AgentJob, "jobId" | "status">[],
): Record<string, JobProgress> {
  const live = new Set(jobs.filter((j) => isLiveJob(j)).map((j) => j.jobId));
  const out: Record<string, JobProgress> = {};
  for (const [id, p] of Object.entries(cur)) if (live.has(id)) out[id] = p;
  return out;
}

/** endProgress drops one job's progress once an update says it is no
 *  longer live (other jobs keep theirs). */
export function endProgress(
  cur: Readonly<Record<string, JobProgress>>,
  job: Pick<AgentJob, "jobId" | "status">,
): Readonly<Record<string, JobProgress>> {
  if (!cur[job.jobId] || isLiveJob(job)) return cur;
  const { [job.jobId]: _gone, ...rest } = cur;
  return rest;
}

/** turnStart is when a live job's work began: its turn's start, else when a
 *  bridge started it, else when it was asked. */
export function turnStart(job: Pick<AgentJob, "startedAtMs" | "claimedAtMs" | "createdAtMs">, p?: JobProgress): number {
  return p?.startedAtMs || job.startedAtMs || job.claimedAtMs || job.createdAtMs;
}

/** formatElapsed is a running time: "8s", "3m 05s", "1h 02m". */
export function formatElapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

/** stepLabel is a step as a line: its title, else what its kind means. */
export function stepLabel(s: JobStep): string {
  const title = s.title?.trim();
  if (title) return title;
  return s.kind === "thinking" ? "Thinking" : s.kind === "text" ? "Writing" : "Running a step";
}

/** currentStep and earlierSteps split the steps the strip shows: the newest
 *  and up to SHOWN_STEPS before it, newest first. */
export function currentStep(p?: JobProgress): JobStep | undefined {
  return p?.steps.at(-1);
}

export function earlierSteps(p?: JobProgress): JobStep[] {
  if (!p || p.steps.length < 2) return [];
  return p.steps.slice(0, -1).slice(-SHOWN_STEPS).reverse();
}

/** quietNote is the plain note a quiet turn shows (null while steps still
 *  come in): after QUIET_AFTER_MS without a new step, the notes rotate every
 *  NOTE_EVERY_MS. The agent is named as members know it (its provider and
 *  model), never by its core, which names deployment internals. */
export function quietNote(
  job: Pick<AgentJob, "provider" | "model" | "startedAtMs" | "claimedAtMs" | "createdAtMs">,
  p: JobProgress | undefined,
  nowMs: number,
): string | null {
  const start = turnStart(job, p);
  const since = Math.max(p?.changedAtMs ?? 0, start);
  const quiet = nowMs - since;
  if (quiet < QUIET_AFTER_MS) return null;
  const mins = Math.floor((nowMs - start) / 60_000);
  const notes = [
    "Complex tasks can take several minutes",
    "You can leave this room; you'll be notified when it's done",
    job.model ? `${job.provider} · ${job.model}` : `${job.provider} is working`,
    mins >= 1 ? `Working for ${mins} min` : "Still working",
  ];
  return notes[Math.floor((quiet - QUIET_AFTER_MS) / NOTE_EVERY_MS) % notes.length]!;
}
