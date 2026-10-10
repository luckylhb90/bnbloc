import { inDesktopShell } from "./config.ts";
import { appWorkerWanted } from "./pwa.ts";

// The app's one service worker (public/push-sw.js): browser push (N4) and the
// installed app's offline shell (R14). This module is the only place that
// registers it — at page load (main.tsx) and, idempotently, when browser push
// is turned on. Never in the desktop shell.

const WORKER_URL = "./push-sw.js";

let registration: Promise<ServiceWorkerRegistration | null> | null = null;

// registerAppWorker registers the worker once per page and resolves to its
// registration, or null where there is none (desktop shell, insecure origin,
// no worker API, registration refused).
export function registerAppWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!registration) {
    const wanted = appWorkerWanted({
      inDesktopShell: inDesktopShell(),
      secureContext: typeof window !== "undefined" && window.isSecureContext,
      serviceWorker: typeof navigator !== "undefined" && "serviceWorker" in navigator,
    });
    registration = wanted
      ? navigator.serviceWorker.register(WORKER_URL, { scope: "./" }).catch(() => null)
      : Promise.resolve(null);
  }
  return registration;
}

// registerAppWorkerAfterLoad defers the boot registration until the page has
// loaded, so the worker's shell priming never competes with first paint.
export function registerAppWorkerAfterLoad(): void {
  if (typeof window === "undefined") return;
  if (document.readyState === "complete") void registerAppWorker();
  else window.addEventListener("load", () => void registerAppWorker(), { once: true });
}
