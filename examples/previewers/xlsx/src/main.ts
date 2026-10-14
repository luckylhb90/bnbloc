// Excel previewer (bnbloc.previewer.contract/v1).
//
// Reads a .xlsx with ExcelJS and draws the active sheet as a grid: values
// through their number formats, fonts, fills, borders, alignment, merged
// cells, column widths, row heights, hidden rows and columns. Sheet tabs
// switch sheets; the toolbar zoom is host-rendered; the frame title names
// the sheet. Huge sheets draw their first MAX_CELLS cells and say so.

import ExcelJS from "exceljs";

import {
  ZOOM_MAX,
  ZOOM_MIN,
  onFile,
  onInit,
  onToolbar,
  reportError,
  setTitle,
  setToolbar,
  type PreviewFile,
  type ToolbarItem,
} from "../../../../web/previewer-sdk/src/index.ts";
import { shortName, sniffOffice, unreadableMessage, type OfficeKind } from "../../lib/office.ts";
import { cellCss, colName, colWidthPx, displayText, drawnExtent, isNumeric, parseRange, rowHeightPx, type XStyle } from "./sheet.ts";

const EXCEL: OfficeKind = { noun: "Excel workbook", ext: ".xlsx", legacy: ".xls", apps: "Excel or Numbers" };

const gridEl = document.getElementById("grid") as HTMLElement;
const noteEl = document.getElementById("note") as HTMLElement;
const tabsEl = document.getElementById("tabs") as HTMLElement;
const statusEl = document.getElementById("status") as HTMLElement;

let generation = 0;
let fileName = "";
let sheets: ExcelJS.Worksheet[] = [];
let active = 0;
let zoom = 1;

function publishToolbar(): void {
  const items: ToolbarItem[] = [{ id: "zoom", icon: "zoom-in", label: "Zoom", kind: "zoom", value: zoom }];
  setToolbar(items);
}

function title(): string {
  const file = shortName(fileName);
  return sheets.length > 1 ? `${file} — ${sheets[active].name}` : file;
}

/** Draws sheet i into the grid. */
function draw(i: number): void {
  active = i;
  const ws = sheets[i];
  const extent = drawnExtent(ws.rowCount, ws.columnCount);
  // Merged ranges: the top-left cell spans, the others are skipped.
  const span = new Map<string, { rows: number; cols: number }>();
  const covered = new Set<string>();
  const merges: string[] = (ws.model as { merges?: string[] }).merges ?? [];
  for (const m of merges) {
    const r = parseRange(m);
    if (!r) continue;
    span.set(`${r.top},${r.left}`, { rows: r.bottom - r.top + 1, cols: r.right - r.left + 1 });
    for (let y = r.top; y <= r.bottom; y++) for (let x = r.left; x <= r.right; x++) if (y !== r.top || x !== r.left) covered.add(`${y},${x}`);
  }
  const hiddenCols = new Set<number>();
  const table = document.createElement("table");
  table.id = "sheet";
  const colgroup = document.createElement("colgroup");
  const corner = document.createElement("col");
  corner.style.width = `${Math.max(32, String(extent.rows).length * 8 + 12)}px`;
  colgroup.append(corner);
  for (let c = 1; c <= extent.cols; c++) {
    const col = ws.getColumn(c);
    if (col.hidden) {
      hiddenCols.add(c);
      continue;
    }
    const el = document.createElement("col");
    el.style.width = `${colWidthPx(col.width ?? ws.properties.defaultColWidth)}px`;
    colgroup.append(el);
  }
  table.append(colgroup);
  const head = document.createElement("thead");
  const headRow = document.createElement("tr");
  headRow.append(document.createElement("th"));
  for (let c = 1; c <= extent.cols; c++) {
    if (hiddenCols.has(c)) continue;
    const th = document.createElement("th");
    th.textContent = colName(c);
    headRow.append(th);
  }
  head.append(headRow);
  table.append(head);
  const body = document.createElement("tbody");
  for (let r = 1; r <= extent.rows; r++) {
    const row = ws.getRow(r);
    if (row.hidden) continue;
    const tr = document.createElement("tr");
    tr.style.height = `${rowHeightPx(row.height ?? ws.properties.defaultRowHeight)}px`;
    const th = document.createElement("th");
    th.textContent = String(r);
    tr.append(th);
    for (let c = 1; c <= extent.cols; c++) {
      if (hiddenCols.has(c) || covered.has(`${r},${c}`)) continue;
      const cell = row.getCell(c);
      const td = document.createElement("td");
      const s = span.get(`${r},${c}`);
      if (s) {
        if (s.rows > 1) td.rowSpan = s.rows;
        if (s.cols > 1) td.colSpan = s.cols;
      }
      const style = cell.style as XStyle;
      td.textContent = displayText(cell.value, style?.numFmt);
      const css = cellCss(style, isNumeric(cell.value));
      if (css) td.style.cssText = css;
      tr.append(td);
    }
    body.append(tr);
  }
  table.append(body);
  gridEl.replaceChildren(table);
  gridEl.scrollTo(0, 0);
  table.style.zoom = String(zoom);

  const notes: string[] = [];
  if (extent.rows < ws.rowCount || extent.cols < ws.columnCount) {
    notes.push(
      `Showing rows 1–${extent.rows.toLocaleString("en")} and columns A–${colName(extent.cols)} of ${ws.rowCount.toLocaleString("en")} × ${ws.columnCount.toLocaleString("en")}; download the file for the rest.`,
    );
  }
  if (ws.getImages().length > 0) notes.push("Pictures and charts in this sheet are not shown.");
  noteEl.textContent = notes.join(" ");
  for (const [k, b] of [...tabsEl.children].entries()) b.setAttribute("aria-selected", String(k === i));
  setTitle(title());
}

