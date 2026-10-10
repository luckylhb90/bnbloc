import { artifactExtension, artifactFileName, inferMime, type ArtifactFileFacts } from "@/lib/artifactFiles";
import type { ArtifactFileInfo } from "./registry";

/** What a viewer is told about one version, from its file facts: the one
 *  derivation the workbench (room artifacts) and the public share page
 *  (a link's metadata) both use, so a file selects the same viewer in both. */
export function fileInfoFromFacts(facts: ArtifactFileFacts, version: number, sizeBytes?: number): ArtifactFileInfo {
  return {
    name: artifactFileName(facts),
    mimeType: inferMime(facts),
    extension: artifactExtension(facts),
    kind: facts.kind ?? "",
    language: facts.language,
    sizeBytes,
    version,
  };
}
