// The member's remembered "Open with…" choice per file type: picking a
// viewer for a file makes it the default for files of the same MIME type
// (when known) and extension, in this browser/desktop profile only. The
// registry still decides whether the remembered viewer claims a given file
// (selectViewer ignores a preference that is no longer a candidate, e.g. an
// uninstalled previewer), so a stale entry is harmless.

import { StorageKey, browserStorage } from "./storageKeys.ts";

export const VIEWER_PREFERENCE_KEY = StorageKey.viewerChoice;
const MAX_ENTRIES = 200;

export interface PreferenceStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

type FileType = { readonly mimeType: string; readonly extension: string };

function keysFor(file: FileType): string[] {
  const keys: string[] = [];
  const mime = file.mimeType.trim().toLowerCase();
  const ext = file.extension.trim().toLowerCase().replace(/^\./, "");
  if (mime && mime !== "application/octet-stream") keys.push(`mime:${mime}`);
  if (ext) keys.push(`ext:${ext}`);
  return keys;
}

function read(storage: PreferenceStorage | null): Record<string, string> {
  if (!storage) return {};
  try {
    const parsed: unknown = JSON.parse(storage.getItem(VIEWER_PREFERENCE_KEY) ?? "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: Record<string, string> = {};
    for (const [k, v] of Object.entries(parsed)) if (typeof v === "string") out[k] = v;
    return out;
  } catch {
    return {};
  }
}

function write(storage: PreferenceStorage | null, prefs: Record<string, string>): void {
  if (!storage) return;
  const entries = Object.entries(prefs);
  const kept = entries.slice(Math.max(0, entries.length - MAX_ENTRIES));
  try {
    storage.setItem(VIEWER_PREFERENCE_KEY, JSON.stringify(Object.fromEntries(kept)));
  } catch {
    // Quota / private mode: the choice simply is not remembered.
  }
}

function defaultStorage(): PreferenceStorage | null {
  return browserStorage();
}

/** The remembered viewer id for a file type (MIME first, then extension). */
export function preferredViewerFor(file: FileType, storage: PreferenceStorage | null = defaultStorage()): string | undefined {
  const prefs = read(storage);
  for (const key of keysFor(file)) if (prefs[key]) return prefs[key];
  return undefined;
}

/** Remembers viewerId for the file's type; null forgets the choice (back to
 *  the registry's best match). */
export function rememberViewer(file: FileType, viewerId: string | null, storage: PreferenceStorage | null = defaultStorage()): void {
  const prefs = read(storage);
  for (const key of keysFor(file)) {
    delete prefs[key];
    if (viewerId) prefs[key] = viewerId;
  }
  write(storage, prefs);
}
