// The one scorer of "does this declaration apply to this file"
// (docs/prd/2026-10-14-file-prompts-and-compare.md rule 1). A match spec is
// the wire FileMatch (MIME types, extensions; validated once, in
// internal/filematch) plus, for built-in viewers only, legacy artifact
// kinds. Viewers, comparers and file prompts all rank through
// matchSpecificity. Pure, unit-tested under node.

import type { FileMatch } from "./wire.gen.ts";

/** What a file is, for matching. */
export interface FileFacts {
  /** Display name with extension ("report.pdf"). */
  readonly name: string;
  /** Effective MIME type ("" when unknown). */
  readonly mimeType: string;
  /** Lower-case extension without the dot ("" when none). */
  readonly extension: string;
  /** Legacy artifact kind (inline artifacts carry no MIME type). */
  readonly kind?: string;
}

/** A FileMatch (any one is a MatchSpec), plus legacy kinds (built-in
 *  viewers only). */
export interface MatchSpec {
  /** Exact ("image/png") or wildcard ("image/*") MIME types, lower-case. */
  readonly mimeTypes?: readonly string[];
  /** Extensions, lower-case, with or without the leading dot. */
  readonly extensions?: readonly string[];
  readonly kinds?: readonly string[];
}

function normalizeExt(ext: string): string {
  return ext.replace(/^\./, "").toLowerCase();
}

/** How specifically a spec claims a file; 0 = not at all. A multi-part
 *  extension (".atlas.json") matching the end of the name is the most
 *  specific claim (it beats an exact generic MIME such as application/json),
 *  then exact MIME, plain extension, kind, MIME wildcard. */
export function matchSpecificity(match: MatchSpec, file: FileFacts): number {
  const mime = file.mimeType.toLowerCase();
  const mimes = (match.mimeTypes ?? []).map((m) => m.toLowerCase());
  if (mime && mimes.includes(mime)) return 4;
  const name = file.name.toLowerCase();
  const ext = file.extension.toLowerCase();
  for (const raw of match.extensions ?? []) {
    const claimed = normalizeExt(raw);
    if (!claimed) continue;
    if (claimed.includes(".") && name.endsWith(`.${claimed}`)) return 5;
  }
  for (const raw of match.extensions ?? []) {
    if (normalizeExt(raw) === ext) return 3;
  }
  if (file.kind && (match.kinds ?? []).some((k) => k.toLowerCase() === file.kind?.toLowerCase())) return 2;
  const top = mime.split("/")[0];
  if (top && mimes.includes(`${top}/*`)) return 1;
  return 0;
}

/** A match typed as text (the Library's "For files" field): extensions
 *  (".pptx") and MIME types ("image/*"), separated by commas or spaces.
 *  null when empty; the server validates the entries. */
export function parseFileMatch(text: string): FileMatch | null {
  const mimeTypes: string[] = [];
  const extensions: string[] = [];
  for (const raw of text.split(/[,\s]+/)) {
    const t = raw.trim().toLowerCase();
    if (!t) continue;
    if (t.includes("/")) {
      if (!mimeTypes.includes(t)) mimeTypes.push(t);
    } else {
      const ext = t.startsWith(".") ? t : `.${t}`;
      if (!extensions.includes(ext)) extensions.push(ext);
    }
  }
  if (mimeTypes.length + extensions.length === 0) return null;
  return { ...(mimeTypes.length ? { mimeTypes } : {}), ...(extensions.length ? { extensions } : {}) };
}

/** The text form of a match (parseFileMatch's inverse). */
export function formatFileMatch(match: FileMatch | undefined): string {
  return [...(match?.extensions ?? []), ...(match?.mimeTypes ?? [])].join(", ");
}
