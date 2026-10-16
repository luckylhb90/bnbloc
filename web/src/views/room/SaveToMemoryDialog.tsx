import { useMemo } from "react";
import { SAVE_TYPES, noteDraftFromMessage } from "@/lib/notes";
import { useToast } from "@/ui/Toast";
import { NoteEditor } from "@/views/room/NoteEditor";

// The message a note is saved from.
export interface MemorySource {
  readonly text: string;
  readonly seq: number;
}

// SaveToMemoryDialog saves a chat message as a room note: a small form
// prefilled from the message (its first line as the title, its text as the
// body, cut to the note limits), with the type to choose. The note names the
// message as its source. Any member who may act can save; nothing else is
// asked, and no AI is involved.
export function SaveToMemoryDialog({
  roomId,
  source,
  onClose,
}: {
  readonly roomId: string;
  readonly source: MemorySource | null;
  readonly onClose: () => void;
}) {
  const toast = useToast();
  const draft = useMemo(() => (source ? noteDraftFromMessage(source.text, source.seq) : null), [source]);
  if (!source || !draft) return null;
  return (
    <NoteEditor
      key={source.seq}
      roomId={roomId}
      note={null}
      quick
      title="Save to memory"
      types={SAVE_TYPES}
      seed={{ type: "memory", title: draft.title, body: draft.body, sources: draft.sources }}
      onClose={onClose}
      onSaved={() => {
        toast.success("Saved to memory");
        onClose();
      }}
    />
  );
}
