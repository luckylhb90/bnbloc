import { useEffect, useMemo, useState } from "react";
import { cn } from "@/lib/cn";
import { promptsFromMessages, titleFromText, type PromptDraft } from "@/lib/promptPacks";
import { announcePromptPacksChanged, promptPacksApi } from "@/lib/promptPacksApi";
import { parseTags } from "@/lib/team";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import type { FeedItem } from "@/views/room/types";
import { errorText } from "@/lib/errors";

// The message a prompt is saved from.
export interface PromptSource {
  readonly text: string;
  readonly seq?: number;
}

// How many recent messages "Add more messages" offers.
const MORE_MESSAGES = 30;

// SavePromptDialog saves a message as a prompt into the member's personal
// pack ("My prompts", created on the first save, a new version after).
// Selecting more messages from the room continues in the prompt pack form
// (onContinue) with one prompt per message, into a new pack or one of the
// member's.
export function SavePromptDialog({
  source,
  feed,
  onClose,
  onContinue,
}: {
  readonly source: PromptSource | null;
  readonly feed: readonly FeedItem[];
  readonly onClose: () => void;
  readonly onContinue: (drafts: PromptDraft[]) => void;
}) {
  const toast = useToast();
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [tags, setTags] = useState("");
  const [more, setMore] = useState(false);
  const [picked, setPicked] = useState<ReadonlySet<number>>(new Set());
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!source) return;
    setTitle(titleFromText(source.text));
    setBody(source.text);
    setTags("");
    setMore(false);
    setPicked(new Set());
  }, [source]);

  // The room's recent messages with text, newest first, but the source.
  const candidates = useMemo(() => {
    if (!source) return [];
    const out: FeedItem[] = [];
    for (let i = feed.length - 1; i >= 0 && out.length < MORE_MESSAGES; i--) {
      const m = feed[i];
      if (!m.text.trim() || (source.seq !== undefined && m.seq === source.seq && m.text === source.text)) continue;
      out.push(m);
    }
    return out;
  }, [feed, source]);

  const toggle = (id: number) =>
    setPicked((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const save = async () => {
    if (!body.trim() || busy) return;
    if (picked.size > 0) {
      const first = { ...promptsFromMessages([body])[0], title: title.trim() || titleFromText(body), tags };
      const rest = promptsFromMessages(
        candidates.filter((m) => picked.has(m.id)).reverse().map((m) => m.text),
        [first],
      );
      onContinue([first, ...rest]);
      onClose();
      return;
    }
    setBusy(true);
    try {
      const saved = await promptPacksApi.savePrompt({ title: title.trim(), body, tags: parseTags(tags) });
      toast.success(saved.created ? "Saved to My prompts (it is on in your composer)" : "Saved to My prompts");
      announcePromptPacksChanged();
      onClose();
    } catch (e) {
      toast.error(errorText(e, "Could not save the prompt"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={!!source}
      onClose={onClose}
      size="lg"
      title="Save as prompt"
      description="Keep this message as a prompt in My prompts. Use {{name}} for a value asked when it is inserted."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || !body.trim()}>
            {busy ? "Saving…" : picked.size > 0 ? `Continue with ${picked.size + 1} prompts` : "Save"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Title">
          <Input value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} autoFocus />
        </Field>
        <Field label="Prompt">
          <Textarea rows={8} value={body} onChange={(e) => setBody(e.target.value)} className="resize-y font-mono text-xs" />
        </Field>
        <Field label="Tags" hint="Separate tags with commas or spaces.">
          <Input value={tags} placeholder="e.g. review, release" onChange={(e) => setTags(e.target.value)} />
        </Field>
        {candidates.length > 0 && (
          <div>
            <button type="button" onClick={() => setMore((v) => !v)} className="tactile text-sm text-[var(--color-accent)] hover:underline">
              {more ? "Hide other messages" : "Add more messages from this room"}
            </button>
            {more && (
              <ul className="mt-2 flex max-h-60 flex-col gap-1 overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border)] p-1">
                {candidates.map((m) => (
                  <li key={m.id}>
                    <label
                      className={cn(
                        "flex cursor-pointer items-start gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm hover:bg-[var(--color-surface-2)]",
                        picked.has(m.id) && "bg-[var(--color-accent-soft)]",
                      )}
                    >
                      <input type="checkbox" className="mt-1" checked={picked.has(m.id)} onChange={() => toggle(m.id)} />
                      <span className="line-clamp-2 min-w-0 flex-1 text-[var(--color-fg-muted)]">{m.text}</span>
                    </label>
                  </li>
                ))}
              </ul>
            )}
            {picked.size > 0 && (
              <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">Several messages continue in the prompt pack form, into a new pack or one of yours.</p>
            )}
          </div>
        )}
      </div>
    </Dialog>
  );
}
