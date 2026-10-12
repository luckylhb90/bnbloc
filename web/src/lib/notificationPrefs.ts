// Member notification preferences (GET/PUT /v1/my/notification-prefs) and
// the notifier components a member is reached through (/v1/my/notifiers).
// Shapes are internal/wire/notifications.go's (wire.gen.ts); the contract is documented in
// docs/notifications.md.

export type { MemberNotifier, NotificationPrefs, NotificationTestResult } from "./wire.gen.ts";
import type { NotificationPrefs, NotificationTestResult } from "./wire.gen.ts";

// NotificationToggle is a per-type switch of the member's preferences.
export type NotificationToggle = "mentions" | "agentFinished" | "agentFailed" | "borrowRequests" | "contentShared" | "contentReview";

// NOTIFICATION_TOGGLES are Settings → Notifications' switches, one per
// notification type a member can turn off.
export const NOTIFICATION_TOGGLES: readonly { readonly key: NotificationToggle; readonly label: string; readonly hint: string }[] = [
  { key: "mentions", label: "Mentions", hint: "Someone @-mentions you in a room." },
  { key: "agentFinished", label: "Agent finished", hint: "An agent finished a request you sent it." },
  { key: "agentFailed", label: "Agent failed", hint: "An agent could not complete a request you sent it." },
  { key: "borrowRequests", label: "Borrow requests", hint: "A member asks to use (or needs more runs of) your agent." },
  { key: "contentShared", label: "Shared with you", hint: "Someone shares an expert, skill pack or prompt pack with you, or with a room you join." },
  { key: "contentReview", label: "Content reviews", hint: "Content waits for your approval as an organization admin, or an admin decided on content you asked to publish." },
];

// The server default for a member who never saved preferences.
export const DEFAULT_NOTIFICATION_PREFS: NotificationPrefs = {
  mentions: true,
  agentFinished: true,
  agentFailed: true,
  borrowRequests: true,
  contentShared: true,
  contentReview: true,
  quietRooms: [],
};

// isRoomMuted reports whether the member muted notifications for roomId.
export function isRoomMuted(prefs: NotificationPrefs | null, roomId: string): boolean {
  return !!prefs && prefs.quietRooms.includes(roomId);
}

// withRoomMuted returns prefs with roomId added to (muted) or removed from the
// quiet rooms, leaving every other field untouched.
export function withRoomMuted(prefs: NotificationPrefs, roomId: string, muted: boolean): NotificationPrefs {
  const rest = prefs.quietRooms.filter((r) => r !== roomId);
  return { ...prefs, quietRooms: muted ? [...rest, roomId] : rest };
}

// describeTestResult renders a test-send outcome as a toast message.
export function describeTestResult(r: NotificationTestResult): { ok: boolean; message: string } {
  if (r.delivered) return { ok: true, message: `Test notification delivered (HTTP ${r.status ?? 200})` };
  return { ok: false, message: `Test notification failed: ${r.error || "no response"}` };
}
