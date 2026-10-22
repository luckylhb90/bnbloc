// Gateway base URL resolution. The same web bundle runs in three places:
//
//   1. Gateway-embedded (served by bnbloc-gateway at its own origin): requests
//      are same-origin, so the base is "" and paths stay relative.
//   2. Browser pointed at a deployed gateway: same as (1) — same origin.
//   3. Tauri desktop shell (bundled): the UI loads from a tauri:// / app origin,
//      so /v1 and /api must be sent to an absolute gateway URL the shell knows.
//
// The desktop shell injects that URL on the window before the app boots
// (window.__BNBLOC_GATEWAY__). Everywhere else the base is empty and behavior
// is byte-for-byte what it was before this indirection.

import { StorageKey, readStored, writeStored } from "./storageKeys.ts";

declare global {
  interface Window {
    __BNBLOC_GATEWAY__?: string;
    __TAURI__?: unknown;
    __TAURI_INTERNALS__?: unknown;
  }
}

function normalizeBase(raw: string): string {
  const trimmed = raw.trim().replace(/\/+$/, "");
  return trimmed;
}


function injectedGatewayBase(): string {
  return normalizeBase(
    (typeof window !== "undefined" && window.__BNBLOC_GATEWAY__) || "",
  );
}


function persistedGatewayOverride(): string {
  return normalizeBase(readStored(StorageKey.gatewayOverride));
}

let apiBase = persistedGatewayOverride() || injectedGatewayBase();

// gatewayBase returns the absolute origin for /v1 + /api, or "" for same-origin.
export function gatewayBase(): string {
  return apiBase;
}

// bindGatewayBase points THIS window at its account's gateway (what a window
// shows is one account's, docs/prd/2026-10-22-multi-account.md §2.1). In memory
// only: the persisted override is where a sign-in starts, not where an
// account lives.
export function bindGatewayBase(url: string) {
  apiBase = normalizeBase(url);
}

export function setGatewayOverride(url: string) {
  const next = normalizeBase(url);
  writeStored(StorageKey.gatewayOverride, next);
  apiBase = next;
}

export function clearGatewayOverride() {
  writeStored(StorageKey.gatewayOverride, "");
  apiBase = injectedGatewayBase();
}

// defaultGatewayBase is the gateway this build was configured with (injected
// by the desktop shell from its build-time default), or "" when there is none.
export function defaultGatewayBase(): string {
  return injectedGatewayBase();
}

// needsGatewaySetup: a desktop shell with no gateway at all (no build-time
// default and no saved choice) cannot reach anything until the member enters
// one. A browser is always same-origin, so it never needs setup.
export function needsGatewaySetup(): boolean {
  return inDesktopShell() && !gatewayBase();
}

// apiUrlOn is a gateway path on a given gateway (an account of the vault that
// is not this window's): "" is same-origin.
export function apiUrlOn(gateway: string, path: string): string {
  const base = normalizeBase(gateway);
  return base ? base + path : path;
}

// apiUrl prefixes a gateway path (e.g. "/v1/rooms") with the configured base.
export function apiUrl(path: string): string {
  return apiBase ? apiBase + path : path;
}

// inDesktopShell reports whether the client runs inside the Tauri shell, so the
// UI can render shell-only surfaces (the Bridge panel) and hide them in a plain
// browser. Feature-detection only — the web app fully degrades without it.
export function inDesktopShell(): boolean {
  return (
    typeof window !== "undefined" &&
    (window.__TAURI__ !== undefined || window.__TAURI_INTERNALS__ !== undefined)
  );
}

// appLinkBase is the base of links a member hands to someone else (a
// deliverable link, a public share link): the gateway root in the desktop
// shell (the app's own origin is tauri:// and means nothing elsewhere),
// else this page.
export function appLinkBase(): string {
  return inDesktopShell() ? gatewayBase() || window.location.href : window.location.href;
}
