// Desktop-notification helper. Wraps the browser Notification API so the app can
// surface a native notification when a new message arrives while the tab is in
// the background. Everything is a no-op when notifications are unsupported or the
// user has denied permission, so callers never have to feature-detect.

export interface NotifyOptions {
  body?: string;
  // A stable tag coalesces repeated notifications (e.g. per-room) into one, so a
  // burst of messages doesn't stack up a wall of toasts.
  tag?: string;
  // Invoked when the user clicks the notification (after focusing the window).
  onClick?: () => void;
}

// notificationsSupported reports whether the Notification API exists in this
// runtime (absent in some embedded webviews and older browsers).
export function notificationsSupported(): boolean {
  return typeof window !== "undefined" && "Notification" in window;
}

// requestNotificationPermission asks the user to allow notifications. Returns
// true only when permission ends up granted. Safe to call repeatedly: if
// permission is already decided the browser resolves immediately without a
// prompt. No-ops (returns false) when unsupported.
export async function requestNotificationPermission(): Promise<boolean> {
  if (!notificationsSupported()) return false;
  if (Notification.permission === "granted") return true;
  if (Notification.permission === "denied") return false;
  try {
    const result = await Notification.requestPermission();
    return result === "granted";
  } catch {
    return false;
  }
}

// notify shows a desktop notification only when the tab is hidden and permission
// has been granted. It is a deliberate no-op when the tab is focused (the user
// already sees the update), when unsupported, or when permission is not granted.
// Returns true if a notification was actually shown.
export function notify(title: string, opts: NotifyOptions = {}): boolean {
  if (!notificationsSupported()) return false;
  if (Notification.permission !== "granted") return false;
  // Only interrupt the user when they're not already looking at the tab.
  if (typeof document !== "undefined" && !document.hidden) return false;
  try {
    const n = new Notification(title, { body: opts.body, tag: opts.tag });
    if (opts.onClick) {
      n.onclick = () => {
        try {
          window.focus();
        } catch {
          // ignore — focusing may be blocked; still fire the handler.
        }
        opts.onClick?.();
        n.close();
      };
    }
    return true;
  } catch {
    // Constructing a Notification can throw in some contexts (e.g. requires a
    // service worker on mobile Chrome); degrade silently.
    return false;
  }
}
