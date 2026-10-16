// node --test examples/previewers/drawio/build.test.mjs
// (after `pnpm --dir examples/previewers/drawio install --frozen-lockfile`)
// Builds the draw.io previewer into a temp dir and checks the bundle the way
// the gateway and desktop will read it (../lib/bundle-check.mjs), then the
// pure modules: the file reader (src/model.ts), styles (src/style.ts), units
// and pointing (src/units.ts) and the shape table (src/shapeTable.ts).

import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { deflateRawSync } from "node:zlib";
import { fileURLToPath } from "node:url";

import { build } from "./build.mjs";
import { assertBundle } from "../lib/bundle-check.mjs";
import { coverLayout, fitCover, leaveBox } from "./src/cover.ts";
import { MAX_CELLS, MAX_INFLATED_BYTES, MAX_PAGES, graphXml, labelText, readDrawing } from "./src/model.ts";
import { CUSTOM_SHAPES, NAMED_STYLES, shapeSize } from "./src/shapeTable.ts";
import { isContainer, normalizeStyle, parseStyle, shapeOf } from "./src/style.ts";
import { cellPoint, drawingUnits, findCell } from "./src/units.ts";

const HERE = dirname(fileURLToPath(import.meta.url));
const sample = (name) => readFile(join(HERE, "samples", name), "utf8");

const identity = (html) => html;
const model = (cells) => `<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>${cells}</root></mxGraphModel>`;
const box = (id, label, extra = "") => `<mxCell id="${id}" value="${label}" style="rounded=1;html=1;" vertex="1" parent="1"${extra}><mxGeometry x="10" y="10" width="80" height="40" as="geometry"/></mxCell>`;
const file = (pages) => `<mxfile>${pages.map(([id, name, body]) => `<diagram id="${id}" name="${name}">${body}</diagram>`).join("")}</mxfile>`;

