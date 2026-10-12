import { useCallback, useEffect, useMemo, useState } from "react";
import {
  BrainIcon,
  EyeIcon,
  MagnifyingGlassIcon,
  PencilSimpleIcon,
  PlusIcon,
  SparkleIcon,
  TrashIcon,
  UploadSimpleIcon,
  WrenchIcon,
} from "@phosphor-icons/react";
import { contentLabel, isBuiltin, matchesContent, type ContentComponent, type ContentKind } from "@/lib/contentComponents";
import { apiErrorText, contentApi } from "@/lib/contentComponentsApi";
import { relativeTime } from "@/lib/format";
import { improvePrompt, workshopPrompt } from "@/lib/workshop";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { Input } from "@/ui/Input";
import { EmptyState, PageHeader } from "@/ui/Page";
import { Switch } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { ContentEditorDialog, ImportDialog, type EditorTarget } from "@/views/library/ContentEditorDialog";
import { ContentViewDialog, PermissionBadges } from "@/views/library/ContentViewDialog";
import { useOpenWorkshop } from "@/views/library/workshopFlow";
import { UserName } from "@/views/UserName";

const COPY: Record<ContentKind, { subtitle: string; empty: string; hint: string; search: string }> = {
  expert: {
    subtitle: "Roles your agents can wear: instructions, skills and quick prompts",
    empty: "No experts yet",
    hint: "Create one with AI: the Expert builder asks a few questions, drafts it and saves it. Or create one manually, or import a folder with INSTRUCTIONS.md or a plugin with an agent. Pick it in a room's composer.",
    search: "Search experts…",
  },
  skill: {
    subtitle: "Capabilities your agents load when a task matches",
    empty: "No skill packs yet",
    hint: "Create one with AI, create one manually, or import a skill folder (SKILL.md). Turn it on for all your agents, or for a room in its settings.",
    search: "Search skills…",
  },
};

