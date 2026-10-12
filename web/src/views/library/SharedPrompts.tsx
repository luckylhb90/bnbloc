import { useEffect, useState } from "react";
import { BooksIcon, CopyIcon } from "@phosphor-icons/react";
import { contentApi } from "@/lib/contentComponentsApi";
import { sharedPrompts, type SharedPrompt } from "@/lib/contentSharing";
import { queueLibraryTab } from "@/lib/team";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { useCopy } from "@/ui/useCopy";
import { UserName } from "@/views/UserName";

// SharedPrompts shows the organization's (and the platform's) prompts on the
// Plaza: the prompts of the prompt packs published to everyone, the most
// recently updated few, with a link to the Library's prompts. Hidden when
// nothing is published.
export function SharedPrompts() {
  const copy = useCopy();
  const [prompts, setPrompts] = useState<SharedPrompt[]>([]);

  useEffect(() => {
    let cancelled = false;
    contentApi
      .list("prompt")
      .then((r) => !cancelled && setPrompts(sharedPrompts(r.components ?? [], 6)))
      .catch(() => {
        // Optional showcase: a failure simply hides it.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (prompts.length === 0) return null;
  return (
    <section className="mt-10">
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
          <BooksIcon size={16} />
          Shared prompts
        </h2>
        <a href="#/library" onClick={() => queueLibraryTab("prompts")} className="text-sm text-[var(--color-accent)] hover:underline">
          Open library
        </a>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {prompts.map((p) => (
          <Card key={`${p.packId}/${p.id}`} className="flex min-w-0 flex-col gap-1.5 p-4">
            <h3 className="truncate font-medium" title={p.title}>
              {p.title}
              {p.slash && <span className="ml-2 font-mono text-xs text-[var(--color-fg-subtle)]">{p.slash}</span>}
            </h3>
            <p className="line-clamp-2 text-sm text-[var(--color-fg-muted)]">{p.body}</p>
            <div className="mt-auto flex items-center justify-between gap-2 pt-1 text-xs text-[var(--color-fg-subtle)]">
              <span className="truncate">
                {p.packName}
                {p.author ? (
                  <>
                    {" · "}
                    <UserName id={p.author} />
                  </>
                ) : (
                  " · Platform"
                )}
              </span>
              <Button
                size="sm"
                variant="outline"
                onClick={() => void copy(p.body, "prompt")}
              >
                <CopyIcon size={13} />
                Copy
              </Button>
            </div>
          </Card>
        ))}
      </div>
    </section>
  );
}
