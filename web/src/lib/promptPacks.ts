// Prompt packs (docs/prd/2026-10-12-experts-and-teams.md §4): the wire
// shapes of a pack's prompts (internal/wire/content_prompts.go) and the pure
// helpers of the composer (prompt choices, /slash matching, variable
// filling, insertion) and the Library form (drafts <-> payload, variables
// kept in step with the body, prompts drafted from messages). Unit-tested.
import type { ContentComponent, ContentUse } from "./contentComponents.ts";

export interface PromptVariable {
  readonly name: string;
  readonly label?: string;
  readonly default?: string;
  readonly options?: readonly string[];
}

export interface ContentPrompt {
  readonly id: string;
  readonly title: string;
  readonly body: string;
  readonly variables?: readonly PromptVariable[];
  readonly tags?: readonly string[];
  readonly slash?: string;
  readonly uses: number;
}

// A pack's prompt an expert quick prompt resolved to.
export interface PromptRef {
  readonly componentId: string;
  readonly pack: string;
  readonly version: string;
  readonly prompt: ContentPrompt;
}

// The member's personal pack (created by their first "Save as prompt").
export const PERSONAL_PROMPT_PACK = "my-prompts";

// ---------------------------------------------------------------- composer

// A prompt the composer offers: the prompt and the pack it comes from (its
// version in effect, and the room it is on in when it came from the room).
export interface PromptChoice {
  readonly prompt: ContentPrompt;
  readonly componentId: string;
  readonly pack: string;
  readonly packLabel: string;
  readonly version: string;
  readonly source: "mine" | "room";
}

function packLabel(c: Pick<ContentComponent, "displayName" | "name">): string {
  return c.displayName?.trim() || c.name;
}

// promptChoices lists the prompts of the packs turned on for the member,
// then the room's; a pack on in both is listed once (as the member's).
export function promptChoices(member: readonly ContentUse[], room: readonly ContentUse[]): PromptChoice[] {
  const out: PromptChoice[] = [];
  const seen = new Set<string>();
  const add = (uses: readonly ContentUse[], source: PromptChoice["source"]) => {
    for (const u of uses) {
      const c = u.component;
      if (u.kind !== "prompt" || !c || seen.has(u.componentId)) continue;
      seen.add(u.componentId);
      for (const p of c.prompts ?? []) {
        out.push({ prompt: p, componentId: c.id, pack: c.name, packLabel: packLabel(c), version: c.version || c.latestVersion, source });
      }
    }
  };
  add(member, "mine");
  add(room, "room");
  return out;
}

// choiceFromRef turns an expert quick prompt's resolved reference into a
// choice (inserted like any other prompt).
export function choiceFromRef(ref: PromptRef): PromptChoice {
  return { prompt: ref.prompt, componentId: ref.componentId, pack: ref.pack, packLabel: ref.pack, version: ref.version, source: "room" };
}

// slashQuery returns the typed command while the message so far is just
// "/partial" (a slash command starts the message), else null.
export function slashQuery(textBeforeCaret: string): string | null {
  const m = /^\s*\/([a-z0-9-]*)$/i.exec(textBeforeCaret);
  return m ? m[1].toLowerCase() : null;
}

// slashMatches lists the prompts whose slash command starts with the
// query, most used first, at most limit.
export function slashMatches(choices: readonly PromptChoice[], query: string, limit = 8): PromptChoice[] {
  const q = `/${query.toLowerCase()}`;
  return choices
    .filter((c) => c.prompt.slash && c.prompt.slash.startsWith(q))
    .sort((a, b) => (a.prompt.slash === q ? -1 : 0) - (b.prompt.slash === q ? -1 : 0) || b.prompt.uses - a.prompt.uses)
    .slice(0, limit);
}

// promptMatches is the picker's search: title, body, tags, slash and pack,
// case-insensitive; with no query, the most used first.
export function promptMatches(choices: readonly PromptChoice[], query: string): PromptChoice[] {
  const q = query.trim().toLowerCase();
  const hit = (c: PromptChoice) =>
    !q ||
    [c.prompt.title, c.prompt.body, c.prompt.slash ?? "", c.packLabel, ...(c.prompt.tags ?? [])].join("\n").toLowerCase().includes(q);
  return choices.filter(hit).sort((a, b) => b.prompt.uses - a.prompt.uses);
}

const PLACEHOLDER = /\{\{\s*([A-Za-z_][A-Za-z0-9_]{0,39})\s*\}\}/g;

// placeholders returns the distinct {{name}} placeholders of a body in
// order (the server declares the same set).
export function placeholders(body: string): string[] {
  const out: string[] = [];
  for (const m of body.matchAll(PLACEHOLDER)) if (!out.includes(m[1])) out.push(m[1]);
  return out;
}

