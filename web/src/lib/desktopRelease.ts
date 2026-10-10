import type { ReleaseEntry, ReleaseFile, UpdateConfig, UpdatePolicy } from "./api";
import { updateCandidateForArchive } from "../views/updateConfigFields.ts";

// The operator CLI (`bnbloc updates publish`) applies these same rules in Go:
// cmd/bnbloc/desktop_release.go. Change both together (their tests mirror
// each other).
export interface UploadedDesktopArchive {
  readonly name: string;
  readonly url: string;
  readonly sha256: string;
  readonly size: number;
  readonly signature: string;
  readonly version: string;
  readonly notes: string;
  readonly pubDate: string;
}

export interface DesktopReleaseSelection {
  readonly file: File;
  readonly signature: string;
  readonly version: string;
  readonly notes: string;
}

export function validateDesktopReleaseSelection(selection: DesktopReleaseSelection): string {
  if (!/^bnbloc\.app\.tar\.gz$/i.test(selection.file.name)) {
    throw new Error("Select the signed bnbloc.app.tar.gz updater archive, not a DMG installer");
  }
  const encoded = selection.signature.trim();
  if (!encoded) throw new Error("The paired Tauri signature file is required");
  
  if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(encoded)) {
    throw new Error("The selected .sig file is not valid base64 Tauri signature data");
  }
  const decoded = atob(encoded);
  if (!decoded.startsWith("untrusted comment: signature from tauri secret key\n")) {
    throw new Error("The selected .sig file is not a Tauri Minisign updater signature");
  }
  const lines = decoded.trimEnd().split("\n");
  if (lines.length < 4 || !lines[2].startsWith("trusted comment: ") || !lines[3]) {
    throw new Error("The selected .sig file is incomplete");
  }
  if (!lines[2].includes(`file:${selection.file.name}`)) throw new Error("The updater signature was generated for a different archive file");
  return encoded;
}

export function publishBlockReason(input: {
  readonly archive: File | null;
  readonly signature: string | null;
  readonly version: string;
}): string | null {
  if (!input.archive && !input.signature) return "Select the signed archive and its matching .sig file.";
  if (!input.archive) return "Select the signed .app.tar.gz archive.";
  if (!input.signature) return "Select the .sig generated for this exact archive.";
  if (!isDesktopReleaseVersion(input.version)) return "Enter a valid release version before publishing.";
  return null;
}

export type DesktopPublishStep = 0 | 1 | 2 | 3;

export function nextDesktopPublishStep(input: {
  readonly step: DesktopPublishStep;
  readonly archive: File | null;
  readonly signature: string | null;
  readonly version: string;
  readonly gatewayReady?: boolean;
}): DesktopPublishStep {
  if (input.step === 0) {
    if (!input.archive || !input.signature) return 0;
    validateDesktopReleaseSelection({ file: input.archive, signature: input.signature, version: input.version, notes: "" });
    return 1;
  }
  if (input.step === 1) {
    if (!isDesktopReleaseVersion(input.version)) throw new Error("Enter a valid release version (for example, 1.2.3) before continuing.");
    return 2;
  }
  if (input.step === 2) {
    if (input.gatewayReady === false) return 2;
    return 3;
  }
  return 3;
}

export function isDesktopReleaseVersion(version: string): boolean {
  return /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version.trim());
}

