import { SparkleIcon, XIcon } from "@phosphor-icons/react";
import { suggestionAction, suggestionText, type UseSuggestion } from "@/lib/suggestUse";
import { Button } from "@/ui/Button";

// UseSuggestionRow is the composer's recommendation of what fits the request
// being written (docs/prd/2026-10-25-suggest-what-to-use.md): one sentence, the
// button that turns it on, and a way to dismiss it for good. It never sends and
// never blocks the input; the hook (useUseSuggestion) owns what the click does.
export function UseSuggestionRow({
  suggestion,
  busy,
  onAccept,
  onDismiss,
}: {
  readonly suggestion: UseSuggestion;
  readonly busy: boolean;
  readonly onAccept: () => void;
  readonly onDismiss: () => void;
}) {
  const c = suggestion.candidate;
  return (
    <div className="px-3 pt-2" role="status" aria-live="polite" data-use-suggestion={c.kind}>
      <div className="flex min-w-0 items-center gap-2 rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] py-1 pl-3 pr-1.5 text-xs text-[var(--color-fg)]">
        <SparkleIcon size={12} weight="fill" className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate" title={c.description ? `${c.label}: ${c.description}` : c.label}>
          {suggestionText(c)}
        </span>
        <Button size="sm" variant="primary" disabled={busy} onClick={onAccept} className="shrink-0">
          {busy ? "…" : suggestionAction(c)}
        </Button>
        <button
          type="button"
          onClick={onDismiss}
          title="Don't suggest this again"
          aria-label={`Don't suggest ${c.label} again`}
          className="tactile shrink-0 rounded-full p-1 text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
        >
          <XIcon size={12} />
        </button>
      </div>
    </div>
  );
}
