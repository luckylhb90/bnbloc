import { useEffect, useMemo } from "react";
import type { ContentComponent } from "@/lib/contentComponents";
import { ApiError } from "@/lib/errors";
import { useMediaCapability } from "@/lib/media";
import { invalidateQueries } from "@/lib/useQuery";
import { MaxWorkCoverBytes } from "@/lib/wire.gen";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { coverEncodings, createCoverTries, frameTime, workCoverPlan } from "@/lib/workCovers";
import type { ArtifactViewer, ArtifactViewerProps } from "@/views/artifact/registry";
import { componentCover, coverStored } from "@/views/workbench/AppCover";
import { loadComponentCover } from "@/views/workbench/artifactData";
import { GALLERY_QUERY } from "./WorkParts";

// A work opened without a cover gets one (docs/prd/2026-10-19-work-covers.md
// §2.3): the detail asks the version's cover (the card's coverUrl) and, when
// it answers 404, makes one by the work's plan (lib/workCovers) and stores
// it: an image scaled, a video's frame, its viewer's cover (the onCover this
// returns, handed to the viewer), or, for the author of a work no viewer
// draws, an AI cover the gateway draws. Each version is tried once per
// session; a stored cover reaches every card showing it (coverStored).

const tries = createCoverTries();

/** useWorkCover makes the work version's cover when it has none. blob is
 *  the work's file (an image's, a video's) once loaded; the answer is where
 *  its viewer's cover goes (absent unless the viewer draws it). */
export function useWorkCover(work: ContentComponent, viewer: ArtifactViewer | null, blob: Blob | null): ArtifactViewerProps["onCover"] {
  const plan = workCoverPlan(work, viewer?.cover, useMediaCapability("image") === true);
  const key = `${work.id}@${work.version}`;
  const coverUrl = work.coverUrl;

  useEffect(() => {
    if (!coverUrl || !plan || plan === "viewer") return;
    const make =
      plan === "ai"
        ? () => workGalleryApi.drawCover(work)
        : blob && (() => (plan === "image" ? imageCover(blob) : videoFrameCover(blob)).then((image) => workGalleryApi.putCover(work, image)));
    if (!make || !tries.take(key)) return;
    void keepCover(coverUrl, make);
    // The version's identity is the key.
  }, [key, coverUrl, plan, blob]);

  return useMemo<ArtifactViewerProps["onCover"]>(() => {
    if (!coverUrl || plan !== "viewer") return undefined;
    return (png) => {
      if (!tries.take(key)) return;
      const image = new Blob([png], { type: "image/png" });
      void keepCover(coverUrl, () => workGalleryApi.putCover(work, image));
    };
    // The version's identity is the key.
  }, [key, coverUrl, plan]);
}

/** keepCover stores a cover made by make when the version still has none,
 *  and has the cards showing it ask again once it is kept. A failure keeps
 *  the generated cover: nothing is shown. */
async function keepCover(coverUrl: string, make: () => Promise<{ stored: boolean }>): Promise<void> {
  try {
    const missing = await loadComponentCover(coverUrl).then(
      () => false,
      (e: unknown) => e instanceof ApiError && e.status === 404,
    );
    if (!missing || !(await make()).stored) return;
    coverStored(componentCover(coverUrl));
    invalidateQueries(GALLERY_QUERY);
  } catch {
    // The card keeps its generated cover.
  }
}

/** imageCover is an image scaled into a JPEG within the cover cap. */
async function imageCover(blob: Blob): Promise<Blob> {
  const url = URL.createObjectURL(blob);
  try {
    const img = new Image();
    img.src = url;
    await img.decode();
    return await canvasCover(img, img.naturalWidth, img.naturalHeight);
  } finally {
    URL.revokeObjectURL(url);
  }
}

/** videoFrameCover is a frame of a video (frameTime: about a second in),
 *  from a player of its own once it has loaded data. */
async function videoFrameCover(blob: Blob): Promise<Blob> {
  const url = URL.createObjectURL(blob);
  const video = document.createElement("video");
  try {
    video.muted = true;
    video.playsInline = true;
    video.preload = "auto";
    video.src = url;
    await mediaEvent(video, "loadeddata");
    video.currentTime = frameTime(video.duration);
    await mediaEvent(video, "seeked");
    return await canvasCover(video, video.videoWidth, video.videoHeight);
  } finally {
    video.removeAttribute("src");
    video.load();
    URL.revokeObjectURL(url);
  }
}

/** mediaEvent resolves on the player's event, rejects on its error or after
 *  30 seconds without it. */
function mediaEvent(video: HTMLVideoElement, event: "loadeddata" | "seeked"): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => done(new Error(`no ${event}`)), 30_000);
    const ok = () => done(null);
    const failed = () => done(new Error("the video could not be read"));
    function done(err: Error | null) {
      window.clearTimeout(timer);
      video.removeEventListener(event, ok);
      video.removeEventListener("error", failed);
      if (err) reject(err);
      else resolve();
    }
    video.addEventListener(event, ok);
    video.addEventListener("error", failed);
  });
}

/** canvasCover draws source on white at the first of coverEncodings whose
 *  JPEG fits the cover cap. */
async function canvasCover(source: CanvasImageSource, width: number, height: number): Promise<Blob> {
  const canvas = document.createElement("canvas");
  for (const e of coverEncodings(width, height)) {
    canvas.width = e.width;
    canvas.height = e.height;
    const ctx = canvas.getContext("2d");
    if (!ctx) break;
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, e.width, e.height);
    ctx.drawImage(source, 0, 0, e.width, e.height);
    const jpeg = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", e.quality));
    if (jpeg && jpeg.size <= MaxWorkCoverBytes) return jpeg;
  }
  throw new Error("no cover fits");
}
