// A job's live view (docs/prd/2026-10-17-browser-component.md §2.8): a tool
// with something live to show (the browser component's tab) posts frames
// while its turn runs; the room learns of each from job_live_frame (no
// image) and viewers fetch the newest image themselves, so a slow viewer
// skips frames instead of falling behind. Pure helpers (no React, no DOM),
// unit-tested; the API calls are jobLiveApi.ts.

import type { JobLiveFrame, LiveInput } from "./wire.gen.ts";

/** A job's live view as the room knows it: the newest frame's facts. */
export type JobLive = JobLiveFrame;

/** applyLiveFrame folds a job_live_frame event into the room's map (by job
 *  id). An older frame (a lower seq of the same run) never replaces a newer
 *  one; a control change (same seq) does. */
export function applyLiveFrame(cur: Readonly<Record<string, JobLive>>, ev: JobLiveFrame): Record<string, JobLive> {
  if (!ev || typeof ev.jobId !== "string" || !ev.jobId) return { ...cur };
  if (ev.ended) return { ...endLive(cur, ev.jobId) };
  const prev = cur[ev.jobId];
  if (prev && prev.run === ev.run && ev.seq < prev.seq) return { ...cur };
  return { ...cur, [ev.jobId]: ev };
}

/** endTurnLive drops a job's view when its turn ends, unless the view
 *  waits for a person (a sign-in): that one stays until its tool ends it
 *  (browser component PRD §3.7). */
export function endTurnLive(cur: Readonly<Record<string, JobLive>>, jobId: string): Readonly<Record<string, JobLive>> {
  return cur[jobId]?.waiting ? cur : endLive(cur, jobId);
}

/** liveList folds the room's views (GET .../live) into the map; a view the
 *  map already has at a newer frame keeps its own. */
export function liveList(cur: Readonly<Record<string, JobLive>>, views: readonly JobLiveFrame[]): Record<string, JobLive> {
  let next: Record<string, JobLive> = { ...cur };
  for (const v of views) next = applyLiveFrame(next, v);
  return next;
}

/** waitingFor answers the newest view the member must act on in the
 *  channel shown: a sign-in on their own machine that only they can do. */
export function waitingFor(live: Readonly<Record<string, JobLive>>, subject: string | null, channelId: string | null): JobLive | undefined {
  if (!subject) return undefined;
  return Object.values(live)
    .filter((l) => l.waiting && l.owner === subject && (!channelId || !l.channelId || l.channelId === channelId))
    .sort((a, b) => b.atMs - a.atMs)[0];
}

/** endLive drops a job's live view (its turn ended). */
export function endLive(cur: Readonly<Record<string, JobLive>>, jobId: string): Readonly<Record<string, JobLive>> {
  if (!cur[jobId]) return cur;
  const { [jobId]: _gone, ...rest } = cur;
  return rest;
}

/** frameKey changes whenever a new picture is worth fetching. */
export function frameKey(live: Pick<JobLive, "run" | "seq">): string {
  return `${live.run ?? ""}:${live.seq}`;
}

/** pointAt maps a pointer position inside the rendered frame (the element's
 *  box, object-fit: contain) to the frame's fractions, or null when it falls
 *  on the letterbox. */
export function pointAt(
  box: { readonly left: number; readonly top: number; readonly width: number; readonly height: number },
  frame: { readonly width: number; readonly height: number },
  clientX: number,
  clientY: number,
): { x: number; y: number } | null {
  if (box.width <= 0 || box.height <= 0 || frame.width <= 0 || frame.height <= 0) return null;
  const scale = Math.min(box.width / frame.width, box.height / frame.height);
  const w = frame.width * scale;
  const h = frame.height * scale;
  const x = (clientX - box.left - (box.width - w) / 2) / w;
  const y = (clientY - box.top - (box.height - h) / 2) / h;
  if (x < 0 || x > 1 || y < 0 || y > 1) return null;
  return { x, y };
}

/** modifiers is CDP's modifier bit set of a keyboard or pointer event. */
export function modifiers(e: { readonly altKey: boolean; readonly ctrlKey: boolean; readonly metaKey: boolean; readonly shiftKey: boolean }): number {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
}

/** keyInput is a key event as live input: printable keys carry their text
 *  (unless a shortcut modifier is held). */
export function keyInput(
  action: "down" | "up",
  e: { readonly key: string; readonly code: string; readonly altKey: boolean; readonly ctrlKey: boolean; readonly metaKey: boolean; readonly shiftKey: boolean },
): LiveInput {
  const m = modifiers(e);
  const printable = e.key.length === 1 && (m & (2 | 4)) === 0;
  return { type: "key", action, key: e.key, code: e.code, ...(printable && action === "down" ? { text: e.key } : {}), modifiers: m };
}
