// Pure helpers of the PowerPoint previewer (no DOM): what kind of file the
// bytes are, and the frame title.

/** What the first bytes say: an OOXML package (zip), an OLE compound file
 *  (a password-protected .pptx, or a legacy .ppt under a new name), or
 *  something else. */
export type DeckFormat = "zip" | "ole" | "unknown";

export function sniffDeck(bytes: ArrayBuffer): DeckFormat {
  const b = new Uint8Array(bytes, 0, Math.min(8, bytes.byteLength));
  if (b.length >= 4 && b[0] === 0x50 && b[1] === 0x4b && b[2] === 0x03 && b[3] === 0x04) return "zip";
  const ole = [0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1];
  if (b.length === 8 && ole.every((v, i) => b[i] === v)) return "ole";
  return "unknown";
}

/** The sentence shown when a file cannot be opened at all. */
export function unreadableMessage(format: DeckFormat, name: string): string {
  switch (format) {
    case "ole":
      return `${name} is password-protected or in the old .ppt format; download it to open it in PowerPoint or Keynote.`;
    case "unknown":
      return `${name} is not a PowerPoint presentation (.pptx); download it to open it.`;
    default:
      return `${name} could not be read as a presentation; download it to open it.`;
  }
}

function shortName(name: string, room: number): string {
  const chars = [...name];
  return chars.length <= room ? name : `${chars.slice(0, room - 1).join("")}…`;
}

/** "<file> — slide N of M" (N is 0-based in, 1-based out). */
export function deckTitle(name: string, index: number, count: number): string {
  const file = shortName(name, 80);
  if (count <= 0) return file;
  const n = Math.min(Math.max(index, 0), count - 1) + 1;
  return `${file} — slide ${n} of ${count}`;
}
