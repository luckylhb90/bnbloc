import { useState } from "react";
import { runJSONAssist, useAssistTask, type AssistTask } from "@/lib/assist";
import { draftRequestText } from "@/lib/assistDrafts";
import { errorText } from "@/lib/errors";
import { isSubmitEnter } from "@/lib/keys";
import type { AssistJSONReply } from "@/lib/wire.gen";
import { Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { AssistButton, type AssistRunState } from "@/views/AssistButton";

// DraftWithAI is a Library form's "Draft with AI" (docs/prd/2026-10-13-ai-
// assist.md §6): one sentence (plus what the form already holds) to a
// draft task, whose reply the form maps onto its fields (lib/assistDrafts).
// Nothing is saved: the member edits the draft and presses Save. Hidden
// when assists are unavailable or the task is missing.

export function DraftWithAI({
  task: taskName,
  placeholder,
  current,
  onDraft,
}: {
  readonly task: string;
  readonly placeholder: string;
  // What the form already holds ("Current form"; null or {} for nothing).
  readonly current: () => Readonly<Record<string, unknown>> | null;
  readonly onDraft: (fields: AssistJSONReply["fields"]) => void;
}) {
  const task = useAssistTask(taskName);
  if (!task) return null;
  return <DraftRow task={task} placeholder={placeholder} current={current} onDraft={onDraft} />;
}

function DraftRow({
  task,
  placeholder,
  current,
  onDraft,
}: {
  readonly task: AssistTask;
  readonly placeholder: string;
  readonly current: () => Readonly<Record<string, unknown>> | null;
  readonly onDraft: (fields: AssistJSONReply["fields"]) => void;
}) {
  const toast = useToast();
  const [sentence, setSentence] = useState("");
  const [state, setState] = useState<AssistRunState>("idle");
  const form = current();
  const hasForm = !!form && Object.keys(form).length > 0;
  const run = async () => {
    if (state !== "idle" || (!sentence.trim() && !hasForm)) return;
    setState("waiting");
    try {
      const r = await runJSONAssist(task.name, { text: draftRequestText(sentence.trim() || "Improve this draft.", form) });
      onDraft(r.fields);
      toast.success("Drafted: review it, then save");
    } catch (e) {
      toast.error(errorText(e, "Could not draft it"));
    } finally {
      setState("idle");
    }
  };
  return (
    <div className="flex items-center gap-2 rounded-[var(--radius)] border border-dashed border-[var(--color-border)] bg-[var(--color-surface-2)]/40 p-2">
      <Input
        className="min-w-0 flex-1"
        value={sentence}
        onChange={(e) => setSentence(e.target.value)}
        onKeyDown={(e) => isSubmitEnter(e) && void run()}
        placeholder={placeholder}
        aria-label="What to draft"
        disabled={state !== "idle"}
      />
      <AssistButton variant="text" label="Draft with AI" state={state} onRun={() => void run()} disabled={!sentence.trim() && !hasForm} />
    </div>
  );
}
