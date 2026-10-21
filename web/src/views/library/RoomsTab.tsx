import { useMemo, useState } from "react";
import { DoorOpenIcon, EyeIcon, MagnifyingGlassIcon, PencilSimpleIcon, TrashIcon } from "@phosphor-icons/react";
import { contentLabel, matchesContent, type ContentComponent } from "@/lib/contentComponents";
import { relativeTime } from "@/lib/format";
import { roomSetupSummary } from "@/lib/roomComponents";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { Input } from "@/ui/Input";
import { EmptyState, PageHeader } from "@/ui/Page";
import { LoadFailed } from "@/ui/QueryState";
import { ContentByline, PublishDialog, ShareDialog, SharingActions, SharingStatus } from "@/views/library/ContentSharing";
import { ContentViewDialog } from "@/views/library/ContentViewDialog";
import { CreateRoomFromComponentDialog } from "@/views/library/CreateRoomFromComponentDialog";
import { useContentLibrary } from "@/views/library/useContentLibrary";
import { SaveRoomComponentDialog } from "@/views/room/SaveRoomComponentDialog";

const noText = () => "";

// RoomsTab is the Library's room components (room components PRD §2.1):
// rooms' setups saved from their rooms, the member's own, shared with them
// or published to their organization. "Create a room" makes a room of one;
// the author saves a new version (from a room, or here), shares and
// publishes it like any content.
export function RoomsTab({ tabs, onOpenRoom }: { readonly tabs: React.ReactNode; readonly onOpenRoom: (roomId: string) => void }) {
  const { items, error, reload, remove } = useContentLibrary("room", { onText: noText });
  const [q, setQ] = useState("");
  const [viewing, setViewing] = useState<ContentComponent | null>(null);
  const [editing, setEditing] = useState<ContentComponent | null>(null);
  const [using, setUsing] = useState<ContentComponent | null>(null);
  const [deleting, setDeleting] = useState<ContentComponent | null>(null);
  const [sharing, setSharing] = useState<ContentComponent | null>(null);
  const [publishing, setPublishing] = useState<ContentComponent | null>(null);
  const shown = useMemo(() => (items ?? []).filter((c) => matchesContent(c, q)), [items, q]);

  return (
    <>
      <PageHeader title="Library" subtitle="Rooms' setups to start a new room from" actions={tabs} />
      <div className="p-6">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <div className="relative min-w-[16rem] max-w-lg flex-1">
            <MagnifyingGlassIcon size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
            <Input className="pl-9" placeholder="Search room components…" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
        </div>
        {error ? (
          <LoadFailed message={error} onRetry={reload} />
        ) : items === null ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-40" />
            ))}
          </div>
        ) : shown.length === 0 ? (
          <EmptyState
            icon={<DoorOpenIcon size={26} />}
            title={q ? "Nothing matches" : "No room components yet"}
            hint={q ? undefined : "Save a room as a component from its header (its owner or an admin): its channels, scheduled tasks, expert, packs and apps, for anyone to start a room from."}
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
                  {c.scope === "platform" && <Badge tone="accent">Default</Badge>}
                  {c.category && <Badge>{c.category}</Badge>}
                </div>
                {c.description && <p className="line-clamp-3 text-sm text-[var(--color-fg-muted)]">{c.description}</p>}
                <p className="text-xs text-[var(--color-fg-subtle)]">{roomSetupSummary(c.room, c.requires)}</p>
                {(c.room?.sources.length ?? 0) > 0 && <p className="text-xs text-[var(--color-fg-subtle)]">Asks to read organization records (needs an admin's consent)</p>}
                <SharingStatus component={c} />
                <div className="mt-auto flex items-center gap-2 pt-1 text-xs text-[var(--color-fg-subtle)]">
                  <span className="min-w-0 flex-1 truncate">
                    <ContentByline component={c} />
                    {` · ${relativeTime(c.updatedAtMs)}`}
                    {c.uses > 0 ? ` · used ${c.uses}×` : ""}
                  </span>
                  <button type="button" onClick={() => setViewing(c)} title="View" aria-label={`View ${c.name}`} className="tactile hover:text-[var(--color-fg)]">
                    <EyeIcon size={15} />
                  </button>
                  {c.mine && (
                    <button type="button" onClick={() => setEditing(c)} title="New version" aria-label={`New version of ${c.name}`} className="tactile hover:text-[var(--color-fg)]">
                      <PencilSimpleIcon size={15} />
                    </button>
                  )}
                  <SharingActions component={c} onShare={setSharing} onPublish={setPublishing} />
                  {c.removable && (
                    <button type="button" onClick={() => setDeleting(c)} title="Delete" aria-label={`Delete ${c.name}`} className="tactile hover:text-[var(--color-danger)]">
                      <TrashIcon size={15} />
                    </button>
                  )}
                  <Button size="sm" variant="primary" onClick={() => setUsing(c)}>
                    Create a room
                  </Button>
                </div>
              </Card>
            ))}
          </div>
        )}
      </div>
      <ContentViewDialog component={viewing} onClose={() => setViewing(null)} />
      <SaveRoomComponentDialog target={editing ? { component: editing } : null} onClose={() => setEditing(null)} />
      <CreateRoomFromComponentDialog
        open={!!using}
        component={using}
        onClose={() => setUsing(null)}
        onCreated={(roomId) => {
          setUsing(null);
          onOpenRoom(roomId);
        }}
      />
      <ShareDialog component={sharing} onClose={() => setSharing(null)} />
      <PublishDialog component={publishing} onClose={() => setPublishing(null)} />
      <ConfirmDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && void remove(deleting)}
        title="Delete room component?"
        description={deleting ? `${contentLabel(deleting)} and all its versions will be removed. Rooms made from it are not affected.` : undefined}
        confirmLabel="Delete"
        danger
      />
    </>
  );
}
