import { MAX_LABELS, MAX_SVG_CHARS, parseTransform, svgLabelsIn } from "./svgLabels.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const svg = (attrs: string, body: string) => `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" ${attrs}>${body}</svg>`;
const at = (x: number, y: number, w: number, h: number) => ({ x, y, w, h });
const natural = { w: 400, h: 200 };

// Transforms compose left to right.
eq(parseTransform("translate(10 20)"), [1, 0, 0, 1, 10, 20], "translate");
eq(parseTransform("translate(10,20) scale(2)"), [2, 0, 0, 2, 10, 20], "translate then scale");
eq(parseTransform("scale(2) translate(10 20)"), [2, 0, 0, 2, 20, 40], "scale then translate");
eq(parseTransform("matrix(1 0 0 1 5 6)"), [1, 0, 0, 1, 5, 6], "matrix");
eq(parseTransform("nonsense(1)"), [1, 0, 0, 1, 0, 0], "unknown is identity");
eq(parseTransform(undefined), [1, 0, 0, 1, 0, 0], "none");

// Labels inside the area, by the middle of their text; viewBox = image pixels.
const flow = svg(
  'width="400" height="200" viewBox="0 0 400 200"',
  `<rect x="20" y="20" width="120" height="60"/><text x="80" y="55" text-anchor="middle">Start</text>` +
    `<rect x="220" y="20" width="140" height="60"/><text x="290" y="55" text-anchor="middle">P2P check passed?</text>` +
    `<text x="80" y="155" text-anchor="middle" font-size="12">Done &amp; dusted</text>`,
);
eq(svgLabelsIn(flow, at(10, 10, 140, 80), natural), ["Start"], "one box");
eq(svgLabelsIn(flow, at(0, 0, 400, 100), natural), ["Start", "P2P check passed?"], "two boxes, reading order");
eq(svgLabelsIn(flow, at(0, 0, 400, 200), natural), ["Start", "P2P check passed?", "Done & dusted"], "everything, entities decoded");
eq(svgLabelsIn(flow, at(150, 100, 100, 50), natural), [], "empty space names nothing");

// The viewBox scales and letterboxes: units are not pixels.
const scaled = svg('width="400" height="200" viewBox="0 0 200 100"', '<text x="100" y="50" text-anchor="middle">Mid</text>');
eq(svgLabelsIn(scaled, at(150, 80, 100, 40), natural), ["Mid"], "2x viewBox: unit (100,50) is pixel (200,100)");
eq(svgLabelsIn(scaled, at(0, 0, 100, 40), natural), [], "not in the top-left");
const boxed = svg('viewBox="0 0 100 100"', '<text x="50" y="50" text-anchor="middle">Mid</text>');
eq(svgLabelsIn(boxed, at(90, 40, 20, 20), { w: 200, h: 100 }), ["Mid"], "meet: a square in a wide box is centred (offset 50)");
eq(svgLabelsIn(boxed, at(10, 40, 20, 20), { w: 200, h: 100 }), [], "the letterbox is empty");
eq(svgLabelsIn(svg('viewBox="0 0 100 100" preserveAspectRatio="xMinYMin meet"', '<text x="50" y="50" text-anchor="middle">Mid</text>'), at(40, 40, 20, 20), { w: 200, h: 100 }), ["Mid"], "xMinYMin: against the left");
eq(svgLabelsIn(svg('viewBox="0 0 100 100" preserveAspectRatio="none"', '<text x="50" y="50" text-anchor="middle">Mid</text>'), at(90, 40, 20, 20), { w: 200, h: 100 }), ["Mid"], "none: stretched");
eq(svgLabelsIn(svg('width="400" height="200"', '<text x="80" y="55" text-anchor="middle">No viewBox</text>'), at(60, 30, 40, 30), natural), ["No viewBox"], "no viewBox: units are pixels");

// Groups move and scale their text; hidden and definition text is not drawn.
const grouped = svg(
  'viewBox="0 0 400 200"',
  `<g transform="translate(100 0)"><text x="20" y="50">Moved</text></g>` +
    `<g display="none"><g><text x="20" y="150">Hidden</text></g></g><text x="20" y="180" text-anchor="start">After hidden</text>` +
    `<defs><text x="20" y="100">Defined</text></defs><g style="visibility:hidden"><text x="300" y="150">Invisible</text></g>`,
);
eq(svgLabelsIn(grouped, at(0, 0, 400, 200), natural), ["Moved", "After hidden"], "group transform, hidden, defs (and the hidden group ends)");
eq(svgLabelsIn(grouped, at(115, 30, 40, 30), natural), ["Moved"], "the moved label sits at x=120+");
eq(svgLabelsIn(svg('viewBox="0 0 400 200"', '<g transform="scale(2)"><text x="50" y="50" text-anchor="middle">Big</text></g>'), at(80, 80, 40, 30), natural), ["Big"], "scaled group");

// Text built of tspans reads as one label; a tspan can carry the position.
eq(svgLabelsIn(svg('viewBox="0 0 400 200"', '<text x="200" y="100" text-anchor="middle"><tspan>Line one</tspan> <tspan dy="14">and two</tspan></text>'), at(150, 80, 100, 40), natural), ["Line one and two"], "tspans joined");
eq(svgLabelsIn(svg('viewBox="0 0 400 200"', '<text text-anchor="middle"><tspan x="200" y="100">At tspan</tspan></text>'), at(150, 80, 100, 40), natural), ["At tspan"], "position from the first tspan");
eq(svgLabelsIn(svg('viewBox="0 0 400 200"', '<text x="0" y="0">Origin</text>'), at(0, 0, 60, 10), natural), [], "x=0 is a position: the middle of the text is to its right");

// Each label once, top to bottom then left to right; bounded.
eq(svgLabelsIn(svg('viewBox="0 0 400 200"', '<text x="300" y="50">B</text><text x="10" y="150">C</text><text x="10" y="50">A</text><text x="100" y="50">A</text>'), at(0, 0, 400, 200), natural), ["A", "B", "C"], "reading order, no repeats");
const many = svg('viewBox="0 0 400 200"', Array.from({ length: MAX_LABELS + 10 }, (_, i) => `<text x="10" y="${10 + i}">L${i}</text>`).join(""));
eq(svgLabelsIn(many, at(0, 0, 400, 200), natural).length, MAX_LABELS, "at most MAX_LABELS");
const wide = svg('viewBox="0 0 400 200"', Array.from({ length: 4 }, (_, i) => `<text x="200" y="${20 + i * 20}" text-anchor="middle">${String(i).repeat(400)}</text>`).join(""));
eq(svgLabelsIn(wide, at(0, 0, 400, 200), natural).length, 3, "bounded by characters too: 1,200 fit, a fourth 400 does not");

// Not an SVG, broken, or too big: nothing, never a throw.
eq(svgLabelsIn("not xml", at(0, 0, 10, 10), natural), [], "not xml");
eq(svgLabelsIn(svg("", "<text x='1' y='1'>unclosed"), at(0, 0, 400, 200), natural), [], "unclosed text is no label");
eq(svgLabelsIn(`<svg>${" ".repeat(MAX_SVG_CHARS)}</svg>`, at(0, 0, 10, 10), natural), [], "too big");
eq(svgLabelsIn(flow, at(0, 0, 10, 10), { w: 0, h: 0 }), [], "no image size");