// initialValues are a prompt's variable values before the member edits
// them: the default, else the first option, else empty.
export function initialValues(vars: readonly PromptVariable[] = []): Record<string, string> {
  const out: Record<string, string> = {};
  for (const v of vars) out[v.name] = v.default ?? v.options?.[0] ?? "";
  return out;
}

// missingValue is the first variable still empty (every variable is
// needed: an empty one would leave a hole in the prompt), or null.
export function missingValue(vars: readonly PromptVariable[] = [], values: Readonly<Record<string, string>>): PromptVariable | null {
  return vars.find((v) => !(values[v.name] ?? "").trim()) ?? null;
}

// fillPrompt replaces each {{name}} with its value; a placeholder with no
// value is left as written.
export function fillPrompt(body: string, values: Readonly<Record<string, string>>): string {
  return body.replace(PLACEHOLDER, (all, name: string) => (name in values ? values[name] : all));
}

// insertPrompt places a prompt's text in the draft: it replaces a typed
// "/command" (the whole draft up to the caret) or, otherwise, joins the
// draft like an injected draft (an empty one is replaced, a non-empty one
// gets the prompt on a new paragraph).
export function insertPrompt(draft: string, text: string, slashEnd: number | null): string {
  if (slashEnd !== null) {
    const rest = draft.slice(slashEnd).replace(/^\s+/, "");
    return rest ? `${text}\n\n${rest}` : text;
  }
  return draft.trim() ? `${draft.replace(/\s+$/, "")}\n\n${text}` : text;
}

// variableLabel is a variable's label, else its name in words
// ("target_audience" -> "Target audience").
export function variableLabel(v: Pick<PromptVariable, "name" | "label">): string {
  if (v.label?.trim()) return v.label.trim();
  const words = v.name.replace(/_/g, " ").trim();
  return words ? words[0].toUpperCase() + words.slice(1) : v.name;
}

// ---------------------------------------------------------------- Library

// The Library form's drafts: free text fields the payload normalizes.
export interface VariableDraft {
  name: string;
  label: string;
  default: string;
  // One option per line; empty = a text field.
  options: string;
}

export interface PromptDraft {
  id: string;
  title: string;
  body: string;
  slash: string;
  // Comma or space separated.
  tags: string;
  variables: VariableDraft[];
}

export interface PromptPackForm {
  name: string;
  displayName: string;
  description: string;
  prompts: PromptDraft[];
}

export function emptyPromptDraft(): PromptDraft {
  return { id: "", title: "", body: "", slash: "", tags: "", variables: [] };
}

export function emptyPromptPackForm(): PromptPackForm {
  return { name: "", displayName: "", description: "", prompts: [emptyPromptDraft()] };
}

// slugify mirrors the server's name generation.
export function slugify(s: string, max = 63): string {
  const out = s.trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  return out.length > max ? out.slice(0, max).replace(/-+$/, "") : out;
}

// uniqueId returns an id from a title that `taken` does not hold yet.
export function uniqueId(title: string, taken: ReadonlySet<string>): string {
  const base = slugify(title) || "prompt";
  let id = base;
  for (let n = 2; taken.has(id); n++) id = `${base.slice(0, 63 - String(n).length - 1).replace(/-+$/, "")}-${n}`;
  return id;
}

// syncVariables keeps a draft's variables in step with its body: one per
// {{name}}, in order, keeping what was typed for names still used.
export function syncVariables(body: string, current: readonly VariableDraft[]): VariableDraft[] {
  return placeholders(body).map((name) => current.find((v) => v.name === name) ?? { name, label: "", default: "", options: "" });
}

