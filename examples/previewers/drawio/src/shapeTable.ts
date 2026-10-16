// What draw.io's own stylesheet and shape library supply that maxGraph does
// not, as data. A diagram written by draw.io names its default shapes as bare
// style tokens ("ellipse;", "rhombus;") that draw.io resolves from its
// stylesheet, and uses shapes of its library ("cylinder3", "parallelogram",
// "document"…) that maxGraph lacks. Adding a shape is adding a row here
// (shapes.ts registers every row; style.ts reads the names). Pure: no
// maxGraph import, so node tests read it.

/** One drawing instruction in the shape's own box, 0..w by 0..h. */
export type Seg = readonly ["M" | "L", number, number] | readonly ["C", number, number, number, number, number, number] | readonly ["Z"];

/** A parameter the shape reads from the cell's style: a fraction of the box
 *  (or pixels with `fixedSize=1`) or plain pixels. */
export interface ShapeSize {
  readonly key: string;
  readonly fallback: number;
  /** "fraction": of the box's width unless the style says fixedSize=1;
   *  "px": pixels always. */
  readonly unit: "fraction" | "px";
  /** The pixels when the cell says fixedSize=1 and names no size (draw.io's
   *  own default for the shape). */
  readonly fixedFallback?: number;
}

export interface ShapeRow {
  /** The outline, filled and stroked. */
  readonly outline: (w: number, h: number, s: number) => readonly Seg[];
  /** Inner lines, stroked only (a cube's edges, a process's bars). */
  readonly lines?: (w: number, h: number, s: number) => readonly Seg[];
  readonly size?: ShapeSize;
}

/** The shape's size parameter in pixels: the cell's own value, else the
 *  shape's default; a fraction of the width unless the cell says
 *  `fixedSize=1`. */
export function shapeSize(row: ShapeRow, style: Readonly<Record<string, unknown>> | null, w: number): number {
  const spec = row.size;
  if (!spec) return 0;
  const raw = Number(style?.[spec.key]);
  const given = style?.[spec.key] !== undefined && Number.isFinite(raw);
  if (spec.unit === "px") return Math.max(0, Math.min(given ? raw : spec.fallback, w));
  if (style?.fixedSize === 1) return Math.max(0, Math.min(given ? raw : (spec.fixedFallback ?? spec.fallback), w));
  return Math.max(0, Math.min(given ? raw : spec.fallback, 1) * w);
}

const K = 0.5522847498; // a circle's quarter as a cubic curve

function ellipseTop(w: number, ry: number): readonly Seg[] {
  const rx = w / 2;
  return [
    ["M", 0, ry],
    ["C", 0, ry - ry * K, rx - rx * K, 0, rx, 0],
    ["C", rx + rx * K, 0, w, ry - ry * K, w, ry],
  ];
}

/** draw.io shapes maxGraph does not draw, by the `shape=` name draw.io writes. */
export const CUSTOM_SHAPES: Readonly<Record<string, ShapeRow>> = {
  parallelogram: {
    size: { key: "size", fallback: 0.25, unit: "fraction", fixedFallback: 20 },
    outline: (w, h, s) => [["M", 0, h], ["L", s, 0], ["L", w, 0], ["L", w - s, h], ["Z"]],
  },
  trapezoid: {
    size: { key: "size", fallback: 0.2, unit: "fraction", fixedFallback: 20 },
    outline: (w, h, s) => [["M", 0, h], ["L", s, 0], ["L", w - s, 0], ["L", w, h], ["Z"]],
  },
  step: {
    size: { key: "size", fallback: 0.25, unit: "fraction", fixedFallback: 20 },
    outline: (w, h, s) => [["M", 0, 0], ["L", w - s, 0], ["L", w, h / 2], ["L", w - s, h], ["L", 0, h], ["L", s, h / 2], ["Z"]],
  },
  process: {
    size: { key: "size", fallback: 0.1, unit: "fraction", fixedFallback: 20 },
    outline: (w, h) => [["M", 0, 0], ["L", w, 0], ["L", w, h], ["L", 0, h], ["Z"]],
    lines: (w, h, s) => [["M", s, 0], ["L", s, h], ["M", w - s, 0], ["L", w - s, h]],
  },
  document: {
    size: { key: "size", fallback: 0.15, unit: "fraction" },
    outline: (w, h, s) => {
      const f = Math.min(s, h / 2);
      return [["M", 0, 0], ["L", w, 0], ["L", w, h - f], ["C", w * 0.75, h - f * 2.2, w * 0.6, h - f * 2.2, w * 0.5, h - f], ["C", w * 0.4, h - f * 0.2, w * 0.25, h - f * 0.2, 0, h - f], ["Z"]];
    },
  },
  cube: {
    size: { key: "size", fallback: 20, unit: "px" },
    outline: (w, h, s) => [["M", 0, s], ["L", s, 0], ["L", w, 0], ["L", w, h - s], ["L", w - s, h], ["L", 0, h], ["Z"]],
    lines: (w, h, s) => [["M", 0, s], ["L", w - s, s], ["L", w, 0], ["M", w - s, s], ["L", w - s, h]],
  },
  note: {
    size: { key: "size", fallback: 30, unit: "px" },
    outline: (w, h, s) => [["M", 0, 0], ["L", w - s, 0], ["L", w, s], ["L", w, h], ["L", 0, h], ["Z"]],
    lines: (w, _h, s) => [["M", w - s, 0], ["L", w - s, s], ["L", w, s]],
  },
  card: {
    size: { key: "size", fallback: 30, unit: "px" },
    outline: (w, h, s) => [["M", s, 0], ["L", w, 0], ["L", w, h], ["L", 0, h], ["L", 0, s], ["Z"]],
  },
  folder: {
    size: { key: "tabWidth", fallback: 60, unit: "px" },
    outline: (w, h, s) => {
      const tab = Math.min(s, w * 0.6);
      const th = Math.min(20, h / 3);
      return [["M", 0, 0], ["L", tab, 0], ["L", tab + th / 2, th], ["L", w, th], ["L", w, h], ["L", 0, h], ["Z"]];
    },
    lines: (w, h, s) => {
      const tab = Math.min(s, w * 0.6);
      const th = Math.min(20, h / 3);
      return [["M", 0, th], ["L", tab + th / 2, th]];
    },
  },
  cylinder3: {
    size: { key: "size", fallback: 15, unit: "px" },
    outline: (w, h, s) => {
      const ry = Math.min(s, h / 2);
      const rx = w / 2;
      return [
        ...ellipseTop(w, ry),
        ["L", w, h - ry],
        ["C", w, h - ry + ry * K, rx + rx * K, h, rx, h],
        ["C", rx - rx * K, h, 0, h - ry + ry * K, 0, h - ry],
        ["Z"],
      ];
    },
    lines: (w, _h, s) => {
      const ry = Math.min(s, _h / 2);
      const rx = w / 2;
      return [["M", 0, ry], ["C", 0, ry + ry * K, rx - rx * K, ry * 2, rx, ry * 2], ["C", rx + rx * K, ry * 2, w, ry + ry * K, w, ry]];
    },
  },
};

