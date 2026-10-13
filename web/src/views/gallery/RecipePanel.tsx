import { useState } from "react";
import { BrainIcon, ChatTextIcon, DownloadSimpleIcon, PaperclipIcon, PuzzlePieceIcon } from "@phosphor-icons/react";
import type { ContentComponent } from "@/lib/contentComponents";
import { errorText } from "@/lib/errors";
import { formatBytes } from "@/lib/format";
import { saveBlob } from "@/lib/saveBlob";
import { recipeChips, recipeHasContent, recipePromptPreview, type RecipeChip, type WorkRecipe } from "@/lib/workGallery";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { useToast } from "@/ui/Toast";

// RecipePanel is a work's recipe in its detail
// (docs/prd/2026-10-13-works-gallery-and-make-similar.md §3, "Display"):
// the skills and the expert (and the prompt packs) as chips, the request's
// prompt collapsed after a few lines with "Expand", then the related files
// (the request's attachments, downloaded from the work). The agent that
// made it is a footnote: Make similar runs on the member's own.

const CHIP_ICONS: Record<RecipeChip["kind"], typeof BrainIcon> = { expert: BrainIcon, skill: PuzzlePieceIcon, prompt: ChatTextIcon };
const CHIP_TITLES: Record<RecipeChip["kind"], string> = { expert: "Expert", skill: "Skill pack", prompt: "Prompt pack" };

export function RecipePanel({ work, recipe }: { readonly work: Pick<ContentComponent, "id" | "version">; readonly recipe: WorkRecipe | undefined }) {
  const toast = useToast();
  const [expanded, setExpanded] = useState(false);
  if (!recipeHasContent(recipe)) {
    return <p className="text-sm text-[var(--color-fg-subtle)]">No recipe: Make similar starts from the work itself.</p>;
  }
  const chips = recipeChips(recipe);
  const preview = recipe.prompt ? recipePromptPreview(recipe.prompt) : null;
  const download = async (file: string, name: string) => {
    try {
      saveBlob(await workGalleryApi.file(work, file), name);
    } catch (e) {
      toast.error(errorText(e, `Could not download ${name}`));
    }
  };
  return (
    <section aria-label="Recipe" className="flex flex-col gap-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] px-4 py-3 text-sm">
      <h3 className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">How it was made</h3>
      {chips.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label="Expert and skills">
          {chips.map((c) => {
            const Icon = CHIP_ICONS[c.kind];
            return (
              <li
                key={`${c.kind}:${c.label}`}
                title={`${CHIP_TITLES[c.kind]}${c.version ? ` v${c.version}` : ""}`}
                className="inline-flex items-center gap-1 rounded-full border border-[var(--color-border)] bg-[var(--color-surface-2)] px-2.5 py-0.5 text-xs text-[var(--color-fg-muted)]"
              >
                <Icon size={12} aria-hidden />
                {c.label}
              </li>
            );
          })}
        </ul>
      )}
      {preview && (
        <div className="flex flex-col gap-1">
          <p className="whitespace-pre-wrap break-words text-[var(--color-fg)]" aria-label="Prompt">
            {expanded ? recipe.prompt : preview.text}
          </p>
          {preview.more && (
            <button type="button" onClick={() => setExpanded((v) => !v)} className="self-start text-xs text-[var(--color-accent)] hover:underline">
              {expanded ? "Collapse" : "Expand"}
            </button>
          )}
        </div>
      )}
      {(recipe.references ?? []).length > 0 && (
        <div className="flex flex-col gap-1">
          <h4 className="text-xs font-medium text-[var(--color-fg-subtle)]">Related files</h4>
          <ul className="flex flex-col gap-1" aria-label="Related files">
            {(recipe.references ?? []).map((r) => (
              <li key={r.file ?? r.name} className="flex min-w-0 items-center gap-2">
                <PaperclipIcon size={13} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                <span className="min-w-0 truncate">{r.name}</span>
                <span className="shrink-0 text-xs text-[var(--color-fg-subtle)]">{formatBytes(r.sizeBytes)}</span>
                {r.file && (
                  <button
                    type="button"
                    onClick={() => void download(r.file!, r.name)}
                    aria-label={`Download ${r.name}`}
                    className="tap ml-auto grid size-7 shrink-0 place-items-center rounded-full text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
                  >
                    <DownloadSimpleIcon size={14} />
                  </button>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
      {recipe.agent && (
        <p className="text-xs text-[var(--color-fg-subtle)]">
          Made by a {recipe.agent.provider} agent{recipe.agent.core ? ` on ${recipe.agent.core}` : ""}. Make similar uses your own.
        </p>
      )}
    </section>
  );
}
