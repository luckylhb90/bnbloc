import { useEffect, useState } from "react";
import { BooksIcon } from "@phosphor-icons/react";
import { api, type Prompt } from "@/lib/api";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { UsePromptDialog } from "@/views/library/PromptDialogs";
import { UserName } from "@/views/UserName";

// SharedPrompts shows the organization's shared prompts on the Plaza (the
// tenant's shared showcase): the most recently updated few, each usable in a
// room, with a link to the full library. Hidden when nothing is shared.
export function SharedPrompts({ onOpenRoom }: { onOpenRoom: (roomId: string) => void }) {
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [using, setUsing] = useState<Prompt | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .listPrompts()
      .then((r) => !cancelled && setPrompts((r.prompts ?? []).filter((p) => p.visibility === "tenant").slice(0, 6)))
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
        <a href="#/library" className="text-sm text-[var(--color-accent)] hover:underline">
          Open library
        </a>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {prompts.map((p) => (
          <Card key={p.promptId} className="flex min-w-0 flex-col gap-1.5 p-4">
            <h3 className="truncate font-medium" title={p.title}>
              {p.title}
            </h3>
            <p className="line-clamp-2 text-sm text-[var(--color-fg-muted)]">{p.body}</p>
            <div className="mt-auto flex items-center justify-between gap-2 pt-1 text-xs text-[var(--color-fg-subtle)]">
              <span className="truncate">
                by <UserName id={p.author} />
              </span>
              <Button size="sm" variant="outline" onClick={() => setUsing(p)}>
                Use
              </Button>
            </div>
          </Card>
        ))}
      </div>
      <UsePromptDialog prompt={using} onClose={() => setUsing(null)} onOpenRoom={onOpenRoom} />
    </section>
  );
}
