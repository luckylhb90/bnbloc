import { useState } from "react";
import { runJSONAssist, useAssistTask, type AssistTask } from "@/lib/assist";
import type { ComponentSetting } from "@/lib/componentSettings";
import { errorText } from "@/lib/errors";
import { FILL_SETTINGS, fillRequestText, fillResult, type FillResult } from "@/lib/settingsFill";
import { Button } from "@/ui/Button";
import { Textarea } from "@/ui/Input";
import { AssistButton, type AssistRunState } from "@/views/AssistButton";

// FillFromNote is the one settings form's "Fill from a note"
// (docs/prd/2026-10-19-component-settings.md §2.5): the person pastes a
// setup note (an email, a README section), the fill-settings assist maps it
// onto the fields this form edits, and the person ticks what goes into the
// form. Nothing is saved until they press Save; a ticked value is checked
// like a typed one. A secret's value is never shown, only that the note
// gave one. Hidden when assists are unavailable.

export function FillFromNote({ fields, onApply }: { readonly fields: readonly ComponentSetting[]; readonly onApply: (values: Readonly<Record<string, string>>) => void }) {
  const task = useAssistTask(FILL_SETTINGS);
  const [open, setOpen] = useState(false);
  if (!task || fields.length === 0) return null;
  if (!open) {
    return (
      <span>
        <AssistButton variant="text" label="Fill from a note" state="idle" onRun={() => setOpen(true)} />
      </span>
    );
  }
  return <FillPanel task={task} fields={fields} onApply={onApply} onClose={() => setOpen(false)} />;
}

function FillPanel({
  task,
  fields,
  onApply,
  onClose,
}: {
  readonly task: AssistTask;
  readonly fields: readonly ComponentSetting[];
  readonly onApply: (values: Readonly<Record<string, string>>) => void;
  readonly onClose: () => void;
}) {
  const [note, setNote] = useState("");
  const [state, setState] = useState<AssistRunState>("idle");
  const [failure, setFailure] = useState("");
  const [result, setResult] = useState<FillResult | null>(null);
  const [ticked, setTicked] = useState<readonly string[]>([]);

  const run = async () => {
    if (state !== "idle" || !note.trim()) return;
    setState("waiting");
    setFailure("");
    try {
      const r = fillResult(fields, (await runJSONAssist(task.name, { text: fillRequestText(fields, note) })).fields);
      setResult(r);
      setTicked(r.proposals.map((p) => p.key));
    } catch (e) {
      setFailure(errorText(e, "Could not read the note"));
    } finally {
      setState("idle");
    }
  };

  const apply = () => {
    if (!result) return;
    onApply(Object.fromEntries(result.proposals.filter((p) => ticked.includes(p.key)).map((p) => [p.key, p.value])));
    onClose();
  };

  return (
    <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-dashed border-[var(--color-border)] bg-[var(--color-surface-2)]/40 p-3">
      <Textarea
        rows={4}
        value={note}
        onChange={(e) => setNote(e.target.value)}
        placeholder="Paste the setup note: an email, a README section. Its values go into the form for you to check; nothing is saved until you press Save."
        aria-label="Setup note"
        disabled={state !== "idle"}
      />
      <div className="flex flex-wrap items-center gap-2">
        <AssistButton variant="text" label="Fill from the note" state={state} onRun={() => void run()} disabled={!note.trim()} />
        <Button size="sm" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </div>
      {failure && <p className="text-xs text-[var(--color-danger)]">{failure}</p>}
      {result && (
        <div className="flex flex-col gap-1.5 text-xs">
          {result.proposals.length === 0 && <span className="text-[var(--color-fg-muted)]">The note gives no value for these settings.</span>}
          {result.proposals.map((p) => (
            <label key={p.key} className="flex items-start gap-2">
              <input
                type="checkbox"
                checked={ticked.includes(p.key)}
                onChange={(e) => setTicked((cur) => (e.target.checked ? [...cur, p.key] : cur.filter((k) => k !== p.key)))}
              />
              <span className="min-w-0">
                <span className="font-medium">{p.label}</span>: <span className="whitespace-pre-wrap break-all font-mono">{p.shown}</span>
              </span>
            </label>
          ))}
          {result.skipped.length > 0 && <span className="text-[var(--color-fg-muted)]">Not filled: {result.skipped.join(", ")}.</span>}
          {result.notes && <span className="text-[var(--color-fg-muted)]">{result.notes}</span>}
          {result.proposals.length > 0 && (
            <span>
              <Button size="sm" variant="primary" disabled={ticked.length === 0} onClick={apply}>
                Put the ticked values in the form
              </Button>
            </span>
          )}
        </div>
      )}
    </div>
  );
}
