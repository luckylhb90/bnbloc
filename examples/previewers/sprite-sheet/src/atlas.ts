// Sprite-sheet / texture-atlas JSON parsing (pure, no DOM: unit-tested by
// build.test.mjs). Accepts the two common generic shapes:
//   hash form : { frames: { "<name>": { frame: {x,y,w,h} } }, meta: { image, size: {w,h} } }
//   array form: { frames: [ { filename, frame: {x,y,w,h} } ], meta: {...} }

export interface AtlasFrame {
  readonly name: string;
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

export interface Atlas {
  readonly frames: readonly AtlasFrame[];
  /** Sheet size: meta.size, else the frames' bounding box. */
  readonly width: number;
  readonly height: number;
  /** meta.image: the sheet image the atlas describes (informational). */
  readonly image: string;
}

/** Upper bound on frames read from one atlas (a preview, not an editor). */
export const MAX_FRAMES = 10000;
/** Upper bound on the sheet side the previewer draws. */
export const MAX_SIDE = 16384;

type Rec = Readonly<Record<string, unknown>>;

function isRecord(value: unknown): value is Rec {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function dimension(value: unknown, allowZero: boolean): number | null {
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  if (value < 0 || (!allowZero && value === 0) || value > MAX_SIDE) return null;
  return value;
}

function readRect(name: string, value: unknown): AtlasFrame | null {
  if (!isRecord(value)) return null;
  const rect = isRecord(value.frame) ? value.frame : null;
  if (!rect) return null;
  const x = dimension(rect.x, true);
  const y = dimension(rect.y, true);
  const w = dimension(rect.w, false);
  const h = dimension(rect.h, false);
  if (x === null || y === null || w === null || h === null) return null;
  return { name, x, y, w, h };
}

/** Parses atlas JSON text; throws an Error with a member-readable message. */
export function parseAtlas(text: string): Atlas {
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch {
    throw new Error("The file is not valid JSON.");
  }
  if (!isRecord(doc)) throw new Error("The atlas must be a JSON object with a frames field.");
  const raw = doc.frames;
  const frames: AtlasFrame[] = [];
  if (Array.isArray(raw)) {
    raw.forEach((entry, i) => {
      const name = isRecord(entry) && typeof entry.filename === "string" ? entry.filename : `frame ${i + 1}`;
      const frame = readRect(name, entry);
      if (frame) frames.push(frame);
    });
  } else if (isRecord(raw)) {
    for (const [name, entry] of Object.entries(raw)) {
      const frame = readRect(name, entry);
      if (frame) frames.push(frame);
    }
  } else {
    throw new Error("The JSON has no frames object or list: it is not a sprite-sheet atlas.");
  }
  if (frames.length === 0) throw new Error("The atlas lists no valid frames.");
  if (frames.length > MAX_FRAMES) throw new Error(`The atlas lists more than ${MAX_FRAMES} frames.`);
  const meta = isRecord(doc.meta) ? doc.meta : {};
  const size = isRecord(meta.size) ? meta.size : {};
  const right = Math.max(...frames.map((f) => f.x + f.w));
  const bottom = Math.max(...frames.map((f) => f.y + f.h));
  const width = dimension(size.w, false) ?? Math.min(right, MAX_SIDE);
  const height = dimension(size.h, false) ?? Math.min(bottom, MAX_SIDE);
  return { frames, width, height, image: typeof meta.image === "string" ? meta.image : "" };
}

/** True when the file should be read as an atlas rather than an image. */
export function isAtlasFile(name: string, mimeType: string): boolean {
  const lower = name.toLowerCase();
  return mimeType === "application/json" || lower.endsWith(".json");
}

/** Cell size for the coarse grid of a plain sheet: the first common sprite
 *  size (16, 32, 64, 8) that divides both sides into 2–128 cells, else 32. */
export function cellSize(width: number, height: number): number {
  for (const size of [16, 32, 64, 8]) {
    const across = width / size;
    const down = height / size;
    if (Number.isInteger(across) && Number.isInteger(down) && Math.min(across, down) >= 2 && Math.max(across, down) <= 128) return size;
  }
  return 32;
}
