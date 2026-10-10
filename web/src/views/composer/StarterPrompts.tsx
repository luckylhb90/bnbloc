import { SparkleIcon } from "@phosphor-icons/react";
import { STARTER_PROMPTS } from "@/lib/onboarding";

// StarterPrompts are an empty room's example prompts, shown above the
// composer (PRD 2026-10-10 v2 §1). Clicking one fills the composer; it never
// sends. The composer renders them only while the feed is empty.
export function StarterPrompts({ onPick }: { readonly onPick: (text: string) => void }) {
  return (
    <div className="px-3 pt-2" role="group" aria-label="Example prompts">
      <p className="mb-1.5 flex items-center gap-1 px-1 text-xs text-[var(--color-fg-subtle)]">
        <SparkleIcon size={12} weight="fill" className="text-[var(--color-accent)]" aria-hidden="true" />
        Try an example
      </p>
      {/* One scrolling row on a phone, wrapping on a wide composer. */}
      <div className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1 @md:flex-wrap @md:overflow-visible">
        {STARTER_PROMPTS.map((p) => (
          <button
            key={p.label}
            type="button"
            onClick={() => onPick(p.text)}
            title={p.text.trim()}
            className="tactile max-w-[18rem] shrink-0 truncate rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-xs text-[var(--color-fg-muted)] hover:border-[var(--color-accent)]/40 hover:bg-[var(--color-accent-soft)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
          >
            {p.text.trim()}
          </button>
        ))}
      </div>
    </div>
  );
}
