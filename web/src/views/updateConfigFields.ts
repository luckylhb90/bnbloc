import type { UpdateConfig, UpdatePlatformArtifact, UpdatePolicy } from "@/lib/api";

export const UPDATE_TARGETS = ["darwin-aarch64", "darwin-x86_64", "windows-x86_64"] as const;
export const MANUAL_UPDATE_TARGETS = ["darwin-x86_64", "windows-x86_64"] as const;

export type UpdateTarget = (typeof UPDATE_TARGETS)[number];
export type UpdateFieldErrors = Readonly<Record<string, string>>;

const EMPTY_ARTIFACT: UpdatePlatformArtifact = { url: "", signature: "" };

// The browser's timer APIs store the delay as a signed 32-bit millisecond
// count: anything above 2^31-1 ms (~24.8 days) overflows and fires
// immediately, turning a long check interval into a tight check loop. This is
// a constant of the JS platform, not of a deployment, so it may live in code.
const MAX_TIMER_DELAY_MS = 2 ** 31 - 1;
const HOUR_MS = 60 * 60 * 1000;
export const MIN_UPDATE_CHECK_INTERVAL_HOURS = 1;
export const MAX_UPDATE_CHECK_INTERVAL_HOURS = Math.floor(MAX_TIMER_DELAY_MS / HOUR_MS);

// updateCheckIntervalMs turns the policy's check interval into a timer delay
// that can never overflow (too large) or spin (zero, negative, NaN).
export function updateCheckIntervalMs(hours: number): number {
  const finite = Number.isFinite(hours) ? hours : MIN_UPDATE_CHECK_INTERVAL_HOURS;
  const clamped = Math.min(MAX_UPDATE_CHECK_INTERVAL_HOURS, Math.max(MIN_UPDATE_CHECK_INTERVAL_HOURS, finite));
  return Math.round(clamped * HOUR_MS);
}

export function completeUpdateConfig(config: UpdateConfig): UpdateConfig {
  const platforms = UPDATE_TARGETS.reduce<Record<UpdateTarget, UpdatePlatformArtifact>>(
    (acc, target) => {
      acc[target] = config.release.platforms[target] ?? EMPTY_ARTIFACT;
      return acc;
    },
    {
      "darwin-aarch64": EMPTY_ARTIFACT,
      "darwin-x86_64": EMPTY_ARTIFACT,
      "windows-x86_64": EMPTY_ARTIFACT,
    },
  );
  return { ...config, release: { ...config.release, platforms } };
}

// updateCandidateForArchive is the updater config a local publish activates.
// The operator's policy is preserved (channel, auto-check, interval,
// auto-install, minimum version); only `enabled` turns on, because publishing
// is the act of rolling the release out. Every target shares the release's
// single version, so when the version changes the other targets' artifacts —
// built for the previous version — are cleared instead of being re-labelled
// as the new one (clients would install the old build and loop). Re-publishing
// the same version keeps them.
// Go mirror (must match): updateCandidateForArchive in
// cmd/bnbloc/desktop_release.go.
export function updateCandidateForArchive(config: UpdateConfig, release: {
  readonly version: string;
  readonly notes: string;
  readonly pubDate: string;
  readonly target: UpdateTarget;
  readonly url: string;
  readonly signature: string;
}): UpdateConfig {
  const sameVersion = config.release.version.trim() === release.version.trim();
  const platforms: Record<string, UpdatePlatformArtifact> = {};
  for (const target of UPDATE_TARGETS) {
    platforms[target] = sameVersion ? config.release.platforms[target] ?? EMPTY_ARTIFACT : EMPTY_ARTIFACT;
  }
  platforms[release.target] = { url: release.url, signature: release.signature };
  return {
    policy: { ...config.policy, enabled: true },
    release: {
      ...config.release,
      version: release.version,
      notes: release.notes,
      pubDate: release.pubDate,
      platforms,
    },
  };
}

export interface PolicyChange {
  readonly field: keyof UpdatePolicy;
  readonly label: string;
  readonly from: string;
  readonly to: string;
}

