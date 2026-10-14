// Shared by the Office previewers (pptx, docx, xlsx): what kind of file the
// bytes are, the sentence for one that cannot be opened, and frame titles.

/** What the first bytes say: an OOXML package (zip), an OLE compound file
 *  (a password-protected OOXML file, or a legacy .ppt/.doc/.xls under a new
 *  name), or something else. */
export type OfficeFormat = "zip" | "ole" | "unknown";

export function sniffOffice(bytes: ArrayBuffer): OfficeFormat {
  const b = new Uint8Array(bytes, 0, Math.min(8, bytes.byteLength));
  if (b.length >= 4 && b[0] === 0x50 && b[1] === 0x4b && b[2] === 0x03 && b[3] === 0x04) return "zip";
  const ole = [0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1];
  if (b.length === 8 && ole.every((v, i) => b[i] === v)) return "ole";
  return "unknown";
}

/** One Office file family as the sentences name it. */
export interface OfficeKind {
  /** "PowerPoint presentation" */
  readonly noun: string;
  /** ".pptx" */
  readonly ext: string;
  /** ".ppt", the legacy binary format */
  readonly legacy: string;
  /** "PowerPoint or Keynote" */
  readonly apps: string;
}

/** The sentence shown when a file cannot be opened at all. */
export function unreadableMessage(kind: OfficeKind, format: OfficeFormat, name: string): string {
  switch (format) {
    case "ole":
      return `${name} is password-protected or in the old ${kind.legacy} format; download it to open it in ${kind.apps}.`;
    case "unknown":
      return `${name} is not a ${kind.noun} (${kind.ext}); download it to open it.`;
    default:
      return `${name} could not be read as a ${kind.noun}; download it to open it.`;
  }
}

/** name shortened to room characters (an ellipsis marks the cut). */
export function shortName(name: string, room = 80): string {
  const chars = [...name];
  return chars.length <= room ? name : `${chars.slice(0, room - 1).join("")}…`;
}

/** "<file> — <unit> N of M" (N is 0-based in, 1-based out); the bare file
 *  name when there is nothing to count. */
export function countedTitle(name: string, unit: string, index: number, count: number): string {
  const file = shortName(name);
  if (count <= 0) return file;
  const n = Math.min(Math.max(index, 0), count - 1) + 1;
  return `${file} — ${unit} ${n} of ${count}`;
}

/** Keeps the frame on the preview: a click on a link that would leave the
 *  document (anything but an in-document #anchor) does nothing. Renderers
 *  draw a document's hyperlinks as real links, and a sandboxed frame that
 *  followed one would show an outside page in place of the file. */
export function keepLinksInside(root: HTMLElement): void {
  root.addEventListener(
    "click",
    (ev) => {
      const target = ev.target instanceof Element ? ev.target.closest("a[href]") : null;
      if (target && !(target.getAttribute("href") ?? "").startsWith("#")) ev.preventDefault();
    },
    true,
  );
}
