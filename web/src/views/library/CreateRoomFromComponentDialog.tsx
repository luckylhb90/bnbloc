import { useEffect, useState } from "react";
import { ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { isSubmitEnter } from "@/lib/keys";
import { roomSetupSummary, type RoomInstallReply } from "@/lib/roomComponents";
import { roomComponentsApi } from "@/lib/roomComponentsApi";
import { roomIdProblem } from "@/lib/roomId";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { RoomSetup } from "@/views/library/RoomSetup";
import { RoomToolsToAdd } from "@/views/library/RoomToolsToAdd";

// CreateRoomFromComponentDialog is "Create a room from this" (room
// components PRD §2.1): a room component (the one given, or one picked
// from the member's Library) becomes a new room the member owns, with its
// channels, expert, packs and apps, and its scheduled tasks paused. What
// it asks to read is shown, never granted. The new room keeps the
// component's welcome and quick prompts; what the install left out is said
// here, once, before the room opens (it is no room state).
export function CreateRoomFromComponentDialog({
  open,
  component,
  onClose,
  onCreated,
}: {
  readonly open: boolean;
  readonly component?: ContentComponent | null;
  readonly onClose: () => void;
  readonly onCreated: (roomId: string) => void;
}) {
  const toast = useToast();
  const { reauth } = useAuth();
  const [list, setList] = useState<ContentComponent[] | null>(null);
  const [pickedId, setPickedId] = useState("");
  const [roomId, setRoomId] = useState("");
  const [vis, setVis] = useState<"private" | "shared">("private");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  // A made room whose install left something out, said before it opens.
  const [made, setMade] = useState<RoomInstallReply | null>(null);

  useEffect(() => {
    if (!open) return;
    setRoomId("");
    setVis("private");
    setErr("");
    setMade(null);
    setPickedId(component?.id ?? "");
    if (component) return;
    let live = true;
    setList(null);
    contentApi
      .list("room")
      .then((r) => {
        if (!live) return;
        setList(r.components ?? []);
        setPickedId(r.components?.[0]?.id ?? "");
      })
      .catch((e) => {
        if (!live) return;
        setList([]);
        toast.error(errorText(e, "Could not load room components"));
      });
    return () => {
      live = false;
    };
  }, [open, component, toast]);

  const chosen = component ?? list?.find((c) => c.id === pickedId) ?? null;

  const submit = async () => {
    if (!chosen || busy) return;
    const problem = roomIdProblem(roomId);
    if (problem) {
      setErr(problem);
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const id = roomId.trim();
      const reply = await roomComponentsApi.createRoom(chosen.id, { roomId: id, name: id, visibility: vis });
      await reauth().catch(() => {});
      toast.success(`Room “${id}” created from ${contentLabel(chosen)}`);
      if (reply.skipped.length > 0 || reply.toolsToAdd.length > 0) setMade(reply);
      else onCreated(id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setErr("A room with this id already exists");
      else setErr(errorText(e, "Could not create the room"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title="New room from a room component"
      description="You own the new room. Its channels, expert, packs and apps are set up, and its scheduled tasks are created paused: turn them on when you are ready."
      footer={
        made ? (
          <Button variant="primary" onClick={() => onCreated(made.roomId)}>
            Open the room
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => void submit()} disabled={busy || !chosen}>
              {busy ? "Creating…" : "Create room"}
            </Button>
          </>
        )
      }
    >
      {made ? (
        <div role="status" className="flex flex-col gap-2 text-sm">
          <p>Room “{made.roomId}” is made.{made.instructions ? ` Its instructions are your expert ${made.instructions}: edit them in your Library.` : ""}</p>
          {made.skipped.length > 0 && (
            <>
              <p>Left out:</p>
              <ul className="list-disc pl-5 text-[var(--color-fg-muted)]">
                {made.skipped.map((s) => (
                  <li key={s}>{s}</li>
                ))}
              </ul>
            </>
          )}
          <RoomToolsToAdd tools={made.toolsToAdd} />
        </div>
      ) : (
      <div className="flex flex-col gap-4">
        {!component && (
          <Field label="Room component">
            {list === null ? (
              <p className="text-sm text-[var(--color-fg-subtle)]">Loading room components…</p>
            ) : list.length === 0 ? (
              <p className="text-sm text-[var(--color-fg-muted)]">No room components yet. Save a room as a component from its header.</p>
            ) : (
              <Select value={pickedId} onChange={(e) => setPickedId(e.target.value)}>
                {list.map((c) => (
                  <option key={c.id} value={c.id}>
                    {contentLabel(c)}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        {chosen && (
          <div className="flex flex-col gap-2 rounded-[var(--radius-lg)] bg-[var(--color-surface-2)] px-3 py-2">
            <p className="text-sm text-[var(--color-fg-muted)]">
              <span className="font-medium text-[var(--color-fg)]">{contentLabel(chosen)}</span> · {roomSetupSummary(chosen.room, chosen.requires)}
            </p>
            <RoomSetup component={chosen} />
          </div>
        )}
        <Field label="Room id" error={err || undefined}>
          <Input
            autoFocus
            placeholder="my-team-room"
            value={roomId}
            onChange={(e) => {
              setRoomId(e.target.value);
              if (err) setErr("");
            }}
            onKeyDown={(e) => isSubmitEnter(e) && void submit()}
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
      )}
    </Dialog>
  );
}
