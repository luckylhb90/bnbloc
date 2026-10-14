import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { SparkleIcon } from "@phosphor-icons/react";
import type { FileFacts } from "@/lib/fileMatch";
import { fileSuggestions, type PromptChoice } from "@/lib/promptPacks";
import type { ViewerSelection } from "@/views/artifact/registry";
import { usePromptChoices } from "@/views/composer/usePromptChoices";

// The action beside a selection (docs/prd/2026-10-14-select-edit-review.md
// rule 3): "Ask AI" and the selection's top suggestions, placed under the
// selection's rect in the preview (above it when there is no room), hidden
// while the selection is off screen. A pick opens the file's Ask AI panel
// with the prompt inserted; "Ask AI" opens it to type.

const SHOWN = 3;
const GAP = 6;

export function SelectionAction({
  roomId,
  file,
  selection,
  onAsk,
}: {
  readonly roomId: string;
  readonly file: FileFacts;
  readonly selection: ViewerSelection;
  readonly onAsk: (choice?: PromptChoice) => void;
}) {
  const { choices } = usePromptChoices(roomId);
  const picks = useMemo(() => fileSuggestions(choices, file, selection.kind, SHOWN), [choices, file, selection.kind]);
  const ref = useRef<HTMLDivElement | null>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const rect = selection.rect;
  useLayoutEffect(() => {
    const el = ref.current;
    const box = el?.offsetParent as HTMLElement | null;
    if (!el || !box || !rect) return setPos(null);
    const W = box.clientWidth;
    const H = box.clientHeight;
    if (rect.y + rect.h < 0 || rect.y > H || rect.x > W || rect.x + rect.w < 0) return setPos(null);
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    let top = rect.y + rect.h + GAP;
    if (top + h > H - GAP) top = rect.y - h - GAP;
    setPos({ left: Math.max(GAP, Math.min(rect.x, W - w - GAP)), top: Math.max(GAP, Math.min(top, H - h - GAP)) });
  }, [rect, picks.length]);
  const chip =
    "tactile shrink-0 rounded-full px-2.5 py-1 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-accent-soft)] hover:text-[var(--color-fg)]";
  return (
    <div
      ref={ref}
      role="toolbar"
      aria-label={`Ask AI about ${selection.label}`}
      data-selection-action
      className="absolute z-20 flex w-max max-w-[calc(100%-12px)] items-center gap-0.5 overflow-x-auto rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface)] p-0.5 shadow-[var(--shadow-pop)]"
      style={pos ? { left: pos.left, top: pos.top } : { left: -9999, top: -9999, visibility: "hidden" }}
    >
      <button type="button" onClick={() => onAsk()} className={`${chip} inline-flex items-center gap-1 font-medium text-[var(--color-accent)]`}>
        <SparkleIcon size={12} weight="fill" aria-hidden="true" /> Ask AI
      </button>
      {picks.map((c) => (
        <button key={`${c.componentId}/${c.prompt.id}`} type="button" title={c.prompt.body} onClick={() => onAsk(c)} className={chip}>
          {c.prompt.title}
        </button>
      ))}
    </div>
  );
}
