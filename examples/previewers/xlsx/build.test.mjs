// node --test examples/previewers/xlsx/build.test.mjs
// (after `pnpm --dir examples/previewers/xlsx install --frozen-lockfile`)
// Builds the Excel previewer into a temp dir and checks the bundle the way
// the gateway and desktop will read it (../lib/bundle-check.mjs), then the
// sheet model on the sample workbook read by ExcelJS (its Node build: the
// same parser as the bundled browser build).

import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import ExcelJS from "exceljs";

import { build } from "./build.mjs";
import { assertBundle } from "../lib/bundle-check.mjs";
import {
  MAX_CELLS,
  cellCss,
  colName,
  colWidthPx,
  coverRange,
  cssColor,
  displayText,
  drawnExtent,
  isNumeric,
  parseRange,
  rangeLabel,
  rangeText,
  rowHeightPx,
} from "./src/sheet.ts";

const HERE = dirname(fileURLToPath(import.meta.url));

test("the build produces a deterministic, parseable bundle with its notices", async () => {
  const outA = await mkdtemp(join(tmpdir(), "xlsx-a-"));
  const outB = await mkdtemp(join(tmpdir(), "xlsx-b-"));
  try {
    const { files, pkg } = await assertBundle(await build({ outDir: outA, quiet: true }), await build({ outDir: outB, quiet: true }), {
      files: ["NOTICES.txt", "index.html", "main.js", "style.css"],
      toolbar: ["zoom"],
    });
    assert.deepEqual(pkg.previewer.match.extensions, [".xlsx", ".xlsm", ".xltx"], "OOXML workbooks only, never legacy .xls");
    const notices = files.get("NOTICES.txt").toString("utf8");
    for (const name of ["exceljs", "numfmt", "jszip", "saxes", "dayjs"]) assert.match(notices, new RegExp(`^${name}@`, "m"), `${name} attributed`);
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});

test("the sample workbook reads as drawn: formats, merges, hidden parts", async () => {
  const wb = new ExcelJS.Workbook();
  await wb.xlsx.load(await readFile(join(HERE, "samples", "book.xlsx")));
  const ws = wb.getWorksheet("Revenue");
  const text = (ref) => displayText(ws.getCell(ref).value, ws.getCell(ref).style?.numFmt);
  assert.equal(text("A1"), "Quarterly revenue by region");
  assert.equal(text("B3"), "$120,000");
  assert.equal(text("D4"), "36.8%");
  assert.equal(text("E5"), "2026-09-30");
  assert.equal(displayText(wb.getWorksheet("Notes").getCell("A2").value, "#,##0.00"), "1,234,567.89");
  assert.deepEqual(ws.model.merges, ["A1:E1"]);
  assert.ok(ws.getColumn(6).hidden, "column F hidden");
  assert.equal(wb.getWorksheet("Scratch").state, "hidden");
  assert.match(cellCss(ws.getCell("A1").style, false), /font-weight:700.*color:#ffffff.*background:#0a74d9.*text-align:center/);
});

test("values: formula results, rich text, errors, booleans, General", () => {
  assert.equal(displayText({ formula: "SUM(A1:A2)", result: 3 }, "0.00"), "3.00");
  assert.equal(displayText({ formula: "NOW()" }, undefined), "", "a formula with no cached result shows nothing");
  assert.equal(displayText({ richText: [{ text: "a" }, { text: "b" }] }, undefined), "ab");
  assert.equal(displayText({ error: "#N/A" }, undefined), "#N/A");
  assert.equal(displayText({ text: "site", hyperlink: "https://example.com" }, undefined), "site");
  assert.equal(displayText(true, undefined), "TRUE");
  assert.equal(displayText(0.1 + 0.2, undefined), "0.3");
  assert.equal(displayText(new Date(Date.UTC(2026, 9, 14, 13, 30)), "yyyy-mm-dd hh:mm"), "2026-10-14 13:30", "dates are UTC instants");
  assert.equal(displayText(null, "0"), "");
  assert.equal(displayText(5, "[invalid"), "######", "a bad format never throws");
  assert.ok(isNumeric(5) && isNumeric(new Date()) && isNumeric({ result: 1 }) && !isNumeric("5"));
});

test("colours, sizes, names and ranges", () => {
  assert.equal(cssColor({ argb: "FF0A74D9" }), "#0a74d9");
  assert.equal(cssColor({ theme: 4 }), "#4472c4");
  assert.equal(cssColor({ theme: 0, tint: -0.5 }), "#808080", "darker white");
  assert.equal(cssColor({ theme: 1, tint: 0.5 }), "#808080", "lighter black");
  assert.equal(cssColor({ indexed: 10 }), "#ff0000");
  assert.equal(cssColor({}), null);
  assert.equal(cssColor({ argb: "nope" }), null);
  assert.equal(colWidthPx(undefined), 64);
  assert.equal(rowHeightPx(15), 20);
  assert.deepEqual([colName(1), colName(26), colName(27), colName(702), colName(703)], ["A", "Z", "AA", "ZZ", "AAA"]);
  assert.deepEqual(parseRange("$B$3:D5"), { top: 3, left: 2, bottom: 5, right: 4 });
  assert.deepEqual(parseRange("D5:B3"), { top: 3, left: 2, bottom: 5, right: 4 });
  assert.equal(parseRange("A1"), null);
  assert.deepEqual(drawnExtent(10, 5), { rows: 10, cols: 5 });
  assert.deepEqual(drawnExtent(1_000_000, 10), { rows: MAX_CELLS / 10, cols: 10 });
  assert.equal(drawnExtent(5, 10_000).cols, 200);
});

test("a picked range: its label, its extent with merges, its values", () => {
  const b3d5 = { top: 3, left: 2, bottom: 5, right: 4 };
  assert.equal(rangeLabel("Revenue", b3d5), "Revenue!B3:D5");
  assert.equal(rangeLabel("Q3 Sales", { top: 3, left: 2, bottom: 3, right: 2 }), "'Q3 Sales'!B3");
  assert.equal(rangeLabel("Bob's", { top: 1, left: 1, bottom: 1, right: 1 }), "'Bob''s'!A1");
  // Dragging from a merged C2:D3 to B5 covers the whole merge.
  assert.deepEqual(coverRange({ top: 2, left: 3, bottom: 3, right: 4 }, { top: 5, left: 2, bottom: 5, right: 2 }), { top: 2, left: 2, bottom: 5, right: 4 });
  const sheet = { text: (r, c) => (c === 3 ? "a\tb" : `${colName(c)}${r}`), rowShown: (r) => r !== 4, colShown: () => true };
  assert.equal(rangeText(b3d5, sheet, 2000), "B3\ta b\tD3\nB5\ta b\tD5");
  const big = rangeText({ top: 1, left: 1, bottom: 10_000, right: 3 }, { text: () => "x", rowShown: () => true, colShown: () => true }, 100);
  assert.ok(big.length < 120, `cut near the limit: ${big.length}`);
});

test("units: non-empty cells keyed by address, merged ranges once, formulas kept, bounded", async () => {
  const { workbookUnits } = await import("./src/units.ts");
  const { diffUnits } = await import("../../../web/src/lib/compareUnits.ts");
  const load = async (buf) => {
    const wb = new ExcelJS.Workbook();
    await wb.xlsx.load(buf);
    return wb;
  };
  const sample = await readFile(join(HERE, "samples", "book.xlsx"));
  const wb = await load(sample);
  const { units } = workbookUnits(wb, 5000);
  assert.equal(units.filter((u) => u.key.endsWith("!B1") || u.key.endsWith("!C1")).length, 0, "a merged title is one cell");
  assert.equal(units.find((u) => u.key === "Revenue!B3")?.text, "$120,000");
  wb.getWorksheet("Revenue").getCell("C3").value = 150000;
  wb.getWorksheet("Revenue").getCell("H3").value = { formula: "B3+C3", result: 270000 };
  const after = workbookUnits(await load(await wb.xlsx.writeBuffer()), 5000).units;
  const changes = diffUnits(units, after).filter((c) => c.kind !== "same");
  assert.deepEqual(changes.map((c) => [c.kind, c.curr?.key, c.curr?.text]), [
    ["changed", "Revenue!C3", "$150,000"],
    ["added", "Revenue!H3", "270000\n=B3+C3"],
  ]);
  assert.deepEqual([workbookUnits(wb, 3).units.length, workbookUnits(wb, 3).truncated], [3, true], "bounded");
});
