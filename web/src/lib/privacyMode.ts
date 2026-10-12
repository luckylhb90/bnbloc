import { useSyncExternalStore } from "react";
import { maskEmailsInText } from "./displayNames.ts";
import { StorageKey, readStored, writeStored } from "./storageKeys.ts";

// Privacy mode (PRD 2026-10-10 §2): "Hide email addresses" for demos and
// screenshots. A presentation setting of this browser only (localStorage);
// nothing is hidden from the server.
const KEY = StorageKey.privacyMode;
const listeners = new Set<() => void>();

function read(): boolean {
  return readStored(KEY) === "1";
}

let current = read();

function emit(): void {
  for (const fn of listeners) fn();
}

if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key !== KEY) return;
    current = read();
    emit();
  });
}

export function privacyModeOn(): boolean {
  return current;
}

export function setPrivacyMode(on: boolean): void {
  current = on;
  writeStored(KEY, on ? "1" : ""); // unavailable storage: the toggle still applies to this tab
  emit();
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

export function usePrivacyMode(): boolean {
  return useSyncExternalStore(subscribe, privacyModeOn, () => false);
}

// useEmailMask returns the text transform for free text that may carry an
// address (audit names/details, server error strings): masked in privacy mode,
// unchanged otherwise.
export function useEmailMask(): (text: string) => string {
  const on = usePrivacyMode();
  return on ? maskEmailsInText : (text) => text;
}
