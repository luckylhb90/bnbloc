import { lazy, Suspense, useState } from "react";
import { cn } from "@/lib/cn";

// The Markdown renderer (parser, GFM, highlighter) is its own chunk: the first
// paint does not need it, and until it arrives a reply shows as plain text.
const Markdown = lazy(() => import("@/lib/markdown").then((m) => ({ default: m.Markdown })));

const LONG_MESSAGE_CHARS = 1800;

export function MessageBody({
  text,
  markdown,
  className,
  fadeClassName = "to-[var(--color-bg)]",
  onAccent,
}: {
  readonly text: string;
  readonly markdown?: boolean;
  readonly className?: string;
  readonly fadeClassName?: string;
  /** The text sits on the accent colour (a member's own bubble): the fade
   *  and the Show more button take that colour's pair. */
  readonly onAccent?: boolean;
}) {
  const [expanded, setExpanded] = useState(false);
  const fade = onAccent ? "to-[var(--color-accent)]" : fadeClassName;
  const long = text.length > LONG_MESSAGE_CHARS || text.split("\n").length > 24;
  const plain = <div className="whitespace-pre-wrap leading-relaxed">{text}</div>;
  const content = markdown ? (
    <Suspense fallback={plain}>
      <Markdown text={text} />
    </Suspense>
  ) : (
    plain
  );
  return (
    <div>
      <div
        className={cn(
          "relative text-sm [overflow-wrap:anywhere]",
          !onAccent && "text-[var(--color-fg)]",
          long && !expanded && "max-h-96 overflow-hidden",
          className,
        )}
      >
        {content}
        {long && !expanded && (
          <div
            className={cn(
              "pointer-events-none absolute inset-x-0 bottom-0 h-16 bg-gradient-to-b from-transparent",
              fade,
            )}
          />
        )}
      </div>
      {long && (
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
          className={cn(
            "tactile mt-1.5 rounded-[var(--radius)] px-2 py-1 text-xs font-medium focus-visible:outline-none focus-visible:ring-2",
            onAccent
              ? "bg-black/15 text-[var(--color-accent-fg)] hover:bg-black/25 focus-visible:ring-[var(--color-accent-fg)]/60"
              : "text-[var(--color-accent)] hover:bg-[var(--color-surface-2)] focus-visible:ring-[var(--color-accent)]/60",
          )}
        >
          {expanded ? "Show less" : "Show more"}
        </button>
      )}
    </div>
  );
}
