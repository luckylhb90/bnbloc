import { useEffect, useState } from "react";
import {
  MagnifyingGlassIcon,
  PlusIcon,
  UsersIcon,
  GlobeIcon,
  LockIcon,
} from "@phosphor-icons/react";
import { api, type PlazaRoom } from "@/lib/api";
import { routeHash } from "@/lib/hashRoute";
import { useAuth } from "@/lib/auth";
import { can } from "@/lib/permissions";
import { errorText } from "@/lib/errors";
import { Button } from "@/ui/Button";
import { Input } from "@/ui/Input";
import { Card, Badge, Skeleton } from "@/ui/Card";
import { Segment } from "@/ui/Segment";
import { PageHeader, EmptyState } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { SharedPrompts } from "@/views/library/SharedPrompts";
import { CreateRoomFromComponentDialog } from "@/views/library/CreateRoomFromComponentDialog";
import { WelcomeCard } from "@/views/WelcomeCard";
import { WorkGallery } from "@/views/gallery/WorkGallery";
import { CreateRoomDialog } from "@/views/CreateRoomDialog";

// Plaza's two sections: shared rooms, and the works gallery
// (docs/prd/2026-10-13-works-gallery-and-make-similar.md §2).
type PlazaSection = "rooms" | "works";
const PLAZA_SECTIONS = [
  { value: "rooms" as const, label: "Rooms" },
  { value: "works" as const, label: "Works" },
];

export function Plaza({ onOpen }: { onOpen: (id: string) => void }) {
  const [q, setQ] = useState("");
  const [rooms, setRooms] = useState<PlazaRoom[] | null>(null);
  // How many rooms match q (the plaza pages; rooms holds the loaded pages).
  const [total, setTotal] = useState(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const [creating, setCreating] = useState(false);
  const [fromComponent, setFromComponent] = useState(false);
  const [section, setSection] = useState<PlazaSection>("works");
  const toast = useToast();
  const { reauth, role } = useAuth();
  // A read-only (viewer) organization role creates no rooms.
  const mayCreate = can(role, "create_room");

  // loadMore appends the next plaza page of the current query.
  const loadMore = async () => {
    if (!rooms) return;
    setLoadingMore(true);
    try {
      const r = await api.searchRooms(q, rooms.length);
      setRooms((cur) => [...(cur ?? []), ...r.rooms.filter((x) => !(cur ?? []).some((c) => c.roomId === x.roomId))]);
      setTotal(r.total);
    } catch (e) {
      toast.error(errorText(e, "Could not load more rooms"));
    } finally {
      setLoadingMore(false);
    }
  };

  const join = async (id: string) => {
    try {
      await api.joinRoom(id);
      await reauth().catch(() => {});
      toast.success(`Joined “${id}”`);
      onOpen(id);
    } catch {
      toast.error("Could not join room");
    }
  };

  // One search per settled query (the initial empty query runs at once, typing
  // is debounced), and only the latest query's answer is applied — an older,
  // slower response can never overwrite a newer one.
  useEffect(() => {
    let cancelled = false;
    const timer = window.setTimeout(
      () => {
        setRooms(null);
        api
          .searchRooms(q)
          .then((r) => {
            if (cancelled) return;
            setRooms(r.rooms);
            setTotal(r.total);
          })
          .catch(() => !cancelled && setRooms([]));
      },
      q ? 250 : 0,
    );
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [q]);

  return (
    <>
      <PageHeader
        title="Plaza"
        subtitle={section === "works" ? "Find work to learn from and make your own, from your organization and the platform" : "Discover shared rooms or start your own"}
        actions={
          mayCreate ? (
            <Button variant="primary" onClick={() => setCreating(true)}>
              <PlusIcon size={16} />
              New room
            </Button>
          ) : undefined
        }
      />

      <div className="p-4 sm:p-6">
        <WelcomeCard onOpen={onOpen} />
        <Segment options={PLAZA_SECTIONS} value={section} onChange={setSection} className="mb-5 w-full max-w-xs" />
        {section === "works" ? (
          <WorkGallery
            onOpenRoom={(roomId, artifactId) => {
              window.location.hash = routeHash({ kind: "room", roomId, ...(artifactId ? { artifactId } : {}) });
            }}
          />
        ) : (
          <>
            <div className="relative mb-6 max-w-lg">
              <MagnifyingGlassIcon
                size={16}
                className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]"
              />
              <Input
                className="pl-9"
                placeholder="Search rooms…"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </div>

            {rooms === null ? (
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
                {Array.from({ length: 6 }).map((_, i) => (
                  <Skeleton key={i} className="h-28" />
                ))}
              </div>
            ) : rooms.length === 0 ? (
              <EmptyState
                icon={<GlobeIcon size={24} />}
                title="No rooms found"
                hint={
                  q
                    ? "Try a different search, or create a room with this name."
                    : "Be the first — create a room to start collaborating."
                }
                action={
                  mayCreate ? (
                    <Button variant="primary" onClick={() => setCreating(true)}>
                      <PlusIcon size={16} />
                      New room
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
                {rooms.map((r) => (
                  <RoomCard key={r.roomId} room={r} onOpen={onOpen} onJoin={join} />
                ))}
              </div>
            )}
            {rooms && rooms.length < total && (
              <div className="mt-4 flex justify-center">
                <Button variant="outline" onClick={() => void loadMore()} disabled={loadingMore}>
                  {loadingMore ? "Loading…" : `Show more rooms (${total - rooms.length})`}
                </Button>
              </div>
            )}
            <SharedPrompts />
          </>
        )}
      </div>

      <CreateRoomDialog
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(id) => {
          setCreating(false);
          toast.success(`Room “${id}” created`);
          onOpen(id);
        }}
        onFromComponent={() => {
          setCreating(false);
          setFromComponent(true);
        }}
      />
      <CreateRoomFromComponentDialog
        open={fromComponent}
        onClose={() => setFromComponent(false)}
        onCreated={(id) => {
          setFromComponent(false);
          onOpen(id);
        }}
      />
    </>
  );
}

function RoomCard({
  room,
  onOpen,
  onJoin,
}: {
  room: PlazaRoom;
  onOpen: (id: string) => void;
  onJoin: (id: string) => void;
}) {
  const shared = room.visibility === "shared";
  return (
    <Card interactive onClick={() => onOpen(room.roomId)} className="p-5 animate-in">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-medium text-[var(--color-fg)]">
            {room.name || room.roomId}
          </h3>
          <p className="mt-1 line-clamp-2 text-sm text-[var(--color-fg-muted)]">
            {room.description || "No description"}
          </p>
        </div>
        <Badge tone={shared ? "accent" : "neutral"}>
          {shared ? <GlobeIcon size={12} /> : <LockIcon size={12} />}
          {shared ? "Shared" : "Private"}
        </Badge>
      </div>
      {room.previewable && (
        <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">
          {shared ? "Open to read before joining" : "Open to read; joining needs an invite code"}
        </p>
      )}
      <div className="mt-4 flex items-center justify-between gap-3 text-xs text-[var(--color-fg-subtle)]">
        <div className="flex items-center gap-3">
          <span className="inline-flex items-center gap-1 tabular">
            <UsersIcon size={13} />
            {room.memberCount ?? 0}
          </span>
          <span className="tabular truncate">{room.roomId}</span>
        </div>
        {shared && (
          <Button
            variant="outline"
            size="sm"
            onClick={(e) => {
              e.stopPropagation();
              onJoin(room.roomId);
            }}
          >
            Join
          </Button>
        )}
      </div>
    </Card>
  );
}
