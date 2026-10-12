import { useEffect, useState } from "react";
import {
  MagnifyingGlassIcon,
  PlusIcon,
  UsersIcon,
  GlobeIcon,
  LockIcon,
} from "@phosphor-icons/react";
import { api, ApiError, type PlazaRoom } from "@/lib/api";
import { isSubmitEnter } from "@/lib/keys";
import { roomIdProblem } from "@/lib/roomId";
import { useAuth } from "@/lib/auth";
import { Button } from "@/ui/Button";
import { Input, Field } from "@/ui/Input";
import { Card, Badge, Skeleton } from "@/ui/Card";
import { Segment } from "@/ui/Segment";
import { Dialog } from "@/ui/Dialog";
import { PageHeader, EmptyState } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { SharedPrompts } from "@/views/library/SharedPrompts";
import { InstantiateTemplateDialog } from "@/views/library/TemplateDialogs";
import { WelcomeCard } from "@/views/WelcomeCard";

export function Plaza({ onOpen }: { onOpen: (id: string) => void }) {
  const [q, setQ] = useState("");
  const [rooms, setRooms] = useState<PlazaRoom[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [fromTemplate, setFromTemplate] = useState(false);
  const toast = useToast();
  const { reauth } = useAuth();

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
          .then((r) => !cancelled && setRooms(r.rooms || []))
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
        subtitle="Discover shared rooms or start your own"
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <PlusIcon size={16} />
            New room
          </Button>
        }
      />

      <div className="p-6">
        <WelcomeCard onOpen={onOpen} />
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
              <Button variant="primary" onClick={() => setCreating(true)}>
                <PlusIcon size={16} />
                New room
              </Button>
            }
          />
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {rooms.map((r) => (
              <RoomCard key={r.roomId} room={r} onOpen={onOpen} onJoin={join} />
            ))}
          </div>
        )}
        <SharedPrompts />
      </div>

      <CreateRoomDialog
        open={creating}
        onClose={() => setCreating(false)}
        onCreated={(id) => {
          setCreating(false);
          toast.success(`Room “${id}” created`);
          onOpen(id);
        }}
        onFromTemplate={() => {
          setCreating(false);
          setFromTemplate(true);
        }}
      />
      <InstantiateTemplateDialog
        open={fromTemplate}
        onClose={() => setFromTemplate(false)}
        onCreated={(id) => {
          setFromTemplate(false);
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

function CreateRoomDialog({
  open,
  onClose,
  onCreated,
  onFromTemplate,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
  onFromTemplate?: () => void;
}) {
  const [id, setId] = useState("");
  const [desc, setDesc] = useState("");
  const [vis, setVis] = useState<"private" | "shared">("private");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const { reauth } = useAuth();
  const toast = useToast();

  const reset = () => {
    setId("");
    setDesc("");
    setVis("private");
    setErr("");
  };

  const submit = async () => {
    const problem = roomIdProblem(id);
    if (problem) {
      setErr(problem);
      return;
    }
    setBusy(true);
    setErr("");
    try {
      await api.createRoom({
        roomId: id.trim(),
        name: id.trim(),
        description: desc.trim(),
        visibility: vis,
      });
      // Refresh room-scope so the new room is immediately reachable.
      await reauth().catch(() => {});
      reset();
      onCreated(id.trim());
    } catch (e) {
      // The form stays open with the id so the member can adjust it.
      if (e instanceof ApiError && e.status === 409) {
        setErr("A room with this id already exists");
      } else {
        const msg = e instanceof ApiError ? e.message : "create failed";
        setErr(msg);
        toast.error(`Could not create room: ${msg}`);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={() => {
        reset();
        onClose();
      }}
      title="New room"
      description="Rooms are private by default. You can change this anytime."
      footer={
        <>
          {onFromTemplate && (
            <Button
              variant="outline"
              className="mr-auto"
              onClick={() => {
                reset();
                onFromTemplate();
              }}
            >
              From template…
            </Button>
          )}
          <Button
            variant="ghost"
            onClick={() => {
              reset();
              onClose();
            }}
          >
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {busy ? "Creating…" : "Create room"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Room id" error={err}>
          <Input
            autoFocus
            placeholder="my-team-room"
            value={id}
            onChange={(e) => {
              setId(e.target.value);
              if (err) setErr("");
            }}
            onKeyDown={(e) => isSubmitEnter(e) && void submit()}
          />
        </Field>
        <Field label="Description" hint="Optional — what is this room for?">
          <Input
            placeholder="Design reviews and agent pairing"
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
          />
        </Field>
        <Field label="Visibility">
          <Segment
            value={vis}
            onChange={setVis}
            options={[
              { value: "private", label: "Private", hint: "invite only" },
              { value: "shared", label: "Shared", hint: "anyone joins" },
            ]}
          />
        </Field>
      </div>
    </Dialog>
  );
}
