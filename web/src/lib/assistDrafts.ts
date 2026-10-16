import { cleanTags, splitList, tagProblem, type ContentComponent, type ContentForm, type ContentFormSkill } from "./contentComponents.ts";
import { emptyPromptDraft, slugify, syncVariables, uniqueId, type PromptDraft, type PromptPackForm } from "./promptPacks.ts";
import { roomIdProblem } from "./roomId.ts";
import type { RecurrenceForm } from "./schedules.ts";
import { bodyLimit, truncateBytes, type NoteType } from "./notes.ts";
import { MaxAssistItems, MaxAssistText, MaxNoteTitleRunes, type AssistJSONReply, type ContentQuickPrompt } from "./wire.gen.ts";

// The second round of AI assist (docs/prd/2026-10-13-ai-assist.md §6), its
// pure side: a JSON task's reply onto the Library's forms (draft-expert,
// draft-skill-pack, draft-prompt-pack), the new room's plan (plan-room)
// and a schedule (draft-schedule), and the text a draft task is asked
// with. The requests are lib/assist.ts; everything here is unit-tested.
// Each mapping takes only what the form can hold: anything malformed is
// dropped, never guessed, and the member edits the result before saving.

/** The built-in tasks of the second round. */
export const DRAFT_EXPERT = "draft-expert";
export const DRAFT_SKILL_PACK = "draft-skill-pack";
export const DRAFT_PROMPT_PACK = "draft-prompt-pack";
export const PLAN_ROOM = "plan-room";
export const DRAFT_SCHEDULE = "draft-schedule";
export const DRAFT_NOTE = "draft-note";

type Fields = AssistJSONReply["fields"];

// ---- Reading a reply's fields ----

/** fieldText reads a text field (trimmed; "" when absent or not a string). */
export function fieldText(fields: Fields, name: string): string {
  const v = fields[name];
  return typeof v === "string" ? v.trim() : "";
}

/** fieldList reads a list field: strings, or one comma-separated string. */
export function fieldList(fields: Fields, name: string): string[] {
  const v = fields[name];
  if (Array.isArray(v)) return v.filter((x): x is string => typeof x === "string").map((x) => x.trim()).filter(Boolean);
  return typeof v === "string" ? splitList(v) : [];
}

/** fieldItems reads an items field: the objects' string fields (others and
 *  non-objects dropped). */
export function fieldItems(fields: Fields, name: string): Record<string, string>[] {
  const v = fields[name];
  if (!Array.isArray(v)) return [];
  const out: Record<string, string>[] = [];
  for (const x of v) {
    if (!x || typeof x !== "object" || Array.isArray(x)) continue;
    const item: Record<string, string> = {};
    for (const [k, val] of Object.entries(x as Record<string, unknown>)) if (typeof val === "string") item[k] = val.trim();
    out.push(item);
  }
  return out;
}

// ---- The text a draft task is asked with ----

const CURRENT_FORM = "\n\nCurrent form:\n";

/** draftRequestText is a draft task's text: the member's sentence and,
 *  when the form already holds something, the form as JSON (cut to fit
 *  the request's bound). */
export function draftRequestText(sentence: string, current: Readonly<Record<string, unknown>> | null): string {
  const ask = sentence.trim().slice(0, MaxAssistText);
  if (!current || Object.keys(current).length === 0) return ask;
  const room = MaxAssistText - ask.length - CURRENT_FORM.length;
  if (room < 200) return ask;
  const json = JSON.stringify(current, null, 1);
  return ask + CURRENT_FORM + (json.length > room ? json.slice(0, room - 1) + "…" : json);
}

/** filledForm is the part of a Library form worth sending as "Current
 *  form": its non-empty text fields, quick prompts and skills. */
