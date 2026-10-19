// A work opened without a cover gets one (docs/prd/2026-10-19-work-covers.md
// §2.3): when a work version's cover route answers 404, its detail makes one
// and stores it beside the version (PUT …/cover?source=render, POST
// …/cover/ai). What it makes depends on the work's type and on its viewer;
// the gateway keeps the first render, and an author's AI cover replaces it.
// Pure: no React, no DOM; unit-tested under node.

import type { ContentComponent } from "./contentComponents.ts";

/** How a work version without a cover gets one:
 *  - "image": the image itself, scaled into a JPEG within the cover cap;
 *  - "frame": a frame of its video;
 *  - "viewer": the cover its viewer draws when it renders the file (a PDF's
 *    first page, a deck's, a document's, a diagram's);
 *  - "ai": an AI cover from its title, description and type, only when its
 *    author opens it and the organization's image generation is on.
 *  Nothing else draws one: the card keeps its generated cover. */
export type WorkCoverPlan = "image" | "frame" | "viewer" | "ai";

export function workCoverPlan(
  work: Pick<ContentComponent, "workType" | "mine">,
  viewerCover: "text" | "render" | undefined,
  imageGeneration: boolean,
): WorkCoverPlan | null {
  if (work.workType === "image") return "image";
  if (work.workType === "video") return "frame";
  if (viewerCover === "render") return "viewer";
  return work.mine && imageGeneration ? "ai" : null;
}

// The widths and JPEG qualities a cover is tried at, widest and best first
// (the gateway's tarbundle.FitCover tries the same).
const COVER_WIDTHS = [1600, 1200, 800];
const COVER_QUALITIES = [0.85, 0.7];

/** One size and quality a cover is encoded at. */
export interface CoverEncoding {
  readonly width: number;
  readonly height: number;
  readonly quality: number;
}

/** coverEncodings are the sizes and qualities an image of width × height is
 *  tried at, in order, until one fits the cover cap: at most each of the
 *  cover widths (its aspect kept, never enlarged), each at both qualities. */
export function coverEncodings(width: number, height: number): CoverEncoding[] {
  if (!(width > 0 && height > 0)) return [];
  const out: CoverEncoding[] = [];
  for (const max of COVER_WIDTHS) {
    const w = Math.min(width, max);
    const h = Math.max(1, Math.round((height * w) / width));
    if (out.some((e) => e.width === w)) continue;
    for (const quality of COVER_QUALITIES) out.push({ width: w, height: h, quality });
  }
  return out;
}

/** frameTime is where a video's cover frame is taken (seconds): about one
 *  second in (an unbounded length too), the middle of a clip shorter than
 *  two, the start when it has no length. */
export function frameTime(duration: number): number {
  if (!(duration > 0)) return 0;
  return Number.isFinite(duration) && duration < 2 ? duration / 2 : 1;
}

/** A session's covers already tried, by version: each is made at most once,
 *  whatever its outcome, so a refused or failed cover never loops. */
export interface CoverTries {
  /** True the first time key is asked for, false after. */
  take(key: string): boolean;
}

export function createCoverTries(): CoverTries {
  const tried = new Set<string>();
  return {
    take(key) {
      if (tried.has(key)) return false;
      tried.add(key);
      return true;
    },
  };
}
