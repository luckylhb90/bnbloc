import { pushSupport, pushToggleState, sameKey, subscriptionBody, urlBase64ToBytes } from "./webPushModel.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

const browser = { inDesktopShell: false, secureContext: true, serviceWorker: true, pushManager: true, notifications: true };

test("the desktop shell never offers browser push", () => {
  eq(pushSupport({ ...browser, inDesktopShell: true }), "desktop", "desktop");
  eq(pushToggleState({ support: "desktop", serverKey: "k", permission: "granted", subscribed: true }), "hidden", "hidden");
});

test("browser support needs a secure context and the push APIs", () => {
  eq(pushSupport(browser), "supported", "full browser");
  eq(pushSupport({ ...browser, secureContext: false }), "unsupported", "http origin");
  eq(pushSupport({ ...browser, pushManager: false }), "unsupported", "no PushManager");
  eq(pushSupport({ ...browser, serviceWorker: false }), "unsupported", "no service worker");
});

test("the toggle follows server config, permission and subscription", () => {
  const s = { support: "supported" as const, serverKey: "k", permission: "granted" as NotificationPermission, subscribed: true };
  eq(pushToggleState({ ...s, serverKey: null }), "hidden", "channel disabled");
  eq(pushToggleState({ ...s, support: "unsupported" }), "unsupported", "unsupported");
  eq(pushToggleState({ ...s, permission: "denied" }), "blocked", "denied");
  eq(pushToggleState(s), "on", "subscribed");
  eq(pushToggleState({ ...s, subscribed: false }), "off", "not subscribed");
  eq(pushToggleState({ ...s, permission: "default" }), "off", "permission not granted yet");
});

test("the VAPID key decodes from base64url", () => {
  const bytes = urlBase64ToBytes("BAEC_-8");
  eq(Array.from(bytes), [4, 1, 2, 255, 239], "decoded");
  eq(sameKey(bytes.buffer as ArrayBuffer, "BAEC_-8"), true, "same key");
  eq(sameKey(bytes.buffer as ArrayBuffer, "BAED_-8"), false, "rotated key");
  eq(sameKey(null, "BAEC_-8"), false, "no key");
});

test("subscription bodies need an https endpoint and both keys", () => {
  eq(
    subscriptionBody({ endpoint: "https://push.example/x", keys: { p256dh: "p", auth: "a" } }),
    { endpoint: "https://push.example/x", keys: { p256dh: "p", auth: "a" } },
    "valid",
  );
  eq(subscriptionBody({ endpoint: "http://push.example/x", keys: { p256dh: "p", auth: "a" } }), null, "http");
  eq(subscriptionBody({ endpoint: "https://push.example/x", keys: { p256dh: "p" } }), null, "no auth");
  eq(subscriptionBody({ endpoint: null, keys: null }), null, "empty");
});