export type PlatformChangeKind = "added" | "replaced" | "removed" | "kept" | "none";

export interface PlatformChange {
  readonly target: UpdateTarget;
  readonly change: PlatformChangeKind;
}

export interface UpdateDelta {
  readonly version: { readonly from: string; readonly to: string };
  readonly policy: readonly PolicyChange[];
  readonly platforms: readonly PlatformChange[];
}

const POLICY_LABELS: Record<keyof UpdatePolicy, string> = {
  enabled: "Updates",
  channel: "Channel",
  autoCheck: "Automatic checks",
  checkIntervalHours: "Check interval hours",
  autoInstall: "Auto-install",
  minSupportedVersion: "Minimum supported version",
};

function policyValue(field: keyof UpdatePolicy, value: UpdatePolicy[keyof UpdatePolicy]): string {
  if (field === "enabled") return value ? "Enabled" : "Disabled";
  if (typeof value === "boolean") return value ? "On" : "Off";
  if (field === "channel") return value === "internal" ? "Internal" : "Stable";
  return String(value) || "(none)";
}

function hasArtifact(artifact: UpdatePlatformArtifact | undefined): boolean {
  return !!artifact && artifact.url.trim() !== "";
}

// describeUpdateDelta lists everything a publish changes in the live updater
// config, so the review step shows the full effect before anything is written.
export function describeUpdateDelta(current: UpdateConfig, candidate: UpdateConfig): UpdateDelta {
  const policy: PolicyChange[] = [];
  for (const field of Object.keys(POLICY_LABELS) as (keyof UpdatePolicy)[]) {
    const from = current.policy[field];
    const to = candidate.policy[field];
    if (from !== to) policy.push({ field, label: POLICY_LABELS[field], from: policyValue(field, from), to: policyValue(field, to) });
  }
  const platforms = UPDATE_TARGETS.map((target): PlatformChange => {
    const before = current.release.platforms[target];
    const after = candidate.release.platforms[target];
    const had = hasArtifact(before);
    const has = hasArtifact(after);
    if (!had && !has) return { target, change: "none" };
    if (!had) return { target, change: "added" };
    if (!has) return { target, change: "removed" };
    const same = before!.url === after!.url && before!.signature === after!.signature;
    return { target, change: same ? "kept" : "replaced" };
  });
  return { version: { from: current.release.version, to: candidate.release.version }, policy, platforms };
}

export function fieldErrorsFromUpdateMessage(message: string): UpdateFieldErrors {
  const fields: Record<string, string> = {};
  const apply = (field: string) => {
    fields[field] = message;
  };

  if (message.includes("updates.channel")) apply("policy.channel");
  if (message.includes("updates.check_interval_hours")) apply("policy.checkIntervalHours");
  if (message.includes("updates.min_supported_version")) apply("policy.minSupportedVersion");
  if (message.includes("release.version")) apply("release.version");
  if (message.includes("release.pub_date")) apply("release.pubDate");

  for (const target of UPDATE_TARGETS) {
    if (message.includes(`release.${target}.url`)) apply(`release.platforms.${target}.url`);
    if (message.includes(`release.${target}.signature`)) apply(`release.platforms.${target}.signature`);
  }

  return fields;
}

// updateConfigDirty reports whether the editable policy or the manually
// configured platform artifacts differ from the last loaded/saved config.
export function updateConfigDirty(saved: UpdateConfig, edited: UpdateConfig): boolean {
  for (const field of Object.keys(POLICY_LABELS) as (keyof UpdatePolicy)[]) {
    if (saved.policy[field] !== edited.policy[field]) return true;
  }
  return MANUAL_UPDATE_TARGETS.some((target) => {
    const a = saved.release.platforms[target] ?? EMPTY_ARTIFACT;
    const b = edited.release.platforms[target] ?? EMPTY_ARTIFACT;
    return a.url !== b.url || a.signature !== b.signature;
  });
}
