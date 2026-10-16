import { useMemo, useState } from "react";
import { ApiError, errorText } from "@/lib/errors";
import { formatBytes } from "@/lib/format";
import {
  NOTE_TYPE_LABELS,
  bodyLimit,
  byteLength,
  createRequest,
  emptyNoteForm,
  formOfNote,
  replaceableNotes,
  runeCount,
  updateRequest,
  type NoteForm,
  type NoteType,
  type RoomNote,
} from "@/lib/notes";
import { notesApi } from "@/lib/notesApi";
import { MaxNoteTitleRunes, NoteTypes, type NoteSource } from "@/lib/wire.gen";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";

const SELECT =
  "h-10 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)] focus:outline-none focus:border-[var(--color-accent)]";

// What a new note starts with (Save to memory: the message's text and source).
export interface NoteSeed {
  readonly type: NoteType;
  readonly title: string;
  readonly body: string;
  readonly sources?: readonly NoteSource[];
}

// NoteEditor creates a note or edits one. `quick` is the small form of "Save
// to memory" (type, title, body: no pin, no replacement); the panel's form
// also offers the pin and, on create, the active note of the same type this
// one replaces. An edit sends only what changed and the revision it read: a
// change made meanwhile is refused and the editor offers to reload.
export function NoteEditor({
  roomId,
  note,
  notes = [],
  seed,
  types = NoteTypes as readonly NoteType[],
  quick = false,
  title,
  onClose,
  onSaved,
  onStale,
}: {
  readonly roomId: string;
  // The note being edited; null creates one.
  readonly note: RoomNote | null;
  // The room's notes: what a new note may replace.
  readonly notes?: readonly RoomNote[];
  readonly seed?: NoteSeed;
  // The types a new note may have.
  readonly types?: readonly NoteType[];
  readonly quick?: boolean;
  readonly title?: string;
  readonly onClose: () => void;
  readonly onSaved: (saved: RoomNote) => void;
  // Reload the note after a refused edit (the editor then opens on the new version).
  readonly onStale?: () => void;
}) {
  const [form, setForm] = useState<NoteForm>(() => (note ? formOfNote(note) : { ...emptyNoteForm(seed?.type ?? types[0]), title: seed?.title ?? "", body: seed?.body ?? "" }));
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<NoteForm>) => setForm((f) => ({ ...f, ...patch }));
  const type = note?.type ?? form.type;
  const isBrief = type === "brief";
  const replaceable = useMemo(() => replaceableNotes(notes, type), [notes, type]);
  const bytes = byteLength(form.body);
  const limit = bodyLimit(type);
  const overBody = bytes > limit;
  const titleChars = runeCount(form.title.trim());
  const edit = note ? updateRequest(note, form) : null;
  const unchanged = !!edit?.unchanged;

  const save = async () => {
    if (busy) return;
    setError("");
    try {
      setBusy(true);
      if (note) {
        if (!edit?.request) {
          if (edit?.error) setError(edit.error);
          return;
        }
        onSaved(await notesApi.update(roomId, note.noteId, edit.request));
      } else {
        const made = createRequest(form, seed?.sources);
        if (!made.request) {
          setError(made.error ?? "Check the note");
          return;
        }
        onSaved(await notesApi.create(roomId, made.request));
      }
    } catch (e) {
      // A refused edit is a note that moved on (the server says which).
      setStale(!!note && e instanceof ApiError && e.status === 409);
      setError(errorText(e, "Could not save the note"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title={title ?? (note ? "Edit note" : "New note")}
      description={NOTE_TYPE_LABELS[type].hint}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || overBody || unchanged || form.title.trim() === ""}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {!note && types.length > 1 && (
          <Field label="Type">
            <Select value={form.type} onChange={(e) => set({ type: e.target.value as NoteType, supersedes: "" })} className={SELECT}>
              {types.map((t) => (
                <option key={t} value={t}>
                  {NOTE_TYPE_LABELS[t].chip}
                </option>
              ))}
            </Select>
          </Field>
        )}
        <Field
          label="Title"
          hint={`${titleChars} / ${MaxNoteTitleRunes} characters, one line`}
        >
          <Input value={form.title} onChange={(e) => set({ title: e.target.value })} autoFocus placeholder="What this note is about" />
        </Field>
        <Field
          label="Note"
          hint="Markdown. Agents in this room read it, so keep it short and about one thing."
          error={overBody ? `A ${type} holds at most ${formatBytes(limit)}` : undefined}
        >
          <Textarea rows={10} value={form.body} onChange={(e) => set({ body: e.target.value })} className="resize-y font-mono text-xs" />
        </Field>
        <p className={cn("-mt-3 text-right text-xs tabular-nums", overBody ? "text-[var(--color-danger)]" : "text-[var(--color-fg-subtle)]")}>
          {bytes.toLocaleString()} / {limit.toLocaleString()} bytes
        </p>
        {!quick && !isBrief && <Switch checked={form.pinned} onChange={(pinned) => set({ pinned })} label="Pin: agents read it before the others" />}
        {!quick && !note && isBrief && <p className="text-xs text-[var(--color-fg-muted)]">A new brief replaces the room&apos;s current one; the old one stays in History.</p>}
        {!quick && !note && replaceable.length > 0 && (
          <Field label="Replaces" hint="The replaced note moves to History. It can be read there, not edited.">
            <Select value={form.supersedes} onChange={(e) => set({ supersedes: e.target.value })} className={SELECT}>
              <option value="">Nothing: keep both</option>
              {replaceable.map((n) => (
                <option key={n.noteId} value={n.noteId}>
                  {n.title}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {error && (
          <ErrorBox>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span>{error}</span>
              {stale && onStale && (
                <Button variant="outline" size="sm" onClick={onStale}>
                  Reload the note
                </Button>
              )}
            </div>
          </ErrorBox>
        )}
      </div>
    </Dialog>
  );
}
