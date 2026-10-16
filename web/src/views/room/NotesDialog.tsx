import { useCallback, useEffect, useMemo, useState } from "react";
import { BrainIcon, CaretDownIcon, CaretRightIcon, PlusIcon } from "@phosphor-icons/react";
import { errorText } from "@/lib/errors";
import { groupNotes, type RoomNote } from "@/lib/notes";
import { notesApi } from "@/lib/notesApi";
import { Button } from "@/ui/Button";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { LoadFailed } from "@/ui/QueryState";
import { useToast } from "@/ui/Toast";
import { NoteCard, type NoteActions } from "@/views/room/NoteCard";
import { NoteEditor } from "@/views/room/NoteEditor";
import { NoteHistory } from "@/views/room/NoteHistory";

// NotesDialog is "What this project knows": the room's notes (its brief,
// knowledge, lessons and memory) which every agent of the room reads. Every
// member reads the list and each note's history; a member who may act (not a
// visitor) also writes, pins, confirms, archives and deletes.
export function NotesDialog({
  roomId,
  mayAct,
  onClose,
}: {
  readonly roomId: string;
  // The member may act in the room (lib/roomRoles mayChat): a visitor reads.
  readonly mayAct: boolean;
  readonly onClose: () => void;
}) {
  const toast = useToast();
  const [notes, setNotes] = useState<RoomNote[] | null>(null);
  const [failed, setFailed] = useState("");
  const [editing, setEditing] = useState<RoomNote | "new" | null>(null);
  const [historyOf, setHistoryOf] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<RoomNote | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [showHistory, setShowHistory] = useState(false);

  // reload answers the room's notes (null when the load failed).
  const reload = useCallback(async (): Promise<RoomNote[] | null> => {
    try {
      const r = await notesApi.list(roomId);
      setFailed("");
      setNotes(r.notes ?? []);
      return r.notes ?? [];
    } catch (e) {
      setFailed(errorText(e, "Could not load the notes"));
      return null;
    }
  }, [roomId]);
  useEffect(() => {
    void reload();
  }, [reload]);

  const groups = useMemo(() => groupNotes(notes ?? []), [notes]);
  const byId = useMemo(() => new Map((notes ?? []).map((n) => [n.noteId, n])), [notes]);
  const historyNote = historyOf ? byId.get(historyOf) : undefined;

  // act runs one change of a note, then re-reads the list.
  const act = useCallback(
    async (n: RoomNote, fn: () => Promise<unknown>, done: string, failure: string) => {
      setBusy(n.noteId);
      try {
        await fn();
        toast.success(done);
      } catch (e) {
        toast.error(errorText(e, failure));
      } finally {
        setBusy(null);
        void reload();
      }
    },
    [reload, toast],
  );

  const actions = useMemo<NoteActions>(
    () => ({
      onEdit: setEditing,
      onHistory: (n) => setHistoryOf(n.noteId),
      onDelete: setDeleting,
      onPin: (n) =>
        void act(n, () => notesApi.update(roomId, n.noteId, { pinned: !n.pinned }), n.pinned ? "Unpinned" : "Pinned", "Could not change the pin"),
      onVerify: (n) => void act(n, () => notesApi.update(roomId, n.noteId, { verify: true }), "Marked as still true", "Could not confirm the note"),
      onArchive: (n) => {
        const archive = n.status === "active";
        void act(
          n,
          () => notesApi.update(roomId, n.noteId, { status: archive ? "archived" : "active" }),
          archive ? "Note archived" : "Note restored",
          archive ? "Could not archive the note" : "Could not restore the note",
        );
      },
    }),
    [act, roomId],
  );

  const list = (items: readonly RoomNote[]) =>
    items.map((n) => (
      <NoteCard
        key={n.noteId}
        note={n}
        replaces={n.supersedes ? byId.get(n.supersedes)?.title : undefined}
        mayAct={mayAct}
        busy={busy === n.noteId}
        actions={actions}
      />
    ));

  return (
    <>
      <Dialog
        open
        onClose={onClose}
        size="lg"
        title="What this project knows"
        description="Notes every member and agent of this room can read: the project's brief, decisions, lessons and dated facts."
        footer={
          <>
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
            {mayAct && (
              <Button variant="primary" onClick={() => setEditing("new")} disabled={notes === null}>
                <PlusIcon size={14} /> New note
              </Button>
            )}
          </>
        }
      >
        {failed ? (
          <LoadFailed message={failed} onRetry={() => void reload()} />
        ) : notes === null ? (
          <p className="py-6 text-center text-sm text-[var(--color-fg-muted)]">Loading…</p>
        ) : notes.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-8 text-center">
            <BrainIcon size={26} className="text-[var(--color-fg-subtle)]" />
            <p className="text-sm font-medium">No notes yet</p>
            <p className="max-w-sm text-xs text-[var(--color-fg-muted)]">
              Notes are short things this project has decided or learned, and the agents in this room read them before they work.
              {mayAct ? " Add one here, or save a message to memory from its menu." : ""}
            </p>
          </div>
        ) : (
          <div className="flex flex-col gap-5">
            {groups.sections.length === 0 && <p className="text-sm text-[var(--color-fg-muted)]">No active notes: everything is in History.</p>}
            {groups.sections.map((s) => (
              <section key={s.key} aria-label={s.label}>
                <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
                  {s.label} <span className="font-normal">({s.notes.length})</span>
                </h3>
                <ul className="flex flex-col gap-2">{list(s.notes)}</ul>
              </section>
            ))}
            {groups.history.length > 0 && (
              <section aria-label="History">
                <button
                  type="button"
                  onClick={() => setShowHistory((v) => !v)}
                  aria-expanded={showHistory}
                  className="tactile mb-2 flex items-center gap-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
                >
                  {showHistory ? <CaretDownIcon size={12} /> : <CaretRightIcon size={12} />}
                  History <span className="font-normal">({groups.history.length})</span>
                </button>
                {showHistory && <ul className="flex flex-col gap-2">{list(groups.history)}</ul>}
              </section>
            )}
          </div>
        )}
      </Dialog>
      {editing && (
        <NoteEditor
          // A reloaded note opens a fresh form on its new revision.
          key={editing === "new" ? "new" : `${editing.noteId}:${editing.revision}`}
          roomId={roomId}
          note={editing === "new" ? null : editing}
          notes={notes ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            toast.success(editing === "new" ? "Note added" : "Note saved");
            setEditing(null);
            void reload();
          }}
          onStale={() => {
            const id = editing === "new" ? "" : editing.noteId;
            void reload().then((fresh) => {
              const now = fresh?.find((n) => n.noteId === id);
              setEditing(now ?? null);
              if (!now) toast.info("That note is gone");
            });
          }}
        />
      )}
      {historyNote && (
        <NoteHistory
          roomId={roomId}
          note={historyNote}
          mayAct={mayAct}
          onClose={() => setHistoryOf(null)}
          onRolledBack={() => void reload()}
        />
      )}
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() => {
          const n = deleting;
          setDeleting(null);
          if (n) void act(n, () => notesApi.remove(roomId, n.noteId), "Note deleted", "Could not delete the note");
        }}
        title="Delete this note?"
        description="The note and its history are removed. Agents stop reading it. Archive it instead to keep it out of the way."
        confirmLabel="Delete"
        danger
      />
    </>
  );
}
