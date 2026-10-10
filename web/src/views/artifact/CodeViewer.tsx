import { useMemo, useState, type ReactNode } from "react";
import { highlightLines } from "@/lib/highlight";
import { cn } from "@/lib/cn";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";

// CodeViewer: source text with line numbers, a wrap toggle and syntax
// coloring. Coloring reuses the app's own dependency-free tokenizer
// (lib/highlight, already shipped for chat code fences): zero added bytes, no
// grammar downloads, React spans only (no HTML injection). Large files stay
// responsive: rows render in fixed blocks with `content-visibility: auto`, so
// the browser lays out only what is on screen, and coloring is skipped above
// HIGHLIGHT_MAX_CHARS.

const BLOCK_LINES = 200;
const LINE_PX = 20;
const HIGHLIGHT_MAX_CHARS = 1_000_000;

function TextLines({ content, colored, toolbar }: { content: string; colored: boolean; toolbar: ArtifactViewerProps["toolbar"] }) {
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
      else if (id === "copy") void navigator.clipboard?.writeText(content).catch(() => {});
    },
  );
  const gutter = `${String(lines.length).length + 1}ch`;
  const blocks: number[] = [];
  for (let start = 0; start < lines.length; start += BLOCK_LINES) blocks.push(start);
  return (
    <div className="h-full overflow-auto bg-[var(--color-surface)] py-2 font-mono text-[12px] text-[var(--color-fg)]" style={{ lineHeight: `${LINE_PX}px` }}>
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
                  <div key={n} className="flex hover:bg-[var(--color-surface-2)]">
                    {numbers && (
                      <span
                        className="sticky left-0 shrink-0 select-none bg-[var(--color-surface)] pr-3 text-right text-[var(--color-fg-subtle)]"
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

export default function CodeViewer({ content, toolbar }: ArtifactViewerProps) {
  return <TextLines content={content} colored toolbar={toolbar} />;
}

export function PlainTextViewer({ content, toolbar }: ArtifactViewerProps) {
  return <TextLines content={content} colored={false} toolbar={toolbar} />;
}
