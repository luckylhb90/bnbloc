import type { FileFacts } from "@/lib/fileMatch";
import type { PromptChoice } from "@/lib/promptPacks";

export const toolButton =
  "tactile grid size-10 shrink-0 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60";


/** The file a composer's conversation is about (Composer fileContext). */
export interface ComposerFileContext {
  readonly file: FileFacts;
  /** What the member points at in it (a selection's kind): its
   *  suggestions come first. */
  readonly selectionKind?: string;
  /** Where a picked suggestion goes; default: this composer's input. */
  readonly onPick?: (c: PromptChoice) => void;
}

/** The @-mention the caret is in: its query, and where its "@" starts; null when the caret is not on one. */
export function mentionAt(value: string, caret: number): { query: string; start: number } | null {
  const m = /(?:^|\s)@([\w/:.-]*)$/.exec(value.slice(0, caret));
  return m ? { query: m[1], start: caret - m[1].length - 1 } : null;
}
