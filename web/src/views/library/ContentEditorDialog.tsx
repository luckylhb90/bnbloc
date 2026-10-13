import { useEffect, useRef, useState } from "react";
import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import {
  ARCHIVE_ACCEPT,
  contentKindWord,
  emptyContentForm,
  formFromVersion,
  formProblem,
  type ContentComponent,
  type ContentForm,
  type ContentFormSkill,
  type ContentKind,
} from "@/lib/contentComponents";
import type { MemberComponent } from "@/lib/apiTypes";
import { contentApi } from "@/lib/contentComponentsApi";
import { packPromptOptions, type PackPromptOption } from "@/lib/contentFormRefs";
import { mcpToolsApi } from "@/lib/mcpToolsApi";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { AssistFormFields } from "@/views/library/AssistFormFields";
import { QuickPromptsEditor, RequirementsFields, TagsIconFields, TeamEditor } from "@/views/library/ContentFormExtras";
import { errorText } from "@/lib/errors";

// The editor target: a new component of a kind, or a new version of one of
// the member's components (prefilled from its latest version).
export type EditorTarget = { readonly kind: ContentKind } | { readonly base: ContentComponent };

function SkillsEditor({ skills, onChange }: { readonly skills: ContentFormSkill[]; readonly onChange: (s: ContentFormSkill[]) => void }) {
  const set = (i: number, patch: Partial<ContentFormSkill>) => onChange(skills.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  return (
    <div className="flex flex-col gap-3">
      {skills.map((s, i) => (
        <div key={i} className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] p-3">
          <div className="flex items-center gap-2">
            <Input placeholder="skill-name" value={s.name} onChange={(e) => set(i, { name: e.target.value })} aria-label="Skill name" />
            <button
              type="button"
              onClick={() => onChange(skills.filter((_, j) => j !== i))}
              aria-label="Remove skill"
              title="Remove skill"
              className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]"
            >
              <TrashIcon size={16} />
            </button>
          </div>
          <Input placeholder="When the agent should use it" value={s.description} onChange={(e) => set(i, { description: e.target.value })} aria-label="Skill description" />
          <Textarea rows={4} placeholder="How to do it (SKILL.md body)" value={s.body} onChange={(e) => set(i, { body: e.target.value })} aria-label="Skill body" />
        </div>
      ))}
      <Button variant="outline" size="sm" className="self-start" onClick={() => onChange([...skills, { name: "", description: "", body: "" }])}>
        <PlusIcon size={14} /> Add skill
      </Button>
    </div>
  );
}

