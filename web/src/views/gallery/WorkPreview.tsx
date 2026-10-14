import { useEffect, useMemo, useState } from "react";
import { isTextual, type ArtifactFileFacts } from "@/lib/artifactFiles";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { errorText } from "@/lib/errors";
import { saveBlob } from "@/lib/saveBlob";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { useToast } from "@/ui/Toast";
import { ArtifactBody, resolveViewer } from "@/views/artifact/ArtifactBody";
import { useViewerRegistry } from "@/views/artifact/registry";
import { fileInfoFromFacts } from "@/views/artifact/fileInfo";
import type { SiteSource } from "@/views/artifact/registry";

// WorkPreview renders a work in its detail with the artifact viewers
// (docs/prd/2026-10-13-works-gallery-and-make-similar.md §2, "Detail
// view"), exactly as a deliverable of that type opens in a room: an image
// inline, video and audio with the player's own controls, a document in
// its viewer, a site or an app in the sandboxed site frame (from the URL
// its demo route mints: an app's SDK keeps its data in the page). A type
// no viewer takes is offered as a download. The viewers never fetch: the
// bytes are the work's file (GET …/versions/{v}/file).

export function WorkPreview({ work }: { readonly work: ContentComponent }) {
  useViewerRegistry();
  const toast = useToast();
  const label = contentLabel(work);
  const site = work.workType === "app" || work.workType === "site";
  const facts: ArtifactFileFacts = site ? { kind: "site", title: label } : { kind: "file", title: work.workFile, fileName: work.workFile };
  const file = fileInfoFromFacts(facts, 1);
  const viewer = resolveViewer(file, undefined, isTextual(facts));
  const runsSite = viewer?.input === "site";
  const key = `${work.id}@${work.version}`;
  const [bytes, setBytes] = useState<{ key: string; blob: Blob | null; error: unknown } | null>(null);
  const source = useMemo<SiteSource | null>(() => (site ? { resolve: () => workGalleryApi.demo(work) } : null), [site, key]);

  useEffect(() => {
    if (site || !viewer) return;
    let live = true;
    workGalleryApi
      .file(work)
      .then((blob) => live && setBytes({ key, blob, error: null }))
      .catch((error: unknown) => live && setBytes({ key, blob: null, error }));
    return () => {
      live = false;
    };
    // The version's identity is the key.
  }, [key, site, viewer?.id]);

  const download = async () => {
    try {
      saveBlob(await workGalleryApi.file(work), work.workFile || label);
    } catch (e) {
      toast.error(errorText(e, "Could not download it"));
    }
  };
  const loaded = bytes?.key === key ? bytes : null;
  return (
    <div className="h-[min(60vh,32rem)] w-full overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)]">
      {loaded?.error ? (
        <p className="p-4 text-sm text-[var(--color-fg-subtle)]">{errorText(loaded.error, "Could not load this work")}</p>
      ) : viewer && !runsSite && !loaded ? (
        <p className="p-4 text-sm text-[var(--color-fg-subtle)]" role="status">
          Loading preview…
        </p>
      ) : (
        <ArtifactBody
          roomId=""
          artifactId={work.id}
          file={file}
          blob={loaded?.blob ?? null}
          site={source}
          viewer={viewer}
          onDownload={site ? undefined : () => void download()}
        />
      )}
    </div>
  );
}