export function filledForm(f: ContentForm): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of ["displayName", "description", "instructions", "welcome"] as const) if (f[key].trim()) out[key] = f[key].trim();
  const qp = f.quickPrompts.filter((q) => q.title.trim() || q.prompt.trim()).map((q) => ({ title: q.title.trim(), prompt: q.prompt.trim() }));
  if (qp.length > 0) out.quickPrompts = qp;
  const skills = f.skills.filter((s) => s.name.trim() || s.description.trim() || s.body.trim());
  if (skills.length > 0) out.skills = skills;
  if ((f.tags ?? []).length > 0) out.tags = f.tags;
  return out;
}

/** filledPromptPack is a prompt pack form's "Current form". */
export function filledPromptPack(f: PromptPackForm): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (f.displayName.trim()) out.displayName = f.displayName.trim();
  if (f.description.trim()) out.description = f.description.trim();
  const prompts = f.prompts.filter((p) => p.title.trim() || p.body.trim()).map((p) => ({ title: p.title.trim(), body: p.body.trim() }));
  if (prompts.length > 0) out.prompts = prompts;
  return out;
}

// ---- Library forms ----

const MAX_TAGS = 12;
const MAX_QUICK_PROMPTS = 12;
const MAX_QUICK_TITLE = 80;
const MAX_QUICK_PROMPT = 4000;
const MAX_SKILLS = 64;
const MAX_SKILL_DESCRIPTION = 1024;

/** draftTags: the reply's tags the tag rule admits (at most 12). */
function draftTags(fields: Fields): string[] {
  return cleanTags(fieldList(fields, "tags"))
    .filter((t) => tagProblem([t]) === null)
    .slice(0, MAX_TAGS);
}

/** nameFrom is a component name from a display name ("" when it has no
 *  latin letters or digits to make one of). */
function nameFrom(displayName: string): string {
  return slugify(displayName);
}

/** The shared envelope of every Library draft: display name, description,
 *  tags and, while the member has not named it, a name from the display
 *  name. */
function envelopePatch(fields: Fields, current: { readonly name: string }, editingName: boolean) {
  const patch: { displayName?: string; description?: string; name?: string } = {};
  const displayName = fieldText(fields, "displayName");
  if (displayName) {
    patch.displayName = displayName;
    const name = nameFrom(displayName);
    if (editingName && !current.name.trim() && name) patch.name = name;
  }
  const description = fieldText(fields, "description");
  if (description) patch.description = description;
  return patch;
}

/** expertDraftPatch is a draft-expert reply onto the expert form. The name
 *  is set only for a new expert the member has not named. */
export function expertDraftPatch(fields: Fields, form: ContentForm, isNew: boolean): Partial<ContentForm> {
  const patch: Partial<ContentForm> = { ...envelopePatch(fields, form, isNew) };
  const instructions = fieldText(fields, "instructions");
  if (instructions) patch.instructions = instructions;
  const welcome = fieldText(fields, "welcome");
  if (welcome) patch.welcome = welcome;
  const quick: ContentQuickPrompt[] = fieldItems(fields, "quickPrompts")
    .map((q) => ({ title: (q.title ?? "").slice(0, MAX_QUICK_TITLE), prompt: (q.prompt ?? "").slice(0, MAX_QUICK_PROMPT) }))
    .filter((q) => q.title && q.prompt)
    .slice(0, MAX_QUICK_PROMPTS);
  if (quick.length > 0) patch.quickPrompts = quick;
  const tags = draftTags(fields);
  if (tags.length > 0) patch.tags = tags;
  return patch;
}

/** skillName makes a valid skill name of what the model named it: a slug,
 *  unique in the pack ("skill", "skill-2", … when nothing is left). */
function skillName(raw: string, taken: Set<string>): string {
  const base = slugify(raw) || "skill";
  let name = base;
  for (let n = 2; taken.has(name); n++) name = `${base.slice(0, 63 - String(n).length - 1).replace(/-+$/, "")}-${n}`;
  taken.add(name);
  return name;
}

