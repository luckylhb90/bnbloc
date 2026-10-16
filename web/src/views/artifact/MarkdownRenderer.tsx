import { useRef } from "react";
import { Markdown } from "@/lib/markdown.tsx";
import { tableText, type PartRule } from "../../../previewer-sdk/src/pointing.ts";
import type { ArtifactViewerProps } from "./registry";
import { usePointing } from "./usePointing";

// What a click picks in a Markdown file: the part rules are data, the
// innermost match wins (a list item in a quote is a list item), selected text
// is named by the block it starts in (previewer-sdk/src/pointing.ts).
const MARKDOWN_PARTS: readonly PartRule[] = [
  { selector: "h1, h2, h3, h4, h5, h6", noun: "Heading", kind: "block" },
  { selector: "li", noun: "List item", kind: "block" },
  { selector: "blockquote", noun: "Quote", kind: "block" },
  { selector: "pre", noun: "Code block", kind: "block" },
  { selector: "table", noun: "Table", kind: "block", text: (el) => tableText(el as HTMLTableElement) },
  { selector: "p", noun: "Paragraph", kind: "block" },
];

export default function MarkdownRenderer({ content, onSelection }: ArtifactViewerProps) {
  const ref = useRef<HTMLDivElement | null>(null);
  usePointing(ref, onSelection, MARKDOWN_PARTS);
  return (
    <div ref={ref} className="h-full overflow-auto px-4 py-3">
      <Markdown text={content} />
    </div>
  );
}
