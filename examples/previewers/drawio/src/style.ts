// A draw.io style string ("ellipse;whiteSpace=wrap;fillColor=#d5e8d4;") read
// and cleaned for maxGraph. Pure: node tests read it.

import { COLOR_KEYS, NAMED_STYLES, PERIMETERS } from "./shapeTable.ts";

export interface ParsedStyle {
  /** Bare tokens ("ellipse"), in order. */
  readonly names: readonly string[];
  readonly props: ReadonlyMap<string, string>;
}

export function parseStyle(style: string): ParsedStyle {
  const names: string[] = [];
  const props = new Map<string, string>();
  for (const part of style.split(";")) {
    const token = part.trim();
    if (!token) continue;
    const eq = token.indexOf("=");
    if (eq < 0) names.push(token);
    else if (eq > 0) props.set(token.slice(0, eq), token.slice(eq + 1));
  }
  return { names, props };
}

/** Whether the label is HTML (`html=1`) rather than plain text. */
export function isHtmlStyle(style: string): boolean {
  return parseStyle(style).props.get("html") === "1";
}

const LIGHT_DARK = /^light-dark\(\s*([^,()]+?)\s*(?:,[^()]*)?\)$/i;

/** One property's value as maxGraph reads it; null drops the property (the
 *  default style applies instead). */
function cleanValue(key: string, value: string): string | null {
  if (COLOR_KEYS.has(key)) {
    const themed = LIGHT_DARK.exec(value);
    const color = themed ? themed[1] : value;
    return color === "default" || color === "" ? null : color;
  }
  if (key === "perimeter") return PERIMETERS[value] ?? "rectanglePerimeter";
  if (key === "image") {
    // draw.io drops ";base64" from a data URI (its own renderer puts it back)
    // and a style string cannot carry "=": a browser reads unpadded base64.
    return value.replace(/^data:(image\/[\w.+-]+),/, "data:$1;base64,").replace(/=+$/, "");
  }
  return value;
}

/** The style as maxGraph can read it: colors resolved to the light theme,
 *  "default" colors dropped, perimeters and images made readable. Bare names
 *  are kept (maxGraph resolves them from NAMED_STYLES). */
export function normalizeStyle(style: string): string {
  const { names, props } = parseStyle(style);
  const out: string[] = [...names];
  for (const [key, value] of props) {
    const clean = cleanValue(key, value);
    if (clean !== null) out.push(`${key}=${clean}`);
  }
  return out.join(";");
}

/** What the shape is called, for the agent: draw.io's `shape=` name, or its
 *  bare shape token, or "rectangle" ("rounded rectangle"). */
export function shapeOf(style: string): string {
  const { names, props } = parseStyle(style);
  const named = props.get("shape") ?? names.find((n) => n in NAMED_STYLES && NAMED_STYLES[n].shape);
  if (named) return named.replace(/^mxgraph\./, "");
  return props.get("rounded") === "1" ? "rounded rectangle" : "rectangle";
}

/** Whether the style is a container the cells inside belong to. */
export function isContainer(style: string): boolean {
  const { names, props } = parseStyle(style);
  return names.includes("swimlane") || names.includes("group") || props.get("shape") === "swimlane" || props.get("container") === "1";
}