/** skillPackDraftPatch is a draft-skill-pack reply onto the skill pack
 *  form: its skills replace the form's (each with a valid, unique name
 *  and a description; a skill with neither a description nor a body is
 *  dropped). */
export function skillPackDraftPatch(fields: Fields, form: ContentForm, isNew: boolean): Partial<ContentForm> {
  const patch: Partial<ContentForm> = { ...envelopePatch(fields, form, isNew) };
  const taken = new Set<string>();
  const skills: ContentFormSkill[] = fieldItems(fields, "skills")
    .filter((s) => (s.description ?? "") || (s.body ?? ""))
    .slice(0, MAX_SKILLS)
    .map((s) => ({ name: skillName(s.name ?? "", taken), description: (s.description ?? "").slice(0, MAX_SKILL_DESCRIPTION), body: s.body ?? "" }));
  if (skills.length > 0) patch.skills = skills;
  const tags = draftTags(fields);
  if (tags.length > 0) patch.tags = tags;
  return patch;
}

/** promptPackDraftPatch is a draft-prompt-pack reply onto the prompt pack
 *  form: its prompts replace the form's. A prompt titled like one the form
 *  holds keeps that one's id, command, tags, starter flag and variables
 *  (a new version of a pack); the others get ids unique from their titles
 *  and variables read from their {{placeholders}}. */
export function promptPackDraftPatch(fields: Fields, form: PromptPackForm, isNew: boolean): Partial<PromptPackForm> {
  const patch: Partial<PromptPackForm> = { ...envelopePatch(fields, form, isNew) };
  const ids = new Set<string>();
  const kept = new Set<PromptDraft>();
  const prompts: PromptDraft[] = fieldItems(fields, "prompts")
    .filter((p) => p.body)
    .slice(0, MaxAssistItems)
    .map((p) => {
      const title = (p.title ?? "").slice(0, MAX_QUICK_TITLE);
      const body = p.body ?? "";
      const same = form.prompts.find((d) => !kept.has(d) && d.title.trim() && d.title.trim().toLowerCase() === title.toLowerCase());
      const base = same ?? emptyPromptDraft();
      if (same) kept.add(same);
      const id = same?.id && !ids.has(same.id) ? same.id : uniqueId(title || body, ids);
      ids.add(id);
      return { ...base, id, title, body, variables: syncVariables(body, base.variables) };
    });
  if (prompts.length > 0) patch.prompts = prompts;
  return patch;
}

// ---- The new room's plan ----

/** A new room as plan-room proposed it, matched against the library. */
export interface RoomPlan {
  readonly roomId: string;
  readonly name: string;
  readonly description: string;
  // The library's expert and skill packs the proposal names (an unknown
  // name is dropped).
  readonly expert: ContentComponent | null;
  readonly skills: readonly ContentComponent[];
  readonly firstMessage: string;
}

/** findByName matches a component the model named: its name, else its
 *  display name (case-insensitive). */
export function findByName(list: readonly ContentComponent[], raw: string): ContentComponent | null {
  const n = raw.trim().toLowerCase();
  if (!n) return null;
  return list.find((c) => c.name.toLowerCase() === n) ?? list.find((c) => (c.displayName ?? "").trim().toLowerCase() === n) ?? null;
}

/** roomPlanFrom is a plan-room reply: the room id (a valid one, else ""),
 *  name, description, the expert and skill packs found in the member's
 *  library (experts / skills, what they can use), and the first message. */
export function roomPlanFrom(fields: Fields, experts: readonly ContentComponent[], skills: readonly ContentComponent[]): RoomPlan {
  const rawId = fieldText(fields, "roomId");
  const roomId = rawId && !roomIdProblem(rawId) ? rawId : slugify(rawId);
  const picked: ContentComponent[] = [];
  for (const n of fieldList(fields, "skills")) {
    const c = findByName(skills, n);
    if (c && c.kind === "skill" && !picked.some((p) => p.id === c.id)) picked.push(c);
  }
  const expert = findByName(experts, fieldText(fields, "expert"));
  return {
    roomId: roomId && !roomIdProblem(roomId) ? roomId : "",
    name: fieldText(fields, "name"),
    description: fieldText(fields, "description"),
    expert: expert && expert.kind === "expert" ? expert : null,
    skills: picked,
    firstMessage: fieldText(fields, "firstMessage"),
  };
}

