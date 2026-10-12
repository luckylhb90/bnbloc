import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ChatTextIcon,
  DoorOpenIcon,
  EyeIcon,
  MagnifyingGlassIcon,
  PencilSimpleIcon,
  PlusIcon,
  SparkleIcon,
  TrashIcon,
  UploadSimpleIcon,
} from "@phosphor-icons/react";
import { contentLabel, matchesContent, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { announcePromptPacksChanged } from "@/lib/promptPacksApi";
import { relativeTime } from "@/lib/format";
import { packUses, PERSONAL_PROMPT_PACK } from "@/lib/promptPacks";
import { takeLibraryQuery } from "@/lib/team";
import { improvePrompt, workshopPrompt } from "@/lib/workshop";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { Input } from "@/ui/Input";
import { EmptyState, PageHeader } from "@/ui/Page";
import { Switch } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { AddToRoomDialog, PromptImportDialog, PromptPackViewDialog } from "@/views/library/PromptPackDialogs";
import { PromptPackEditorDialog, type PromptPackTarget } from "@/views/library/PromptPackEditorDialog";
import { ContentByline, PublishDialog, ShareDialog, SharingActions, SharingStatus } from "@/views/library/ContentSharing";
import { useOpenWorkshop } from "@/views/library/workshopFlow";
import { errorText } from "@/lib/errors";

// PromptsTab is the Library's prompt packs: the member's packs (and those
// published to them) with search, view, create by form, import, new
// version, delete, "on for me" (their composer) and "turn on for a room".
export function PromptsTab({ tabs, onOpenRoom }: { readonly tabs: React.ReactNode; readonly onOpenRoom: (roomId: string) => void }) {
  const toast = useToast();
  const workshop = useOpenWorkshop(onOpenRoom);
  const [items, setItems] = useState<ContentComponent[] | null>(null);
  const [enabled, setEnabled] = useState<ReadonlySet<string>>(new Set());
  const [q, setQ] = useState(takeLibraryQuery);
  const [editing, setEditing] = useState<PromptPackTarget | null>(null);
  const [viewing, setViewing] = useState<ContentComponent | null>(null);
  const [deleting, setDeleting] = useState<ContentComponent | null>(null);
  const [rooming, setRooming] = useState<ContentComponent | null>(null);
  const [sharing, setSharing] = useState<ContentComponent | null>(null);
  const [publishing, setPublishing] = useState<ContentComponent | null>(null);
  const [importing, setImporting] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const reload = useCallback(() => setEpoch((n) => n + 1), []);

  useEffect(() => {
    let live = true;
    setItems(null);
    Promise.all([contentApi.list("prompt"), contentApi.uses()])
      .then(([list, uses]) => {
        if (!live) return;
        setItems(list.components ?? []);
        setEnabled(new Set(uses.member.map((u) => u.componentId)));
      })
      .catch((e) => {
        if (!live) return;
        setItems([]);
        toast.error(errorText(e, "Could not load the prompt packs"));
      });
    return () => {
      live = false;
    };
  }, [epoch, toast]);

  const shown = useMemo(() => (items ?? []).filter((c) => matchesContent(c, q)), [items, q]);
  const mine = useMemo(() => (items ?? []).filter((c) => c.mine), [items]);
  const closeEditor = useCallback(() => setEditing(null), []);

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
      announcePromptPacksChanged();
      toast.success(on ? `${contentLabel(c)} is on in your composer` : `${contentLabel(c)} is off in your composer`);
    } catch (e) {
      toast.error(errorText(e, "Could not change it"));
    }
  };
  // A new pack made here is on for its author at once (their composer
  // offers it); they can turn it off. A new version keeps their choice.
  const saved = async (c: ContentComponent, isNew: boolean) => {
    if (isNew && !enabled.has(c.id)) {
      await contentApi.putUse("member", c.id).catch(() => {});
      announcePromptPacksChanged();
    }
    reload();
  };
  const remove = async (c: ContentComponent) => {
    try {
      await contentApi.remove(c.id);
      toast.success(`Deleted ${c.name}`);
      announcePromptPacksChanged();
      reload();
    } catch (e) {
      toast.error(errorText(e, "Could not delete it"));
    }
  };

  return (
    <>
      <PageHeader title="Library" subtitle="Prompts with variables, inserted from the composer or as /commands" actions={tabs} />
      <div className="p-6">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <div className="relative min-w-[16rem] max-w-lg flex-1">
            <MagnifyingGlassIcon size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
            <Input className="pl-9" placeholder="Search prompts…" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <div className="flex-1" />
          <Button variant="ghost" onClick={() => setImporting(true)}>
            <UploadSimpleIcon size={16} />
            Import
          </Button>
          <Button variant="outline" onClick={() => setEditing({ kind: "new" })}>
            <PlusIcon size={16} />
            Create manually
          </Button>
          <Button variant="primary" onClick={() => void workshop.open(workshopPrompt("prompt"))} disabled={workshop.busy}>
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
            icon={<ChatTextIcon size={26} />}
            title={q ? "Nothing matches" : "No prompt packs yet"}
            hint={
              q
                ? undefined
                : "Save a message as a prompt (it goes to My prompts), create a pack with AI or by hand, or import a markdown or json file of prompts. Turn a pack on for yourself or for a room."
            }
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {shown.map((c) => {
              const used = packUses(c);
              return (
                <Card key={c.id} className="flex min-w-0 flex-col gap-2 p-4">
                  <div className="min-w-0">
                    <h3 className="truncate font-medium" title={contentLabel(c)}>
                      {contentLabel(c)}
                    </h3>
                    <p className="truncate font-mono text-xs text-[var(--color-fg-subtle)]">
                      {c.name} · v{c.latestVersion} · {c.prompts?.length ?? 0} prompts
                    </p>
                  </div>
                  {c.description && <p className="line-clamp-2 text-sm text-[var(--color-fg-muted)]">{c.description}</p>}
                  {(c.prompts?.length ?? 0) > 0 && (
                    <div className="flex flex-wrap gap-1">
                      {c.prompts?.slice(0, 6).map((p) => (
                        <Badge key={p.id} title={p.body}>
                          {p.slash ?? p.title}
                        </Badge>
                      ))}
                      {(c.prompts?.length ?? 0) > 6 && <Badge>+{(c.prompts?.length ?? 0) - 6}</Badge>}
                    </div>
                  )}
                  <Switch checked={enabled.has(c.id)} onChange={(on) => void toggleForMe(c, on)} label="On in my composer" />
                  <SharingStatus component={c} onChanged={reload} />
                  <div className="mt-auto flex items-center gap-2 pt-1 text-xs text-[var(--color-fg-subtle)]">
                    <span className="min-w-0 flex-1 truncate">
                      <ContentByline component={c} /> · {relativeTime(c.updatedAtMs)}
                      {used > 0 ? ` · used ${used}×` : ""}
                    </span>
                    <button type="button" onClick={() => setViewing(c)} title="View" aria-label={`View ${c.name}`} className="tactile hover:text-[var(--color-fg)]">
                      <EyeIcon size={15} />
                    </button>
                    <button type="button" onClick={() => setRooming(c)} title="Turn on for a room" aria-label={`Turn ${c.name} on for a room`} className="tactile hover:text-[var(--color-fg)]">
                      <DoorOpenIcon size={15} />
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
                    <SharingActions component={c} onShare={setSharing} onPublish={setPublishing} />
                    {c.removable && (
                      <button type="button" onClick={() => setDeleting(c)} title="Delete" aria-label={`Delete ${c.name}`} className="tactile hover:text-[var(--color-danger)]">
                        <TrashIcon size={15} />
                      </button>
                    )}
                  </div>
                </Card>
              );
            })}
          </div>
        )}
      </div>
      <PromptPackEditorDialog target={editing} onClose={closeEditor} onSaved={(c) => void saved(c, !!editing && !("base" in editing))} />
      <PromptPackViewDialog pack={viewing} onClose={() => setViewing(null)} />
      <PromptImportDialog open={importing} mine={mine} onClose={() => setImporting(false)} onImported={(c) => void saved(c, !mine.some((m) => m.id === c.id))} />
      <AddToRoomDialog pack={rooming} onClose={() => setRooming(null)} />
      <ShareDialog component={sharing} onClose={() => setSharing(null)} />
      <PublishDialog component={publishing} onClose={() => setPublishing(null)} onChanged={reload} />
      <ConfirmDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && void remove(deleting)}
        title="Delete prompt pack?"
        description={
          deleting
            ? `${contentLabel(deleting)} and all its versions will be removed, and rooms using it lose it.${deleting.name === PERSONAL_PROMPT_PACK ? " Your next Save as prompt starts a new one." : ""}`
            : undefined
        }
        confirmLabel="Delete"
        danger
      />
    </>
  );
}
