import { useCallback, useEffect, useMemo, useState } from "react";
import { BrainIcon, TrashIcon, WrenchIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent, type ContentUse } from "@/lib/contentComponents";
import { apiErrorText, contentApi } from "@/lib/contentComponentsApi";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Segment } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { ContentViewDialog, PermissionBadges } from "@/views/library/ContentViewDialog";
import { useMyRoomRole } from "@/views/room/useMyRoomRole";
import { UserName } from "@/views/UserName";

type PinMode = "latest" | "pinned";
const PIN_MODES = [
  { value: "pinned" as const, label: "This version" },
  { value: "latest" as const, label: "Follow latest" },
];

// RoomContentSection is the room settings' experts and skills: experts
// pinned for the room's composer (and @mentions), and skill packs every
// agent in the room loads. The room's owner and admins change them; every
// member sees them.
export function RoomContentSection({ roomId }: { readonly roomId: string }) {
  const { subject } = useAuth();
  const role = useMyRoomRole(roomId, subject);
  const canEdit = role === "owner" || role === "admin";
  const toast = useToast();
  const [uses, setUses] = useState<readonly ContentUse[] | null>(null);
  const [mine, setMine] = useState<readonly ContentComponent[]>([]);
  const [adding, setAdding] = useState("");
  const [mode, setMode] = useState<PinMode>("pinned");
  const [viewing, setViewing] = useState<ContentComponent | null>(null);
  const [epoch, setEpoch] = useState(0);
  const reload = useCallback(() => setEpoch((n) => n + 1), []);

  useEffect(() => {
    let live = true;
    contentApi
      .uses(roomId)
      .then((r) => live && setUses(r.room ?? []))
      .catch((e) => {
        if (!live) return;
        setUses([]);
        toast.error(apiErrorText(e, "Could not load the room's experts and skills"));
      });
    return () => {
      live = false;
    };
  }, [roomId, epoch, toast]);
  useEffect(() => {
    if (!canEdit) return;
    let live = true;
    contentApi
      .list()
      .then((r) => live && setMine((r.components ?? []).filter((c) => c.mine)))
      .catch(() => live && setMine([]));
    return () => {
      live = false;
    };
  }, [canEdit, epoch]);

  const addable = useMemo(() => mine.filter((c) => !(uses ?? []).some((u) => u.componentId === c.id)), [mine, uses]);

  const put = async (componentId: string, version: string | undefined, done: string) => {
    try {
      await contentApi.putUse("room", componentId, { roomId, version });
      toast.success(done);
      reload();
    } catch (e) {
      toast.error(apiErrorText(e, "Could not change the room's experts and skills"));
    }
  };
  const add = () => {
    const c = mine.find((x) => x.id === adding);
    if (!c) return;
    setAdding("");
    void put(c.id, mode === "pinned" ? c.latestVersion : undefined, `${contentLabel(c)} added to the room`);
  };
  const remove = async (u: ContentUse) => {
    try {
      await contentApi.removeUse("room", u.componentId, roomId);
      toast.success(`${u.component ? contentLabel(u.component) : "It"} removed from the room`);
      reload();
    } catch (e) {
      toast.error(apiErrorText(e, "Could not remove it"));
    }
  };

  const row = (u: ContentUse) => {
    const c = u.component;
    if (!c) return null;
    return (
      <li key={u.componentId} className="flex flex-col gap-1 rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-2">
        <div className="flex min-w-0 items-center gap-2 text-sm">
          {u.kind === "expert" ? <BrainIcon size={15} className="shrink-0 text-[var(--color-accent)]" /> : <WrenchIcon size={15} className="shrink-0" />}
          <button type="button" onClick={() => setViewing(c)} className="tactile min-w-0 truncate text-left font-medium hover:underline">
            {contentLabel(c)}
          </button>
          <span className="shrink-0 font-mono text-xs text-[var(--color-fg-subtle)]">
            {u.pinnedVersion ? `v${u.pinnedVersion}` : `latest (v${c.latestVersion})`}
          </span>
          {u.updateAvailable && <Badge tone="accent">v{c.latestVersion} available</Badge>}
          <span className="min-w-0 flex-1 truncate text-xs text-[var(--color-fg-subtle)]">
            by {c.mine ? "you" : <UserName id={c.owner} />}
          </span>
          {canEdit && u.updateAvailable && (
            <Button size="sm" variant="outline" onClick={() => void put(u.componentId, c.latestVersion, `Moved to v${c.latestVersion}`)}>
              Update
            </Button>
          )}
          {canEdit && (
            <button type="button" onClick={() => void remove(u)} aria-label={`Remove ${c.name}`} title="Remove from the room" className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]">
              <TrashIcon size={15} />
            </button>
          )}
        </div>
        <PermissionBadges component={c} />
      </li>
    );
  };

  const experts = (uses ?? []).filter((u) => u.kind === "expert");
  const skills = (uses ?? []).filter((u) => u.kind === "skill");
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] p-4">
      <p className="text-sm font-medium">Experts and skills</p>
      <p className="mb-3 text-xs text-[var(--color-fg-muted)]">
        Pinned experts are offered in this room's composer and as @name. Skill packs load for every agent in the room.
        {!canEdit && role !== null ? " The room's owner and admins change them." : ""}
      </p>
      {uses === null ? (
        <p className="text-xs text-[var(--color-fg-subtle)]">Loading…</p>
      ) : (
        <div className="flex flex-col gap-3">
          <section>
            <p className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Pinned experts</p>
            {experts.length === 0 ? <p className="text-xs text-[var(--color-fg-subtle)]">None</p> : <ul className="flex flex-col gap-1.5">{experts.map(row)}</ul>}
          </section>
          <section>
            <p className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Room skill packs</p>
            {skills.length === 0 ? <p className="text-xs text-[var(--color-fg-subtle)]">None</p> : <ul className="flex flex-col gap-1.5">{skills.map(row)}</ul>}
          </section>
          {canEdit && (
            <div className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <Select value={adding} onChange={(e) => setAdding(e.target.value)} aria-label="Add an expert or skill pack">
                  <option value="">{addable.length > 0 ? "Add one of yours…" : "Nothing of yours to add (create in the Library)"}</option>
                  {addable.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.kind === "expert" ? "Expert" : "Skill pack"}: {contentLabel(c)} (v{c.latestVersion})
                    </option>
                  ))}
                </Select>
                <Button variant="secondary" onClick={add} disabled={!adding}>
                  Add
                </Button>
              </div>
              <Segment options={PIN_MODES} value={mode} onChange={setMode} className="w-64" />
            </div>
          )}
        </div>
      )}
      <ContentViewDialog component={viewing} roomId={roomId} onClose={() => setViewing(null)} />
    </div>
  );
}
