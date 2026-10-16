import { useState } from "react";
import {
  ArchiveIcon,
  ArrowUUpLeftIcon,
  CaretDownIcon,
  CaretRightIcon,
  CheckCircleIcon,
  ClockCounterClockwiseIcon,
  PencilSimpleIcon,
  PushPinIcon,
  PushPinSlashIcon,
  TrashIcon,
} from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { exactTime } from "@/lib/format";
import { NOTE_TYPE_LABELS, canEditNote, noteDates, notePreview, sourceLabel, type RoomNote } from "@/lib/notes";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { UserName } from "@/views/UserName";

export interface NoteActions {
  readonly onEdit: (n: RoomNote) => void;
  readonly onPin: (n: RoomNote) => void;
  readonly onVerify: (n: RoomNote) => void;
  readonly onArchive: (n: RoomNote) => void;
  readonly onDelete: (n: RoomNote) => void;
  readonly onHistory: (n: RoomNote) => void;
}

// NoteCard is one note of the panel: its type, title and preview, whether it
// is verified, and (opened) its text and sources. A member who may act gets
// Edit, Pin, "Still true", Archive and Delete; everyone gets History. A
// superseded note is history: it can be read and removed, not changed. The
// brief is always read, so it is neither pinned nor archived.
export function NoteCard({
  note,
  replaces,
  mayAct,
  busy,
  actions,
}: {
  readonly note: RoomNote;
  // The title of the note this one replaced, when the room still has it.
  readonly replaces?: string;
  readonly mayAct: boolean;
  readonly busy: boolean;
  readonly actions: NoteActions;
}) {
  const [open, setOpen] = useState(false);
  const isBrief = note.type === "brief";
  const active = note.status === "active";
  const editable = canEditNote(note);
  const dates = noteDates(note);
  const Caret = open ? CaretDownIcon : CaretRightIcon;
  return (
    <li className="rounded-[var(--radius)] border border-[var(--color-border)] p-3">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="tactile flex w-full items-start gap-2 rounded-[var(--radius)] text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
      >
        <Caret size={14} className="mt-1 shrink-0 text-[var(--color-fg-subtle)]" />
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-2">
            <Badge tone={isBrief ? "accent" : "neutral"}>{NOTE_TYPE_LABELS[note.type].chip}</Badge>
            {note.pinned && active && <PushPinIcon size={14} weight="fill" aria-label="Pinned" className="text-[var(--color-accent)]" />}
            {note.status !== "active" && <Badge tone="warning">{note.status === "superseded" ? "Replaced" : "Archived"}</Badge>}
            <span className="min-w-0 break-words text-sm font-medium">{note.title}</span>
          </span>
          {!open && note.body && <span className="mt-1 block break-words text-xs text-[var(--color-fg-muted)]">{notePreview(note.body)}</span>}
        </span>
      </button>
      <p className="ml-6 mt-1 flex flex-wrap items-center gap-x-2 text-xs text-[var(--color-fg-subtle)]">
        <span title={dates.verifiedTitle} className={cn(note.verifiedAtMs ? "text-[var(--color-fg-muted)]" : "text-[var(--color-warning)]")}>
          {dates.verified}
        </span>
        <span aria-hidden>·</span>
        <span title={exactTime(note.updatedAtMs)}>{dates.updated}</span>
        <span aria-hidden>·</span>
        <UserName id={note.author} />
      </p>
      {open && (
        <div className="ml-6 mt-2 flex flex-col gap-2">
          <p className="whitespace-pre-wrap break-words text-sm">{note.body || "No text: the title says it."}</p>
          {replaces && <p className="text-xs text-[var(--color-fg-subtle)]">Replaces: {replaces}</p>}
          {note.sources && note.sources.length > 0 && (
            <p className="text-xs text-[var(--color-fg-subtle)]">From {note.sources.map(sourceLabel).join(", ")}</p>
          )}
        </div>
      )}
      <div className="mt-2 flex flex-wrap items-center justify-end gap-1">
        {mayAct && active && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => actions.onVerify(note)} title="Confirm this note is still true">
            <CheckCircleIcon size={14} /> Still true
          </Button>
        )}
        {mayAct && active && !isBrief && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => actions.onPin(note)}>
            {note.pinned ? <PushPinSlashIcon size={14} /> : <PushPinIcon size={14} />} {note.pinned ? "Unpin" : "Pin"}
          </Button>
        )}
        {mayAct && editable && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => actions.onEdit(note)}>
            <PencilSimpleIcon size={14} /> Edit
          </Button>
        )}
        {mayAct && editable && !isBrief && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => actions.onArchive(note)}>
            {active ? <ArchiveIcon size={14} /> : <ArrowUUpLeftIcon size={14} />} {active ? "Archive" : "Restore"}
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={() => actions.onHistory(note)}>
          <ClockCounterClockwiseIcon size={14} /> History
        </Button>
        {mayAct && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => actions.onDelete(note)} aria-label="Delete note" title="Delete note and its history">
            <TrashIcon size={14} />
          </Button>
        )}
      </div>
    </li>
  );
}
