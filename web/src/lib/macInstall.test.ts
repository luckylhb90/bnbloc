import type { ReleaseEntry, ReleaseFile } from "./apiModels.ts";
import {
  afterReleaseDownload,
  closeMacInstallGuide,
  hasMacInstaller,
  isMacInstaller,
  macInstallGuideOpen,
  QUARANTINE_COMMAND,
  subscribeMacInstallGuide,
} from "./macInstall.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function file(platform: string, name: string, target?: string): ReleaseFile {
  return { platform, arch: "universal", name, url: `https://gw.example/v1/artifacts/${name}`, ...(target ? { target } : {}) };
}

function testMacInstallerDetection(): void {
  assert(isMacInstaller(file("macOS", "bnbloc_0.1.21_universal.dmg")), "a macOS .dmg is a mac installer");
  assert(isMacInstaller(file("darwin", "bnbloc.pkg")), "darwin platform spelling counts");
  assert(isMacInstaller(file("any", "bnbloc.zip", "darwin-aarch64")), "a darwin target counts");
  assert(isMacInstaller(file("", "bnbloc.dmg")), "a .dmg with no platform label counts");
  assert(!isMacInstaller(file("macOS", "bnbloc.app.tar.gz")), "the updater archive is not an installer");
  assert(!isMacInstaller(file("Windows", "bnbloc_x64-setup.exe")), "a Windows installer is not a mac installer");
  const releases: ReleaseEntry[] = [
    { version: "1", channel: "stable", releaseDate: "", notes: "", files: [file("Windows", "a.exe")] },
  ];
  assert(!hasMacInstaller(releases), "no mac installer in a Windows-only hub");
  assert(hasMacInstaller([...releases, { ...releases[0], version: "2", files: [file("macOS", "b.dmg")] }]), "found across releases");
}

function testGuideOpensOnlyForMacDownloads(): void {
  let notified = 0;
  const off = subscribeMacInstallGuide(() => notified++);
  closeMacInstallGuide();
  afterReleaseDownload(file("Windows", "bnbloc_x64-setup.exe"));
  assert(!macInstallGuideOpen() && notified === 0, "a Windows download does not open the guide");
  afterReleaseDownload(file("macOS", "bnbloc.dmg"));
  assert(macInstallGuideOpen() && notified === 1, "a macOS download opens the guide once");
  afterReleaseDownload(file("macOS", "bnbloc.dmg"));
  assert(notified === 1, "opening an open guide notifies nobody");
  closeMacInstallGuide();
  assert(!macInstallGuideOpen() && notified === 2, "closing notifies");
  off();
}

function testQuarantineCommand(): void {
  assert(QUARANTINE_COMMAND === "xattr -dr com.apple.quarantine /Applications/bnbloc.app", "the documented command");
}

testMacInstallerDetection();
testGuideOpensOnlyForMacDownloads();
testQuarantineCommand();