type NamedStyle = Readonly<Record<string, string | number>>;

/** draw.io's named styles (its stylesheet's `<add as="ellipse">` entries):
 *  what a bare token like "ellipse;" stands for. */
export const NAMED_STYLES: Readonly<Record<string, NamedStyle>> = {
  ellipse: { shape: "ellipse", perimeter: "ellipsePerimeter" },
  doubleEllipse: { shape: "doubleEllipse", perimeter: "ellipsePerimeter" },
  rhombus: { shape: "rhombus", perimeter: "rhombusPerimeter" },
  triangle: { shape: "triangle", perimeter: "trianglePerimeter" },
  hexagon: { shape: "hexagon", perimeter: "hexagonPerimeter" },
  cloud: { shape: "cloud", perimeter: "ellipsePerimeter" },
  cylinder: { shape: "cylinder" },
  actor: { shape: "actor" },
  label: { shape: "label" },
  swimlane: { shape: "swimlane", fontStyle: 1, align: "center", verticalAlign: "top", startSize: 23 },
  line: { shape: "line", strokeWidth: 4, labelBackgroundColor: "#ffffff", verticalAlign: "top", spacingTop: 8 },
  image: { shape: "image", verticalLabelPosition: "bottom", verticalAlign: "top" },
  text: { fillColor: "none", strokeColor: "none", align: "left", verticalAlign: "top", spacingLeft: 4, spacingRight: 4, whiteSpace: "wrap", overflow: "hidden" },
  group: { verticalAlign: "top", fillColor: "none", strokeColor: "none" },
};

/** draw.io's defaults for a vertex and an edge that name no style. */
export const DEFAULT_VERTEX: NamedStyle = {
  align: "center",
  verticalAlign: "middle",
  fontSize: 12,
  fontFamily: "Helvetica, Arial, sans-serif",
  fontColor: "#000000",
  strokeColor: "#000000",
  fillColor: "#ffffff",
};
export const DEFAULT_EDGE: NamedStyle = {
  align: "center",
  verticalAlign: "middle",
  fontSize: 11,
  fontFamily: "Helvetica, Arial, sans-serif",
  fontColor: "#000000",
  strokeColor: "#000000",
  endArrow: "classic",
  labelBackgroundColor: "#ffffff",
};

/** The perimeters maxGraph knows; draw.io's other names fall back to the
 *  rectangle (an arrow then ends at the box, not the exact outline). */
export const PERIMETERS: Readonly<Record<string, string>> = {
  ellipsePerimeter: "ellipsePerimeter",
  rhombusPerimeter: "rhombusPerimeter",
  trianglePerimeter: "trianglePerimeter",
  rectanglePerimeter: "rectanglePerimeter",
  hexagonPerimeter: "hexagonPerimeter",
  hexagonPerimeter2: "hexagonPerimeter",
};

/** The color-valued style keys ("default" and "light-dark()" mean something
 *  to draw.io's themes). */
export const COLOR_KEYS: ReadonlySet<string> = new Set([
  "fillColor",
  "strokeColor",
  "fontColor",
  "gradientColor",
  "labelBackgroundColor",
  "labelBorderColor",
  "swimlaneFillColor",
]);
