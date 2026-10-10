// Member notification preferences (GET/PUT /v1/my/notification-prefs) and
// the notifier components a member is reached through (/v1/my/notifiers).
// Shapes mirror internal/wire notifications.go; the contract is documented in
// docs/notifications.md.

export interface NotificationPrefs {
  mentions: boolean;
  agentFinished: boolean;
  agentFailed: boolean;
  borrowRequests: boolean;
  quietRooms: string[];
}

// A notifier component as the member sees it (the organization's delivery).
// An opt-in notifier reaches the member only once they turned it on.
export interface MemberNotifier {
  name: string;
  description?: string;
  optIn: boolean;
  installed: boolean;
}

export interface NotificationRecipientResult {
  userId: string;
  ok: boolean;
  error?: string;
  retryable?: boolean;
}

export interface NotificationTestResult {
  delivered: boolean;
  status?: number;
  error?: string;
  eventId: string;
  results?: NotificationRecipientResult[];
}

// The server default for a member who never saved preferences.
export const DEFAULT_NOTIFICATION_PREFS: NotificationPrefs = {
  mentions: true,
  agentFinished: true,
  agentFailed: true,
  borrowRequests: true,
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
