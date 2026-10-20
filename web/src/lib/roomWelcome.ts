// A room's welcome and quick prompts (docs/prd/2026-10-20-room-setup-by-conversation.md
// §2.1): the room's own (internal/wire/rooms.go RoomDetail), shown to
// every member while the room is empty (RoomStartCard) and edited by its
// owner or admins in the room's settings (RoomWelcomeSection). A quick
// prompt's click puts its text into the message box; nothing is sent. The
// server checks the bounds (a room package's); the helpers here are pure.

import type { RoomQuickPrompt } from "./wire.gen.ts";

export type { RoomQuickPrompt } from "./wire.gen.ts";

/** What an empty room shows: its welcome and the quick prompts with text. */
export interface RoomStart {
  readonly welcome: string;
  readonly prompts: readonly RoomQuickPrompt[];
}

/** roomStartOf is what an empty room shows, or null when it has neither a
 *  welcome nor a quick prompt (a preview of a room has none). */
export function roomStartOf(room: { readonly welcome?: string; readonly quickPrompts?: readonly RoomQuickPrompt[] } | null | undefined): RoomStart | null {
  const welcome = room?.welcome?.trim() ?? "";
  const prompts = (room?.quickPrompts ?? []).filter((q) => q.prompt.trim());
  return welcome || prompts.length > 0 ? { welcome, prompts } : null;
}

/** cleanQuickPrompts is the settings' rows as a save sends them: trimmed,
 *  with the rows left wholly empty dropped (a half-filled row is kept, so
 *  the server says what it lacks). */
export function cleanQuickPrompts(rows: readonly RoomQuickPrompt[]): RoomQuickPrompt[] {
  return rows.map((q) => ({ title: q.title.trim(), prompt: q.prompt.trim() })).filter((q) => q.title || q.prompt);
}