// ContentEditorDialog creates an expert or skill pack by form, or saves a
// new version of one (the server keeps the base version's scripts and
// references and bumps the version).
export function ContentEditorDialog({
  target,
  onClose,
  onSaved,
}: {
  readonly target: EditorTarget | null;
  readonly onClose: () => void;
  readonly onSaved: () => void;
}) {
  const toast = useToast();
  const [form, setForm] = useState<ContentForm | null>(null);
  const [busy, setBusy] = useState(false);
  const base = target && "base" in target ? target.base : null;
  // What the reference pickers offer: the prompts of the prompt packs and
  // the MCP tools the member can see (best-effort: a failed load offers none).
  const [packPrompts, setPackPrompts] = useState<readonly PackPromptOption[]>([]);
  const [tools, setTools] = useState<readonly MemberComponent[] | null>(null);
  const open = !!target;
  useEffect(() => {
    if (!open) return;
    let live = true;
    setTools(null);
    contentApi
      .list("prompt")
      .then((r) => live && setPackPrompts(packPromptOptions(r.components ?? [])))
      .catch(() => live && setPackPrompts([]));
    mcpToolsApi
      .list()
      .then((t) => live && setTools(t))
      .catch(() => live && setTools([]));
    return () => {
      live = false;
    };
  }, [open]);

  useEffect(() => {
    if (!target) {
      setForm(null);
      return;
    }
    if (!("base" in target)) {
      setForm(emptyContentForm(target.kind));
      return;
    }
    let live = true;
    contentApi
      .version(target.base.id, target.base.latestVersion)
      .then((d) => live && setForm(formFromVersion(d)))
      .catch((e) => {
        if (!live) return;
        toast.error(errorText(e, "Could not load the latest version"));
        onClose();
      });
    return () => {
      live = false;
    };
  }, [target, toast, onClose]);

  const patch = (p: Partial<ContentForm>) => setForm((f) => (f ? { ...f, ...p } : f));
  const save = async () => {
    if (!form || busy) return;
    const problem = formProblem(form);
    if (problem) {
      toast.error(problem);
      return;
    }
    setBusy(true);
    try {
      const d = base ? await contentApi.newVersion(base.id, form, base.latestVersion) : await contentApi.create(form);
      toast.success(base ? `Saved version ${d.component.latestVersion}` : `Created ${contentKindWord(form.kind)} ${d.component.name}`);
      onSaved();
      onClose();
    } catch (e) {
      toast.error(errorText(e, "Could not save"));
    } finally {
      setBusy(false);
    }
  };

  const kind = form?.kind ?? (target && !("base" in target) ? target.kind : base?.kind ?? "expert");
  return (
    <Dialog
      open={!!target}
      onClose={onClose}
      size="lg"
      title={base ? `New version of ${base.displayName || base.name}` : `New ${contentKindWord(kind)}`}
      description={
        base
          ? `Saves version after ${base.latestVersion}. Earlier versions stay as they are.`
          : kind === "expert"
            ? "A role an agent wears: always-on instructions, optional skills and quick prompts."
            : kind === "assist"
              ? "A task of the AI assist behind a manual entry: one with the same name as a built-in replaces it for you."
              : "Capabilities an agent loads when a skill's description matches the task."
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={!form || busy}>
            {busy ? "Saving…" : base ? "Save version" : "Create"}
          </Button>
        </>
      }
    >
      {!form ? (
        <p className="text-sm text-[var(--color-fg-muted)]">Loading…</p>
      ) : (
        <div className="flex flex-col gap-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Name" hint="Lower-case letters, digits and hyphens">
              <Input value={form.name} disabled={!!base} onChange={(e) => patch({ name: e.target.value })} placeholder={kind === "expert" ? "reviewer" : kind === "assist" ? "enhance-prompt" : "lint-kit"} />
            </Field>
            <Field label="Display name">
              <Input value={form.displayName} onChange={(e) => patch({ displayName: e.target.value })} placeholder={kind === "expert" ? "Code reviewer" : kind === "assist" ? "Enhance prompt" : "Lint kit"} />
            </Field>
          </div>
          <Field label="Description">
            <Input value={form.description} onChange={(e) => patch({ description: e.target.value })} />
          </Field>
          <TagsIconFields form={form} patch={patch} />
          {kind === "expert" && (
            <>
              <Field label="Category" hint="Optional, e.g. engineering or writing">
                <Input value={form.category} onChange={(e) => patch({ category: e.target.value })} />
              </Field>
              <Field label="Instructions" hint="The always-on part: persona and method">
                <Textarea rows={8} value={form.instructions} onChange={(e) => patch({ instructions: e.target.value })} />
              </Field>
              <Field label="Welcome" hint="Shown when someone picks this expert">
                <Textarea rows={2} value={form.welcome} onChange={(e) => patch({ welcome: e.target.value })} />
              </Field>
              <QuickPromptsEditor form={form} patch={patch} options={packPrompts} />
            </>
          )}
          {kind === "assist" ? (
            <AssistFormFields form={form} patch={patch} />
          ) : (
            <>
              <div className="flex flex-col gap-2">
                <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
                  {kind === "expert" ? "Embedded skills (optional)" : "Skills"}
                </span>
                <SkillsEditor skills={form.skills} onChange={(skills) => patch({ skills })} />
              </div>
              <RequirementsFields form={form} patch={patch} kind={kind} tools={tools} />
            </>
          )}
          {kind === "expert" && <TeamEditor team={form.team} onChange={(team) => patch({ team })} />}
        </div>
      )}
    </Dialog>
  );
}

// ImportDialog imports a skill directory, a Claude Code-style plugin or a
// package archive as a new component of the tab's kind, or as a new version
// of one of the member's components.
export function ImportDialog({
  kind,
  open,
  mine,
  onClose,
  onImported,
}: {
  readonly kind: ContentKind;
  readonly open: boolean;
  readonly mine: readonly ContentComponent[];
  readonly onClose: () => void;
  readonly onImported: () => void;
}) {
  const toast = useToast();
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [into, setInto] = useState("");
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (open) {
      setFile(null);
      setName("");
      setInto("");
    }
  }, [open]);
  const submit = async () => {
    if (!file || busy) return;
    setBusy(true);
    try {
      const d = await contentApi.importArchive(file, { kind, name, existingId: into || undefined });
      toast.success(into ? `Imported version ${d.component.latestVersion}` : `Imported ${d.component.name}`);
      onImported();
      onClose();
    } catch (e) {
      toast.error(errorText(e, "Could not import"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`Import ${kind === "expert" ? "an expert" : "a skill pack"}`}
      description={
        kind === "expert"
          ? "A .zip or .tar(.gz) of a folder with INSTRUCTIONS.md (or AGENTS.md / CLAUDE.md), or a Claude Code-style plugin with an agent."
          : "A .zip or .tar(.gz) of a skill folder (SKILL.md), a folder of skills, or a Claude Code-style plugin."
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!file || busy}>
            {busy ? "Importing…" : "Import"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Archive">
          <div className="flex items-center gap-2">
            <input ref={fileRef} type="file" accept={ARCHIVE_ACCEPT} className="hidden" onChange={(e) => setFile(e.currentTarget.files?.[0] ?? null)} />
            <Button variant="outline" size="sm" onClick={() => fileRef.current?.click()}>
              Choose file
            </Button>
            <span className="min-w-0 truncate text-sm text-[var(--color-fg-muted)]">{file ? file.name : "No file chosen"}</span>
          </div>
        </Field>
        <Field label="Import as">
          <Select value={into} onChange={(e) => setInto(e.target.value)}>
            <option value="">A new {contentKindWord(kind)}</option>
            {mine.map((c) => (
              <option key={c.id} value={c.id}>
                New version of {c.displayName || c.name} (now {c.latestVersion})
              </option>
            ))}
          </Select>
        </Field>
        {!into && (
          <Field label="Name" hint="Optional: taken from the archive when empty">
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        )}
      </div>
    </Dialog>
  );
}
