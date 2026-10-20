import { useState } from "react";
import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { api, type RoomDetail } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { cleanQuickPrompts, type RoomQuickPrompt } from "@/lib/roomWelcome";
import { Button } from "@/ui/Button";
import { Input, Textarea } from "@/ui/Input";
import { useToast } from "@/ui/Toast";

// RoomWelcomeSection is the room settings' welcome and quick prompts: what
// the empty room shows every member (RoomStartCard). The room's owner and
// admins change them here (or their agent, through room_setup, when they
// ask it); every member sees them.
export function RoomWelcomeSection({ room, canEdit, onChanged }: { readonly room: RoomDetail; readonly canEdit: boolean; readonly onChanged: (r: RoomDetail) => void }) {
  const toast = useToast();
  const [welcome, setWelcome] = useState(room.welcome);
  const [rows, setRows] = useState<RoomQuickPrompt[]>(room.quickPrompts);
  const [busy, setBusy] = useState(false);
  const setRow = (i: number, patch: Partial<RoomQuickPrompt>) => setRows((list) => list.map((q, k) => (k === i ? { ...q, ...patch } : q)));

  const save = async () => {
    setBusy(true);
    try {
      const updated = await api.updateRoomWelcome(room.roomId, { welcome: welcome.trim(), quickPrompts: cleanQuickPrompts(rows) });
      setRows(updated.quickPrompts);
      toast.success("Welcome saved");
      onChanged(updated);
    } catch (e) {
      toast.error(errorText(e, "Could not save the welcome"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] p-4">
      <p className="text-sm font-medium">Welcome and quick prompts</p>
      <p className="mb-3 text-xs text-[var(--color-fg-muted)]">
        What this room shows every member while it is empty. A quick prompt puts its text into the message box; nothing is sent.
        {!canEdit ? " The room's owner and admins change them." : ""}
      </p>
      {canEdit ? (
        <div className="flex flex-col gap-2">
          <Textarea aria-label="Welcome" rows={3} placeholder="What this room is for, and how to start" value={welcome} onChange={(e) => setWelcome(e.target.value)} />
          {rows.map((q, i) => (
            <div key={i} className="flex items-start gap-2">
              <Input aria-label={`Quick prompt ${i + 1} title`} placeholder="Title" value={q.title} onChange={(e) => setRow(i, { title: e.target.value })} className="max-w-40" />
              <Textarea aria-label={`Quick prompt ${i + 1} text`} rows={1} placeholder="The text it puts into the message box" value={q.prompt} onChange={(e) => setRow(i, { prompt: e.target.value })} />
              <button
                type="button"
                onClick={() => setRows((list) => list.filter((_, k) => k !== i))}
                aria-label={`Remove quick prompt ${i + 1}`}
                title="Remove"
                className="tactile mt-2 text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]"
              >
                <TrashIcon size={15} />
              </button>
            </div>
          ))}
          <div className="flex items-center gap-2">
            <Button size="sm" variant="outline" onClick={() => setRows((list) => [...list, { title: "", prompt: "" }])}>
              <PlusIcon size={14} />
              Quick prompt
            </Button>
            <span className="flex-1" />
            <Button size="sm" variant="primary" onClick={() => void save()} disabled={busy}>
              {busy ? "Saving…" : "Save welcome"}
            </Button>
          </div>
        </div>
      ) : room.welcome || room.quickPrompts.length > 0 ? (
        <div className="flex flex-col gap-1 text-sm">
          {room.welcome && <p className="whitespace-pre-wrap">{room.welcome}</p>}
          {room.quickPrompts.map((q) => (
            <p key={q.title + q.prompt} className="text-xs text-[var(--color-fg-muted)]">
              <span className="font-medium text-[var(--color-fg)]">{q.title}</span> · {q.prompt}
            </p>
          ))}
        </div>
      ) : (
        <p className="text-xs text-[var(--color-fg-subtle)]">None</p>
      )}
    </div>
  );
}
