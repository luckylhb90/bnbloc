import { useEffect, useState } from "react";
import { Select } from "@/ui/Select";
import { api, ApiError, type Prompt, type PromptVisibility, type Room } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { composerDrafts, lastRoom, parseTags, promptTitleFromText } from "@/lib/team";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";

const VISIBILITY = [
  { value: "private" as const, label: "Only me" },
  { value: "tenant" as const, label: "My organization" },
];

// The source a new library entry is saved from: a feed message's text and,
// when persisted, its room + sequence (provenance only — the body is copied).
export interface PromptSource {
  readonly text: string;
  readonly roomId?: string;
  readonly seq?: number;
}

// PromptEditorDialog saves a message to the library (source set) or edits an
// existing entry (prompt set). The title defaults to the message's first line.
export function PromptEditorDialog({
  source,
  prompt,
  onClose,
  onSaved,
}: {
  readonly source?: PromptSource | null;
  readonly prompt?: Prompt | null;
  readonly onClose: () => void;
  readonly onSaved?: (p: Prompt) => void;
}) {
  const toast = useToast();
  const open = !!source || !!prompt;
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [tags, setTags] = useState("");
  const [visibility, setVisibility] = useState<PromptVisibility>("private");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (prompt) {
      setTitle(prompt.title);
      setBody(prompt.body);
      setTags(prompt.tags.join(", "));
      setVisibility(prompt.visibility);
    } else if (source) {
      setTitle(promptTitleFromText(source.text));
      setBody(source.text);
      setTags("");
      setVisibility("private");
    }
  }, [prompt, source]);

  const submit = async () => {
    if (!title.trim() || !body.trim() || busy) return;
    setBusy(true);
    try {
      const saved = prompt
        ? await api.updatePrompt(prompt.promptId, { title: title.trim(), body, tags: parseTags(tags), visibility })
        : await api.createPrompt({
            title: title.trim(),
            body,
            tags: parseTags(tags),
            visibility,
            ...(source?.roomId && source.seq ? { sourceRoomId: source.roomId, sourceSeq: Math.floor(source.seq) } : {}),
          });
      toast.success(prompt ? "Prompt updated" : "Saved to library");
      onSaved?.(saved);
      onClose();
    } catch (e) {
      toast.error(e instanceof ApiError && e.status === 403 ? "You can only edit your own prompts" : "Could not save the prompt");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={prompt ? "Edit prompt" : "Save to library"}
      description={prompt ? undefined : "Keep this message as a reusable prompt."}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !title.trim() || !body.trim()}>
            {busy ? "Saving…" : prompt ? "Save changes" : "Save"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Title">
          <Input value={title} maxLength={256} onChange={(e) => setTitle(e.target.value)} autoFocus />
        </Field>
        <Field label="Prompt">
          <Textarea rows={8} value={body} onChange={(e) => setBody(e.target.value)} className="resize-y font-mono text-xs" />
        </Field>
        <Field label="Tags" hint="Separate tags with commas or spaces.">
          <Input value={tags} placeholder="e.g. review, release" onChange={(e) => setTags(e.target.value)} />
        </Field>
        <Field label="Visible to">
          <Segment options={VISIBILITY} value={visibility} onChange={setVisibility} />
        </Field>
      </div>
    </Dialog>
  );
}

// UsePromptDialog picks the room whose composer receives a prompt (defaulting
// to the last opened room), counts the use, queues the draft and opens the
// room. The composer merges it into any draft in progress.
export function UsePromptDialog({
  prompt,
  onClose,
  onOpenRoom,
}: {
  readonly prompt: Prompt | null;
  readonly onClose: () => void;
  readonly onOpenRoom: (roomId: string) => void;
}) {
  const { subject } = useAuth();
  const toast = useToast();
  const [rooms, setRooms] = useState<Room[] | null>(null);
  const [roomId, setRoomId] = useState("");

  useEffect(() => {
    if (!prompt || !subject) return;
    let cancelled = false;
    api
      .myRooms(subject)
      .then((r) => {
        if (cancelled) return;
        const list = r.rooms ?? [];
        setRooms(list);
        const last = lastRoom();
        setRoomId(list.some((x) => x.roomId === last) ? (last as string) : (list[0]?.roomId ?? ""));
      })
      .catch(() => !cancelled && setRooms([]));
    return () => {
      cancelled = true;
    };
  }, [prompt, subject]);

  const use = () => {
    if (!prompt || !roomId) return;
    // The counter is best-effort: using a prompt never waits on it.
    void api.usePrompt(prompt.promptId).catch(() => {});
    composerDrafts.queue(roomId, prompt.body);
    onClose();
    onOpenRoom(roomId);
    toast.info("Prompt added to the composer");
  };

  return (
    <Dialog
      open={!!prompt}
      onClose={onClose}
      title="Use prompt"
      description={prompt ? `Insert “${prompt.title}” into a room's composer.` : undefined}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={use} disabled={!roomId}>
            Use in room
          </Button>
        </>
      }
    >
      {rooms === null ? (
        <p className="text-sm text-[var(--color-fg-subtle)]">Loading rooms…</p>
      ) : rooms.length === 0 ? (
        <p className="text-sm text-[var(--color-fg-muted)]">Join or create a room first.</p>
      ) : (
        <Field label="Room">
          <Select
            value={roomId}
            onChange={(e) => setRoomId(e.target.value)}
            className="h-10 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)]"
          >
            {rooms.map((r) => (
              <option key={r.roomId} value={r.roomId}>
                {r.name || r.roomId}
              </option>
            ))}
          </Select>
        </Field>
      )}
    </Dialog>
  );
}
