import type { ComponentArtifact, ComponentPackageEnvelope } from "./apiTypes";

export type ArtifactRequirement = {
  readonly platform: string;
};

export type ArtifactUploadSlot = {
  readonly uploaded?: ComponentArtifact;
};

type UploadedArtifactInput = ComponentArtifact | ArtifactUploadSlot;

export class MissingArtifactUploadError extends Error {
  readonly name = "MissingArtifactUploadError";
  readonly platform: string;

  constructor(platform: string) {
    super(`Upload a binary for ${platform} before publishing.`);
    this.platform = platform;
  }
}

export function artifactRequirementsForPackage(envelope: ComponentPackageEnvelope): readonly ArtifactRequirement[] {
  // Sidecar or stdio MCP binaries per platform, or a previewer's one bundle
  // archive.
  if (!runtimeNeedsArtifacts(envelope.runtime)) return [];
  return Object.keys(envelope.artifacts ?? {})
    .sort((left, right) => left.localeCompare(right))
    .map((platform) => ({ platform }));
}

// runtimeNeedsArtifacts: the runtimes whose package pins uploaded files.
export function runtimeNeedsArtifacts(runtime: ComponentPackageEnvelope["runtime"] | undefined): boolean {
  return runtime === "sidecar" || runtime === "bundle" || runtime === "stdio";
}

export function uploadedArtifactsComplete(
  requirements: readonly ArtifactRequirement[],
  uploaded: Readonly<Record<string, UploadedArtifactInput | undefined>>,
): boolean {
  return requirements.every((requirement) => artifactFromSlot(uploaded[requirement.platform]) !== undefined);
}

export function buildPackageWithUploadedArtifacts(
  envelope: ComponentPackageEnvelope,
  uploaded: Readonly<Record<string, UploadedArtifactInput | undefined>>,
): ComponentPackageEnvelope {
  const requirements = artifactRequirementsForPackage(envelope);
  if (requirements.length === 0) return envelope;
  const artifacts: Record<string, ComponentArtifact> = {};
  for (const requirement of requirements) {
    const artifact = artifactFromSlot(uploaded[requirement.platform]);
    if (!artifact) throw new MissingArtifactUploadError(requirement.platform);
    artifacts[requirement.platform] = { url: artifact.url, sha256: artifact.sha256 };
  }
  return { ...envelope, artifacts };
}

export function buildFinalComponentPackageJSON(
  envelope: ComponentPackageEnvelope,
  uploaded: Readonly<Record<string, UploadedArtifactInput | undefined>>,
): string {
  return JSON.stringify(buildPackageWithUploadedArtifacts(envelope, uploaded));
}

function artifactFromSlot(slot: UploadedArtifactInput | undefined): ComponentArtifact | undefined {
  if (!slot) return undefined;
  if ("url" in slot && "sha256" in slot) {
    if (slot.url.trim() === "" || slot.sha256.trim() === "") return undefined;
    return slot;
  }
  return slot.uploaded;
}
