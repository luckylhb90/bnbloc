import { artifactExtension, artifactFileName, inferMime, type ArtifactFileFacts } from "@/lib/artifactFiles";
import type { AttachmentTabFile } from "@/lib/workbenchTabs";
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

/** The facts of a chat attachment: a plain file named by its upload. */
export function attachmentFacts(a: AttachmentTabFile): ArtifactFileFacts {
  return { kind: "file", title: a.filename, fileName: a.filename, mimeType: a.contentType };
}

/** What a viewer is told about a chat attachment (one immutable version). */
export function attachmentFileInfo(a: AttachmentTabFile): ArtifactFileInfo {
  return fileInfoFromFacts(attachmentFacts(a), 1, a.size);
}
