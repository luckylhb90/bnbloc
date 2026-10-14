// The workbook's units for Compare (capability "units"): one per non-empty
// cell of each visible sheet, keyed and labelled by its address as a
// formula names it ("Revenue!B3", "'Q3 Sales'!B3"). A cell's text is what it
// shows (its number format applied, sheet.ts displayText), and its formula
// when it has one: an edited formula is a change even when the value is the
// same. At most maxCells cells: a bigger workbook is cut, and says so.

import type ExcelJS from "exceljs";
import { displayText, rangeLabel, type XStyle } from "./sheet.ts";

export interface CellUnit {
  readonly key: string;
  readonly label: string;
  readonly text: string;
  /** Where it is: its address, as the key (main.ts reveals it). */
  readonly at: string;
}

export function workbookUnits(wb: ExcelJS.Workbook, maxCells: number): { units: CellUnit[]; truncated: boolean } {
  const units: CellUnit[] = [];
  for (const ws of wb.worksheets) {
    if (ws.state === "hidden" || ws.state === "veryHidden") continue;
    let full = false;
    ws.eachRow({ includeEmpty: false }, (row, r) => {
      if (full) return;
      row.eachCell({ includeEmpty: false }, (cell, c) => {
        if (full) return;
        // A merged range is its top-left cell (the others echo its value).
        if (cell.isMerged && cell.master.address !== cell.address) return;
        const shown = displayText(cell.value, (cell.style as XStyle)?.numFmt);
        const formula = cell.formula ? `=${cell.formula}` : "";
        if (!shown && !formula) return;
        if (units.length >= maxCells) {
          full = true;
          return;
        }
        const address = rangeLabel(ws.name, { top: r, left: c, bottom: r, right: c });
        units.push({ key: address, label: address, at: address, text: formula ? `${shown}\n${formula}` : shown });
      });
    });
    if (full) return { units, truncated: true };
  }
  return { units, truncated: false };
}
