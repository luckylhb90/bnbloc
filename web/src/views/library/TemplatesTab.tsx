import { useCallback, useEffect, useState } from "react";
import { GlobeIcon, LockIcon, PencilSimpleIcon, PlusIcon, SquaresFourIcon, TrashIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { relativeTime } from "@/lib/format";
import { templateSummary, type RoomTemplate } from "@/lib/roomTemplates";
import { roomTemplatesApi } from "@/lib/roomTemplatesApi";
import { Button } from "@/ui/Button";
import { Card, Skeleton } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { EmptyState, PageHeader } from "@/ui/Page";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { InstantiateTemplateDialog, TemplateEditorDialog } from "@/views/library/TemplateDialogs";
import { UserName } from "@/views/UserName";
import { can } from "@/lib/permissions";

type Scope = "all" | "mine";
const SCOPES = [
  { value: "all" as const, label: "All" },
  { value: "mine" as const, label: "Mine" },
];

// TemplatesTab is the Library's room-template library (N3): the member's own
// templates plus every template shared with their organization. "Create
// room" instantiates one; authors edit/delete, org admins remove shared ones.
export function TemplatesTab({ tabs, onOpenRoom }: { tabs: React.ReactNode; onOpenRoom: (roomId: string) => void }) {
  const { subject, role } = useAuth();
  const toast = useToast();
  const [scope, setScope] = useState<Scope>("all");
  const [templates, setTemplates] = useState<RoomTemplate[] | null>(null);
  const [editing, setEditing] = useState<RoomTemplate | "new" | null>(null);
  const [using, setUsing] = useState<RoomTemplate | null>(null);
  const [deleting, setDeleting] = useState<RoomTemplate | null>(null);
  const [epoch, setEpoch] = useState(0);
  const isAdmin = can(role, "manage_org");
  const reload = useCallback(() => setEpoch((n) => n + 1), []);

  useEffect(() => {
    let live = true;
    roomTemplatesApi
      .list(scope === "mine")
      .then((r) => live && setTemplates(r.templates ?? []))
      .catch(() => {
        if (!live) return;
        setTemplates([]);
        toast.error("Could not load templates");
      });
    return () => {
      live = false;
    };
  }, [scope, epoch, toast]);

  const remove = async (t: RoomTemplate) => {
    try {
      if (t.createdBy === subject) await roomTemplatesApi.remove(t.templateId);
      else await roomTemplatesApi.moderate(t.templateId);
      toast.success("Template deleted");
      reload();
    } catch {
      toast.error("Could not delete the template");
    }
  };

  return (
    <>
      <PageHeader title="Library" subtitle="Room templates for recurring setups" actions={tabs} />
      <div className="p-6">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <Segment options={SCOPES} value={scope} onChange={setScope} className="w-44" />
          <div className="flex-1" />
          <Button variant="primary" onClick={() => setEditing("new")}>
            <PlusIcon size={16} />
            New template
          </Button>
        </div>
        {templates === null ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-36" />
            ))}
          </div>
        ) : templates.length === 0 ? (
          <EmptyState
            icon={<SquaresFourIcon size={26} />}
            title={scope === "mine" ? "You have no templates" : "No room templates yet"}
            hint="Save a room as a template from its header, or create one here."
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {templates.map((t) => {
              const mine = t.createdBy === subject;
              const canDelete = mine || (isAdmin && t.visibility === "tenant");
              return (
                <Card key={t.templateId} className="flex min-w-0 flex-col gap-2 p-4">
                  <div className="flex items-start gap-2">
                    <h3 className="min-w-0 flex-1 truncate font-medium" title={t.name}>
                      {t.name}
                    </h3>
                    <span title={t.visibility === "tenant" ? "Shared with your organization" : "Only you"}>
                      {t.visibility === "tenant" ? (
                        <GlobeIcon size={14} className="text-[var(--color-fg-subtle)]" />
                      ) : (
                        <LockIcon size={14} className="text-[var(--color-fg-subtle)]" />
                      )}
                    </span>
                  </div>
                  {t.description && <p className="line-clamp-3 text-sm text-[var(--color-fg-muted)]">{t.description}</p>}
                  <p className="text-xs text-[var(--color-fg-subtle)]">{templateSummary(t.body)}</p>
                  <div className="mt-auto flex items-center gap-2 pt-1 text-xs text-[var(--color-fg-subtle)]">
                    <span className="min-w-0 flex-1 truncate">
                      {mine ? "You" : <UserName id={t.createdBy} />} · {relativeTime(t.updatedAtMs)}
                      {t.useCount > 0 ? ` · used ${t.useCount}×` : ""}
                    </span>
                    {mine && (
                      <button type="button" onClick={() => setEditing(t)} title="Edit" aria-label="Edit template" className="tactile hover:text-[var(--color-fg)]">
                        <PencilSimpleIcon size={15} />
                      </button>
                    )}
                    {canDelete && (
                      <button type="button" onClick={() => setDeleting(t)} title="Delete" aria-label="Delete template" className="tactile hover:text-[var(--color-danger)]">
                        <TrashIcon size={15} />
                      </button>
                    )}
                    <Button size="sm" variant="primary" onClick={() => setUsing(t)}>
                      Create room
                    </Button>
                  </div>
                </Card>
              );
            })}
          </div>
        )}
      </div>

      <TemplateEditorDialog template={editing} onClose={() => setEditing(null)} onSaved={reload} />
      <InstantiateTemplateDialog
        open={!!using}
        template={using}
        onClose={() => setUsing(null)}
        onCreated={(roomId) => {
          setUsing(null);
          reload();
          onOpenRoom(roomId);
        }}
      />
      <ConfirmDialog
        open={!!deleting}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && void remove(deleting)}
        title="Delete template?"
        description={deleting ? `“${deleting.name}” will be removed. Rooms created from it are not affected.` : undefined}
        confirmLabel="Delete"
        danger
      />
    </>
  );
}