// ContentTab is the Library's Experts or Skills tab: the member's content
// components of one kind with search, view, create (with AI in the expert
// workshop, or manually), import, new version and delete; a skill pack can be
// turned on for all the member's agents. Built-in content is read-only.
export function ContentTab({
  kind,
  tabs,
  onOpenRoom,
}: {
  readonly kind: ContentKind;
  readonly tabs: React.ReactNode;
  readonly onOpenRoom: (roomId: string) => void;
}) {
  const toast = useToast();
  const workshop = useOpenWorkshop(onOpenRoom);
  const [items, setItems] = useState<ContentComponent[] | null>(null);
  const [enabled, setEnabled] = useState<Set<string>>(new Set());
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<EditorTarget | null>(null);
  const [viewing, setViewing] = useState<ContentComponent | null>(null);
  const [deleting, setDeleting] = useState<ContentComponent | null>(null);
  const [importing, setImporting] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const reload = useCallback(() => setEpoch((n) => n + 1), []);
  const copy = COPY[kind];

  useEffect(() => {
    let live = true;
    setItems(null);
    Promise.all([contentApi.list(kind), kind === "skill" ? contentApi.uses() : Promise.resolve({ member: [] })])
      .then(([list, uses]) => {
        if (!live) return;
        setItems(list.components ?? []);
        setEnabled(new Set(uses.member.map((u) => u.componentId)));
      })
      .catch((e) => {
        if (!live) return;
        setItems([]);
        toast.error(apiErrorText(e, "Could not load the library"));
      });
    return () => {
      live = false;
    };
  }, [kind, epoch, toast]);

  const shown = useMemo(() => (items ?? []).filter((c) => matchesContent(c, q)), [items, q]);
  const mine = useMemo(() => (items ?? []).filter((c) => c.mine), [items]);
  const closeEditor = useCallback(() => setEditing(null), []);

  const remove = async (c: ContentComponent) => {
    try {
      await contentApi.remove(c.id);
      toast.success(`Deleted ${c.name}`);
      reload();
    } catch (e) {
      toast.error(apiErrorText(e, "Could not delete it"));
    }
  };
  const toggleForMe = async (c: ContentComponent, on: boolean) => {
    try {
      if (on) await contentApi.putUse("member", c.id);
      else await contentApi.removeUse("member", c.id);
      setEnabled((cur) => {
        const next = new Set(cur);
        if (on) next.add(c.id);
        else next.delete(c.id);
        return next;
      });
      toast.success(on ? `${contentLabel(c)} is on for all your agents` : `${contentLabel(c)} is off for your agents`);
    } catch (e) {
      toast.error(apiErrorText(e, "Could not change it"));
    }
  };

  return (
    <>
      <PageHeader title="Library" subtitle={copy.subtitle} actions={tabs} />
      <div className="p-6">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <div className="relative min-w-[16rem] max-w-lg flex-1">
            <MagnifyingGlassIcon size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
            <Input className="pl-9" placeholder={copy.search} value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <div className="flex-1" />
          <Button variant="ghost" onClick={() => setImporting(true)}>
            <UploadSimpleIcon size={16} />
            Import
          </Button>
          <Button variant="outline" onClick={() => setEditing({ kind })}>
            <PlusIcon size={16} />
            Create manually
          </Button>
          <Button variant="primary" onClick={() => void workshop.open(workshopPrompt(kind))} disabled={workshop.busy}>
            <SparkleIcon size={16} weight="fill" />
            {workshop.busy ? "Opening…" : "Create with AI"}
          </Button>
        </div>
        {items === null ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-40" />
            ))}
          </div>
        ) : shown.length === 0 ? (
          <EmptyState
            icon={kind === "expert" ? <BrainIcon size={26} /> : <WrenchIcon size={26} />}
            title={q ? "Nothing matches" : copy.empty}
            hint={q ? undefined : copy.hint}
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {shown.map((c) => (
              <Card key={c.id} className="flex min-w-0 flex-col gap-2 p-4">
                <div className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <h3 className="truncate font-medium" title={contentLabel(c)}>
                      {contentLabel(c)}
                    </h3>
                    <p className="truncate font-mono text-xs text-[var(--color-fg-subtle)]">
                      {c.name} · v{c.latestVersion}
                    </p>
                  </div>
                  {isBuiltin(c) && <Badge tone="accent">Built-in</Badge>}
                  {c.category && <Badge>{c.category}</Badge>}
                </div>
                {c.description && <p className="line-clamp-3 text-sm text-[var(--color-fg-muted)]">{c.description}</p>}
                {(c.skills?.length ?? 0) > 0 && (
                  <div className="flex flex-wrap gap-1">
                    {c.skills?.map((s) => (
                      <Badge key={s.name} title={s.description}>
                        {s.name}
                      </Badge>
                    ))}
                  </div>
                )}
                <PermissionBadges component={c} />
                {c.needsSandbox && (
                  <p className="text-xs text-[var(--color-fg-subtle)]">Runs in the agent sandbox when someone else uses it</p>
                )}
                {kind === "skill" && c.mine && (
                  <Switch checked={enabled.has(c.id)} onChange={(on) => void toggleForMe(c, on)} label="On for all my agents" />
                )}
                <div className="mt-auto flex items-center gap-2 pt-1 text-xs text-[var(--color-fg-subtle)]">
                  <span className="min-w-0 flex-1 truncate">
                    {isBuiltin(c) ? (
                      "Built-in · bnbloc"
                    ) : (
                      <>
                        {c.mine ? "You" : <UserName id={c.owner} />} · {relativeTime(c.updatedAtMs)}
                      </>
                    )}
                    {c.uses > 0 ? ` · in use ${c.uses}×` : ""}
                  </span>
                  <button type="button" onClick={() => setViewing(c)} title="View" aria-label={`View ${c.name}`} className="tactile hover:text-[var(--color-fg)]">
                    <EyeIcon size={15} />
                  </button>
                  {c.mine && (
                    <button
                      type="button"
                      onClick={() => void workshop.open(improvePrompt(c))}
                      disabled={workshop.busy}
                      title="Improve with AI"
                      aria-label={`Improve ${c.name} with AI`}
                      className="tactile hover:text-[var(--color-fg)]"
                    >
                      <SparkleIcon size={15} />
                    </button>
                  )}
                  {c.mine && (
                    <button type="button" onClick={() => setEditing({ base: c })} title="New version" aria-label={`New version of ${c.name}`} className="tactile hover:text-[var(--color-fg)]">
                      <PencilSimpleIcon size={15} />
                    </button>
                  )}
                  {c.mine && (
                    <button type="button" onClick={() => setDeleting(c)} title="Delete" aria-label={`Delete ${c.name}`} className="tactile hover:text-[var(--color-danger)]">
                      <TrashIcon size={15} />
                    </button>
                  )}
                </div>
              </Card>
            ))}
          </div>
        )}
      </div>
      <ContentEditorDialog target={editing} onClose={closeEditor} onSaved={reload} />
      <ContentViewDialog
        component={viewing}
        onClose={() => setViewing(null)}
        onImprove={(c) => {
          setViewing(null);
          void workshop.open(improvePrompt(c));
        }}
      />
      <ImportDialog kind={kind} open={importing} mine={mine} onClose={() => setImporting(false)} onImported={reload} />
      <ConfirmDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && void remove(deleting)}
        title={kind === "expert" ? "Delete expert?" : "Delete skill pack?"}
        description={deleting ? `${contentLabel(deleting)} and all its versions will be removed, and rooms using it lose it.` : undefined}
        confirmLabel="Delete"
        danger
      />
    </>
  );
}
