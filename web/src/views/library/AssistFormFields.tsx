import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import type { ContentForm } from "@/lib/contentComponents";
import { AssistContextValues, type AssistContext, type AssistField, type AssistSpec } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Field, Input, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";

// AssistFormFields is the Library form's part for an AI assist
// (docs/prd/2026-10-13-ai-assist.md §3): its instructions (the task's
// system prompt), its output (streamed text, or a JSON object of named
// fields an entry fills) and the context the server may give it.

const CONTEXT_LABELS: Readonly<Record<AssistContext, string>> = {
  room: "The room (its expert, skills and recent messages)",
  artifact: "The deliverable (its title, type and recipe)",
  attachments: "Attached files (names and types)",
};

export function AssistFormFields({ form, patch }: { readonly form: ContentForm; readonly patch: (p: Partial<ContentForm>) => void }) {
  const spec: AssistSpec = form.assist ?? { output: "text" };
  const set = (p: Partial<AssistSpec>) => patch({ assist: { ...spec, ...p } });
  const fields = spec.fields ?? [];
  const setField = (i: number, p: Partial<AssistField>) => set({ fields: fields.map((f, j) => (j === i ? { ...f, ...p } : f)) });
  const context = spec.context ?? [];
  return (
    <>
      <Field label="Instructions" hint="The task's system prompt: what to do with what the member typed. Text in the context reaches it fenced as data.">
        <Textarea rows={10} value={form.instructions} onChange={(e) => patch({ instructions: e.target.value })} />
      </Field>
      <Field label="Output" hint="Text streams into the field it was asked from; JSON fills named form fields.">
        <Select value={spec.output} onChange={(e) => set({ output: e.target.value === "json" ? "json" : "text" })}>
          <option value="text">Text</option>
          <option value="json">JSON fields</option>
        </Select>
      </Field>
      {spec.output === "json" && (
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Reply fields</legend>
          {fields.map((f, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input className="w-40" value={f.name} onChange={(e) => setField(i, { name: e.target.value })} placeholder="displayName" aria-label="Field name" />
              <Input className="flex-1" value={f.description ?? ""} onChange={(e) => setField(i, { description: e.target.value })} placeholder="What the field holds" aria-label="Field description" />
              <label className="flex shrink-0 items-center gap-1 text-xs text-[var(--color-fg-muted)]">
                <input type="checkbox" checked={!!f.list} onChange={(e) => setField(i, { list: e.target.checked })} />
                List
              </label>
              <button
                type="button"
                onClick={() => set({ fields: fields.filter((_, j) => j !== i) })}
                title="Remove the field"
                aria-label="Remove the field"
                className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]"
              >
                <TrashIcon size={15} />
              </button>
            </div>
          ))}
          <div>
            <Button variant="ghost" size="sm" onClick={() => set({ fields: [...fields, { name: "", description: "" }] })}>
              <PlusIcon size={14} />
              Add a field
            </Button>
          </div>
        </fieldset>
      )}
      <fieldset className="flex flex-col gap-1.5">
        <legend className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Context it may receive</legend>
        {AssistContextValues.map((c) => (
          <label key={c} className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={context.includes(c)}
              onChange={(e) => set({ context: e.target.checked ? [...context, c] : context.filter((x) => x !== c) })}
            />
            {CONTEXT_LABELS[c]}
          </label>
        ))}
      </fieldset>
    </>
  );
}
