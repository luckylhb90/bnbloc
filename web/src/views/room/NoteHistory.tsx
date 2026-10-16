import { useCallback, useEffect, useState } from "react";
import { ArrowCounterClockwiseIcon } from "@phosphor-icons/react";
import { errorText } from "@/lib/errors";
import { agoText, exactTime } from "@/lib/format";
import { NOTE_TYPE_LABELS, canEditNote, notePreview, type NoteRevision, type RoomNote } from "@/lib/notes";
import { notesApi } from "@/lib/notesApi";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { LoadFailed } from "@/ui/QueryState";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";

// NoteHistory lists a note's revisions, newest first. A member who may act
// can roll the note back to one: its content returns as a new revision, so a
// rollback can itself be rolled back. A superseded note is history and is not
// rolled back; everyone, a visitor too, reads the revisions.
export function NoteHistory({
  roomId,
  note,
  mayAct,
  onClose,
  onRolledBack,
}: {
  readonly roomId: string;
  readonly note: RoomNote;
  readonly mayAct: boolean;
  readonly onClose: () => void;
  // The note changed: the panel reloads its list.
  readonly onRolledBack: () => void;
}) {
  const toast = useToast();
  const [revisions, setRevisions] = useState<NoteRevision[] | null>(null);
  const [failed, setFailed] = useState("");
  const [open, setOpen] = useState<number | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const noteId = note.noteId;

  const load = useCallback(() => {
    setFailed("");
    notesApi
      .revisions(roomId, noteId)
      .then((r) => setRevisions(r.revisions ?? []))
      .catch((e) => setFailed(errorText(e, "Could not load the history")));
  }, [roomId, noteId]);
  useEffect(load, [load]);

  const rollback = async (revision: number) => {
    setBusy(revision);
    try {
      await notesApi.rollback(roomId, noteId, revision);
      toast.success(`Rolled back to revision ${revision}`);
      onRolledBack();
      load();
    } catch (e) {
      toast.error(errorText(e, "Could not roll back the note"));
    } finally {
      setBusy(null);
    }
  };

  const canRollBack = mayAct && canEditNote(note);
  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title={`History: ${note.title}`}
      description={canRollBack ? "Roll back to bring an earlier version back as a new revision." : "Every version of this note, newest first."}
      footer={
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
      }
    >
      {failed ? (
        <LoadFailed message={failed} onRetry={load} />
      ) : revisions === null ? (
        <p className="py-6 text-center text-sm text-[var(--color-fg-muted)]">Loading…</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {revisions.map((v) => {
            const current = v.revision === note.revision;
            const expanded = open === v.revision;
            return (
              <li key={v.revision} className="rounded-[var(--radius)] border border-[var(--color-border)] p-3">
                <div className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
                      <span className="break-words">{v.title}</span>
                      <Badge>Revision {v.revision}</Badge>
                      {current && <Badge tone="accent">Current</Badge>}
                      {v.type !== note.type && <Badge>{NOTE_TYPE_LABELS[v.type].chip}</Badge>}
                      {v.status !== "active" && <Badge tone="warning">{v.status}</Badge>}
                    </p>
                    <p className="mt-0.5 text-xs text-[var(--color-fg-subtle)]">
                      <UserName id={v.changedBy} /> · <span title={exactTime(v.atMs)}>{agoText(v.atMs)}</span>
                    </p>
                    {expanded ? (
                      <p className="mt-2 whitespace-pre-wrap break-words text-sm">{v.body || "(no text)"}</p>
                    ) : (
                      v.body && <p className="mt-1 break-words text-xs text-[var(--color-fg-muted)]">{notePreview(v.body)}</p>
                    )}
                  </div>
                  <div className="flex shrink-0 flex-col items-end gap-1">
                    {v.body.length > 140 && (
                      <Button size="sm" variant="ghost" onClick={() => setOpen(expanded ? null : v.revision)}>
                        {expanded ? "Less" : "Full text"}
                      </Button>
                    )}
                    {canRollBack && !current && (
                      <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => void rollback(v.revision)}>
                        <ArrowCounterClockwiseIcon size={14} /> {busy === v.revision ? "Rolling back…" : "Roll back"}
                      </Button>
                    )}
                  </div>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </Dialog>
  );
}
