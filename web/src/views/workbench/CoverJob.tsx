import type { Artifact, ArtifactVersion } from "@/lib/api";
import { ArtifactBody } from "@/views/artifact/ArtifactBody";
import type { ArtifactViewer } from "@/views/artifact/registry";
import { fileInfoFor, useVersionBlob } from "./artifactData";

// A card's cover, made by its viewer (docs/prd/2026-10-16-file-covers.md level
// 3): the file rendered once in the sandbox the viewer always runs in, on the
// page but invisible and out of the way. The viewer stores the cover itself
// (RemotePreviewer, PdfViewer); the card, told by the refreshed version, stops
// rendering this. No toolbar, no selection, no member action: nothing is shown.
export function CoverJob({ roomId, artifact, meta, viewer }: { roomId: string; artifact: Artifact; meta: ArtifactVersion; viewer: ArtifactViewer }) {
  const { blob } = useVersionBlob(roomId, artifact, meta.version, meta);
  if (!blob) return null;
  return (
    <div aria-hidden inert className="pointer-events-none fixed left-0 top-0 -z-10 h-[600px] w-[800px] overflow-hidden opacity-0">
      <ArtifactBody
        roomId={roomId}
        artifactId={artifact.artifactId}
        file={fileInfoFor(artifact, meta.version, meta)}
        blob={blob}
        viewer={viewer}
      />
    </div>
  );
}
