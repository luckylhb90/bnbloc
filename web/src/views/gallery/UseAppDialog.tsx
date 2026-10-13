import { useEffect, useState } from "react";
import { CopySimpleIcon } from "@phosphor-icons/react";
import { api, type RoomSummary } from "@/lib/api";
import { defaultAppCopyTarget, appCopyErrorText, appCopyProblem, appCopyRequest, appCopyRooms, type AppCopyTarget } from "@/lib/appGallery";
import { appGalleryApi } from "@/lib/appGalleryApi";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { errorText } from "@/lib/errors";
import { can } from "@/lib/permissions";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";

// UseAppDialog is "Use this app" (docs/prd/2026-10-13-app-gallery.md §8):
// a copy of the app's site as a new deliverable in a room the member owns
// (one they may post in) or in a new room named after the app, with a data
// space of its own that starts empty. The room choice and the request are
// lib/appGallery.ts; the server decides (a room the member does not own,
// an app they no longer see: appCopyErrorText). On success the room opens
// with the copy.

export function UseAppDialog({
  app,
  onClose,
  onUsed,
}: {
  readonly app: ContentComponent | null;
  readonly onClose: () => void;
  readonly onUsed: (roomId: string, artifactId: string) => void;
}) {
  const { subject, role, reauth } = useAuth();
  const toast = useToast();
  const [rooms, setRooms] = useState<RoomSummary[] | null>(null);
  const [roomsError, setRoomsError] = useState("");
  const [target, setTarget] = useState<AppCopyTarget>(defaultAppCopyTarget(""));
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  // A new room needs the organization's room creation.
  const mayCreate = can(role, "create_room");
  const label = app ? contentLabel(app) : "";

  useEffect(() => {
    if (!app || !subject) return;
    let live = true;
    setFailure("");
    setRooms(null);
    setRoomsError("");
    api
      .myRooms(subject)
      .then((r) => {
        if (!live) return;
        const owned = appCopyRooms(r.rooms ?? []);
        setRooms(owned);
        setTarget(mayCreate || owned.length === 0 ? defaultAppCopyTarget(label) : { kind: "room", roomId: owned[0].roomId });
      })
      .catch((e: unknown) => {
        if (!live) return;
        setRooms([]);
        setRoomsError(errorText(e, "Could not load your rooms"));
        setTarget(defaultAppCopyTarget(label));
      });
    return () => {
      live = false;
    };
  }, [app, subject, mayCreate, label]);

  if (!app) return null;
  const owned = rooms ?? [];
  const problem = appCopyProblem(target, owned) ?? (target.kind === "new" && !mayCreate ? "Your organization role does not create rooms: pick one of yours" : null);
  const submit = async () => {
    if (problem || busy) return;
    setBusy(true);
    setFailure("");
    try {
      const reply = await appGalleryApi.use(app.id, appCopyRequest(target, app.version));
      // A new room is the member's: refresh their room scope before opening it.
      if (reply.createdRoom) await reauth().catch(() => {});
      toast.success(`${label} is in ${reply.createdRoom ? "a new room" : "the room"}, with its own empty data`);
      onUsed(reply.roomId, reply.artifactId);
    } catch (e) {
      setFailure(appCopyErrorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={`Use ${label}`}
      description="You get your own copy of the app as a new deliverable, with its own empty data. Ask an agent in the room to change it as you like; the original stays as it is."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!!problem || busy || rooms === null}>
            <CopySimpleIcon size={15} />
            {busy ? "Copying…" : "Use this app"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Segment
          options={[
            { value: "new" as const, label: "New room" },
            { value: "room" as const, label: "One of my rooms" },
          ]}
          value={target.kind}
          onChange={(kind) => setTarget(kind === "new" ? defaultAppCopyTarget(label) : { kind: "room", roomId: owned[0]?.roomId ?? "" })}
        />
        {target.kind === "new" ? (
          <Field label="Room name" hint={mayCreate ? "A new room of yours, named after the app unless you change it." : "Your organization role does not create rooms."}>
            <Input value={target.name} onChange={(e) => setTarget({ kind: "new", name: e.target.value })} placeholder={label} disabled={!mayCreate} autoFocus />
          </Field>
        ) : rooms === null ? (
          <p className="text-sm text-[var(--color-fg-subtle)]" role="status">
            Loading your rooms…
          </p>
        ) : owned.length === 0 ? (
          <p className="text-sm text-[var(--color-fg-muted)]">{roomsError || "You have no room you can post in yet. Use a new room instead."}</p>
        ) : (
          <Field label="Room" hint="A room you can post in: the copy is its owners' to change and to share.">
            <Select value={target.roomId} onChange={(e) => setTarget({ kind: "room", roomId: e.target.value })}>
              {owned.map((r) => (
                <option key={r.roomId} value={r.roomId}>
                  {r.name || r.roomId}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {failure && <ErrorBox>{failure}</ErrorBox>}
      </div>
    </Dialog>
  );
}
