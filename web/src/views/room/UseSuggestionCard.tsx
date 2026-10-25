import { createContext, useContext, useSyncExternalStore } from "react";
import { SparkleIcon } from "@phosphor-icons/react";
import { cardForRow, sentRequestCards } from "@/lib/sentRequestCard";
import { suggestionAction, suggestionText } from "@/lib/suggestUse";
import { Button } from "@/ui/Button";
import type { FeedItem } from "@/views/room/types";

// UseSuggestionCard is what fits the member's request, drawn under their own message
// once it was sent (docs/prd/2026-10-25-suggest-what-to-use.md): one sentence, a
// button that does the one thing (use it and ask again, nothing before the click), and
// "Not now". It is the member's alone, kept in this session only, and reaches the
// memoized Message rows through context, so a card shows without re-rendering the feed.

export interface UseSuggestionActions {
  readonly room: string;
  readonly accept: () => void;
  readonly dismiss: () => void;
  readonly busy: boolean;
}

const Ctx = createContext<UseSuggestionActions | null>(null);
export const UseSuggestionProvider = Ctx.Provider;

export function UseSuggestionCard({ item }: { readonly item: FeedItem }) {
  const actions = useContext(Ctx);
  const room = actions?.room ?? "";
  const card = useSyncExternalStore(sentRequestCards.subscribe, () => cardForRow(sentRequestCards.of(room), item.clientMsgId));
  if (!actions || !card) return null;
  const c = card.suggestion.candidate;
  return (
    <div data-use-suggestion={c.kind} role="status" aria-live="polite" className="mt-2 flex max-w-[80%] flex-col items-end gap-1.5 self-end">
      <div className="flex min-w-0 flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-3 py-2 text-sm text-[var(--color-fg)]">
        <p className="flex items-start gap-2">
          <SparkleIcon size={14} weight="fill" className="mt-0.5 shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
          <span className="min-w-0">
            <span className="font-medium">{suggestionText(c)}</span>
            {c.description ? <span className="text-[var(--color-fg-muted)]"> {c.description.length > 140 ? `${c.description.slice(0, 137)}…` : c.description}</span> : null}
          </span>
        </p>
        <div className="flex items-center justify-end gap-2">
          <Button size="sm" variant="ghost" onClick={actions.dismiss} disabled={actions.busy}>
            Not now
          </Button>
          <Button size="sm" variant="primary" onClick={actions.accept} disabled={actions.busy}>
            {actions.busy ? "…" : suggestionAction(c)}
          </Button>
        </div>
      </div>
    </div>
  );
}
