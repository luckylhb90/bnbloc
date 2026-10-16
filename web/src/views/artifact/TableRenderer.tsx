import { useEffect, useMemo, useRef, useState } from "react";
import { fitSelection } from "../../../previewer-sdk/src/protocol.ts";
import { selectionReporter } from "../../../previewer-sdk/src/pointing.ts";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";
import { columnWidths, detectDelimiter, parseDelimited } from "./tableData";
import { isWholeColumns, rangeDetail, rangeKeys, rangeLabel, spanCells, type CellRange } from "./tableRange";

// CSV/TSV as a virtualized grid: only the rows in view (plus a small
// overscan) are in the DOM, so a 100k-row export scrolls like a 100-row one.
// Cells are text nodes — never markup. Pointing (previewer-sdk/src/pointing.ts
// reports the rect): a click picks a cell, a drag or a shift-click a range, a
// header click the column; the addresses are Compare's (the header is row 1),
// so a selection and a version's changed cells name the same units.

const ROW_PX = 28;
const OVERSCAN = 12;
const MAX_ROWS = 500_000;

export default function TableRenderer({ content, file, toolbar, onSelection }: ArtifactViewerProps) {
  const table = useMemo(() => {
    const delimiter = detectDelimiter(content, { mimeType: file.mimeType, extension: file.extension });
    return parseDelimited(content, delimiter, MAX_ROWS);
  }, [content, file.mimeType, file.extension]);
  const [headerRow, setHeaderRow] = useState(true);
  const header = headerRow ? table.rows[0] : undefined;
  const body = headerRow ? table.rows.slice(1) : table.rows;
  const columns = useMemo(() => Math.max(0, ...table.rows.slice(0, 200).map((r) => r.length)), [table]);
  const widths = useMemo(() => {
    const w = columnWidths(table.rows);
    while (w.length < columns) w.push(96);
    return w;
  }, [table, columns]);
  const template = `56px ${widths.map((w) => `${w}px`).join(" ")}`;

  const scrollRef = useRef<HTMLDivElement | null>(null);
  const [viewport, setViewport] = useState({ top: 0, height: 600 });
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setViewport({ top: el.scrollTop, height: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useViewerToolbar(toolbar, [{ id: "header", kind: "toggle", icon: "rows", label: "First row is a header", active: headerRow }], (id, value) => {
    if (id === "header") setHeaderRow(value === true);
  });

  // The picked range (file rows, the header being row 0) and the drag's start.
  const [range, setRange] = useState<CellRange | null>(null);
  const dragFrom = useRef<{ r: number; c: number } | null>(null);
  const reporter = useRef<ReturnType<typeof selectionReporter> | null>(null);
  const onSelectionRef = useRef(onSelection);
  onSelectionRef.current = onSelection;
  const rowCount = table.rows.length;
  const firstBody = headerRow ? 1 : 0;

  useEffect(() => {
    if (!onSelectionRef.current) return;
    const r = selectionReporter((draft) => onSelectionRef.current?.(draft ? fitSelection(draft) : null));
    reporter.current = r;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setRange(null);
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      r.dispose();
      reporter.current = null;
      onSelectionRef.current?.(null);
    };
  }, []);
  // A new table, or the header toggled, moves every address.
  useEffect(() => setRange(null), [table, headerRow]);

  useEffect(() => {
    const r = reporter.current;
    if (!r) return;
    if (!range) return r.clear();
    const rectOf = () => {
      const sc = scrollRef.current;
      const box = sc?.closest("[data-viewer-box]");
      if (!sc) return null;
      const at = sc.getBoundingClientRect();
      const b = box?.getBoundingClientRect() ?? at;
      let left = 56 - sc.scrollLeft;
      for (let c = 0; c < range.c0; c++) left += widths[c] ?? 96;
      let width = 0;
      for (let c = range.c0; c <= range.c1; c++) width += widths[c] ?? 96;
      const top = Math.max(0, range.r0 * ROW_PX - sc.scrollTop);
      const height = Math.min((range.r1 - range.r0 + 1) * ROW_PX, sc.clientHeight - top);
      return new DOMRect(at.left - b.left + left, at.top - b.top + top, width, Math.max(ROW_PX, height));
    };
    r.set(
      {
        label: rangeLabel(range, rowCount),
        detail: rangeDetail(table.rows, range, headerRow),
        kind: "cells",
        ...(isWholeColumns(range, rowCount) ? {} : { keys: rangeKeys(range) }),
      },
      rectOf,
    );
  }, [range, table, headerRow, widths, rowCount]);

  const cellAt = (target: EventTarget | null) => {
    const el = target instanceof Element ? target.closest<HTMLElement>("[data-c]") : null;
    return el ? { r: Number(el.dataset.r), c: Number(el.dataset.c), header: el.dataset.header === "1" } : null;
  };
  const onPointerDown = (e: React.PointerEvent) => {
    if (e.button !== 0 || !onSelection) return;
    const hit = cellAt(e.target);
    if (!hit) return;
    if (hit.header) {
      const from = e.shiftKey && range ? range.c0 : hit.c;
      setRange({ r0: 0, c0: Math.min(from, hit.c), r1: rowCount - 1, c1: Math.max(from, hit.c) });
      dragFrom.current = null;
      return;
    }
    const again = range && range.r0 === hit.r && range.r1 === hit.r && range.c0 === hit.c && range.c1 === hit.c;
    if (again && !e.shiftKey) {
      setRange(null);
      dragFrom.current = null;
      return;
    }
    dragFrom.current = e.shiftKey && range ? { r: range.r0, c: range.c0 } : { r: hit.r, c: hit.c };
    setRange(spanCells(dragFrom.current, hit));
  };
  const onPointerMove = (e: React.PointerEvent) => {
    if (!dragFrom.current || !(e.buttons & 1)) return;
    const hit = cellAt(e.target);
    if (hit && !hit.header) setRange(spanCells(dragFrom.current, hit));
  };
  const picked = (r: number, c: number) => range !== null && r >= range.r0 && r <= range.r1 && c >= range.c0 && c <= range.c1;

  if (table.rows.length === 0) return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">No table data.</p>;
  const first = Math.max(0, Math.floor(viewport.top / ROW_PX) - OVERSCAN);
  const last = Math.min(body.length, Math.ceil((viewport.top + viewport.height) / ROW_PX) + OVERSCAN);
  const cell = "truncate border-b border-r border-[var(--color-border)] px-2";
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        ref={scrollRef}
        onScroll={(e) => setViewport({ top: e.currentTarget.scrollTop, height: e.currentTarget.clientHeight })}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={() => (dragFrom.current = null)}
        className="relative min-h-0 flex-1 select-none overflow-auto text-[12px] text-[var(--color-fg)]"
        role="table"
        aria-rowcount={table.rows.length}
        aria-colcount={columns}
      >
        <div className="w-max min-w-full">
          {header && (
            <div
              role="row"
              className="sticky top-0 z-10 grid bg-[var(--color-surface-2)] font-semibold"
              style={{ gridTemplateColumns: template, height: ROW_PX, lineHeight: `${ROW_PX - 1}px` }}
            >
              <span className={`${cell} sticky left-0 bg-[var(--color-surface-2)] text-right text-[var(--color-fg-subtle)]`} aria-hidden>
                #
              </span>
              {Array.from({ length: columns }, (_, c) => (
                <span
                  key={c}
                  role="columnheader"
                  data-r={0}
                  data-c={c}
                  data-header="1"
                  className={`${cell} ${onSelection ? "cursor-pointer" : ""} ${picked(0, c) ? "bg-[var(--color-accent-soft)]" : ""}`}
                  title={header[c] ?? ""}
                >
                  {header[c] ?? ""}
                </span>
              ))}
            </div>
          )}
          <div className="relative" style={{ height: body.length * ROW_PX }}>
            {body.slice(first, last).map((row, i) => {
              const index = first + i;
              return (
                <div
                  key={index}
                  role="row"
                  className="absolute left-0 grid w-full hover:bg-[var(--color-surface-2)]"
                  style={{ top: index * ROW_PX, gridTemplateColumns: template, height: ROW_PX, lineHeight: `${ROW_PX - 1}px` }}
                >
                  <span className={`${cell} sticky left-0 bg-[var(--color-surface)] text-right tabular text-[var(--color-fg-subtle)]`} aria-hidden>
                    {index + 1 + firstBody}
                  </span>
                  {Array.from({ length: columns }, (_, c) => (
                    <span
                      key={c}
                      role="cell"
                      data-r={index + firstBody}
                      data-c={c}
                      className={`${cell} ${picked(index + firstBody, c) ? "bg-[var(--color-accent-soft)]" : ""}`}
                      title={row[c] ?? ""}
                    >
                      {row[c] ?? ""}
                    </span>
                  ))}
                </div>
              );
            })}
          </div>
        </div>
      </div>
      <div className="shrink-0 border-t border-[var(--color-border)] px-3 py-1 text-[11px] text-[var(--color-fg-subtle)] tabular">
        {body.length.toLocaleString()} rows · {columns} columns
        {table.truncated ? ` · first ${MAX_ROWS.toLocaleString()} rows shown` : ""}
      </div>
    </div>
  );
}
