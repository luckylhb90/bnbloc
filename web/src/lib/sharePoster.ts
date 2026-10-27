// The picture a share page shows while a file's previewer loads (the version's
// stored cover): when to show it, and when to take it away. Pure, so the table
// is tested under node; views/share/shareViewerHooks.ts drives it.

/** How long the cover stays if the previewer never reports a first drawing:
 *  past it the frame shows, whatever state it is in, so it does not hide
 *  behind a picture. */
export const POSTER_LIMIT_MS = 20_000;

export interface PosterState {
  /** The share has a stored cover for the shown version. */
  readonly cover: boolean;
  /** The viewer chosen for the file is an installed previewer (a built-in
   *  viewer draws at once and needs no picture). */
  readonly viewerIsPreviewer: boolean;
  /** The previewer reported its first drawing. */
  readonly rendered: boolean;
  /** The previewer reported an error (it will not draw; its notice shows). */
  readonly errored: boolean;
  /** Time since the previewer began to load. */
  readonly elapsedMs: number;
}

/** Whether the cover is on screen over the loading previewer. */
export function posterVisible(s: PosterState): boolean {
  if (!s.cover || !s.viewerIsPreviewer) return false;
  if (s.rendered || s.errored) return false;
  return s.elapsedMs < POSTER_LIMIT_MS;
}
