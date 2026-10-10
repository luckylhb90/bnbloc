// URLs on the desktop shell's own URI schemes (bnbloc-artifact,
// bnbloc-previewer, bnbloc-site). Pure, so it is unit-tested under node.

/** Tauri's convertFileSrc(path, protocol). */
export type ConvertFileSrc = (path: string, protocol: string) => string;

/** The URL of `path` (URL-safe, "/"-separated) on a desktop scheme.
 *  convertFileSrc builds the platform form of the scheme root
 *  (`<scheme>://localhost/` on macOS/Linux, `http://<scheme>.localhost/` on
 *  Windows) but percent-encodes its whole argument, slashes included — a
 *  multi-segment path would arrive as one segment and relative URLs inside
 *  the framed document would resolve against the root. So only the root is
 *  taken from it and the path is appended verbatim. */
export function schemeUrl(convert: ConvertFileSrc, scheme: string, path: string): string {
  return convert("", scheme).replace(/\/+$/, "") + "/" + path.replace(/^\/+/, "");
}
