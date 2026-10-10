import { req } from "./api.ts";
import type { MemberNotifier, NotificationPrefs, NotificationTestResult } from "./notificationPrefs.ts";

function notifierPath(name: string, suffix: string): string {
  return `/v1/my/notifiers/${encodeURIComponent(name)}${suffix}`;
}

export const notificationsApi = {
  prefs: () => req<NotificationPrefs>("/v1/my/notification-prefs"),
  savePrefs: (prefs: NotificationPrefs) =>
    req<NotificationPrefs>("/v1/my/notification-prefs", { method: "PUT", body: JSON.stringify(prefs) }),
  // Notifier components visible to the member and whether they opted in.
  notifiers: () => req<{ notifiers: MemberNotifier[] }>("/v1/my/notifiers"),
  testNotifier: (name: string) => req<NotificationTestResult>(notifierPath(name, "/test"), { method: "POST" }),
};
