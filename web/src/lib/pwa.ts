// Installed web app (R14): the pure decisions behind registering the one
// service worker and the iOS "add to Home Screen" hint. Browser glue lives in
// appWorker.ts; the worker itself is public/push-sw.js.

// What this runtime offers the app worker. The desktop shell never registers
// it: it embeds its own bundle (no offline shell needed) and receives
// notifications through the notifier components, not web push.
export interface WorkerEnv {
  inDesktopShell: boolean;
  secureContext: boolean;
  serviceWorker: boolean;
}

export function appWorkerWanted(e: WorkerEnv): boolean {
  return !e.inDesktopShell && e.secureContext && e.serviceWorker;
}

// The navigator/display facts the iOS checks read.
export interface DisplayEnv {
  userAgent: string;
  platform: string;
  maxTouchPoints: number;
  // navigator.standalone: iOS Safari's own "launched from the Home Screen".
  navigatorStandalone?: boolean;
  // matchMedia("(display-mode: standalone)").matches
  displayModeStandalone: boolean;
}

// isIos reports iPhone/iPad/iPod. iPadOS asks for desktop sites by default
// and reports a Mac platform, so a touch-capable "MacIntel" counts as iOS.
export function isIos(e: DisplayEnv): boolean {
  if (/\b(iPhone|iPad|iPod)\b/.test(e.userAgent)) return true;
  return e.platform === "MacIntel" && e.maxTouchPoints > 1;
}

// isStandalone reports whether the page runs as the installed app.
export function isStandalone(e: DisplayEnv): boolean {
  return e.navigatorStandalone === true || e.displayModeStandalone;
}

// iosInstallHint: on iOS, web push exists only inside the installed app
// (iOS 16.4+), so a Safari tab without push support should say how to get it
// rather than "this browser cannot".
export function iosInstallHint(e: DisplayEnv): boolean {
  return isIos(e) && !isStandalone(e);
}

// displayEnv reads the live facts (browser only).
export function displayEnv(): DisplayEnv {
  const nav = navigator as Navigator & { standalone?: boolean };
  return {
    userAgent: nav.userAgent,
    platform: nav.platform,
    maxTouchPoints: nav.maxTouchPoints || 0,
    navigatorStandalone: nav.standalone,
    displayModeStandalone: typeof window.matchMedia === "function" && window.matchMedia("(display-mode: standalone)").matches,
  };
}
