// Room sources (docs/prd/2026-10-20-rooms-as-components.md §2.3): what a
// room component asks to read of the organization, the organization
// admin's grant (to a private room, read by its owner) and who reviews a
// room. The shapes are internal/wire/room_sources.go's (wire.gen.ts); the
// rule of what a grant covers and when it holds is the gateway's
// (room_sources.go), never re-decided here, and so is the grant dialog's
// AI input (RoomSourceDetail.explainInput). The helpers are pure: words,
// the save form's check, a changed task's diff, the AI's reply. The
// requests are lib/roomSourcesApi.ts.

import { AssistTaskExplainSourceUse, MaxRoomSourcePurpose, type RoomReviewer, type RoomSource, type RoomSourceAsk, type RoomSourceDrift, type RoomSourceScope } from "./wire.gen.ts";

export type { RoomReviewer, RoomSourceAsk, RoomSourceAskState, RoomSourceDetail, RoomSourceDrift, RoomSourceGrant, RoomSourcesInfo, RoomSourceScope } from "./wire.gen.ts";

/** What a source holds, in words (wire.RoomSources). */
export const ROOM_SOURCE_LABELS: Readonly<Record<RoomSource, string>> = {
  runs: "the organization's agent conversations",
};

/** The assist the grant dialog runs (resolved by name, organization then
 *  platform, never a member's copy; its output contract is
 *  wire.AssistContracts). */
export const EXPLAIN_SOURCE_USE = AssistTaskExplainSourceUse;

/** A grant's scope: its label and what it covers, for the admin. */
export const SCOPE_LABELS: Readonly<Record<RoomSourceScope, { readonly label: string; readonly hint: string }>> = {
  all: { label: "All rooms", hint: "Every room of the organization" },
  "none-private": { label: "All but private rooms", hint: "Every room except the invite-only ones" },
  "opted-in": { label: "Rooms that opt in", hint: "Only rooms whose owner opted in, in the room's settings" },
};

/** sourceAsk is how a component's ask is shown before install: the
 *  source, its purpose and who decides. */
export function sourceAsk(a: RoomSourceAsk): string {
  return `Asks to read ${ROOM_SOURCE_LABELS[a.name]} to: ${a.purpose} — your organization admin decides`;
}

/** sourceState is where a room's ask stands. */
export function sourceState(scope: RoomSourceScope | "" | undefined): string {
  return scope ? `Granted: ${SCOPE_LABELS[scope].label.toLowerCase()}` : "Waiting for your organization admin";
}

/** grantState is an ask's state on the console: a grant whose tasks
 *  changed since the admin consented says so until it is acknowledged. */
export function grantState(g: { readonly scope?: RoomSourceScope; readonly changed?: number }): string {
  const state = sourceState(g.scope);
  if (!g.scope || !g.changed) return state;
  return `${state} · ${g.changed === 1 ? "a task" : `${g.changed} tasks`} changed since granted`;
}

/** reviewLine is what a member of a covered room reads first (PRD §2.3,
 *  transparency): the room that reviews it (the note names whose tasks
 *  read, r.grantee, by their display name). */
export function reviewLine(r: RoomReviewer): string {
  return `This organization reviews conversation outcomes in ${r.roomName}`;
}

/** sourcesProblem is what the save form must fix in its asks: a purpose
 *  for each (the admin consents to it), within its bound. */
export function sourcesProblem(sources: readonly RoomSourceAsk[]): string | null {
  for (const s of sources) {
    const n = [...s.purpose.trim()].length;
    if (n === 0) return `Say what the room's tasks read ${ROOM_SOURCE_LABELS[s.name]} for: your organization admin decides on that purpose`;
    if (n > MaxRoomSourcePurpose) return `A source's purpose is at most ${MaxRoomSourcePurpose} characters`;
  }
  return null;
}

/** A changed task as the console shows it: added or changed, its words
 *  then and now, and the AI's explanation the admins were sent. */
export interface DriftView {
  readonly scheduleId: string;
  readonly change: "added" | "changed";
  readonly before: string;
  readonly after: string;
  readonly explanation: string;
  readonly beyond: boolean;
}

/** driftView is a changed task for the console (a mismatch is the
 *  explanation shown first). */
export function driftView(d: RoomSourceDrift): DriftView {
  const mismatch = d.mismatch?.trim() ?? "";
  return {
    scheduleId: d.scheduleId,
    change: d.before ? "changed" : "added",
    before: d.before,
    after: d.after,
    explanation: mismatch || d.summary?.trim() || "No AI explanation (still being made, or unavailable): read the task against the purpose.",
    beyond: mismatch !== "",
  };
}

/** The grant dialog's AI explanation: what the tasks do with the records
 *  and what goes beyond the stated purpose ("" when nothing does). */
export interface SourceUseExplanation {
  readonly summary: string;
  readonly uses: readonly string[];
  readonly mismatch: string;
}

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");

/** explainReply reads an explain-source-use reply; null when it says
 *  nothing. */
export function explainReply(fields: Readonly<Record<string, unknown>>): SourceUseExplanation | null {
  const summary = str(fields.summary);
  const uses = Array.isArray(fields.uses) ? fields.uses.map(str).filter(Boolean) : [];
  if (!summary && uses.length === 0) return null;
  return { summary, uses, mismatch: str(fields.mismatch) };
}
