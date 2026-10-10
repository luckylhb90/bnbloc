import assert from "node:assert/strict";
import test from "node:test";
import { prepareDesktopRelease, publishDesktopRelease } from "../src/lib/desktopRelease.ts";

const archive = {
  name: "bnbloc.app.tar.gz",
  url: "http://127.0.0.1:7090/v1/artifacts/sha256/abc123",
  sha256: "abc123",
  size: 16287701,
  signature: "signed archive signature",
  version: "0.1.3",
  notes: "Desktop agent connection improvements.",
  pubDate: "2026-10-05T12:00:00Z",
  channel: "stable",
};

function existingConfig() {
  return {
    policy: {
      enabled: false,
      channel: "stable",
      autoCheck: false,
      checkIntervalHours: 12,
      autoInstall: false,
      minSupportedVersion: "0.1.1",
    },
    release: {
      version: "0.1.2",
      notes: "previous release",
      pubDate: "2026-09-20T00:00:00Z",
      platforms: {
        "darwin-aarch64": { url: "https://old.example/app.tar.gz", signature: "old-signature" },
        "windows-x86_64": { url: "https://windows.example/app.zip", signature: "windows-signature" },
      },
    },
  };
}

const existingHub = [
  {
    version: "0.1.3",
    channel: "stable",
    releaseDate: "2026-09-30T00:00:00Z",
    notes: "old notes",
    files: [
      { platform: "Windows", arch: "x64", target: "windows-x86_64", name: "app.exe", url: "https://win.example/app.exe", sha256: "f00d", size: 100 },
      { platform: "macOS", arch: "Apple Silicon", target: "darwin-aarch64", name: "old.tar.gz", url: "https://old.example/old.tar.gz", sha256: "beef", size: 200 },
    ],
  },
  {
    version: "0.1.2",
    channel: "stable",
    releaseDate: "2026-09-20T00:00:00Z",
    notes: "keep history",
    files: [],
  },
];

test("publishing enables updates but preserves the operator policy, and clears targets built for the previous version", () => {
  const prepared = prepareDesktopRelease({ config: existingConfig(), hubReleases: existingHub, archive });
  assert.equal(prepared.updateConfig.policy.enabled, true);
  assert.equal(prepared.updateConfig.policy.channel, "stable");
  assert.equal(prepared.updateConfig.policy.autoCheck, false);
  assert.equal(prepared.updateConfig.policy.autoInstall, false);
  assert.equal(prepared.updateConfig.policy.checkIntervalHours, 12);
  assert.equal(prepared.updateConfig.policy.minSupportedVersion, "0.1.1");
  // 0.1.2 -> 0.1.3: the Windows artifact was built for 0.1.2 and must not be
  // relabelled as 0.1.3.
  assert.deepEqual(prepared.updateConfig.release.platforms["windows-x86_64"], { url: "", signature: "" });
  assert.equal(prepared.updateConfig.release.platforms["darwin-aarch64"].url, archive.url);
  assert.equal(prepared.updateConfig.release.platforms["darwin-aarch64"].signature, archive.signature);
});

test("re-publishing the same version keeps the other targets' artifacts", () => {
  const config = existingConfig();
  const sameVersion = { ...config, release: { ...config.release, version: archive.version } };
  const prepared = prepareDesktopRelease({ config: sameVersion, hubReleases: existingHub, archive });
  assert.deepEqual(prepared.updateConfig.release.platforms["windows-x86_64"], config.release.platforms["windows-x86_64"]);
});

test("publishing follows the internal policy channel instead of forcing stable", () => {
  const config = existingConfig();
  const internal = { ...config, policy: { ...config.policy, channel: "internal" } };
  const prepared = prepareDesktopRelease({ config: internal, hubReleases: [], archive: { ...archive, channel: "internal" } });
  assert.equal(prepared.updateConfig.policy.channel, "internal");
  assert.equal(prepared.hubRelease.channel, "internal");
});

test("publishing refuses when the policy channel changed since review", () => {
  assert.throws(() => prepareDesktopRelease({ config: existingConfig(), hubReleases: [], archive: { ...archive, channel: "internal" } }), /review the release again/i);
});

