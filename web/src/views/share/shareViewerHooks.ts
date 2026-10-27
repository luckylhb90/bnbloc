import { useCallback, useEffect, useState } from "react";
import { POSTER_LIMIT_MS, posterVisible } from "@/lib/sharePoster";
import { publicShareApi } from "@/lib/sharesApi";
import { startSharePreviewers } from "@/views/artifact/remotePreviewers";

// What the public share page needs besides the file's bytes to show a file in
// its previewer (docs/prd/2026-10-27-shared-file-viewers.md): the share's
// previewers in the registry, and the cover picture shown until the previewer
// has drawn. The decisions are lib/previewerSync and lib/sharePoster; these are
// their React drivers.

/** Registers the share's previewers (what its creator can offer for the file) in
 *  the viewer registry; true once that lookup has answered, failed or run out of
 *  time, which is when the page may choose a viewer. Without the wait a file
 *  with a previewer would show as code first. */
export function useSharePreviewers(link: string): boolean {
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    let live = true;
    const lookup = startSharePreviewers(link);
    void lookup.settled.then(() => {
      if (live) setSettled(true);
    });
    return () => {
      live = false;
      lookup.stop();
    };
  }, [link]);
  return settled;
}

/** The version's cover as a picture to lay over the loading previewer: its
 *  object URL while it should show, else null, and the callback the previewer
 *  calls when it has drawn or failed. A share with no cover, or a built-in
 *  viewer, shows none. */
export function useSharePoster(link: string, cover: boolean, viewerIsPreviewer: boolean): { readonly poster: string | null; readonly onRendered: () => void } {
  const [url, setUrl] = useState<string | null>(null);
  const [rendered, setRendered] = useState(false);
  const [elapsedMs, setElapsedMs] = useState(0);

  // The cover is fetched at once, while the previewer is still being looked up.
  useEffect(() => {
    if (!cover) return;
    let cancelled = false;
    let made: string | null = null;
    publicShareApi
      .cover(link)
      .then((blob) => {
        if (cancelled) return;
        made = URL.createObjectURL(blob);
        setUrl(made);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      if (made) URL.revokeObjectURL(made);
    };
  }, [link, cover]);

  // The limit runs from the moment a previewer starts to load.
  useEffect(() => {
    if (!viewerIsPreviewer) return;
    const timer = window.setTimeout(() => setElapsedMs(POSTER_LIMIT_MS), POSTER_LIMIT_MS);
    return () => window.clearTimeout(timer);
  }, [viewerIsPreviewer]);

  const onRendered = useCallback(() => setRendered(true), []);
  // onRendered also fires when the previewer fails, so `rendered` covers both.
  const show = url !== null && posterVisible({ cover: true, viewerIsPreviewer, rendered, errored: false, elapsedMs });
  return { poster: show ? url : null, onRendered };
}
