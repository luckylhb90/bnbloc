import { useEffect, useMemo, useRef, useState } from "react";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";
import { columnWidths, detectDelimiter, parseDelimited } from "./tableData";

// CSV/TSV as a virtualized grid: only the rows in view (plus a small
// overscan) are in the DOM, so a 100k-row export scrolls like a 100-row one.
// Cells are text nodes — never markup.

const ROW_PX = 28;
const OVERSCAN = 12;
const MAX_ROWS = 500_000;

export default function TableRenderer({ content, file, toolbar }: ArtifactViewerProps) {
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

  if (table.rows.length === 0) return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">No table data.</p>;
  const first = Math.max(0, Math.floor(viewport.top / ROW_PX) - OVERSCAN);
  const last = Math.min(body.length, Math.ceil((viewport.top + viewport.height) / ROW_PX) + OVERSCAN);
  const cell = "truncate border-b border-r border-[var(--color-border)] px-2";
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        ref={scrollRef}
        onScroll={(e) => setViewport({ top: e.currentTarget.scrollTop, height: e.currentTarget.clientHeight })}
        className="relative min-h-0 flex-1 overflow-auto text-[12px] text-[var(--color-fg)]"
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
                <span key={c} role="columnheader" className={cell} title={header[c] ?? ""}>
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
                    {index + 1}
                  </span>
                  {Array.from({ length: columns }, (_, c) => (
                    <span key={c} role="cell" className={cell} title={row[c] ?? ""}>
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
