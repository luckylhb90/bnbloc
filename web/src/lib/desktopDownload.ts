import type { ReleaseEntry, ReleaseFile } from "./api.ts";

// Pure desktop-installer selection for the "Download desktop app" affordances.
// Every input comes from the gateway's Release Hub (no baked-in hosts or
// versions); this only decides which of the published files fits this browser.

export type DesktopPlatform = "macos" | "windows";
export type DesktopArch = "arm" | "x86";

export function detectDesktopPlatform(userAgent: string): DesktopPlatform | null {
  if (/Win/i.test(userAgent)) return "windows";
  if (/Mac/i.test(userAgent)) return "macos";
  return null;
}

// Updater-only payloads (Tauri's .app.tar.gz / .nsis.zip / .msi.zip bundles
// and their detached signatures) are what the in-app updater consumes; they
// are not installers a person can run, so a download link must never offer
// them.
const UPDATER_ONLY = [/\.tar\.gz$/i, /\.sig$/i, /\.minisig$/i, /\.nsis\.zip$/i, /\.msi\.zip$/i];

export function isInstallerFile(file: ReleaseFile): boolean {
  const names = [file.name, file.url.split(/[?#]/)[0] ?? ""];
  return !names.some((name) => UPDATER_ONLY.some((pattern) => pattern.test(name)));
}

// Architecture labels are free-form in the hub ("Apple Silicon", "x64", …);
// these are the spellings each detected CPU family may appear under.
const ARCH_ALIASES: Record<DesktopArch, readonly string[]> = {
  arm: ["apple silicon", "arm64", "aarch64", "arm"],
  x86: ["intel", "x64", "x86_64", "amd64", "x86"],
};

function archRank(fileArch: string, detected: DesktopArch | null): number {
  const arch = fileArch.trim().toLowerCase();
  if (detected && ARCH_ALIASES[detected].includes(arch)) return 0;
  if (arch === "universal") return 1;
  if (arch === "any") return 2;
  return 3;
}

function latest(releases: readonly ReleaseEntry[]): ReleaseEntry | undefined {
  const [first] = releases;
  if (!first) return undefined;
  return releases.reduce((best, release) => {
    const time = release.createdAtMs ?? Number.NEGATIVE_INFINITY;
    const bestTime = best.createdAtMs ?? Number.NEGATIVE_INFINITY;
    return time > bestTime ? release : best;
  }, first);
}

// pickDesktopDownload returns the installer for this machine from the newest
// release on the instance's update channel. `channel` null means the channel
// is unknown (policy unreadable): no channel filter applies. A release with no
// channel (e.g. the gateway's single env-configured download) matches any.
export function pickDesktopDownload(
  releases: readonly ReleaseEntry[],
  input: { readonly platform: DesktopPlatform | null; readonly channel: string | null; readonly arch: DesktopArch | null },
): ReleaseFile | null {
  if (!input.platform) return null;
  const onChannel = releases.filter((release) => !input.channel || !release.channel || release.channel === input.channel);
  const withInstaller = onChannel.filter((release) => release.files.some((file) => fitsPlatform(file, input.platform!)));
  const release = latest(withInstaller);
  if (!release) return null;
  const candidates = release.files.filter((file) => fitsPlatform(file, input.platform!));
  const ranked = [...candidates].sort((a, b) => archRank(a.arch, input.arch) - archRank(b.arch, input.arch));
  return ranked[0] ?? null;
}

function fitsPlatform(file: ReleaseFile, platform: DesktopPlatform): boolean {
  const filePlatform = file.platform.trim().toLowerCase();
  return (filePlatform === platform || filePlatform === "any") && isInstallerFile(file);
}

// detectDesktopArch asks the browser for its CPU family where the
// User-Agent Client Hints API exists (Chromium); elsewhere it is unknown and
// the picker falls back to a universal build.
export async function detectDesktopArch(): Promise<DesktopArch | null> {
  const data = (globalThis.navigator as Navigator & {
    userAgentData?: { getHighEntropyValues?: (hints: string[]) => Promise<{ architecture?: string }> };
  } | undefined)?.userAgentData;
  if (!data?.getHighEntropyValues) return null;
  try {
    const { architecture } = await data.getHighEntropyValues(["architecture"]);
    if (architecture === "arm") return "arm";
    if (architecture === "x86") return "x86";
    return null;
  } catch {
    return null;
  }
}
