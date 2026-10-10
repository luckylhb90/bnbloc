import { Markdown } from "@/lib/markdown.tsx";
import type { ArtifactViewerProps } from "./registry";

export default function MarkdownRenderer({ content }: ArtifactViewerProps) {
  return (
    <div className="h-full overflow-auto px-4 py-3">
      <Markdown text={content} />
    </div>
  );
}
