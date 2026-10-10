import { useCallback, useEffect, useMemo, useState } from "react";
import {
  BooksIcon,
  CopyIcon,
  GlobeIcon,
  LockIcon,
  MagnifyingGlassIcon,
  PencilSimpleIcon,
  TrashIcon,
} from "@phosphor-icons/react";
import { api, type Prompt } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import { takeLibraryQuery } from "@/lib/team";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { Input } from "@/ui/Input";
import { EmptyState, PageHeader } from "@/ui/Page";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { PromptEditorDialog, UsePromptDialog } from "@/views/library/PromptDialogs";
import { TemplatesTab } from "@/views/library/TemplatesTab";
import { UserName } from "@/views/UserName";

type Scope = "all" | "mine";
const SCOPES = [
  { value: "all" as const, label: "All" },
  { value: "mine" as const, label: "Mine" },
];

// Library is the prompt library page (F2): the member's own prompts plus
// every prompt shared with their organization, searchable and filterable by
// tag. "Use" drops a prompt into a room's composer.
function PromptLibrary({ onOpenRoom, tabs }: { onOpenRoom: (roomId: string) => void; tabs: React.ReactNode }) {
  const { subject, role } = useAuth();
  const toast = useToast();
  const [q, setQ] = useState(() => takeLibraryQuery() ?? "");
  const [tag, setTag] = useState("");
  const [scope, setScope] = useState<Scope>("all");
  const [prompts, setPrompts] = useState<Prompt[] | null>(null);
  const [editing, setEditing] = useState<Prompt | null>(null);
  const [using, setUsing] = useState<Prompt | null>(null);
  const [deleting, setDeleting] = useState<Prompt | null>(null);
  const [epoch, setEpoch] = useState(0);
  const isAdmin = role === "org_admin" || role === "super_admin";

  // Debounced, latest-wins load (an older response never overwrites a newer
  // query's answer).
  useEffect(() => {
    let cancelled = false;
    const timer = window.setTimeout(
      () => {
        api
          .listPrompts({ q: q.trim(), tag, mine: scope === "mine" })
          .then((r) => !cancelled && setPrompts(r.prompts ?? []))
          .catch(() => {
            if (cancelled) return;
            setPrompts([]);
            toast.error("Could not load the library");
          });
      },
      q ? 250 : 0,
    );
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [q, tag, scope, epoch, toast]);

  const reload = useCallback(() => setEpoch((n) => n + 1), []);
  const tags = useMemo(() => {
    const all = new Set<string>();
    for (const p of prompts ?? []) for (const t of p.tags) all.add(t);
    if (tag) all.add(tag);
    return [...all].sort();
  }, [prompts, tag]);

  const copy = async (p: Prompt) => {
    try {
      await navigator.clipboard.writeText(p.body);
      toast.success("Prompt copied");
    } catch {
      toast.error("Could not copy to the clipboard");
    }
  };
  const remove = async (p: Prompt) => {
    try {
      if (p.author === subject) await api.deletePrompt(p.promptId);
      else await api.moderatePrompt(p.promptId);
      toast.success("Prompt deleted");
      reload();
    } catch {
      toast.error("Could not delete the prompt");
    }
  };

  return (
    <>
      <PageHeader title="Library" subtitle="Reusable prompts saved from your rooms" actions={tabs} />
      <div className="p-6">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <div className="relative min-w-[16rem] max-w-lg flex-1">
            <MagnifyingGlassIcon
              size={16}
              className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]"
            />
            <Input className="pl-9" placeholder="Search prompts…" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <Segment options={SCOPES} value={scope} onChange={setScope} className="w-44" />
        </div>
        {tags.length > 0 && (
          <div className="mb-5 flex flex-wrap gap-1.5">
            {["", ...tags].map((t) => (
              <button
                key={t || "__all"}
                type="button"
                onClick={() => setTag(t)}
                className={cn(
                  "tactile rounded-full border px-2.5 py-0.5 text-xs",
                  tag === t
                    ? "border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] text-[var(--color-accent)]"
                    : "border-[var(--color-border)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
                )}
              >
                {t ? `#${t}` : "All tags"}
              </button>
            ))}
          </div>
        )}

        {prompts === null ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-40" />
            ))}
          </div>
        ) : prompts.length === 0 ? (
          <EmptyState
            icon={<BooksIcon size={26} />}
            title={q || tag || scope === "mine" ? "No prompts match" : "Your library is empty"}
            hint="Save any message from a room with the bookmark action to reuse it here."
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {prompts.map((p) => {
              const mine = p.author === subject;
              const canDelete = mine || (isAdmin && p.visibility === "tenant");
              return (
                <Card key={p.promptId} className="flex min-w-0 flex-col gap-2 p-4">
                  <div className="flex items-start gap-2">
                    <h3 className="min-w-0 flex-1 truncate font-medium" title={p.title}>
                      {p.title}
                    </h3>
                    <span title={p.visibility === "tenant" ? "Shared with your organization" : "Only you"}>
                      {p.visibility === "tenant" ? (
                        <GlobeIcon size={14} className="text-[var(--color-fg-subtle)]" />
                      ) : (
                        <LockIcon size={14} className="text-[var(--color-fg-subtle)]" />
                      )}
                    </span>
                  </div>
                  <p className="line-clamp-4 whitespace-pre-wrap break-words text-sm text-[var(--color-fg-muted)]">{p.body}</p>
                  {p.tags.length > 0 && (
                    <div className="flex flex-wrap gap-1">
                      {p.tags.map((t) => (
                        <Badge key={t}>#{t}</Badge>
                      ))}
                    </div>
                  )}
                  <div className="mt-auto flex items-center gap-2 pt-1 text-xs text-[var(--color-fg-subtle)]">
                    <span className="min-w-0 flex-1 truncate">
                      {mine ? "You" : <UserName id={p.author} />} · {relativeTime(p.updatedAtMs)}
                      {p.useCount > 0 ? ` · used ${p.useCount}×` : ""}
                    </span>
                    <button type="button" onClick={() => void copy(p)} title="Copy" aria-label="Copy prompt" className="tactile hover:text-[var(--color-fg)]">
                      <CopyIcon size={15} />
                    </button>
                    {mine && (
                      <button type="button" onClick={() => setEditing(p)} title="Edit" aria-label="Edit prompt" className="tactile hover:text-[var(--color-fg)]">
                        <PencilSimpleIcon size={15} />
                      </button>
                    )}
                    {canDelete && (
                      <button type="button" onClick={() => setDeleting(p)} title="Delete" aria-label="Delete prompt" className="tactile hover:text-[var(--color-danger)]">
                        <TrashIcon size={15} />
                      </button>
                    )}
                    <Button size="sm" variant="primary" onClick={() => setUsing(p)}>
                      Use
                    </Button>
                  </div>
                </Card>
              );
            })}
          </div>
        )}
      </div>

      <PromptEditorDialog prompt={editing} onClose={() => setEditing(null)} onSaved={reload} />
      <UsePromptDialog prompt={using} onClose={() => setUsing(null)} onOpenRoom={onOpenRoom} />
      <ConfirmDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && void remove(deleting)}
        title="Delete prompt?"
        description={deleting ? `“${deleting.title}” will be removed from the library.` : undefined}
        confirmLabel="Delete"
        danger
      />
    </>
  );
}

type LibraryTab = "prompts" | "templates";
const LIBRARY_TABS = [
  { value: "prompts" as const, label: "Prompts" },
  { value: "templates" as const, label: "Templates" },
];

// Library holds the prompt library (F2) and the room-template library (N3).
export function Library({ onOpenRoom }: { onOpenRoom: (roomId: string) => void }) {
  const [tab, setTab] = useState<LibraryTab>("prompts");
  const tabs = <Segment options={LIBRARY_TABS} value={tab} onChange={setTab} className="w-56" />;
  return tab === "templates" ? (
    <TemplatesTab tabs={tabs} onOpenRoom={onOpenRoom} />
  ) : (
    <PromptLibrary tabs={tabs} onOpenRoom={onOpenRoom} />
  );
}
