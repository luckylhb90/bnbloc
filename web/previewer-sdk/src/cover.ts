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

// --- covers drawn from a document's text ---------------------------------------
// A viewer that reads a document (a deck, a page, a sheet) but has no use for
// its own renderer on a 240 px card draws its first screen from the text it
// already reads: same canvas, no foreignObject, nothing to taint.

const SANS = 'system-ui, -apple-system, "Segoe UI", sans-serif';

/** The text broken into lines that fit `width`, at most `maxLines`; when
 *  text is left over, the last line ends with an ellipsis. Breaks at spaces;
 *  a word wider than the line (CJK has no spaces) at any character. */
function wrap(ctx: CanvasRenderingContext2D, text: string, width: number, maxLines: number): string[] {
  const tokens = text.trim().split(/(\s+)/).flatMap((t) => (ctx.measureText(t).width > width ? [...t] : [t]));
  const lines: string[] = [];
  let line = "";
  for (const token of tokens) {
    if (line === "" || ctx.measureText(line + token).width <= width) {
      line += token;
      continue;
    }
    lines.push(line.trimEnd());
    if (lines.length === maxLines) {
      let last = lines[maxLines - 1];
      while (last !== "" && ctx.measureText(`${last}…`).width > width) last = last.slice(0, -1);
      lines[maxLines - 1] = `${last}…`;
      return lines;
    }
    line = token.trimStart();
  }
  if (line.trim() !== "") lines.push(line.trimEnd());
  return lines;
}

/** A slide: the first one of a deck, a white slide on the paper with its
 *  title and bullets. */
export function renderSlideCover(slide: { readonly title: string; readonly lines: readonly string[] }): Promise<ArrayBuffer> {
  return renderCover((ctx, w, h) => {
    const sw = w - 40;
    const sh = Math.round((sw * 9) / 16);
    const x = 20;
    const y = Math.round((h - sh) / 2);
    ctx.fillStyle = "#ffffff";
    ctx.strokeStyle = "#dcdcd6";
    ctx.lineWidth = 1;
    ctx.fillRect(x, y, sw, sh);
    ctx.strokeRect(x + 0.5, y + 0.5, sw - 1, sh - 1);
    ctx.fillStyle = INK;
    ctx.textBaseline = "top";
    ctx.font = `700 22px ${SANS}`;
    let top = y + 22;
    for (const line of wrap(ctx, slide.title, sw - 48, 2)) {
      ctx.fillText(line, x + 24, top);
      top += 28;
    }
    top += 8;
    ctx.font = `13px ${SANS}`;
    ctx.fillStyle = "#4a4a46";
    for (const text of slide.lines) {
      if (top > y + sh - 22) break;
      const [first = ""] = wrap(ctx, text, sw - 64, 1);
      ctx.fillText(`•  ${first}`, x + 28, top);
      top += 20;
    }
  });
}

export interface PageBlock {
  readonly text: string;
  readonly heading?: boolean;
}

/** A document's first page: a white page in the middle of the cover, running
 *  off the bottom, headings bold and larger. */
export function renderPageCover(blocks: readonly PageBlock[]): Promise<ArrayBuffer> {
  return renderCover((ctx, w, h) => {
    const pw = 320;
    const x = (w - pw) / 2;
    ctx.fillStyle = "#ffffff";
    ctx.strokeStyle = "#dcdcd6";
    ctx.lineWidth = 1;
    ctx.fillRect(x, 10, pw, h);
    ctx.strokeRect(x + 0.5, 10.5, pw - 1, h);
    ctx.fillStyle = INK;
    ctx.textBaseline = "top";
    let top = 28;
    for (const block of blocks) {
      if (top > h) break;
      ctx.font = block.heading ? `700 15px ${SANS}` : `11px ${SANS}`;
      ctx.fillStyle = block.heading ? INK : "#4a4a46";
      const pitch = block.heading ? 20 : 15;
      // A block's own line breaks stay (a table's rows); each line wraps on its own.
      const own = block.text.split("\n");
      for (const part of own) {
        for (const line of wrap(ctx, part, pw - 48, block.heading ? 2 : own.length > 1 ? 1 : 3)) {
          ctx.fillText(line, x + 24, top);
          top += pitch;
        }
      }
      top += block.heading ? 4 : 7;
    }
  });
}

/** A sheet's first rows and columns as a grid; the first row shaded when it is
 *  a header. */
export function renderGridCover(rows: readonly (readonly string[])[], header: boolean): Promise<ArrayBuffer> {
  return renderCover((ctx, w, h) => {
    const cols = Math.max(1, ...rows.map((r) => r.length));
    const colW = Math.min(150, (w - 24) / cols);
    const rowH = 26;
    const x0 = (w - colW * cols) / 2;
    const y0 = 16;
    const shown = rows.filter((_, r) => y0 + r * rowH <= h);
    // The cells first, then the text: a cell's text runs on over the empty
    // cells to its right (as in a sheet) and must not be painted over by them.
    shown.forEach((_, r) => {
      for (let c = 0; c < cols; c++) {
        ctx.fillStyle = header && r === 0 ? "#ecebe8" : "#ffffff";
        ctx.fillRect(x0 + c * colW, y0 + r * rowH, colW, rowH);
        ctx.strokeStyle = "#d6d5d1";
        ctx.strokeRect(x0 + c * colW + 0.5, y0 + r * rowH + 0.5, colW, rowH);
      }
    });
    ctx.textBaseline = "middle";
    ctx.fillStyle = INK;
    shown.forEach((row, r) => {
      ctx.font = `${header && r === 0 ? "700 " : ""}12px ${SANS}`;
      for (let c = 0; c < cols; c++) {
        const text = row[c] ?? "";
        if (text === "") continue;
        const x = x0 + c * colW;
        const y = y0 + r * rowH + rowH / 2 + 1;
        if (/^[-+$€£¥]?[\d,.]+%?$/.test(text)) {
          ctx.textAlign = "right";
          ctx.fillText(text, x + colW - 8, y, colW - 16);
        } else {
          let room = colW;
          for (let n = c + 1; n < cols && (row[n] ?? "") === ""; n++) room += colW;
          ctx.textAlign = "left";
          ctx.fillText(text, x + 8, y, room - 16);
        }
      }
    });
    ctx.textAlign = "left";
  });
}