function splitTags(s: string): string[] {
  const out: string[] = [];
  for (const raw of s.split(/[,\s]+/)) {
    const t = raw.trim().toLowerCase().replace(/^#/, "");
    if (t && !out.includes(t)) out.push(t);
  }
  return out;
}

function splitOptions(s: string): string[] {
  const out: string[] = [];
  for (const raw of s.split("\n")) {
    const o = raw.trim();
    if (o && !out.includes(o)) out.push(o);
  }
  return out;
}

// draftFromPrompt prefills a draft from a pack's prompt (new version).
export function draftFromPrompt(p: ContentPrompt): PromptDraft {
  return {
    id: p.id,
    title: p.title,
    body: p.body,
    slash: p.slash ?? "",
    tags: (p.tags ?? []).join(", "),
    variables: (p.variables ?? []).map((v) => ({ name: v.name, label: v.label ?? "", default: v.default ?? "", options: (v.options ?? []).join("\n") })),
  };
}

// formFromPack prefills the form from a pack at a version.
export function formFromPack(c: ContentComponent): PromptPackForm {
  return {
    name: c.name,
    displayName: c.displayName ?? "",
    description: c.description ?? "",
    prompts: (c.prompts ?? []).map(draftFromPrompt),
  };
}

// promptsFromMessages drafts one prompt per selected message: its text,
// titled by its first line.
export function promptsFromMessages(texts: readonly string[], taken: readonly PromptDraft[] = []): PromptDraft[] {
  const ids = new Set(taken.map((d) => d.id).filter(Boolean));
  return texts
    .map((t) => t.trim())
    .filter(Boolean)
    .map((body) => {
      const title = titleFromText(body);
      const id = uniqueId(title, ids);
      ids.add(id);
      return { ...emptyPromptDraft(), id, title, body, variables: syncVariables(body, []) };
    });
}

// titleFromText proposes a title: the first non-empty line, without a
// heading mark, trimmed to a readable length.
export function titleFromText(text: string, max = 80): string {
  const line = text.split("\n").map((l) => l.trim()).find((l) => l !== "") ?? "";
  const plain = line.replace(/^#+\s*/, "");
  return plain.length > max ? `${plain.slice(0, max - 1).trimEnd()}…` : plain;
}

// promptPackPayload is the JSON form a save sends (kind "prompt"): ids
// filled from titles (unique), slash commands with their "/", tags and
// options split, blank drafts left out.
export function promptPackPayload(f: PromptPackForm): Record<string, unknown> {
  const out: Record<string, unknown> = { kind: "prompt", name: f.name.trim() };
  if (f.displayName.trim()) out.displayName = f.displayName.trim();
  if (f.description.trim()) out.description = f.description.trim();
  const drafts = f.prompts.filter((d) => d.title.trim() || d.body.trim());
  const ids = new Set(drafts.map((d) => d.id.trim()).filter(Boolean));
  out.prompts = drafts.map((d) => {
    let id = d.id.trim();
    if (!id) {
      id = uniqueId(d.title || titleFromText(d.body), ids);
      ids.add(id);
    }
    const slash = d.slash.trim().toLowerCase().replace(/^\/*/, "");
    const tags = splitTags(d.tags);
    const variables = syncVariables(d.body, d.variables).map((v) => {
      const options = splitOptions(v.options);
      return {
        name: v.name,
        ...(v.label.trim() ? { label: v.label.trim() } : {}),
        ...(v.default.trim() ? { default: v.default.trim() } : {}),
        ...(options.length > 0 ? { options } : {}),
      };
    });
    return {
      id,
      title: d.title.trim() || titleFromText(d.body),
      body: d.body.trim(),
      ...(slash ? { slash: `/${slash}` } : {}),
      ...(tags.length > 0 ? { tags } : {}),
      ...(variables.length > 0 ? { variables } : {}),
    };
  });
  return out;
}

// promptPackProblem is the first thing the form must fix before saving
// (the server validates everything; this only catches the obvious early).
export function promptPackProblem(f: PromptPackForm): string | null {
  const name = f.name.trim();
  if (!name) return "Give the pack a name";
  if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)) return "The name uses lower-case letters, digits and hyphens";
  const drafts = f.prompts.filter((d) => d.title.trim() || d.body.trim());
  if (drafts.length === 0) return "Add at least one prompt";
  const slashes = new Set<string>();
  for (const d of drafts) {
    if (!d.body.trim()) return `Write the prompt ${d.title.trim() || "without a body"}`;
    const slash = d.slash.trim().toLowerCase().replace(/^\/*/, "");
    if (slash && !/^[a-z0-9][a-z0-9-]{0,39}$/.test(slash)) return `The command /${slash} uses lower-case letters, digits and hyphens`;
    if (slash && slashes.has(slash)) return `Two prompts use /${slash}`;
    if (slash) slashes.add(slash);
    for (const v of syncVariables(d.body, d.variables)) {
      const options = splitOptions(v.options);
      if (options.length > 0 && v.default.trim() && !options.includes(v.default.trim())) {
        return `The default of ${v.name} is not one of its options`;
      }
    }
  }
  return null;
}

// packUses sums a pack's prompt insertions (its card's count).
export function packUses(c: Pick<ContentComponent, "prompts">): number {
  return (c.prompts ?? []).reduce((n, p) => n + p.uses, 0);
}

// The upload Content-Type of a prompt import (a single markdown or json
// file uploads as is; an archive keeps its type).
export const PROMPT_IMPORT_ACCEPT = ".md,.markdown,.txt,.json,.zip,.tar,.tar.gz,.tgz";

export function promptImportContentType(fileName: string): string {
  const n = fileName.toLowerCase();
  if (n.endsWith(".json")) return "application/json";
  if (n.endsWith(".md") || n.endsWith(".markdown") || n.endsWith(".txt")) return "text/markdown";
  if (n.endsWith(".zip")) return "application/zip";
  if (n.endsWith(".tar.gz") || n.endsWith(".tgz")) return "application/gzip";
  return "application/x-tar";
}
