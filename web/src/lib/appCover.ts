// An app's generated cover (docs/prd/2026-10-13-app-gallery.md §4): what a
// card draws when the app's site has no cover image of its own (or its
// cover is refused). One owner: every app card asks appCover. Deterministic:
// the same app id always draws the same gradient, so a card never flickers
// between renders, sessions or devices; the initial comes from the app's
// name. Pure (no React, no DOM); unit-tested under node.

export interface AppCover {
  /** The CSS background: a two-stop linear gradient. */
  readonly background: string;
  /** The app name's first letter or digit, upper-cased ("" when it has none). */
  readonly initial: string;
}

/** FNV-1a over the id's UTF-16 code units: a stable 32-bit seed. */
function seed(id: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

/** The first letter or digit of name, upper-cased. */
export function appInitial(name: string): string {
  const m = name.match(/[\p{L}\p{N}]/u);
  return m ? m[0].toLocaleUpperCase("en-US") : "";
}

/** appCover is app appId's generated cover, with name's initial. */
export function appCover(appId: string, name: string): AppCover {
  const h = seed(appId);
  const hue = h % 360;
  // The second stop turns 30–109° away, so no two stops are the same hue.
  const hue2 = (hue + 30 + ((h >>> 9) % 80)) % 360;
  const angle = 100 + ((h >>> 17) % 120);
  return {
    background: `linear-gradient(${angle}deg, hsl(${hue} 70% 52%), hsl(${hue2} 75% 40%))`,
    initial: appInitial(name),
  };
}
