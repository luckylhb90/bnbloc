import { useEffect, useRef, useState } from "react";
import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import {
  emptyPromptDraft,
  emptyPromptPackForm,
  formFromPack,
  promptPackProblem,
  syncVariables,
  uniqueId,
  type PromptDraft,
  type PromptPackForm,
} from "@/lib/promptPacks";
import { promptPacksApi } from "@/lib/promptPacksApi";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";
import { DRAFT_PROMPT_PACK, filledPromptPack, problemList, promptPackDraftPatch } from "@/lib/assistDrafts";
import { DraftWithAI } from "@/views/library/DraftWithAI";

// The editor target: a new pack, a new version of one of the member's packs
// (prefilled from its latest version), or drafted prompts (from selected
// messages) saved into a new pack or appended to one of the member's.
export type PromptPackTarget =
  | { readonly kind: "new" }
  | { readonly base: ContentComponent }
  | { readonly drafts: readonly PromptDraft[] };

function PromptEditor({ draft, onChange, onRemove }: { readonly draft: PromptDraft; readonly onChange: (d: PromptDraft) => void; readonly onRemove: () => void }) {
  const set = (patch: Partial<PromptDraft>) => onChange({ ...draft, ...patch });
  const setVar = (i: number, patch: Partial<PromptDraft["variables"][number]>) =>
    set({ variables: draft.variables.map((v, j) => (j === i ? { ...v, ...patch } : v)) });
  return (
    <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] p-3">
      <div className="flex items-center gap-2">
        <Input className="min-w-0 flex-1" value={draft.title} placeholder="Title" aria-label="Prompt title" onChange={(e) => set({ title: e.target.value })} />
        <Input className="w-32 font-mono text-xs" value={draft.slash} placeholder="/command" aria-label="Slash command" onChange={(e) => set({ slash: e.target.value })} />
        <button type="button" onClick={onRemove} aria-label="Remove prompt" title="Remove prompt" className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]">
          <TrashIcon size={15} />
        </button>
      </div>
      <Textarea
        rows={4}
        value={draft.body}
        placeholder="The prompt. Use {{name}} for a value asked when it is inserted."
        aria-label="Prompt"
        className="resize-y font-mono text-xs"
        onChange={(e) => set({ body: e.target.value, variables: syncVariables(e.target.value, draft.variables) })}
      />
      <Input value={draft.tags} placeholder="Tags, e.g. writing, review" aria-label="Tags" onChange={(e) => set({ tags: e.target.value })} />
      <Input
        value={draft.files}
        placeholder="For files, e.g. .pptx, .key, image/* (empty: any conversation)"
        aria-label="For files"
        className="font-mono text-xs"
        onChange={(e) => set({ files: e.target.value })}
      />
      <Input
        value={draft.selection}
        placeholder="For selections, e.g. text, cells, region (empty: whole file)"
        aria-label="For selections"
        className="font-mono text-xs"
        onChange={(e) => set({ selection: e.target.value })}
      />
      <Switch
        checked={draft.starter}
        onChange={(on) => set({ starter: on })}
        label={
          draft.selection.trim()
            ? "Suggested: offered while a member selects such a part, beside it and in the Ask AI panel"
            : draft.files.trim()
            ? "Suggested: offered for these files, in their Ask AI panel and in a room showing one"
            : "Starter: offered in empty rooms that use this pack"
        }
      />
      {draft.variables.length > 0 && (
        <div className="flex flex-col gap-2">
          <p className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Variables</p>
          {draft.variables.map((v, i) => (
            <div key={v.name} className="grid grid-cols-1 gap-2 rounded-[var(--radius)] bg-[var(--color-surface-2)] p-2 sm:grid-cols-3">
              <span className="font-mono text-xs text-[var(--color-accent)] sm:col-span-3">{`{{${v.name}}}`}</span>
              <Input value={v.label} placeholder="Label" aria-label={`Label of ${v.name}`} onChange={(e) => setVar(i, { label: e.target.value })} />
              <Input value={v.default} placeholder="Default" aria-label={`Default of ${v.name}`} onChange={(e) => setVar(i, { default: e.target.value })} />
              <Textarea
                rows={2}
                value={v.options}
                placeholder="Options, one per line (a select)"
                aria-label={`Options of ${v.name}`}
                className="resize-y text-xs"
                onChange={(e) => setVar(i, { options: e.target.value })}
              />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// PromptPackEditorDialog creates a prompt pack, saves a new version of one,
// or saves drafted prompts (from selected messages) into a new pack or one
// of the member's. The server validates; its message shows in the toast.
export function PromptPackEditorDialog({
  target,
  onClose,
  onSaved,
}: {
  readonly target: PromptPackTarget | null;
  readonly onClose: () => void;
  readonly onSaved?: (c: ContentComponent) => void;
}) {
  const toast = useToast();
  const [form, setForm] = useState<PromptPackForm>(emptyPromptPackForm());
  // The pack a save writes a new version of ("" = a new pack).
  const [into, setInto] = useState<ContentComponent | null>(null);
  const [mine, setMine] = useState<readonly ContentComponent[]>([]);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const drafts = target && "drafts" in target ? target.drafts : null;
  const loadSeq = useRef(0);

  // load prefills the form from a pack's latest version plus extra drafts
  // (appended with ids unique in the pack).
  const load = async (c: ContentComponent, extra: readonly PromptDraft[]) => {
    const seq = ++loadSeq.current;
    setLoading(true);
    try {
      const d = await contentApi.version(c.id, c.latestVersion);
      if (seq !== loadSeq.current) return;
      const f = formFromPack(d.component);
      const ids = new Set(f.prompts.map((p) => p.id));
      const added = extra.map((p) => {
        const id = ids.has(p.id) || !p.id ? uniqueId(p.title || p.body, ids) : p.id;
        ids.add(id);
        return { ...p, id };
      });
      setForm({ ...f, prompts: [...f.prompts, ...added] });
      setInto(d.component);
    } catch (e) {
      if (seq === loadSeq.current) toast.error(errorText(e, "Could not load the pack"));
    } finally {
      if (seq === loadSeq.current) setLoading(false);
    }
  };

  useEffect(() => {
    loadSeq.current++;
    setInto(null);
    setLoading(false);
    if (!target) return;
    if ("base" in target) {
      setForm(formFromPack(target.base));
      void load(target.base, []);
    } else {
      setForm({ ...emptyPromptPackForm(), prompts: "drafts" in target ? [...target.drafts] : [emptyPromptDraft()] });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target]);
  useEffect(() => {
    if (!drafts) return;
    let live = true;
    contentApi
      .list("prompt")
      .then((r) => live && setMine((r.components ?? []).filter((c) => c.mine)))
      .catch(() => live && setMine([]));
    return () => {
      live = false;
    };
  }, [drafts]);

  const pickInto = (id: string) => {
    if (!drafts) return;
    const c = mine.find((x) => x.id === id);
    if (!c) {
      loadSeq.current++;
      setInto(null);
      setForm({ ...emptyPromptPackForm(), prompts: [...drafts] });
      return;
    }
    void load(c, drafts);
  };

  const problem = promptPackProblem(form);
  const save = async () => {
    if (problem || busy || loading) return;
    setBusy(true);
    try {
      const d = into ? await promptPacksApi.newVersion(into.id, form, into.latestVersion) : await promptPacksApi.create(form);
      toast.success(into ? `Saved ${contentLabel(d.component)} v${d.component.latestVersion}` : `Created the prompt pack ${d.component.name}`);
      onSaved?.(d.component);
      onClose();
    } catch (e) {
      toast.error(errorText(e, "Could not save the prompt pack"));
    } finally {
      setBusy(false);
    }
  };

  const setPrompt = (i: number, d: PromptDraft) => setForm((f) => ({ ...f, prompts: f.prompts.map((p, j) => (j === i ? d : p)) }));
  const title = !target ? undefined : "base" in target ? `New version of ${contentLabel(target.base)}` : drafts ? "Save messages as prompts" : "New prompt pack";
  return (
    <Dialog
      open={!!target}
      onClose={onClose}
      size="lg"
      title={title}
      description="Prompts with {{variables}}, inserted from the composer's prompt picker or as /commands."
      footer={
        <>
          {problem && <span className="mr-auto text-xs text-[var(--color-fg-subtle)]">{problem}</span>}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={!!problem || busy || loading}>
            {busy ? "Saving…" : into ? "Save new version" : "Create"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {!loading && !drafts && (
          <DraftWithAI
            task={DRAFT_PROMPT_PACK}
            placeholder="Describe the prompts, e.g. weekly report and retro prompts for our team"
            current={() => filledPromptPack(form)}
            check={(fields) => problemList(promptPackProblem({ ...form, ...promptPackDraftPatch(fields, form, !into) }))}
            onDraft={(fields) => setForm((f) => ({ ...f, ...promptPackDraftPatch(fields, f, !into) }))}
          />
        )}
        {drafts && (
          <Field label="Save into">
            <Select value={into?.id ?? ""} onChange={(e) => pickInto(e.target.value)} aria-label="Save into">
              <option value="">A new prompt pack</option>
              {mine.map((c) => (
                <option key={c.id} value={c.id}>
                  {contentLabel(c)} (v{c.latestVersion})
                </option>
              ))}
            </Select>
          </Field>
        )}
        {!into && (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Name" hint="Lower-case letters, digits and hyphens.">
              <Input value={form.name} placeholder="release-writing" onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} />
            </Field>
            <Field label="Display name">
              <Input value={form.displayName} placeholder="Release writing" onChange={(e) => setForm((f) => ({ ...f, displayName: e.target.value }))} />
            </Field>
          </div>
        )}
        <Field label="Description">
          <Input value={form.description} onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))} />
        </Field>
        <Switch checked={form.everyRoom} onChange={(on) => setForm((f) => ({ ...f, everyRoom: on }))} label="Offer its starters in every empty room" />
        {loading ? (
          <p className="text-sm text-[var(--color-fg-muted)]">Loading…</p>
        ) : (
          <div className="flex flex-col gap-3">
            {form.prompts.map((d, i) => (
              <PromptEditor
                key={i}
                draft={d}
                onChange={(next) => setPrompt(i, next)}
                onRemove={() => setForm((f) => ({ ...f, prompts: f.prompts.filter((_, j) => j !== i) }))}
              />
            ))}
            <Button variant="outline" onClick={() => setForm((f) => ({ ...f, prompts: [...f.prompts, emptyPromptDraft()] }))}>
              <PlusIcon size={14} /> Add prompt
            </Button>
          </div>
        )}
      </div>
    </Dialog>
  );
}