// ---- A note ----

/** A note as draft-note wrote it: a one-line title and a body, both fitted
 *  to what the server accepts (title at most MaxNoteTitleRunes characters,
 *  the body at most its type's bytes); "" for what the reply did not give. */
export interface NoteDraft {
  readonly title: string;
  readonly body: string;
}

/** noteDraftFrom is a draft-note reply onto the note form: the title made one
 *  line and cut to the title limit with an ellipsis, the body cut to the
 *  type's limit on a character boundary. */
export function noteDraftFrom(fields: Fields, type: NoteType): NoteDraft {
  const line = fieldText(fields, "title").replace(/[\u0000-\u001f\u007f-\u009f]+/g, " ").replace(/\s+/g, " ").trim();
  const chars = Array.from(line);
  const title = chars.length > MaxNoteTitleRunes ? `${chars.slice(0, MaxNoteTitleRunes - 1).join("").trimEnd()}…` : line;
  return { title, body: truncateBytes(fieldText(fields, "body"), bodyLimit(type)) };
}

/** noteRequestText is draft-note's text: the member's sentence and the form
 *  as it is (its type always, so the task knows what to write). */
export function noteRequestText(sentence: string, form: { readonly type: NoteType; readonly title: string; readonly body: string }): string {
  const current: Record<string, unknown> = { type: form.type };
  if (form.title.trim()) current.title = form.title.trim();
  if (form.body.trim()) current.body = form.body.trim();
  return draftRequestText(sentence, current);
}

// ---- A schedule ----

const TIME = /^([01]\d|2[0-3]):[0-5]\d$/;
const LOCAL_AT = /^\d{4}-\d{2}-\d{2}T([01]\d|2[0-3]):[0-5]\d$/;
const KINDS = ["once", "daily", "weekly", "cron"] as const;

/** A schedule as draft-schedule described it: the message text ("" when
 *  absent) and the recurrence fields it gave. */
export interface ScheduleDraft {
  readonly text: string;
  readonly form: Partial<RecurrenceForm>;
}

/** scheduleDraftFrom is a draft-schedule reply onto the schedule form: a
 *  known kind, a valid HH:MM time, weekdays 0–6 (Sunday = 0), a five-field
 *  cron, a local YYYY-MM-DDTHH:MM for once; anything else is dropped. A
 *  weekly schedule without valid days, a cron one without a valid
 *  expression and a once one without a valid time keep the form's kind. */
export function scheduleDraftFrom(fields: Fields): ScheduleDraft {
  const form: { -readonly [K in keyof RecurrenceForm]?: RecurrenceForm[K] } = {};
  const time = fieldText(fields, "time");
  if (TIME.test(time)) form.time = time;
  const days = [...new Set(fieldList(fields, "days").filter((d) => /^[0-6]$/.test(d)).map(Number))].sort((a, b) => a - b);
  if (days.length > 0) form.days = days;
  const cron = fieldText(fields, "cron").replace(/\s+/g, " ");
  if (cron.split(" ").length === 5) form.cron = cron;
  const at = fieldText(fields, "at");
  if (LOCAL_AT.test(at)) form.onceLocal = at;
  const kind = fieldText(fields, "kind");
  const known = (KINDS as readonly string[]).includes(kind) ? (kind as RecurrenceForm["kind"]) : null;
  const complete =
    known === "daily" ? !!form.time : known === "weekly" ? !!form.time && !!form.days : known === "cron" ? !!form.cron : known === "once" ? !!form.onceLocal : false;
  if (known && complete) form.kind = known;
  return { text: fieldText(fields, "text"), form };
}
