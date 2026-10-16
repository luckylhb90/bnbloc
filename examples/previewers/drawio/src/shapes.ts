// Teaches maxGraph what draw.io's stylesheet and shape library supply: the
// custom shapes and named styles of shapeTable.ts, and draw.io's defaults for
// a box and an arrow. A shape the table does not know is drawn by maxGraph as
// a plain box with its label (never blank, never an error).

import { Graph, Shape, ShapeRegistry, type AbstractCanvas2D } from "@maxgraph/core";

import { CUSTOM_SHAPES, DEFAULT_EDGE, DEFAULT_VERTEX, NAMED_STYLES, shapeSize, type Seg, type ShapeRow } from "./shapeTable.ts";

function trace(c: AbstractCanvas2D, segs: readonly Seg[]): void {
  for (const s of segs) {
    switch (s[0]) {
      case "M":
        c.moveTo(s[1], s[2]);
        break;
      case "L":
        c.lineTo(s[1], s[2]);
        break;
      case "C":
        c.curveTo(s[1], s[2], s[3], s[4], s[5], s[6]);
        break;
      case "Z":
        c.close();
        break;
    }
  }
}

function rowShape(row: ShapeRow): new () => Shape {
  return class extends Shape {
    override paintVertexShape(c: AbstractCanvas2D, x: number, y: number, w: number, h: number): void {
      const s = shapeSize(row, this.style as Record<string, unknown> | null, w);
      c.translate(x, y);
      c.begin();
      trace(c, row.outline(w, h, s));
      c.fillAndStroke();
      if (row.lines) {
        c.setFillColor("none");
        c.begin();
        trace(c, row.lines(w, h, s));
        c.stroke();
      }
    }
  };
}

let registered = false;

/** Registers the custom shapes once per page load. */
function registerShapes(): void {
  if (registered) return;
  registered = true;
  for (const [name, row] of Object.entries(CUSTOM_SHAPES)) ShapeRegistry.add(name, rowShape(row));
}

/** A graph that draws like draw.io: its defaults, its named styles, its
 *  extra shapes. View only (no plugins: nothing to edit, no handlers). */
export function createGraph(container: HTMLElement): Graph {
  registerShapes();
  const graph = new Graph(container, undefined, []);
  const sheet = graph.getStylesheet();
  Object.assign(sheet.getDefaultVertexStyle(), DEFAULT_VERTEX);
  Object.assign(sheet.getDefaultEdgeStyle(), DEFAULT_EDGE);
  for (const [name, style] of Object.entries(NAMED_STYLES)) sheet.putCellStyle(name, style);
  graph.setEnabled(false);
  // A label is HTML only where the style says html=1: model.ts's graphXml
  // sanitized exactly those; every other label is text.
  graph.isHtmlLabel = (cell) => (graph.getCurrentCellStyle(cell) as Record<string, unknown>).html === 1;
  // The fold icons are images the frame cannot load, and there is nothing to fold in a viewer.
  graph.getFoldingImage = () => null;
  return graph;
}
