import type { ReleaseEntry, ReleaseFile } from "./apiModels.ts";
import { isInstallerFile } from "./desktopDownload.ts";

// The macOS install guide for the not-yet-notarized desktop app: Gatekeeper
// blocks its first launch, and the way through (Privacy & Security → Open
// Anyway) is not obvious. Every macOS installer download opens the guide, and
// the Release Hub can reopen it. Remove this (and the DMG background's steps)
// once the app is signed and notarized.

// The app bundle path a drag-install produces. `bnbloc` is the desktop
// bundle's productName (apps/desktop/src-tauri/tauri.conf.json), a property
// of our own build rather than of any deployment.
export const MAC_APP_PATH = "/Applications/bnbloc.app";

// Removes the quarantine flag the browser put on the download, so macOS opens
// the app without the Gatekeeper prompt.
export const QUARANTINE_COMMAND = `xattr -dr com.apple.quarantine ${MAC_APP_PATH}`;

const MAC_PLATFORMS = new Set(["macos", "mac", "darwin", "osx"]);

// isMacInstaller reports whether a release-hub file is a macOS installer a
// person downloads (a .dmg, or a file the hub lists for macOS), never an
// updater-only archive.
export function isMacInstaller(file: ReleaseFile): boolean {
  if (!isInstallerFile(file)) return false;
  const platform = file.platform.trim().toLowerCase();
  const target = (file.target ?? "").trim().toLowerCase();
  const name = (file.name || file.url.split(/[?#]/)[0] || "").toLowerCase();
  return MAC_PLATFORMS.has(platform) || target.startsWith("darwin") || name.endsWith(".dmg");
}

export function hasMacInstaller(releases: readonly ReleaseEntry[]): boolean {
  return releases.some((release) => release.files.some(isMacInstaller));
}

// One guide for the whole app: any download path opens it, one host renders
// it (MacInstallGuideHost).
let open = false;
const listeners = new Set<() => void>();

function set(next: boolean): void {
  if (open === next) return;
  open = next;
  listeners.forEach((fn) => fn());
}

export function openMacInstallGuide(): void {
  set(true);
}

export function closeMacInstallGuide(): void {
  set(false);
}

export function macInstallGuideOpen(): boolean {
  return open;
}

export function subscribeMacInstallGuide(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

// afterReleaseDownload is called once a download has started: a macOS
// installer opens the guide.
export function afterReleaseDownload(file: ReleaseFile): void {
  if (isMacInstaller(file)) openMacInstallGuide();
}
