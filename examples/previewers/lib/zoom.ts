// Zoom and fit for a previewer that draws one picture (a diagram): the toolbar
// zoom factor's bounds and the factor that shows the whole picture.

import { ZOOM_MAX, ZOOM_MIN } from "../../../web/previewer-sdk/src/index.ts";

export function clampZoom(z: number): number {
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z));
}

/** The zoom that shows the whole picture (its natural size, in CSS pixels)
 *  in the stage, never above 100% (a small picture is not blown up). */
export function fitZoom(stage: HTMLElement, natural: { readonly w: number; readonly h: number }, margin = 32): number {
  if (!natural.w || !natural.h) return 1;
  const w = Math.max(1, stage.clientWidth - margin);
  const h = Math.max(1, stage.clientHeight - margin);
  return clampZoom(Math.min(1, w / natural.w, h / natural.h));
}
