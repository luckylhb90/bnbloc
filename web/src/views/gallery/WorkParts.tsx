import { useEffect, useState } from "react";
import { StarIcon } from "@phosphor-icons/react";
import { api, type RoomSummary } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { can } from "@/lib/permissions";
import { useContentChanged } from "@/lib/useContentChanged";
import { invalidateQueries, queryKey, useQuery, type QueryResult } from "@/lib/useQuery";
import { defaultGalleryTarget, galleryRoomProblem, galleryRooms, workAuthor, workTypeLabel, type GalleryRoomTarget } from "@/lib/workGallery";
import { Badge } from "@/ui/Card";
import { Field, Input } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { UserName } from "@/views/UserName";

// The gallery's small parts, shared by its cards, a work's detail and the
// dialogs that put a work into a room ("Use this app", "Make similar").

/** The gallery listing's query name. */
const GALLERY_QUERY = "content-works";

/** useWorks is the works listing (the content listing's works): one cached
 *  query for the gallery and the Library's works, read again after every
 *  change of a work (lib/contentChanges.ts: a publish, a favorite, a
 *  stored cover, a delete). */
export function useWorks(): QueryResult<ContentComponent[]> {
  const { subject, tenant } = useAuth();
  useContentChanged(["work"], () => invalidateQueries(GALLERY_QUERY));
  return useQuery(queryKey(GALLERY_QUERY, { subject, tenant }), () => contentApi.list("work").then((r) => r.components ?? []), { staleMs: 30_000 });
}

/** WorkAuthorName is a work's author line: the person, or "Platform". */
export function WorkAuthorName({ work }: { readonly work: ContentComponent }) {
  const author = workAuthor(work);
  if (author.kind === "platform") return <>Platform</>;
  return <UserName id={author.id} />;
}

/** WorkTypeBadge is a work's small type badge (App, Image, Video, …). */
export function WorkTypeBadge({ work, className }: { readonly work: Pick<ContentComponent, "workType">; readonly className?: string }) {
  const label = workTypeLabel(work.workType);
  if (!label) return null;
  return (
    <Badge tone={work.workType === "app" ? "accent" : "neutral"} className={className}>
      {label}
    </Badge>
  );
}

/** FavoriteStar is the member's star on a work and how many of their
 *  organization starred it. */
export function FavoriteStar({ work, onToggle, className }: { readonly work: ContentComponent; readonly onToggle: () => void; readonly className?: string }) {
  const label = contentLabel(work);
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={work.favorite}
      aria-label={work.favorite ? `Remove ${label} from your favorites` : `Add ${label} to your favorites`}
      title={work.favorite ? "In your favorites" : "Add to your favorites"}
      className={cn(
        "tap tactile inline-flex h-8 items-center gap-1 rounded-full px-2 text-xs tabular hover:bg-[var(--color-surface-2)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60",
        work.favorite ? "text-[var(--color-warning)]" : "text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]",
        className,
      )}
    >
      <StarIcon size={16} weight={work.favorite ? "fill" : "regular"} />
      {work.favoriteCount > 0 && <span>{work.favoriteCount}</span>}
    </button>
  );
}

/** useGalleryRoom is the room choice of a dialog that puts a work into a
 *  room: the member's rooms they may post in (loaded when open), the
 *  target (a new room named after the work by default, when they may
 *  create rooms) and what keeps it from being sent. */
export function useGalleryRoom(open: boolean, label: string) {
  const { subject, role } = useAuth();
  const [rooms, setRooms] = useState<RoomSummary[] | null>(null);
  const [roomsError, setRoomsError] = useState("");
  const [target, setTarget] = useState<GalleryRoomTarget>(defaultGalleryTarget(""));
  // A new room needs the organization's room creation.
  const mayCreate = can(role, "create_room");

  useEffect(() => {
    if (!open || !subject) return;
    let live = true;
    setRooms(null);
    setRoomsError("");
    api
      .myRooms(subject)
      .then((r) => {
        if (!live) return;
        const owned = galleryRooms(r.rooms ?? []);
        setRooms(owned);
        setTarget(mayCreate || owned.length === 0 ? defaultGalleryTarget(label) : { kind: "room", roomId: owned[0].roomId });
      })
      .catch((e: unknown) => {
        if (!live) return;
        setRooms([]);
        setRoomsError(errorText(e, "Could not load your rooms"));
        setTarget(defaultGalleryTarget(label));
      });
    return () => {
      live = false;
    };
  }, [open, subject, mayCreate, label]);

  const owned = rooms ?? [];
  const problem =
    rooms === null
      ? "Loading your rooms"
      : (galleryRoomProblem(target, owned) ?? (target.kind === "new" && !mayCreate ? "Your organization role does not create rooms: pick one of yours" : null));
  return { rooms, owned, roomsError, target, setTarget, mayCreate, problem, label };
}

/** GalleryRoomField is the room choice: a new room (its name) or one of
 *  the member's rooms. */
export function GalleryRoomField({ room, roomHint }: { readonly room: ReturnType<typeof useGalleryRoom>; readonly roomHint: string }) {
  const { rooms, owned, roomsError, target, setTarget, mayCreate, label } = room;
  return (
    <>
      <Segment
        options={[
          { value: "new" as const, label: "New room" },
          { value: "room" as const, label: "One of my rooms" },
        ]}
        value={target.kind}
        onChange={(kind) => setTarget(kind === "new" ? defaultGalleryTarget(label) : { kind: "room", roomId: owned[0]?.roomId ?? "" })}
      />
      {target.kind === "new" ? (
        <Field label="Room name" hint={mayCreate ? "A new room of yours, named after the work unless you change it." : "Your organization role does not create rooms."}>
          <Input value={target.name} onChange={(e) => setTarget({ kind: "new", name: e.target.value })} placeholder={label} disabled={!mayCreate} autoFocus />
        </Field>
      ) : rooms === null ? (
        <p className="text-sm text-[var(--color-fg-subtle)]" role="status">
          Loading your rooms…
        </p>
      ) : owned.length === 0 ? (
        <p className="text-sm text-[var(--color-fg-muted)]">{roomsError || "You have no room you can post in yet. Use a new room instead."}</p>
      ) : (
        <Field label="Room" hint={roomHint}>
          <Select value={target.roomId} onChange={(e) => setTarget({ kind: "room", roomId: e.target.value })}>
            {owned.map((r) => (
              <option key={r.roomId} value={r.roomId}>
                {r.name || r.roomId}
              </option>
            ))}
          </Select>
        </Field>
      )}
    </>
  );
}
