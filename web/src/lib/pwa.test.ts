import { appWorkerWanted, iosInstallHint, isIos, isStandalone, type DisplayEnv } from "./pwa.ts";

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
  if (a !== b) throw new Error(`${message}: got ${String(a)}, want ${String(b)}`);
}

const browser = { inDesktopShell: false, secureContext: true, serviceWorker: true };

test("the app worker registers in secure browsers only, never in the desktop shell", () => {
  eq(appWorkerWanted(browser), true, "browser");
  eq(appWorkerWanted({ ...browser, inDesktopShell: true }), false, "desktop shell");
  eq(appWorkerWanted({ ...browser, secureContext: false }), false, "http origin");
  eq(appWorkerWanted({ ...browser, serviceWorker: false }), false, "no worker API");
});

const iphoneSafari: DisplayEnv = {
  userAgent:
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
  platform: "iPhone",
  maxTouchPoints: 5,
  navigatorStandalone: false,
  displayModeStandalone: false,
};
const ipadDesktopUA: DisplayEnv = {
  userAgent:
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
  platform: "MacIntel",
  maxTouchPoints: 5,
  navigatorStandalone: false,
  displayModeStandalone: false,
};
const macSafari: DisplayEnv = { ...ipadDesktopUA, maxTouchPoints: 0, navigatorStandalone: undefined };
const androidChrome: DisplayEnv = {
  userAgent:
    "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Mobile Safari/537.36",
  platform: "Linux armv8l",
  maxTouchPoints: 5,
  displayModeStandalone: false,
};

test("iOS detection covers iPhone and iPadOS's desktop user agent, not a Mac", () => {
  eq(isIos(iphoneSafari), true, "iPhone");
  eq(isIos(ipadDesktopUA), true, "iPad asking for desktop sites");
  eq(isIos(macSafari), false, "Mac");
  eq(isIos(androidChrome), false, "Android");
});

test("standalone is the Home Screen app on iOS or display-mode standalone elsewhere", () => {
  eq(isStandalone(iphoneSafari), false, "Safari tab");
  eq(isStandalone({ ...iphoneSafari, navigatorStandalone: true }), true, "iOS Home Screen app");
  eq(isStandalone({ ...androidChrome, displayModeStandalone: true }), true, "installed app");
});

test("the install hint shows on iOS outside the installed app only", () => {
  eq(iosInstallHint(iphoneSafari), true, "iPhone Safari tab");
  eq(iosInstallHint(ipadDesktopUA), true, "iPad Safari tab");
  eq(iosInstallHint({ ...iphoneSafari, navigatorStandalone: true }), false, "installed on iPhone");
  eq(iosInstallHint(androidChrome), false, "Android");
  eq(iosInstallHint(macSafari), false, "Mac");
});
