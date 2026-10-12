import { SparkleIcon } from "@phosphor-icons/react";
import type { PromptChoice } from "@/lib/promptPacks";

const CHIP =
  "tactile max-w-[18rem] shrink-0 truncate rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-xs text-[var(--color-fg-muted)] hover:border-[var(--color-accent)]/40 hover:bg-[var(--color-accent-soft)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60";

// StarterPrompts are an empty room's starter prompts, shown above the
// composer (PRD 2026-10-10 v2 §1): every one a prompt pack's starter
// (starterChoices): the packs turned on for the room first, then the
// built-in packs' general-purpose examples. Clicking one fills the composer
// (a prompt with variables asks for them first); it never sends. The
// composer renders them only while the feed is empty.
export function StarterPrompts({ starters, onPick }: { readonly starters: readonly PromptChoice[]; readonly onPick: (c: PromptChoice) => void }) {
  if (starters.length === 0) return null;
  return (
    <div className="px-3 pt-2" role="group" aria-label="Example prompts">
      <p className="mb-1.5 flex items-center gap-1 px-1 text-xs text-[var(--color-fg-subtle)]">
        <SparkleIcon size={12} weight="fill" className="text-[var(--color-accent)]" aria-hidden="true" />
        {starters.some((c) => c.source === "room") ? "Start with" : "Try an example"}
      </p>
      {/* One scrolling row on a phone, wrapping on a wide composer. */}
      <div className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1 @md:flex-wrap @md:overflow-visible">
        {starters.map((c) => (
          <button
            key={`${c.componentId}/${c.prompt.id}`}
            type="button"
            onClick={() => onPick(c)}
            title={c.source === "room" ? `${c.prompt.body.trim()} (${c.packLabel})` : c.prompt.body.trim()}
            className={c.source === "room" ? `${CHIP} border-[var(--color-accent)]/30` : CHIP}
          >
            {c.prompt.title}
          </button>
        ))}
      </div>
    </div>
  );
}
