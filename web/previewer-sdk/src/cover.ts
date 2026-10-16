// A file's cover: the picture its card shows in the room's file list (the
// version's stored thumbnail). Whoever renders a file can make one, and the
// room keeps the first (services/room/artifact_files.go): a previewer frame
// sends it with provideThumbnail, the app's own text viewers store theirs
// (views/workbench/useCardImage). This module is the shared canvas side: the
// size every cover has (the card is 16:10), a text cover for source-like
// content, and a helper to draw anything else onto the same canvas. The text
// preparation is pure (unit-tested under node); drawing needs a canvas.

/** A cover is drawn at this size in CSS pixels, at twice the pixels. */
export const COVER_WIDTH = 480;
export const COVER_HEIGHT = 300;
const SCALE = 2;

const PAPER = "#fbfbf9";
const INK = "#2b2b29";
const FAINT = "#9a9a94";

const WIDE = /[⺀-﹯＀-￯]/;

/** How many monospace columns a character takes: a full-width one takes two. */
function columns(ch: string): number {
  return WIDE.test(ch) ? 2 : 1;
}

/** The line cut to `width` columns, an ellipsis marking the cut. */
function cut(line: string, width: number): string {
  let total = 0;
  for (const ch of line) total += columns(ch);
  if (total <= width) return line;
  let used = 0;
  let out = "";
  for (const ch of line) {
    const w = columns(ch);
    if (used + w > width - 1) return `${out}…`;
    out += ch;
    used += w;
  }
  return out;
}

/** The lines of a text a cover shows: line endings and tabs normalized, no
 *  leading blank lines, a run of blank lines folded to one, each line cut to
 *  `maxCols` columns, at most `maxLines`. */
export function coverLines(text: string, maxLines = 14, maxCols = 56): string[] {
  const out: string[] = [];
  let blank = false;
  for (const raw of text.replace(/\r\n?/g, "\n").split("\n")) {
    const line = raw.replace(/\t/g, "    ").replace(/\s+$/, "");
    if (line === "") {
      if (out.length > 0) blank = true;
      continue;
    }
    if (blank) out.push("");
    blank = false;
    out.push(cut(line, maxCols));
    if (out.length >= maxLines) break;
  }
  return out;
}

/** Draws on a fresh cover canvas and returns it as PNG bytes. */
export async function renderCover(draw: (ctx: CanvasRenderingContext2D, width: number, height: number) => void): Promise<ArrayBuffer> {
  const canvas = document.createElement("canvas");
  canvas.width = COVER_WIDTH * SCALE;
  canvas.height = COVER_HEIGHT * SCALE;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("no canvas");
  ctx.scale(SCALE, SCALE);
  ctx.fillStyle = PAPER;
  ctx.fillRect(0, 0, COVER_WIDTH, COVER_HEIGHT);
  draw(ctx, COVER_WIDTH, COVER_HEIGHT);
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/png"));
  if (!blob) throw new Error("the cover could not be encoded");
  return blob.arrayBuffer();
}

/** A text cover: the lines as on a page of source. */
export function renderTextCover(lines: readonly string[], options: { mono?: boolean } = {}): Promise<ArrayBuffer> {
  const mono = options.mono ?? true;
  return renderCover((ctx, w, h) => {
    const size = 12.5;
    const pitch = 19;
    ctx.font = `${size}px ${mono ? 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace' : 'system-ui, -apple-system, "Segoe UI", sans-serif'}`;
    ctx.textBaseline = "top";
    ctx.fillStyle = INK;
    lines.forEach((line, i) => ctx.fillText(line, 16, 14 + i * pitch));
    // The page fades out at the bottom: there is more.
    const fade = ctx.createLinearGradient(0, h - 60, 0, h);
    fade.addColorStop(0, "rgba(251,251,249,0)");
    fade.addColorStop(1, PAPER);
    ctx.fillStyle = fade;
    ctx.fillRect(0, h - 60, w, 60);
    ctx.fillStyle = FAINT;
  });
}
