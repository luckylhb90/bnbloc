// Word stores list bullets as private-use code points of the Symbol and
// Wingdings fonts (U+F0B7 is the plain bullet), drawn with those fonts.
// Browsers have no such glyphs, so docx-preview's markers render as empty
// boxes: they are swapped for the Unicode characters the fonts draw.

const SYMBOL_BULLETS: Readonly<Record<string, string>> = {
  "": "•", // Symbol: bullet •
  "": "▪", // Wingdings: small square ▪
  "": "■", // Wingdings: black square ■
  "": "➢", // Wingdings: arrowhead ➢
  "": "❖", // Wingdings: diamond ❖
  "": "✓", // Wingdings: check mark ✓
  "": "◆", // Wingdings: black diamond ◆
};

const PATTERN = new RegExp(`[${Object.keys(SYMBOL_BULLETS).join("")}]`, "g");

/** css with Symbol/Wingdings bullet code points replaced by Unicode. */
export function unicodeBullets(css: string): string {
  return css.replace(PATTERN, (c) => SYMBOL_BULLETS[c] ?? c);
}
