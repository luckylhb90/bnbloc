// Browser push notifications (N4): the pure decisions behind the Settings
// toggle and the subscription the browser hands the server. Shapes mirror
// internal/wire/webpush.go; the contract is documented in docs/notifications.md.
// Browser glue (service worker, PushManager, API calls) lives in webPush.ts.

// The POST /v1/my/push-subscriptions body (PushSubscription.toJSON()).
export interface PushSubscriptionBody {
  endpoint: string;
  keys: { p256dh: string; auth: string };
}

// What this runtime can do. The desktop shell never registers the push
// worker: it is not a browser tab, and the member gets notifications there
// through the notifier components instead.
export interface PushCapabilities {
  inDesktopShell: boolean;
  secureContext: boolean;
  serviceWorker: boolean;
  pushManager: boolean;
  notifications: boolean;
}

export type PushSupport = "supported" | "desktop" | "unsupported";

export function pushSupport(c: PushCapabilities): PushSupport {
  if (c.inDesktopShell) return "desktop";
  if (!c.secureContext || !c.serviceWorker || !c.pushManager || !c.notifications) return "unsupported";
  return "supported";
}

// The toggle's state on this device:
//   hidden      - the deployment has no VAPID key (channel disabled) or this
//                 is the desktop shell: no toggle at all
//   unsupported - this browser cannot receive web push
//   blocked     - the member denied notifications for this site
//   on / off    - subscribed or not
export type PushToggleState = "hidden" | "unsupported" | "blocked" | "on" | "off";

export function pushToggleState(args: {
  support: PushSupport;
  serverKey: string | null;
  permission: NotificationPermission | "unknown";
  subscribed: boolean;
}): PushToggleState {
  if (args.support === "desktop" || !args.serverKey) return "hidden";
  if (args.support === "unsupported") return "unsupported";
  if (args.permission === "denied") return "blocked";
  return args.subscribed && args.permission === "granted" ? "on" : "off";
}

// urlBase64ToBytes decodes the server's base64url VAPID key into the
// applicationServerKey PushManager.subscribe expects.
export function urlBase64ToBytes(b64url: string): Uint8Array {
  const padded = b64url.trim() + "=".repeat((4 - (b64url.trim().length % 4)) % 4);
  const bin = atob(padded.replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// sameKey reports whether a subscription was made with the server's current
// VAPID key (after a key rotation the old subscription can no longer be
// delivered to and must be replaced).
export function sameKey(subscriptionKey: ArrayBuffer | null | undefined, serverKey: string): boolean {
  if (!subscriptionKey) return false;
  const a = new Uint8Array(subscriptionKey);
  const b = urlBase64ToBytes(serverKey);
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

// subscriptionBody extracts the server body from PushSubscription.toJSON(),
// or null when the browser handed back something unusable.
export function subscriptionBody(json: {
  endpoint?: string | null;
  keys?: Record<string, string> | null;
}): PushSubscriptionBody | null {
  const endpoint = json.endpoint ?? "";
  const p256dh = json.keys?.p256dh ?? "";
  const auth = json.keys?.auth ?? "";
  if (!endpoint.startsWith("https://") || !p256dh || !auth) return null;
  return { endpoint, keys: { p256dh, auth } };
}