export function prepareDesktopRelease(input: {
  readonly config: UpdateConfig;
  readonly hubReleases: readonly ReleaseEntry[];
  readonly archive: UploadedDesktopArchive & { readonly channel: UpdatePolicy["channel"] };
}): {
  readonly updateConfig: UpdateConfig;
  readonly hubRelease: ReleaseEntry;
  readonly otherHubReleases: readonly ReleaseEntry[];
} {
  const archive = input.archive;
  if (!/^bnbloc\.app\.tar\.gz$/i.test(archive.name.trim())) {
    throw new Error("Select the signed bnbloc.app.tar.gz updater archive, not a DMG or application bundle");
  }
  if (!archive.signature.trim()) throw new Error("The paired updater signature is required");
  const artifactUrl = new URL(archive.url);
  if (artifactUrl.protocol !== "http:" && artifactUrl.protocol !== "https:") throw new Error("The artifact URL must be an absolute HTTP(S) gateway URL");
  if (!artifactUrl.pathname.includes("/v1/artifacts/") || artifactUrl.pathname.split("/").at(-1)?.toLowerCase() !== archive.sha256.toLowerCase()) throw new Error("The artifact URL must be the content-addressed URL returned by this gateway");
  if (!Number.isSafeInteger(archive.size) || archive.size <= 0) throw new Error("Uploaded artifact size is invalid");
  if (!isDesktopReleaseVersion(archive.version)) throw new Error("Release version must be valid semver");
  if (!Number.isFinite(new Date(archive.pubDate).getTime())) throw new Error("Publish date must be a valid RFC3339 date");
  if (input.config.policy.channel !== archive.channel) {
    throw new Error(`The update policy now uses the ${input.config.policy.channel} channel, not ${archive.channel}; review the release again before publishing`);
  }
  const target = "darwin-aarch64" as const;
  const updateConfig = updateCandidateForArchive(input.config, { version: archive.version.trim(), notes: archive.notes, pubDate: archive.pubDate, target, url: archive.url, signature: archive.signature.trim() });
  const previous = input.hubReleases.find((release) => release.version === archive.version.trim() && release.channel === archive.channel);
  if (input.hubReleases.some((release) => release.version === archive.version.trim() && release.channel !== archive.channel)) {
    throw new Error(`That version already exists on another channel; refusing to replace it as ${archive.channel}`);
  }
  const files = (previous?.files ?? []).filter((file) => file.target !== target && !(file.platform.toLowerCase() === "macos" && file.arch.toLowerCase().includes("silicon")));
  const hubFile: ReleaseFile = { platform: "macOS", arch: "Apple Silicon", target, name: archive.name, url: archive.url, sha256: archive.sha256, size: archive.size };
  const hubRelease: ReleaseEntry = { version: archive.version.trim(), channel: archive.channel, releaseDate: archive.pubDate, notes: archive.notes, files: [...files, hubFile], ...(previous?.createdAtMs ? { createdAtMs: previous.createdAtMs } : {}), ...(previous?.createdBy ? { createdBy: previous.createdBy } : {}) };
  return { updateConfig, hubRelease, otherHubReleases: input.hubReleases.filter((release) => release !== previous) };
}

export interface DesktopReleaseInput {
  readonly archive: {
    readonly file: File;
    readonly signature: string;
    readonly version: string;
    readonly notes: string;
    readonly pubDate: string;
    // The channel the operator reviewed: the current update policy channel.
    readonly channel: UpdatePolicy["channel"];
  };
  readonly loadReleaseState: () => Promise<{ readonly config: UpdateConfig; readonly hubReleases: readonly ReleaseEntry[] }>;
}

export async function publishDesktopRelease(input: DesktopReleaseInput & {
  readonly upload: (file: File) => Promise<UploadedDesktopArchive>;
  readonly saveHub: (release: ReleaseEntry) => Promise<void>;
  readonly saveUpdateConfig: (config: UpdateConfig) => Promise<UpdateConfig>;
  readonly onProgress?: (message: string) => void;
}): Promise<{ readonly artifact: UploadedDesktopArchive; readonly updateConfig: UpdateConfig; readonly hubRelease: ReleaseEntry }> {
  if (input.archive.file.name !== "bnbloc.app.tar.gz") {
    throw new Error("Select the signed bnbloc.app.tar.gz updater archive, not the DMG installer");
  }
  if (!input.archive.signature.trim()) {
    throw new Error("The paired updater signature is required before upload");
  }
  if (!isDesktopReleaseVersion(input.archive.version)) throw new Error("Release version must be valid semver");
  if (!Number.isFinite(new Date(input.archive.pubDate).getTime())) throw new Error("Publish date must be RFC3339");
  const artifact = await input.upload(input.archive.file);
  const current = await input.loadReleaseState();
  if (current.hubReleases.some((release) => release.version === input.archive.version.trim() && release.channel !== input.archive.channel)) {
    throw new Error("That version now exists on another channel; refusing to publish over the latest state");
  }
  const prepared = prepareDesktopRelease({
    config: current.config,
    hubReleases: current.hubReleases,
    archive: { ...artifact, name: input.archive.file.name, signature: input.archive.signature, version: input.archive.version, notes: input.archive.notes, pubDate: input.archive.pubDate, channel: input.archive.channel },
  });

  input.onProgress?.("Updating the Release Hub…");
  try {
    await input.saveHub(prepared.hubRelease);
  } catch (error) {
    const detail = error instanceof Error ? error.message : "unknown error";
    throw new Error(`Artifact uploaded, but Release Hub save failed. Auto-update is not activated. ${detail}`);
  }

  input.onProgress?.(`Activating the ${input.archive.channel} updater…`);
  try {
    const updateConfig = await input.saveUpdateConfig(prepared.updateConfig);
    input.onProgress?.("Published. Existing clients will discover the update on their next check.");
    return { artifact, updateConfig, hubRelease: prepared.hubRelease };
  } catch (error) {
    const detail = error instanceof Error ? error.message : "unknown error";
    const partial = new Error(`Release Hub saved, but updater configuration failed. Update rollout is not activated. ${detail}`);
    Object.assign(partial, { hubSaved: true });
    throw partial;
  }
}
