// Room sources (docs/prd/2026-10-20-rooms-as-components.md §2.3): what a
// room component asks to read of the organization, the organization
// admin's grant and who reviews a room. The shapes are
// internal/wire/room_sources.go's (wire.gen.ts); the rule of what a grant
// covers is the gateway's (room_sources.go), never re-decided here. The
// helpers are pure: words, the save form's check, the grant dialog's AI
// input and reply. The requests are lib/roomSourcesApi.ts.

import { describeRecurrence, recipientLabel } from "./schedules.ts";
import { MaxRoomSourcePurpose, type RoomReviewer, type RoomSource, type RoomSourceAsk, type RoomSourceGrant, type RoomSourceScope } from "./wire.gen.ts";

export type { RoomReviewer, RoomSourceAsk, RoomSourceAskState, RoomSourceGrant, RoomSourcesInfo, RoomSourceScope } from "./wire.gen.ts";

/** What a source holds, in words (wire.RoomSources). */
export const ROOM_SOURCE_LABELS: Readonly<Record<RoomSource, string>> = {
  runs: "the organization's agent conversations",
};

/** The built-in assist the grant dialog runs (resolved by name). */
export const EXPLAIN_SOURCE_USE = "explain-source-use";

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

/** reviewLine is what a member of a covered room reads (PRD §2.3,
 *  transparency): the room that reviews it. */
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

/** explainSourceInput is explain-source-use's <text>: the ask, its purpose
 *  and the package's scheduled tasks in their own words, as JSON. */
export function explainSourceInput(g: RoomSourceGrant): string {
  return JSON.stringify({
    source: g.source,
    reads: ROOM_SOURCE_LABELS[g.source],
    purpose: g.purpose,
    scheduledTasks: g.schedules.map((s) => ({ by: recipientLabel(s.recipient), when: describeRecurrence(s.recurrence, s.timezone), task: s.text })),
  });
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
