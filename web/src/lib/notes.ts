// Room notes (docs/prd/2026-10-16-room-memory-and-evolution.md): the pure
// helpers behind the "What this project knows" panel and "Save to memory". The
// wire shapes and limits are generated from internal/wire/notes.go; the rules
// of what a note may hold mirror internal/notes.Check so the form never offers
// what the server refuses (the server still checks every write).
import { agoText, exactTime } from "./format.ts";
import {
  MaxBriefBodyBytes,
  MaxNoteBodyBytes,
  MaxNoteTitleRunes,
  NoteBrief,
  NoteSourceMessage,
} from "./wire.gen.ts";
import type { NoteCreateRequest, NoteSource, NoteType, NoteUpdateRequest, RoomNote } from "./wire.gen.ts";

export type { NoteRevision, NoteType, RoomNote } from "./wire.gen.ts";

// How each type reads: the chip on a note, the heading of its group and a
// one-line hint for the type select.
export const NOTE_TYPE_LABELS: Readonly<Record<NoteType, { readonly chip: string; readonly group: string; readonly hint: string }>> = {
  brief: { chip: "Brief", group: "Project brief", hint: "The project in one page: goal, conventions, current state. Agents always read it." },
  knowledge: { chip: "Knowledge", group: "Knowledge", hint: "A decision and its reason, a convention, a glossary entry." },
  memory: { chip: "Memory", group: "Memory", hint: "A dated fact from the conversation." },
  lesson: { chip: "Lesson", group: "Lessons", hint: "How this project does something." },
};

// The order a type's group follows the pinned notes in.
const GROUP_ORDER: readonly NoteType[] = ["knowledge", "lesson", "memory"];

// NoteSection is one group of the panel's list.
export interface NoteSection {
  readonly key: NoteType | "pinned";
  readonly label: string;
  readonly notes: readonly RoomNote[];
}

export interface NoteGroups {
  // The active notes: the brief, pinned, then knowledge, lessons, memory.
  readonly sections: readonly NoteSection[];
  // Superseded and archived notes, newest-updated first (a collapsed section).
  readonly history: readonly RoomNote[];
}

const newestFirst = (a: RoomNote, b: RoomNote) => b.updatedAtMs - a.updatedAtMs || a.noteId.localeCompare(b.noteId);

// groupNotes orders a room's notes for the panel: the active brief first, the
// pinned notes, then knowledge, lessons and memory, each newest-updated
// first; a superseded or archived note is history. Empty groups are left out.
export function groupNotes(notes: readonly RoomNote[]): NoteGroups {
  const sorted = [...notes].sort(newestFirst);
  const active = sorted.filter((n) => n.status === "active");
  const brief = active.filter((n) => n.type === NoteBrief);
  const rest = active.filter((n) => n.type !== NoteBrief);
  const sections: NoteSection[] = [];
  if (brief.length > 0) sections.push({ key: "brief", label: NOTE_TYPE_LABELS.brief.group, notes: brief });
  const pinned = rest.filter((n) => n.pinned);
  if (pinned.length > 0) sections.push({ key: "pinned", label: "Pinned", notes: pinned });
  for (const type of GROUP_ORDER) {
    const list = rest.filter((n) => !n.pinned && n.type === type);
    if (list.length > 0) sections.push({ key: type, label: NOTE_TYPE_LABELS[type].group, notes: list });
  }
  return { sections, history: sorted.filter((n) => n.status !== "active") };
}

// replaceableNotes are the active notes a new note of type may replace: the
// same type, not the brief (a new brief replaces the active one on its own).
export function replaceableNotes(notes: readonly RoomNote[], type: NoteType): RoomNote[] {
  if (type === NoteBrief) return [];
  return notes.filter((n) => n.status === "active" && n.type === type).sort(newestFirst);
}

// canEditNote: a superseded note is history and cannot change.
export function canEditNote(n: RoomNote): boolean {
  return n.status !== "superseded";
}

// --- what a note may hold (internal/notes.Check) ---------------------------

export function bodyLimit(type: NoteType): number {
  return type === NoteBrief ? MaxBriefBodyBytes : MaxNoteBodyBytes;
}

export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

// runeCount counts characters the way the server does (code points).
export function runeCount(s: string): number {
  return Array.from(s).length;
}

// truncateBytes cuts text to at most limit UTF-8 bytes on a character
// boundary, the end trimmed.
export function truncateBytes(text: string, limit: number): string {
  if (byteLength(text) <= limit) return text;
  let bytes = 0;
  let out = "";
  for (const ch of text) {
    const size = byteLength(ch);
    if (bytes + size > limit) break;
    bytes += size;
    out += ch;
  }
  return out.trimEnd();
}

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

// titleProblem says why a title is refused, or null.
export function titleProblem(title: string): string | null {
  const t = title.trim();
  if (t === "") return "A note needs a title";
  if (runeCount(t) > MaxNoteTitleRunes) return `A title is at most ${MaxNoteTitleRunes} characters`;
  if (CONTROL.test(t)) return "A title is one line";
  return null;
}