function drawTabs(): void {
  tabsEl.replaceChildren(
    ...sheets.map((ws, i) => {
      const b = document.createElement("button");
      b.type = "button";
      b.role = "tab";
      b.textContent = ws.name;
      b.addEventListener("click", () => draw(i));
      return b;
    }),
  );
  tabsEl.hidden = sheets.length < 2;
}

async function open(bytes: ArrayBuffer, file: PreviewFile): Promise<void> {
  const mine = ++generation;
  fileName = file.name;
  const format = sniffOffice(bytes);
  if (format !== "zip") throw new Error(unreadableMessage(EXCEL, format, file.name));
  statusEl.textContent = `Opening ${file.name}…`;
  const wb = new ExcelJS.Workbook();
  try {
    await wb.xlsx.load(bytes);
  } catch (err) {
    if (mine !== generation) return;
    console.error(err);
    throw new Error(unreadableMessage(EXCEL, "zip", file.name));
  }
  if (mine !== generation) return;
  sheets = wb.worksheets.filter((ws) => ws.state !== "hidden" && ws.state !== "veryHidden");
  statusEl.textContent = "";
  if (sheets.length === 0) {
    gridEl.replaceChildren();
    tabsEl.hidden = true;
    statusEl.textContent = `${file.name} has no visible sheets.`;
    setTitle(shortName(file.name));
    return;
  }
  drawTabs();
  const first = (wb.views?.[0] as { activeTab?: number } | undefined)?.activeTab ?? 0;
  const visibleActive = sheets.indexOf(wb.worksheets[first]);
  draw(visibleActive >= 0 ? visibleActive : 0);
  publishToolbar();
}

onInit((init) => {
  document.documentElement.dataset.theme = init.theme;
  statusEl.textContent = `Loading ${init.file.name}…`;
  publishToolbar();
});

onFile((bytes, file) => {
  open(bytes, file).catch((err) => {
    const message = err instanceof Error ? err.message : String(err);
    gridEl.replaceChildren();
    tabsEl.hidden = true;
    sheets = [];
    statusEl.textContent = message;
    reportError(message);
  });
});

onToolbar((id, value) => {
  if (id !== "zoom" || typeof value !== "number") return;
  zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, value));
  const table = gridEl.querySelector("table");
  if (table) table.style.zoom = String(zoom);
  publishToolbar();
});