test("publishing upserts a stable hub row and replaces only the matching platform file", () => {
  const prepared = prepareDesktopRelease({ config: existingConfig(), hubReleases: existingHub, archive });
  const stable = prepared.hubRelease;
  assert.equal(stable.channel, "stable");
  assert.equal(stable.version, archive.version);
  assert.equal(stable.notes, archive.notes);
  assert.equal(stable.releaseDate, archive.pubDate);
  assert.deepEqual(stable.files, [
    existingHub[0].files[0],
    {
      platform: "macOS",
      arch: "Apple Silicon",
      target: "darwin-aarch64",
      name: archive.name,
      url: archive.url,
      sha256: archive.sha256,
      size: archive.size,
    },
  ]);
  assert.deepEqual(prepared.otherHubReleases, [existingHub[1]]);
});

test("rejecting invalid or ambiguous Tauri packages leaves publishing impossible", () => {
  const hub = [{ ...existingHub[0], channel: "internal" }];
  assert.throws(() => prepareDesktopRelease({ config: existingConfig(), hubReleases: [], archive: { ...archive, signature: " " } }), /signature/i);
  assert.throws(() => prepareDesktopRelease({ config: existingConfig(), hubReleases: [], archive: { ...archive, url: "https://other.example/app.tar.gz" } }), /artifact URL/i);
  assert.throws(() => prepareDesktopRelease({ config: existingConfig(), hubReleases: hub, archive }), /another channel/i);
  assert.throws(() => prepareDesktopRelease({ config: existingConfig(), hubReleases: [], archive: { ...archive, name: "bnbloc.dmg" } }), /\.app\.tar\.gz/i);
  assert.throws(() => prepareDesktopRelease({ config: existingConfig(), hubReleases: [], archive: { ...archive, sha256: "f".repeat(64) } }), /content-addressed|digest/i);
});

test("publish sequence uploads once, writes Hub then activates updater as final step", async () => {
  const calls = [];
  const published = await publishDesktopRelease({
    archive: { file: new File(["binary"], archive.name), signature: archive.signature, version: archive.version, notes: archive.notes, pubDate: archive.pubDate, channel: "stable" },
    loadReleaseState: async () => ({ config: existingConfig(), hubReleases: existingHub }),
    upload: async (file) => { calls.push(["upload", file.name]); return archive; },
    saveHub: async (entry) => { calls.push(["hub", entry.version]); },
    saveUpdateConfig: async (config) => { calls.push(["updater", config.release.version]); return config; },
  });
  assert.deepEqual(calls, [["upload", "bnbloc.app.tar.gz"], ["hub", "0.1.3"], ["updater", "0.1.3"]]);
  assert.equal(published.artifact.url, archive.url);
});

test("invalid signature stops before upload and any release writes", async () => {
  let calls = 0;
  await assert.rejects(publishDesktopRelease({
    archive: { file: new File(["binary"], archive.name), signature: " ", version: archive.version, notes: archive.notes, pubDate: archive.pubDate, channel: "stable" },
    loadReleaseState: async () => ({ config: existingConfig(), hubReleases: existingHub }),
    saveHub: async () => { calls++; }, saveUpdateConfig: async () => { calls++; return existingConfig(); },
  }), /signature/i);
  assert.equal(calls, 0);
});

test("invalid release versions are rejected before artifact upload", async () => {
  let uploaded = false;
  await assert.rejects(publishDesktopRelease({
    archive: { file: new File(["binary"], archive.name), signature: archive.signature, version: "", notes: archive.notes, pubDate: archive.pubDate, channel: "stable" },
    loadReleaseState: async () => ({ config: existingConfig(), hubReleases: existingHub }),
    upload: async () => { uploaded = true; return archive; },
    saveHub: async () => {},
    saveUpdateConfig: async (config) => config,
  }), /valid semver/i);
  assert.equal(uploaded, false);
});

test("updater-config failure identifies that Hub is already saved and rollout is not active", async () => {
  await assert.rejects(publishDesktopRelease({
    archive: { file: new File(["binary"], archive.name), signature: archive.signature, version: archive.version, notes: archive.notes, pubDate: archive.pubDate, channel: "stable" },
    loadReleaseState: async () => ({ config: existingConfig(), hubReleases: existingHub }),
    upload: async () => archive,
    saveHub: async () => {},
    saveUpdateConfig: async () => { throw new Error("platform settings unavailable"); },
  }), (error) => error instanceof Error && "hubSaved" in error && error.hubSaved === true && /not activated/i.test(error.message));
});
