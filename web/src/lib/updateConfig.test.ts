import type { UpdateConfig } from "./api.ts";
import {
  MAX_UPDATE_CHECK_INTERVAL_HOURS,
  describeUpdateDelta,
  updateCandidateForArchive,
  updateCheckIntervalMs,
  updateConfigDirty,
} from "../views/updateConfigFields.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const HOUR = 60 * 60 * 1000;

function config(): UpdateConfig {
  return {
    policy: { enabled: false, channel: "internal", autoCheck: false, checkIntervalHours: 12, autoInstall: false, minSupportedVersion: "" },
    release: {
      version: "0.1.2",
      notes: "",
      pubDate: "",
      platforms: {
        "darwin-aarch64": { url: "https://gw/v1/artifacts/sha256/old", signature: "old" },
        "darwin-x86_64": { url: "", signature: "" },
        "windows-x86_64": { url: "https://win/app.zip", signature: "win" },
      },
    },
  };
}

function testIntervalClampNeverOverflowsOrSpins(): void {
  assert(updateCheckIntervalMs(24) === 24 * HOUR, "a normal interval passes through");
  assert(updateCheckIntervalMs(0) === HOUR, "zero clamps to one hour");
  assert(updateCheckIntervalMs(-5) === HOUR, "negative clamps to one hour");
  assert(updateCheckIntervalMs(Number.NaN) === HOUR, "NaN clamps to one hour");
  assert(updateCheckIntervalMs(10_000) <= 2 ** 31 - 1, "a huge interval never exceeds the 32-bit timer limit");
  assert(updateCheckIntervalMs(597) <= 2 ** 31 - 1, "597h (the reported overflow) is clamped");
  assert(MAX_UPDATE_CHECK_INTERVAL_HOURS * HOUR <= 2 ** 31 - 1, "the published max fits a timer");
  assert((MAX_UPDATE_CHECK_INTERVAL_HOURS + 1) * HOUR > 2 ** 31 - 1, "the published max is the largest safe whole hour");
}

function testDeltaShowsPolicyAndEveryPlatformEffect(): void {
  const current = config();
  const candidate = updateCandidateForArchive(current, { version: "0.1.3", notes: "n", pubDate: "2026-10-07T00:00:00Z", target: "darwin-aarch64", url: "https://gw/v1/artifacts/sha256/new", signature: "new" });
  const delta = describeUpdateDelta(current, candidate);
  assert(delta.version.from === "0.1.2" && delta.version.to === "0.1.3", "version change is reported");
  assert(delta.policy.length === 1 && delta.policy[0]!.field === "enabled" && delta.policy[0]!.to === "Enabled", "only enabled flips; channel stays internal");
  const byTarget = Object.fromEntries(delta.platforms.map((p) => [p.target, p.change]));
  assert(byTarget["darwin-aarch64"] === "replaced", "the published target is replaced");
  assert(byTarget["windows-x86_64"] === "removed", "an artifact built for the old version is removed");
  assert(byTarget["darwin-x86_64"] === "none", "an empty target stays empty");
}

function testSameVersionKeepsOtherArtifacts(): void {
  const current = config();
  const candidate = updateCandidateForArchive(current, { version: "0.1.2", notes: "", pubDate: "", target: "darwin-aarch64", url: "https://gw/v1/artifacts/sha256/new", signature: "new" });
  const byTarget = Object.fromEntries(describeUpdateDelta(current, candidate).platforms.map((p) => [p.target, p.change]));
  assert(byTarget["windows-x86_64"] === "kept", "same-version re-publish keeps the Windows artifact");
}

function testDirtyTracksPolicyAndManualTargetsOnly(): void {
  const saved = config();
  assert(!updateConfigDirty(saved, config()), "identical configs are clean");
  assert(updateConfigDirty(saved, { ...saved, policy: { ...saved.policy, autoInstall: true } }), "a policy edit is dirty");
  const manual = { ...saved, release: { ...saved.release, platforms: { ...saved.release.platforms, "windows-x86_64": { url: "https://win/new.zip", signature: "win" } } } };
  assert(updateConfigDirty(saved, manual), "a manual platform edit is dirty");
  assert(!updateConfigDirty(saved, { ...saved, release: { ...saved.release, notes: "draft" } }), "release text is not a policy edit");
}

testIntervalClampNeverOverflowsOrSpins();
testDeltaShowsPolicyAndEveryPlatformEffect();
testSameVersionKeepsOtherArtifacts();
testDirtyTracksPolicyAndManualTargetsOnly();
console.log("updateConfig tests passed");
