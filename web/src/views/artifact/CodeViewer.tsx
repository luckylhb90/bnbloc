import { useMemo, useRef, useState, type ReactNode } from "react";
import { highlightLines } from "@/lib/highlight";
import { cn } from "@/lib/cn";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";
import { copyText } from "@/lib/clipboard";
import type { PartRule } from "../../../previewer-sdk/src/pointing.ts";
import { usePointing, type PointingHooks } from "./usePointing";

// CodeViewer: source text with line numbers, a wrap toggle and syntax
// coloring. Coloring reuses the app's own dependency-free tokenizer
// (lib/highlight, already shipped for chat code fences): zero added bytes, no
// grammar downloads, React spans only (no HTML injection). Large files stay
// responsive: rows render in fixed blocks with `content-visibility: auto`, so
// the browser lays out only what is on screen, and coloring is skipped above
// HIGHLIGHT_MAX_CHARS.

// Pointing (previewer-sdk/src/pointing.ts): a click on a line's number picks
// the line, selected text is named by the lines it spans ("Lines 12–18").
function rowOf(node: Node): HTMLElement | null {
  const el = node instanceof Element ? node : node.parentElement;
  return el?.closest<HTMLElement>("[data-line]") ?? null;
}
const lineText = (row: HTMLElement | null): string => (row?.lastElementChild?.textContent ?? "").trim();
const CODE_PARTS: readonly PartRule[] = [
  {
    selector: "[data-gutter]",
    noun: (el) => `Line ${el.parentElement?.dataset.line ?? ""}`,
    kind: "block",
    text: (el) => lineText(el.parentElement),
    element: (el) => el.parentElement ?? el,
  },
];
const CODE_POINTING: PointingHooks = {
  describeText(range) {
    const a = rowOf(range.startContainer);
    if (!a) return null;
    const first = Number(a.dataset.line);
    let last = Number(rowOf(range.endContainer)?.dataset.line ?? first);
    // A selection that ends at the start of a line does not include it.
    if (last > first && range.endOffset === 0) last -= 1;
    return { where: last > first ? `Lines ${first}–${last}` : `Line ${first}` };
  },
};

const BLOCK_LINES = 200;
const LINE_PX = 20;
const HIGHLIGHT_MAX_CHARS = 1_000_000;

function TextLines({ content, colored, toolbar, onSelection }: { content: string; colored: boolean; toolbar: ArtifactViewerProps["toolbar"]; onSelection: ArtifactViewerProps["onSelection"] }) {
  const ref = useRef<HTMLDivElement | null>(null);
  usePointing(ref, onSelection, CODE_PARTS, CODE_POINTING);
  const [wrap, setWrap] = useState(false);
  const [numbers, setNumbers] = useState(true);
  const lines = useMemo(() => content.replace(/\r\n?/g, "\n").split("\n"), [content]);
  const highlighted = useMemo<ReactNode[][] | null>(
    () => (colored && content.length <= HIGHLIGHT_MAX_CHARS ? highlightLines(content.replace(/\r\n?/g, "\n")) : null),
    [colored, content],
  );
  useViewerToolbar(
    toolbar,
    [
      { id: "wrap", kind: "toggle", icon: "wrap", label: "Wrap lines", active: wrap },
      { id: "numbers", kind: "toggle", icon: "line-numbers", label: "Line numbers", active: numbers },
      { id: "copy", kind: "button", icon: "copy", label: "Copy all" },
    ],
    (id, value) => {
      if (id === "wrap") setWrap(value === true);
      else if (id === "numbers") setNumbers(value === true);
      else if (id === "copy") void copyText(content).catch(() => {});
    },
  );
  const gutter = `${String(lines.length).length + 1}ch`;
  const blocks: number[] = [];
  for (let start = 0; start < lines.length; start += BLOCK_LINES) blocks.push(start);
  return (
    <div ref={ref} className="h-full overflow-auto bg-[var(--color-surface)] py-2 font-mono text-[12px] text-[var(--color-fg)]" style={{ lineHeight: `${LINE_PX}px` }}>
      <div className={cn(wrap ? "min-w-0" : "w-max min-w-full")}>
        {blocks.map((start) => {
          const end = Math.min(lines.length, start + BLOCK_LINES);
          return (
            <div
              key={start}
              style={{ contentVisibility: "auto", containIntrinsicSize: `auto ${(end - start) * LINE_PX}px` }}
            >
              {lines.slice(start, end).map((line, i) => {
                const n = start + i;
                return (
                  <div key={n} data-line={n + 1} className="flex hover:bg-[var(--color-surface-2)]">
                    {numbers && (
                      <span
                        data-gutter
                        className="sticky left-0 shrink-0 cursor-pointer select-none bg-[var(--color-surface)] pr-3 text-right text-[var(--color-fg-subtle)]"
                        style={{ width: gutter, minWidth: gutter, boxSizing: "content-box", paddingLeft: "0.75rem" }}
                        aria-hidden
                      >
                        {n + 1}
                      </span>
                    )}
                    <span className={cn("min-w-0 flex-1 pr-4", !numbers && "pl-3", wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre")}>
                      {highlighted ? highlighted[n] : line}
                      {line === "" ? " " : null}
                    </span>
                  </div>
                );
              })}
            </div>
          );
        })}
      </div>
    </div>
  );
}

export default function CodeViewer({ content, toolbar, onSelection }: ArtifactViewerProps) {
  return <TextLines content={content} colored toolbar={toolbar} onSelection={onSelection} />;
}

export function PlainTextViewer({ content, toolbar, onSelection }: ArtifactViewerProps) {
  return <TextLines content={content} colored={false} toolbar={toolbar} onSelection={onSelection} />;
}
