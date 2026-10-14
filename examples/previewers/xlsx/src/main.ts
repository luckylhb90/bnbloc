// Excel previewer (bnbloc.previewer.contract/v1).
//
// Reads a .xlsx with ExcelJS and draws the active sheet as a grid: values
// through their number formats, fonts, fills, borders, alignment, merged
// cells, column widths, row heights, hidden rows and columns. Sheet tabs
// switch sheets; the toolbar zoom is host-rendered; the frame title names
// the sheet. Huge sheets draw their first MAX_CELLS cells and say so.
// Clicking, dragging or shift-clicking cells (or a row or column header)
// picks a range and points the Ask AI panel at it ("Revenue!B3:D5", its
// values as tab-separated rows).

import ExcelJS from "exceljs";

import {
  SELECTION_DETAIL_LIMIT,
  ZOOM_MAX,
  ZOOM_MIN,
  onFile,
  onInit,
  onToolbar,
  reportError,
  sendSelection,
  setTitle,
  setToolbar,
  type PreviewFile,
  type ToolbarItem,
} from "../../../../web/previewer-sdk/src/index.ts";
import { shortName, sniffOffice, unreadableMessage, type OfficeKind } from "../../lib/office.ts";
import { PICKED } from "../../lib/selection.ts";
import {
  cellCss,
  colName,
  colWidthPx,
  coverRange,
  displayText,
  drawnExtent,
  isNumeric,
  parseRange,
  rangeLabel,
  rangeText,
  rowHeightPx,
  type CellRange,
  type XStyle,
} from "./sheet.ts";

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

// The drawn sheet: its cells by "row,col" (a merged cell under its
// top-left), extent and hidden rows and columns.
let cells = new Map<string, HTMLElement>();
let drawn = { rows: 0, cols: 0, hiddenRows: new Set<number>(), hiddenCols: new Set<number>() };
// The picked range, the corner it grows from, and the cells it lit.
let range: CellRange | null = null;
let anchor: CellRange | null = null;
let lit: HTMLElement[] = [];
let dragging = false;

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
  const hiddenRows = new Set<number>();
  cells = new Map();
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
    th.dataset.col = String(c);
    headRow.append(th);
  }
  head.append(headRow);
  table.append(head);
  const body = document.createElement("tbody");
  for (let r = 1; r <= extent.rows; r++) {
    const row = ws.getRow(r);
    if (row.hidden) {
      hiddenRows.add(r);
      continue;
    }
    const tr = document.createElement("tr");
    tr.style.height = `${rowHeightPx(row.height ?? ws.properties.defaultRowHeight)}px`;
    const th = document.createElement("th");
    th.textContent = String(r);
    th.dataset.row = String(r);
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
      td.dataset.cell = `${r},${c},${r + (s?.rows ?? 1) - 1},${c + (s?.cols ?? 1) - 1}`;
      cells.set(`${r},${c}`, td);
      const style = cell.style as XStyle;
      td.textContent = displayText(cell.value, style?.numFmt);
      const css = cellCss(style, isNumeric(cell.value));
      if (css) td.style.cssText = css;
      tr.append(td);
    }
    body.append(tr);
  }
  table.append(body);
  drawn = { rows: extent.rows, cols: extent.cols, hiddenRows, hiddenCols };
  unpick();
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

/** The cells (or the header's row or column) an element stands for. */
function rangeAt(el: EventTarget | null): CellRange | null {
  if (!(el instanceof Element)) return null;
  const cell = el.closest<HTMLElement>("td[data-cell], th[data-row], th[data-col]");
  if (!cell || !gridEl.contains(cell)) return null;
  if (cell.dataset.row) return { top: Number(cell.dataset.row), left: 1, bottom: Number(cell.dataset.row), right: drawn.cols };
  if (cell.dataset.col) return { top: 1, left: Number(cell.dataset.col), bottom: drawn.rows, right: Number(cell.dataset.col) };
  const [top, left, bottom, right] = (cell.dataset.cell ?? "").split(",").map(Number);
  return { top, left, bottom, right };
}

function light(next: CellRange): void {
  for (const el of lit) el.classList.remove(PICKED);
  lit = [];
  range = next;
  for (let r = next.top; r <= next.bottom; r++) {
    for (let c = next.left; c <= next.right; c++) {
      const el = cells.get(`${r},${c}`);
      if (el) {
        el.classList.add(PICKED);
        lit.push(el);
      }
    }
  }
}

function unpick(): void {
  for (const el of lit) el.classList.remove(PICKED);
  lit = [];
  if (range) sendSelection(null);
  range = anchor = null;
}

function report(): void {
  if (!range || sheets.length === 0) return;
  const ws = sheets[active];
  const detail = rangeText(
    range,
    {
      text: (r, c) => (cells.has(`${r},${c}`) ? displayText(ws.getRow(r).getCell(c).value, (ws.getRow(r).getCell(c).style as XStyle)?.numFmt) : ""),
      rowShown: (r) => !drawn.hiddenRows.has(r),
      colShown: (c) => !drawn.hiddenCols.has(c),
    },
    SELECTION_DETAIL_LIMIT,
  );
  sendSelection({ label: rangeLabel(ws.name, range), detail });
}

gridEl.addEventListener("pointerdown", (e) => {
  const at = e.button === 0 ? rangeAt(e.target) : null;
  if (!at) return;
  // No text selection while picking; keep keyboard focus (Escape) here.
  e.preventDefault();
  window.focus();
  anchor = e.shiftKey && anchor ? anchor : at;
  light(coverRange(anchor, at));
  dragging = true;
});
gridEl.addEventListener("pointermove", (e) => {
  if (!dragging || !anchor) return;
  const at = rangeAt(e.target);
  if (at) light(coverRange(anchor, at));
});
document.addEventListener("pointerup", () => {
  if (!dragging) return;
  dragging = false;
  report();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") unpick();
});

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