// bodyProblem says why a body is refused, or null.
export function bodyProblem(type: NoteType, body: string): string | null {
  const limit = bodyLimit(type);
  if (byteLength(body) > limit) return `A ${type}'s body is at most ${limit} bytes`;
  return null;
}

// --- the editor's form and the requests it makes ----------------------------

export interface NoteForm {
  readonly type: NoteType;
  readonly title: string;
  readonly body: string;
  readonly pinned: boolean;
  // The active note this one replaces ("" for none).
  readonly supersedes: string;
}

export function emptyNoteForm(type: NoteType = "knowledge"): NoteForm {
  return { type, title: "", body: "", pinned: false, supersedes: "" };
}

export function formOfNote(n: RoomNote): NoteForm {
  return { type: n.type, title: n.title, body: n.body, pinned: !!n.pinned, supersedes: "" };
}

function formProblem(f: NoteForm): string | null {
  return titleProblem(f.title) ?? bodyProblem(f.type, f.body);
}

// createRequest is the request a new note's form makes, or why it cannot.
export function createRequest(f: NoteForm, sources?: readonly NoteSource[]): { request?: NoteCreateRequest; error?: string } {
  const error = formProblem(f);
  if (error) return { error };
  const isBrief = f.type === NoteBrief;
  const request: NoteCreateRequest = { type: f.type, title: f.title.trim(), body: f.body };
  if (f.pinned && !isBrief) request.pinned = true;
  if (f.supersedes && !isBrief) request.supersedes = f.supersedes;
  if (sources && sources.length > 0) request.sources = [...sources];
  return { request };
}

// updateRequest is the change an edit of note n makes: only the fields that
// differ, with the revision the editor read, so a change made meanwhile is
// refused (409). unchanged when nothing differs.
export function updateRequest(n: RoomNote, f: NoteForm): { request?: NoteUpdateRequest; error?: string; unchanged?: boolean } {
  const request: NoteUpdateRequest = { expectedRevision: n.revision };
  let changed = false;
  if (f.title.trim() !== n.title) {
    request.title = f.title.trim();
    changed = true;
  }
  if (f.body !== n.body) {
    request.body = f.body;
    changed = true;
  }
  if (n.type !== NoteBrief && f.pinned !== !!n.pinned) {
    request.pinned = f.pinned;
    changed = true;
  }
  if (!changed) return { unchanged: true };
  const error = formProblem({ ...f, type: n.type });
  return error ? { error } : { request };
}

// --- what a note looks like in a list ---------------------------------------

// notePreview is a note's body on one line, cut to max characters.
export function notePreview(body: string, max = 140): string {
  const flat = body.replace(/\s+/g, " ").trim();
  const chars = Array.from(flat);
  return chars.length > max ? `${chars.slice(0, max - 1).join("").trimEnd()}…` : flat;
}

// noteDates are a note's relative labels ("verified 3 hours ago", "not
// verified") and the absolute time for the tooltip.
export function noteDates(n: RoomNote, now: number = Date.now()): { updated: string; verified: string; verifiedTitle: string } {
  return {
    updated: `updated ${agoText(n.updatedAtMs, now)}`,
    verified: n.verifiedAtMs ? `verified ${agoText(n.verifiedAtMs, now)}` : "not verified",
    verifiedTitle: n.verifiedAtMs ? exactTime(n.verifiedAtMs) : "Nobody has confirmed this note is still true",
  };
}

// sourceLabel says where a note came from.
export function sourceLabel(s: NoteSource): string {
  if (s.kind === NoteSourceMessage) return `message #${s.ref}`;
  return s.version ? `deliverable ${s.ref}, version ${s.version}` : `deliverable ${s.ref}`;
}

// --- "Save to memory" ---------------------------------------------------------

// The types a message can be saved as (a brief is written, not saved).
export const SAVE_TYPES: readonly NoteType[] = ["knowledge", "memory", "lesson"];

// noteDraftFromMessage proposes a note from a chat message: the title is its
// first line (one line, at most the title limit), the body its text cut to
// the body limit on a character boundary, the source the message itself.
export function noteDraftFromMessage(text: string, seq: number): { title: string; body: string; sources: NoteSource[] } {
  const line = text.split("\n").map((l) => l.trim()).find((l) => l !== "") ?? "";
  const plain = line.replace(/^#+\s*/, "").replace(/[\u0000-\u001f\u007f-\u009f]+/g, " ").replace(/\s+/g, " ").trim();
  const chars = Array.from(plain);
  const title = chars.length > MaxNoteTitleRunes ? `${chars.slice(0, MaxNoteTitleRunes - 1).join("").trimEnd()}…` : plain;
  return {
    title,
    body: truncateBytes(text.trim(), MaxNoteBodyBytes),
    sources: [{ kind: NoteSourceMessage, ref: String(seq) }],
  };
}
