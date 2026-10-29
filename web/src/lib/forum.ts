// Forum channels (docs/prd/2026-10-28-channels-and-threads.md §2.1c, C20):
// a channel whose every top-level post opens a thread. The pure rules the
// web draws one by: its vocabulary as the settings dialog edits it (one name
// a line, ids derived from names), the order of its thread list, its
// filters and its board. Unit-tested (forum.test.ts); the server validates
// the same bounds (services/room/channels.go cleanVocabulary).
import { MaxChannelStatuses, MaxChannelTagName, MaxChannelTags, type ChannelSettings, type ChannelStatus, type ChannelTag, type RoomChannel } from "./wire.gen.ts";

/** isForum is the web's reading of ChannelSettings.Forum (postingStyle threads). */
export function isForum(s: ChannelSettings | undefined): boolean {
  return s?.postingStyle === "threads";
}

/** vocabularyId is a tag's or status's id from its name: lower-case letters,
 *  digits, - and _ (other characters become -), within the bound; a name of
 *  none of those (CJK) keeps a stable id of its characters' codes. */
export function vocabularyId(name: string): string {
  const slug = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "");
  if (slug) return slug.slice(0, MaxChannelTagName);
  return [...name.trim()]
    .map((c) => c.codePointAt(0)!.toString(36))
    .join("-")
    .slice(0, MaxChannelTagName);
}

/** vocabularyFromLines is a dialog's "one a line" text as tags or statuses:
 *  blank lines dropped, a repeated id kept once, names held to the bound,
 *  at most `max`; an existing id of the same name is kept (renaming a line
 *  keeps no id: a new name is a new tag). */
export function vocabularyFromLines<T extends ChannelTag | ChannelStatus>(text: string, max: number, existing: readonly T[] = []): T[] {
  const out: T[] = [];
  const seen = new Set<string>();
  for (const line of text.split("\n")) {
    const name = line.trim().slice(0, MaxChannelTagName);
    if (!name) continue;
    const known = existing.find((t) => t.name === name);
    const id = known?.id ?? vocabularyId(name);
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push({ ...(known ?? {}), id, name } as T);
    if (out.length >= max) break;
  }
  return out;
}

export const tagsFromLines = (text: string, existing?: readonly ChannelTag[]) => vocabularyFromLines<ChannelTag>(text, MaxChannelTags, existing);
export const statusesFromLines = (text: string, existing?: readonly ChannelStatus[]) => vocabularyFromLines<ChannelStatus>(text, MaxChannelStatuses, existing);
export const linesOf = (v: readonly { readonly name: string }[] | undefined) => (v ?? []).map((t) => t.name).join("\n");

/** A forum's thread list order: open before closed, then the newest
 *  activity first (its last reply; a thread with none by its root, newest
 *  root first). */
export function forumOrder(threads: readonly RoomChannel[]): RoomChannel[] {
  const last = (c: RoomChannel) => c.thread?.lastReplyAtMs ?? 0;
  const root = (c: RoomChannel) => c.thread?.rootSeq ?? 0;
  const closed = (c: RoomChannel) => Number((c.archivedAtMs ?? 0) > 0);
  return [...threads].sort((a, b) => closed(a) - closed(b) || last(b) - last(a) || root(b) - root(a) || a.channelId.localeCompare(b.channelId));
}

export interface ForumFilter {
  readonly tag?: string;
  readonly status?: string;
}

/** forumFiltered keeps the threads of a tag and of a status ("" any). */
export function forumFiltered(threads: readonly RoomChannel[], f: ForumFilter): RoomChannel[] {
  return threads.filter((t) => (!f.tag || (t.thread?.tags ?? []).includes(f.tag)) && (!f.status || (t.thread?.status ?? "") === f.status));
}

export interface BoardColumn {
  // "" is the column of threads with no status (or one no longer defined).
  readonly id: string;
  readonly name: string;
  readonly threads: readonly RoomChannel[];
}

/** boardColumns is the board: one column per status in the owners' order,
 *  then "No status" for the rest (shown only when it holds a thread). */
export function boardColumns(statuses: readonly ChannelStatus[], threads: readonly RoomChannel[]): BoardColumn[] {
  const known = new Set(statuses.map((s) => s.id));
  const cols: BoardColumn[] = statuses.map((s) => ({ id: s.id, name: s.name, threads: forumOrder(threads.filter((t) => t.thread?.status === s.id)) }));
  const rest = forumOrder(threads.filter((t) => !known.has(t.thread?.status ?? "")));
  return rest.length > 0 || statuses.length === 0 ? [{ id: "", name: "No status", threads: rest }, ...cols] : cols;
}