test("the build produces a deterministic, parseable bundle with its notices", async () => {
  const outA = await mkdtemp(join(tmpdir(), "drawio-a-"));
  const outB = await mkdtemp(join(tmpdir(), "drawio-b-"));
  try {
    const { files, pkg } = await assertBundle(await build({ outDir: outA, quiet: true }), await build({ outDir: outB, quiet: true }), {
      files: ["NOTICES.txt", "index.html", "main.js", "style.css"],
      toolbar: ["zoom", "fit"],
    });
    assert.deepEqual(pkg.previewer.match.extensions, [".drawio", ".dio"]);
    assert.deepEqual(pkg.previewer.capabilities, ["thumbnail", "toolbar", "selection", "units"]);
    const notices = files.get("NOTICES.txt").toString("utf8");
    for (const name of ["@maxgraph/core", "dompurify"]) assert.match(notices, new RegExp(`^${name}@`, "m"), `${name} attributed`);
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});

test("a file is read into pages of cells; a wrapped cell takes its id and label from its wrapper", async () => {
  const d = await readDrawing(await sample("flow.drawio"));
  assert.deepEqual(d.pages.map((p) => [p.id, p.name, p.root, p.cells.length, p.omitted]), [["orders", "Order flow", "0", 17, 0], ["shapes", "Shapes", "0", 15, 0]]);
  assert.equal(d.omittedPages, 0);
  const cells = new Map(d.pages[0].cells.map((c) => [c.id, c]));
  assert.deepEqual(cells.get("1")?.kind, "layer");
  assert.deepEqual([cells.get("start")?.kind, cells.get("e-yes")?.kind], ["vertex", "edge"]);
  assert.equal(cells.get("archive")?.label, "Archive", "<object label> is the label");
  assert.equal(cells.get("archive")?.kind, "vertex");
  assert.equal(cells.get("pick")?.parent, "lane", "a child names its container");
  assert.deepEqual([cells.get("e-yes")?.source, cells.get("e-yes")?.target], ["check", "fulfil"]);
  assert.deepEqual(cells.get("e-ship")?.geometry?.points, [{ x: 110, y: 260 }, { x: 560, y: 260 }, { x: 560, y: 225 }], "an arrow keeps its bends");
  assert.deepEqual(cells.get("start")?.geometry, { x: 200, y: 30, width: 140, height: 60, relative: false, points: [] });
  assert.equal(cells.get("notify")?.html, true);
  assert.equal(cells.get("lane")?.style.startsWith("swimlane"), true);
});

test("a compressed diagram reads as the plain one; a bare model is one page", async () => {
  const plain = await readDrawing(await sample("flow.drawio"));
  const packed = await readDrawing(await sample("compressed.drawio"));
  assert.equal(packed.pages.length, 1);
  assert.equal(packed.pages[0].name, "Order flow (compressed)");
  assert.deepEqual(packed.pages[0].cells, plain.pages[0].cells);
  const bare = await readDrawing(model(box("a", "Hi")));
  assert.deepEqual(bare.pages.map((p) => [p.id, p.name, p.cells.length]), [["page-1", "Page-1", 2]]);
});

test("what cannot be read says why", async () => {
  await assert.rejects(readDrawing("hello"), /not a draw\.io file/);
  await assert.rejects(readDrawing('<mxfile></mxfile>'), /no diagram/);
  await assert.rejects(readDrawing(await sample("broken.drawio")), /Page “Broken”: the diagram's compressed data cannot be read/);
  await assert.rejects(readDrawing(file([["a", "A", "<root/>"]])), /Page “A” is not a draw\.io diagram/);
  // A deflate bomb: 9 MiB of zeros packs to a few KB and is refused unpacked.
  const bomb = deflateRawSync(Buffer.alloc(MAX_INFLATED_BYTES + 1, 0x41)).toString("base64");
  assert.ok(bomb.length < 100_000);
  await assert.rejects(readDrawing(file([["a", "Bomb", bomb]])), /larger than 8 MiB once unpacked/);
});

test("the bounds: first 5,000 cells, first 50 pages, no dangling parents or duplicate ids", async () => {
  const many = Array.from({ length: MAX_CELLS + 2 }, (_, i) => box(`c${i}`, `n${i}`)).join("");
  const d = await readDrawing(model(many));
  assert.equal(d.pages[0].cells.length, MAX_CELLS, "capped");
  assert.equal(d.pages[0].omitted, 3, "the layer is a cell too: 5,003 found, 5,000 kept");
  const pages = Array.from({ length: MAX_PAGES + 1 }, (_, i) => [`p${i}`, `P${i}`, model("")]);
  const p = await readDrawing(file(pages));
  assert.equal(p.pages.length, MAX_PAGES);
  assert.equal(p.omittedPages, 1);
  const odd = await readDrawing(model(`${box("x", "one")}${box("x", "two")}<mxCell id="o" value="orphan" vertex="1" parent="gone"><mxGeometry as="geometry"/></mxCell>`));
  assert.deepEqual(odd.pages[0].cells.map((c) => c.id), ["1", "x"], "first id wins, the orphan is dropped");
  assert.equal(odd.pages[0].cells.find((c) => c.id === "x")?.label, "one");
  const dupPages = await readDrawing(file([["same", "A", model("")], ["same", "B", model("")]]));
  assert.notEqual(dupPages.pages[0].id, dupPages.pages[1].id);
});

test("labels: HTML becomes lines of text, entities decoded", () => {
  assert.equal(labelText({ html: true, label: "<b>Notify</b> requester<br>by <i>email</i>" }), "Notify requester\nby email");
  assert.equal(labelText({ html: true, label: "<div>one</div><div>two&nbsp;&amp;&nbsp;three</div>" }), "one\ntwo & three");
  assert.equal(labelText({ html: false, label: "  a <b>  b  \n\n c " }), "a <b> b\nc", "plain text keeps its characters");
  assert.equal(labelText({ html: true, label: "" }), "");
});

test("the model maxGraph draws: HTML labels cleaned, text escaped, ends and controls handled", async () => {
  const d = await readDrawing(
    model(
      `${box("h", "&lt;b&gt;x&lt;/b&gt;&lt;script&gt;1&lt;/script&gt;")}` +
        `<mxCell id="t" value="&lt;img onerror=1&gt; &amp; &quot;q&quot;&#xa;two&#x1;" style="rounded=1;" vertex="1" parent="1"><mxGeometry as="geometry"/></mxCell>` +
        `<mxCell id="e" value="" style="endArrow=classic" edge="1" parent="1" source="h" target="missing"><mxGeometry relative="1" as="geometry"/></mxCell>`,
    ),
  );
  const seen = [];
  const xml = graphXml(d.pages[0], (html) => (seen.push(html), "[clean]"));
  assert.deepEqual(seen, ["<b>x</b><script>1</script>"], "only an html=1 label goes through the sanitizer");
  assert.match(xml, /id="h"[^>]*value="\[clean\]"/);
  assert.match(xml, /id="t"[^>]*value="&lt;img onerror=1&gt; &amp; &quot;q&quot;&#xa;two"/, "text is escaped, control characters dropped");
  assert.match(xml, /id="e"[^>]*source="h"(?![^>]*target=)/, "a terminal that is not drawn is dropped");
  assert.doesNotMatch(xml, /<(?:script|img)/, "no markup from a label reaches the model as markup");
  // Everything it writes reads back as the same cells.
  const back = await readDrawing(graphXml(d.pages[0], identity));
  assert.deepEqual(back.pages[0].cells.map((c) => [c.id, c.kind, c.parent]), d.pages[0].cells.map((c) => [c.id, c.kind, c.parent]));
});

test("styles: themed colors resolved, default colors dropped, perimeters and images made readable", () => {
  assert.equal(
    normalizeStyle("rounded=1;fillColor=light-dark(#dae8fc,#1d2d44);strokeColor=default;fontColor=#112233;perimeter=hexagonPerimeter2"),
    "rounded=1;fillColor=#dae8fc;fontColor=#112233;perimeter=hexagonPerimeter",
  );
  assert.equal(normalizeStyle("ellipse;whiteSpace=wrap;perimeter=calloutPerimeter"), "ellipse;whiteSpace=wrap;perimeter=rectanglePerimeter");
  assert.equal(normalizeStyle("shape=image;image=data:image/png,iVBORw0KGgo=="), "shape=image;image=data:image/png;base64,iVBORw0KGgo");
  assert.deepEqual([...parseStyle("ellipse;a=1;;b=x=y").props], [["a", "1"], ["b", "x=y"]]);
  assert.deepEqual(parseStyle("ellipse;text;a=1").names, ["ellipse", "text"]);
  assert.deepEqual(["ellipse;html=1", "shape=cylinder3", "rounded=1", "", "shape=mxgraph.flowchart.decision", "swimlane;x=1"].map(shapeOf), ["ellipse", "cylinder3", "rounded rectangle", "rectangle", "flowchart.decision", "swimlane"]);
  assert.deepEqual(["swimlane;a=1", "group", "container=1;rounded=1", "rounded=1"].map(isContainer), [true, true, true, false]);
});

test("the shape table: every shape draws a closed outline inside its box", () => {
  for (const [name, row] of Object.entries(CUSTOM_SHAPES)) {
    const w = 120;
    const h = 80;
    const s = shapeSize(row, null, w);
    for (const segs of [row.outline(w, h, s), ...(row.lines ? [row.lines(w, h, s)] : [])]) {
      assert.equal(segs[0][0], "M", `${name} starts with a move`);
      for (const seg of segs) for (const n of seg.slice(1)) assert.ok(Number.isFinite(n) && n >= -0.001 && n <= Math.max(w, h) + 0.001, `${name}: ${seg} inside the box`);
    }
    assert.equal(row.outline(w, h, s).at(-1)[0], "Z", `${name} closes`);
  }
  for (const [name, style] of Object.entries(NAMED_STYLES)) assert.ok(Object.keys(style).length > 0, name);
});

test("a shape's size: the cell's own value, else the default; a fraction of the width unless fixed", () => {
  const p = CUSTOM_SHAPES.parallelogram;
  assert.equal(shapeSize(p, null, 200), 50, "default quarter of the width");
  assert.equal(shapeSize(p, { size: 0.5 }, 200), 100);
  assert.equal(shapeSize(p, { fixedSize: 1 }, 200), 20, "draw.io's fixed default");
  assert.equal(shapeSize(p, { fixedSize: 1, size: 35 }, 200), 35);
  assert.equal(shapeSize(p, { fixedSize: 1, size: 500 }, 200), 200, "never past the box");
  assert.equal(shapeSize(CUSTOM_SHAPES.cube, null, 200), 20, "pixels always");
  assert.equal(shapeSize(CUSTOM_SHAPES.cube, { size: 60 }, 40), 40);
  assert.equal(shapeSize(CUSTOM_SHAPES.cylinder3, { size: "x" }, 100), 15, "not a number: the default");
});

test("units: one per box and arrow, keyed by page and cell; a move is no change, a relabel is", async () => {
  const d = await readDrawing(await sample("flow.drawio"));
  const units = drawingUnits(d);
  assert.equal(units.length, 17 - 1 + 15 - 1, "layers are not units");
  const check = units.find((u) => u.key === "orders/check");
  assert.deepEqual(check, { key: "orders/check", label: "Order flow: Box “Approved?”", text: "rhombus: Approved?", at: "cell:orders/check" });
  assert.deepEqual(units.find((u) => u.key === "orders/e-yes")?.text, "“Approved?” → “Fulfil order”: yes");
  assert.equal(units.find((u) => u.key === "orders/e-start")?.label, "Order flow: Arrow “Request received” → “Approved?”");
  assert.equal(units.find((u) => u.key === "orders/notify")?.text, "rectangle: Notify requester\nby email");
  const moved = await readDrawing((await sample("flow.drawio")).replace('x="200" y="140" width="140" height="90"', 'x="260" y="150" width="140" height="90"'));
  const relabelled = await readDrawing((await sample("flow.drawio")).replace('value="Approved?"', 'value="Signed off?"'));
  assert.deepEqual(drawingUnits(moved), units, "geometry is not part of a unit");
  const changed = drawingUnits(relabelled).filter((u, i) => JSON.stringify(u) !== JSON.stringify(units[i]));
  assert.deepEqual(changed.map((u) => u.key).sort(), ["orders/check", "orders/e-no", "orders/e-start", "orders/e-yes"], "the box and the arrows that name it");
  assert.equal(drawingUnits(await readDrawing(model(box("a", "Only")))).at(0)?.label, "Box “Only”", "a one-page file has no page prefix");
});

test("a unit's `at` finds its cell, even when an id has a slash", async () => {
  const d = await readDrawing(file([["a", "A", model(box("x/y", "slashed"))], ["a/b", "B", model(box("z", "deep"))]]));
  assert.equal(findCell(d, "cell:a/x/y")?.cell.label, "slashed");
  assert.equal(findCell(d, "cell:a/b/z")?.cell.label, "deep", "the longest page id wins");
  assert.equal(findCell(d, "cell:a/b/z")?.page, 1);
  assert.equal(findCell(d, "cell:nope/z"), null);
  assert.equal(findCell(d, "slide:1"), null);
  assert.equal(findCell(d, "cell:a/missing"), null);
});

test("pointing names the part: chip, detail for the agent, unit keys", async () => {
  const d = await readDrawing(await sample("flow.drawio"));
  const cell = (id) => d.pages[0].cells.find((c) => c.id === id);
  assert.deepEqual(cellPoint(d, 0, cell("check")), {
    label: "Box: “Approved?”",
    detail: "Approved?\nCell id: check\nPage: “Order flow”\nShape: rhombus\nArrows to: “Fulfil order”, “Notify requester by email”\nArrows from: “Request received”",
    keys: ["orders/check"],
  });
  assert.deepEqual(cellPoint(d, 0, cell("e-yes")), {
    label: "Arrow: “yes”",
    detail: "yes\nCell id: e-yes\nPage: “Order flow”\nFrom: “Approved?”\nTo: “Fulfil order”",
    keys: ["orders/e-yes"],
  });
  assert.equal(cellPoint(d, 0, cell("e-start")).label, "Arrow: “Request received” → “Approved?”", "an arrow without text is named by its ends");
  assert.equal(cellPoint(d, 0, cell("lane")).label, "Group: “Warehouse”");
  const one = await readDrawing(model(box("a", "") + box("b", "Long ".repeat(30))));
  assert.equal(cellPoint(one, 0, one.pages[0].cells.find((c) => c.id === "a")).label, "Box (a)", "no text: the id");
  assert.ok([...cellPoint(one, 0, one.pages[0].cells.find((c) => c.id === "b")).label].length <= 60, "a long label is cut");
  assert.doesNotMatch(cellPoint(one, 0, one.pages[0].cells[1]).detail, /Page:/, "a one-page file names no page");
});

test("the card cover: boxes in absolute places, their shapes and colors, arrows through their bends", async () => {
  const d = await readDrawing(await sample("flow.drawio"));
  const layout = coverLayout(d.pages[0]);
  assert.equal(layout.boxes.length, 9, "every box with a geometry: seven on the page and two in the lane");
  assert.equal(layout.edges.length, 7);
  const byLabel = (label) => layout.boxes.find((b) => b.label === label);
  assert.deepEqual([byLabel("Request received").shape, byLabel("Approved?").shape, byLabel("Fulfil order").shape], ["ellipse", "rhombus", "rect"]);
  assert.equal(byLabel("Request received").fill, "#d5e8d4");
  assert.equal(byLabel("Fulfil order").rounded, true);
  // "Pick items" sits at (30, 60) inside the lane at (580, 140).
  assert.deepEqual([byLabel("Pick items").x, byLabel("Pick items").y], [610, 200]);
  assert.equal(byLabel("Notify requester").label, "Notify requester", "a label is its first line");
  const ship = layout.edges.find((e) => e.points.length === 5);
  assert.deepEqual(ship.points.slice(1, 4), [{ x: 110, y: 260 }, { x: 560, y: 260 }, { x: 560, y: 225 }], "an arrow keeps its bends between the two centres");
  assert.deepEqual(ship.points[0], { x: 110, y: 290 }, "from the edge of its source, the side facing its first bend (the box is at y 290..350, the bend above it)");
  assert.equal(byLabel("Warehouse").container, true, "a lane is a container");
  for (const b of layout.boxes) {
    assert.ok(b.x >= layout.bounds.x && b.x + b.w <= layout.bounds.x + layout.bounds.w + 0.001 && b.y >= layout.bounds.y && b.y + b.h <= layout.bounds.y + layout.bounds.h + 0.001, "bounds hold every box");
  }
  assert.equal(coverLayout((await readDrawing(model(""))).pages[0]), null, "an empty page has no cover");
  assert.equal(coverLayout((await readDrawing(model(box("n", "none").replace("<mxGeometry", "<mxGeometry relative=\"1\"")))).pages[0]), null, "a relative geometry is not a box");
});

test("an arrow ends at the edge of its box", () => {
  const box = { x: 100, y: 100, w: 200, h: 100 };
  assert.deepEqual(leaveBox(box, { x: 200, y: 400 }), { x: 200, y: 200 }, "straight down leaves by the bottom");
  assert.deepEqual(leaveBox(box, { x: 600, y: 150 }), { x: 300, y: 150 }, "to the right leaves by the right side");
  assert.deepEqual(leaveBox(box, { x: 250, y: 150 }), { x: 250, y: 150 }, "a point inside the box is where it stays");
  assert.deepEqual(leaveBox(box, { x: 200, y: 150 }), { x: 200, y: 150 }, "the centre itself");
  const diagonal = leaveBox(box, { x: 500, y: 350 });
  assert.ok(Math.abs(diagonal.x - 300) < 1e-9 || Math.abs(diagonal.y - 200) < 1e-9, "a diagonal leaves by a side");
});

test("the cover is fitted and centred, never blown up", () => {
  const wide = fitCover({ x: 100, y: 50, w: 800, h: 200 }, 480, 300);
  assert.equal(wide.k, (480 - 28) / 800);
  assert.ok(Math.abs(100 * wide.k + wide.ox - (480 - 800 * wide.k) / 2) < 1e-9, "centred across");
  assert.equal(fitCover({ x: 0, y: 0, w: 10, h: 10 }, 480, 300).k, 1.5, "a tiny diagram stops at 1.5");
  assert.ok(fitCover({ x: 0, y: 0, w: 0, h: 0 }, 480, 300).k <= 1.5, "an empty extent does not divide by zero");
});
