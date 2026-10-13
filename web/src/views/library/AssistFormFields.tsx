import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import type { ContentForm } from "@/lib/contentComponents";
import { AssistContextValues, type AssistContext, type AssistField, type AssistFieldType, type AssistSpec } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Field, Input, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";

// AssistFormFields is the Library form's part for an AI assist
// (docs/prd/2026-10-13-ai-assist.md §3): its instructions (the task's
// system prompt), its output (streamed text, or a JSON object of named
// fields an entry fills: each a text, a list of texts, or a list of items
// whose own fields it names) and the context the server may give it.

const CONTEXT_LABELS: Readonly<Record<AssistContext, string>> = {
  room: "The room (its expert, skills and recent messages)",
  artifact: "The deliverable (its title, type and recipe)",
  attachments: "Attached files (names and types)",
  library: "The member's library (the experts and skill packs they can use)",
  clock: "The current time in the member's timezone",
};

const FIELD_TYPES: readonly { readonly value: AssistFieldType; readonly label: string }[] = [
  { value: "text", label: "Text" },
  { value: "list", label: "List" },
  { value: "items", label: "Items" },
];

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
            <div key={i} className="flex flex-col gap-1">
              <div className="flex items-center gap-2">
                <Input className="w-40" value={f.name} onChange={(e) => setField(i, { name: e.target.value })} placeholder="displayName" aria-label="Field name" />
                <Input className="flex-1" value={f.description ?? ""} onChange={(e) => setField(i, { description: e.target.value })} placeholder="What the field holds" aria-label="Field description" />
                <Select className="w-24 shrink-0" value={f.type ?? "text"} onChange={(e) => setField(i, fieldType(e.target.value as AssistFieldType, f))} aria-label="Field type">
                  {FIELD_TYPES.map((t) => (
                    <option key={t.value} value={t.value}>
                      {t.label}
                    </option>
                  ))}
                </Select>
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
              {f.type === "items" && (
                <Input
                  className="ml-4"
                  value={(f.item ?? []).map((x) => x.name).join(", ")}
                  onChange={(e) => setField(i, { item: itemFields(e.target.value, f.item ?? []) })}
                  placeholder="Each item's fields, e.g. title, prompt"
                  aria-label="Item fields"
                />
              )}
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

// fieldType switches a field's shape: an items field keeps (or starts) its
// item fields; any other shape drops them.
function fieldType(type: AssistFieldType, f: AssistField): Partial<AssistField> {
  return type === "items" ? { type, item: f.item ?? [] } : { type, item: undefined };
}

// itemFields reads an items field's comma list of sub-field names, keeping
// the description of a name already there (a trailing "" while typing).
function itemFields(list: string, current: readonly AssistField[]): AssistField[] {
  const names = list.split(",").map((n) => n.trim());
  return names
    .filter((n, i) => (n === "" ? i === names.length - 1 : names.indexOf(n) === i))
    .map((name) => current.find((x) => x.name === name) ?? { name });
}
