import { ApiError, req } from "./api.ts";
import { registerAppWorker } from "./appWorker.ts";
import { inDesktopShell } from "./config.ts";
import {
  pushSupport,
  pushToggleState,
  sameKey,
  subscriptionBody,
  urlBase64ToBytes,
  type PushSupport,
  type PushToggleState,
} from "./webPushModel.ts";

// Browser push notifications (N4): the PushManager subscription on the app's
// service worker (appWorker.ts registers web/public/push-sw.js, served from
// the web root by the gateway) and the realtime routes that store it. Never
// used inside the desktop shell.

export function browserPushSupport(): PushSupport {
  return pushSupport({
    inDesktopShell: inDesktopShell(),
    secureContext: typeof window !== "undefined" && window.isSecureContext,
    serviceWorker: typeof navigator !== "undefined" && "serviceWorker" in navigator,
    pushManager: typeof window !== "undefined" && "PushManager" in window,
    notifications: typeof window !== "undefined" && "Notification" in window,
  });
}

// vapidPublicKey returns the deployment's VAPID key, or null when the
// channel is disabled (404).
export async function vapidPublicKey(): Promise<string | null> {
  try {
    const r = await req<{ publicKey: string }>("/v1/push/vapid-public-key");
    return r.publicKey || null;
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

async function existingSubscription(): Promise<PushSubscription | null> {
  const reg = await navigator.serviceWorker.getRegistration();
  return reg ? reg.pushManager.getSubscription() : null;
}

async function saveSubscription(sub: PushSubscription): Promise<void> {
  const body = subscriptionBody(sub.toJSON());
  if (!body) throw new Error("This browser returned an unusable push subscription");
  await req<void>("/v1/my/push-subscriptions", { method: "POST", body: JSON.stringify(body), as: "none" });
}

export interface BrowserPushStatus {
  state: PushToggleState;
  serverKey: string | null;
}

// browserPushStatus reports this device's toggle state. A live subscription
// is re-registered with the server (idempotent), which also restores one the
// server dropped; one made with a rotated VAPID key reads as off.
export async function browserPushStatus(): Promise<BrowserPushStatus> {
  const support = browserPushSupport();
  if (support === "desktop") return { state: "hidden", serverKey: null };
  const serverKey = await vapidPublicKey();
  if (support !== "supported" || !serverKey) {
    return { state: pushToggleState({ support, serverKey, permission: "unknown", subscribed: false }), serverKey };
  }
  let sub = await existingSubscription();
  if (sub && !sameKey(sub.options.applicationServerKey, serverKey)) {
    await sub.unsubscribe().catch(() => {});
    sub = null;
  }
  if (sub && Notification.permission === "granted") await saveSubscription(sub);
  return {
    state: pushToggleState({ support, serverKey, permission: Notification.permission, subscribed: !!sub }),
    serverKey,
  };
}

// enableBrowserPush asks for permission, registers the worker, subscribes
// with the deployment's key and stores the subscription for this member.
export async function enableBrowserPush(serverKey: string): Promise<PushToggleState> {
  if (browserPushSupport() !== "supported") return "unsupported";
  const permission = await Notification.requestPermission();
  if (permission !== "granted") return permission === "denied" ? "blocked" : "off";
  if (!(await registerAppWorker())) throw new Error("This browser could not start the notification worker");
  const reg = await navigator.serviceWorker.ready;
  const sub =
    (await reg.pushManager.getSubscription()) ??
    (await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToBytes(serverKey) as BufferSource,
    }));
  await saveSubscription(sub);
  return "on";
}

// disableBrowserPush removes this device's subscription from the server and
// the browser.
export async function disableBrowserPush(): Promise<PushToggleState> {
  const sub = await existingSubscription();
  if (sub) {
    await req<void>("/v1/my/push-subscriptions", {
      method: "DELETE",
      body: JSON.stringify({ endpoint: sub.endpoint }),
      as: "none",
    });
    await sub.unsubscribe().catch(() => {});
  }
  return "off";
}

// listenForPushNavigation applies a clicked notification's deep link in an
// already-open tab (the worker posts {type: "bnbloc.push.open", path}). It
// returns the unsubscribe function (a no-op where there is no worker API).
export function listenForPushNavigation(): () => void {
  if (typeof navigator === "undefined" || !("serviceWorker" in navigator)) return () => {};
  const onMessage = (e: MessageEvent) => {
    const data = e.data as { type?: string; path?: string } | null;
    if (data?.type === "bnbloc.push.open" && typeof data.path === "string" && data.path.startsWith("#/")) {
      window.location.hash = data.path;
    }
  };
  navigator.serviceWorker.addEventListener("message", onMessage);
  return () => navigator.serviceWorker.removeEventListener("message", onMessage);
}

// forgetBrowserPush drops this browser's push subscription at the push
// service on sign-out (no session needed): the next delivery to it answers
// 410 and the server deletes the row, so a shared browser stops showing the
// previous member's notifications.
export async function forgetBrowserPush(): Promise<void> {
  if (browserPushSupport() !== "supported") return;
  const sub = await existingSubscription();
  await sub?.unsubscribe();
}
