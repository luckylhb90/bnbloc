import type { ReleaseEntry, ReleaseFile } from "./api.ts";
import { detectDesktopPlatform, isInstallerFile, pickDesktopDownload } from "./desktopDownload.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function file(platform: string, arch: string, name: string): ReleaseFile {
  return { platform, arch, name, url: `https://gw.example/v1/artifacts/sha256/${name}` };
}

const stableOld: ReleaseEntry = { version: "0.1.2", channel: "stable", releaseDate: "", notes: "", createdAtMs: 1, files: [file("macOS", "universal", "bnbloc_0.1.2.dmg")] };
const stableNew: ReleaseEntry = {
  version: "0.1.3",
  channel: "stable",
  releaseDate: "",
  notes: "",
  createdAtMs: 2,
  files: [
    file("macOS", "Apple Silicon", "bnbloc.app.tar.gz"),
    file("macOS", "Apple Silicon", "bnbloc_0.1.3_aarch64.dmg"),
    file("macOS", "Intel", "bnbloc_0.1.3_x64.dmg"),
    file("Windows", "x64", "bnbloc_0.1.3_x64-setup.exe"),
    file("Windows", "x64", "bnbloc_0.1.3_x64-setup.nsis.zip"),
  ],
};
const internalNewest: ReleaseEntry = { version: "0.2.0-internal", channel: "internal", releaseDate: "", notes: "", createdAtMs: 3, files: [file("macOS", "universal", "bnbloc_internal.dmg")] };
const updaterOnly: ReleaseEntry = { version: "0.1.4", channel: "stable", releaseDate: "", notes: "", createdAtMs: 4, files: [file("macOS", "Apple Silicon", "bnbloc.app.tar.gz")] };
const all = [stableOld, stableNew, internalNewest, updaterOnly];

function testUpdaterArchivesAreNeverOffered(): void {
  assert(!isInstallerFile(file("macOS", "Apple Silicon", "bnbloc.app.tar.gz")), "a Tauri updater archive is not an installer");
  assert(!isInstallerFile(file("Windows", "x64", "app.nsis.zip")), "a Windows updater zip is not an installer");
  assert(!isInstallerFile(file("macOS", "any", "bnbloc.app.tar.gz.sig")), "a signature is not an installer");
  assert(isInstallerFile(file("macOS", "universal", "bnbloc.dmg")), "a DMG is an installer");
}

function testChannelFilterAndLatestInstaller(): void {
  const mac = pickDesktopDownload(all, { platform: "macos", channel: "stable", arch: "arm" });
  assert(mac?.name === "bnbloc_0.1.3_aarch64.dmg", `stable mac arm gets the newest stable installer, got ${mac?.name}`);
  const internal = pickDesktopDownload(all, { platform: "macos", channel: "internal", arch: null });
  assert(internal?.name === "bnbloc_internal.dmg", "the internal channel gets its own release");
  const unknownChannel = pickDesktopDownload(all, { platform: "macos", channel: null, arch: null });
  assert(unknownChannel?.name === "bnbloc_internal.dmg", "an unknown channel does not filter");
}

function testArchPreference(): void {
  const intel = pickDesktopDownload(all, { platform: "macos", channel: "stable", arch: "x86" });
  assert(intel?.name === "bnbloc_0.1.3_x64.dmg", "an Intel Mac gets the Intel build");
  const win = pickDesktopDownload(all, { platform: "windows", channel: "stable", arch: null });
  assert(win?.name === "bnbloc_0.1.3_x64-setup.exe", "Windows gets the setup exe, never the updater zip");
  const universalFirst = pickDesktopDownload([{ ...stableNew, files: [file("macOS", "Intel", "a.dmg"), file("macOS", "universal", "u.dmg")] }], { platform: "macos", channel: "stable", arch: null });
  assert(universalFirst?.name === "u.dmg", "unknown arch prefers a universal build");
}

function testAnyPlatformAndChannelless(): void {
  const synthetic: ReleaseEntry = { version: "latest", channel: "", releaseDate: "", notes: "", files: [{ platform: "any", arch: "any", name: "bnbloc-desktop", url: "https://files.example/bnbloc.dmg" }] };
  const picked = pickDesktopDownload([synthetic], { platform: "windows", channel: "stable", arch: null });
  assert(picked?.name === "bnbloc-desktop", "a channel-less any-platform download matches every browser");
  assert(pickDesktopDownload(all, { platform: null, channel: "stable", arch: null }) === null, "an unknown OS gets no download");
}

function testPlatformDetection(): void {
  assert(detectDesktopPlatform("Mozilla/5.0 (Windows NT 10.0; Win64; x64)") === "windows", "windows UA");
  assert(detectDesktopPlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)") === "macos", "mac UA");
  assert(detectDesktopPlatform("Mozilla/5.0 (X11; Linux x86_64)") === null, "linux UA");
}

testUpdaterArchivesAreNeverOffered();
testChannelFilterAndLatestInstaller();
testArchPreference();
testAnyPlatformAndChannelless();
testPlatformDetection();
console.log("desktopDownload tests passed");
