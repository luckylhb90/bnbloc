// The works gallery (docs/prd/2026-10-13-app-gallery.md,
// docs/prd/2026-10-13-works-gallery-and-make-similar.md): Plaza → "Works",
// a work's detail and its recipe, "Use this app", "Make similar" and
// "Publish to gallery". The works are the content listing's
// (GET /v1/content-components?kind=work: what contentuse.Visible lets the
// member see, each with its type, star, star count, cover and recipe); the
// helpers here are pure (no React, no DOM) and unit-tested: the type
// filters and category tabs, the gallery's filter, the author line, the
// recipe's chips, the publish form and request, the room choice of a copy
// or a draft, and the draft's hand-off to the composer. The requests are
// lib/workGalleryApi.ts, the views views/gallery/.

export type {
  AppDemo,
  AppUseReply,
  MakeSimilarDraft,
  WorkCategory,
  WorkPublishRequest,
  WorkRecipe,
  WorkRecipeCapture,
  WorkType,
} from "./wire.gen.ts";
import {
  WorkCategoryValues,
  type ChatAttachment,
  type MakeSimilarDraft,
  type MakeSimilarRequest,
  type RoomSummary,
  type WorkCategory,
  type WorkPublishRequest,
  type WorkRecipe,
  type WorkType,
} from "./wire.gen.ts";
import { cleanTags, contentLabel, splitList, tagProblem, type ContentComponent } from "./contentComponents.ts";
import { ApiError, errorText } from "./errors.ts";
import { mayChat } from "./roomRoles.ts";
import { composerAttachments, composerDrafts, composerExpertPicks } from "./team.ts";

/** What the gallery calls each category (its tabs, a card's detail). */
export const WORK_CATEGORY_LABELS: Readonly<Record<WorkCategory, string>> = {
  office: "Office",
  data: "Data",
  productivity: "Productivity",
  content: "Content",
  learning: "Learning",
  engineering: "Engineering",
  finance: "Finance",
  life: "Life",
  other: "Other",
};

/** A gallery tab: every work, or one category. */
export type GalleryTab = "all" | WorkCategory;

/** The tabs in the wire's order (wire.WorkCategories), "All" first. */
export const GALLERY_TABS: readonly { readonly value: GalleryTab; readonly label: string }[] = [
  { value: "all", label: "All" },
  ...WorkCategoryValues.map((c) => ({ value: c, label: WORK_CATEGORY_LABELS[c] })),
];

/** The category's label, "" for none (or one this client does not know). */
export function workCategoryLabel(category: string | undefined): string {
  return category && category in WORK_CATEGORY_LABELS ? WORK_CATEGORY_LABELS[category as WorkCategory] : "";
}

/** What a work of each type is called: its card's badge. */
export const WORK_TYPE_LABELS: Readonly<Record<WorkType, string>> = {
  app: "App",
  site: "Site",
  image: "Image",
  video: "Video",
  audio: "Audio",
  document: "Document",
  slides: "Slides",
  table: "Table",
  other: "File",
};

/** A type filter: every work, or the types it groups. */
export type TypeFilter = "all" | "apps" | "images" | "videos" | "documents" | "slides" | "sites";

/** The type filters, in the gallery's order (works PRD §2). A type no
 *  filter names (audio, a table, any other file) shows under "All". */
export const TYPE_FILTERS: readonly { readonly value: TypeFilter; readonly label: string; readonly types: readonly WorkType[] }[] = [
  { value: "all", label: "All", types: [] },
  { value: "apps", label: "Apps", types: ["app"] },
  { value: "images", label: "Images", types: ["image"] },
  { value: "videos", label: "Videos", types: ["video"] },
  { value: "documents", label: "Documents", types: ["document"] },
  { value: "slides", label: "Slides", types: ["slides"] },
  { value: "sites", label: "Sites", types: ["site"] },
];

/** A work's type label ("" for a type this client does not know). */
export function workTypeLabel(type: string | undefined): string {
  return type && type in WORK_TYPE_LABELS ? WORK_TYPE_LABELS[type as WorkType] : "";
}

export interface GalleryFilter {
  readonly tab: GalleryTab;
  readonly type: TypeFilter;
  readonly query: string;
  readonly favoritesOnly: boolean;
}

/** matchesWork is the gallery's search: every word of the query in the
 *  work's name, display name, description or tags (case-insensitive). */
export function matchesWork(c: ContentComponent, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = [c.name, c.displayName, c.description, ...(c.tags ?? [])].filter(Boolean).join("\n").toLowerCase();
  return words.every((w) => hay.includes(w));
}

/** galleryWorks is the grid: the listing's works under the type filter,
 *  the category tab, the search and "My favorites", newest-updated first.
 *  An author's own published work is one card, their own (the listing
 *  folds its organization publication into it). */
export function galleryWorks(list: readonly ContentComponent[], f: GalleryFilter): ContentComponent[] {
  const works = list.filter((c) => c.kind === "work");
  const types = TYPE_FILTERS.find((t) => t.value === f.type)?.types ?? [];
  return works
    .filter((c) => types.length === 0 || (!!c.workType && types.includes(c.workType)))
    .filter((c) => f.tab === "all" || c.category === f.tab)
    .filter((c) => !f.favoritesOnly || c.favorite)
    .filter((c) => matchesWork(c, f.query))
    .sort((a, b) => b.updatedAtMs - a.updatedAtMs);
}

/** A work card's author: a person, or the platform (platform and built-in
 *  content, which no member authors). */
export type WorkAuthor = { readonly kind: "platform" } | { readonly kind: "person"; readonly id: string };

/** workAuthor: the platform for platform scope; a shared copy's author;
 *  else the member who owns it (an organization publication's author). */
export function workAuthor(c: Pick<ContentComponent, "scope" | "owner" | "sharedBy">): WorkAuthor {
  if (c.scope === "platform" || c.scope === "builtin") return { kind: "platform" };
  return { kind: "person", id: (c.scope === "shared" && c.sharedBy) || c.owner };
}

/** A star's new state on the listing: the member's own star and the count
 *  (never below zero). */
export function withFavorite<T extends Pick<ContentComponent, "favorite" | "favoriteCount">>(c: T, favorite: boolean): T {
  if (c.favorite === favorite) return c;
  return { ...c, favorite, favoriteCount: Math.max(0, (c.favoriteCount ?? 0) + (favorite ? 1 : -1)) };
}

// ---- The recipe ----

/** One chip of a recipe's panel: the expert, a skill pack or a prompt pack. */
export interface RecipeChip {
  readonly kind: "expert" | "skill" | "prompt";
  readonly label: string;
  readonly version?: string;
}

/** recipeChips are the recipe's components as the detail shows them: the
 *  skills, then the expert, then the prompt packs (works PRD §3,
 *  "Display"). */
export function recipeChips(r: WorkRecipe | undefined): RecipeChip[] {
  if (!r) return [];
  const chip = (kind: RecipeChip["kind"]) => (p: { readonly name: string; readonly displayName?: string; readonly version?: string }): RecipeChip => ({
    kind,
    label: p.displayName || p.name,
    ...(p.version ? { version: p.version } : {}),
  });
  return [...(r.skills ?? []).map(chip("skill")), ...(r.expert ? [chip("expert")(r.expert)] : []), ...(r.prompts ?? []).map(chip("prompt"))];
}

/** The lines of a recipe's prompt shown before "Expand". */
export const RECIPE_PROMPT_LINES = 4;

/** recipePromptPreview cuts a prompt to its first lines (and to a length
 *  those lines can show): the collapsed text and whether there is more. */
export function recipePromptPreview(prompt: string, lines = RECIPE_PROMPT_LINES): { readonly text: string; readonly more: boolean } {
  const all = prompt.split("\n");
  const max = lines * 120;
  let text = all.slice(0, lines).join("\n");
  if (text.length > max) text = text.slice(0, max).trimEnd() + "…";
  else if (all.length > lines) text = text.trimEnd() + "…";
  return { text, more: text !== prompt };
}

/** Whether a recipe says how the work was made (the agent alone does
 *  not): the server's contentpkg.RecipeEmpty, read for the detail. */
export function recipeHasContent(r: WorkRecipe | undefined): r is WorkRecipe {
  return !!r && (!!r.prompt || !!r.expert || (r.skills?.length ?? 0) + (r.prompts?.length ?? 0) + (r.references?.length ?? 0) > 0);
}

// ---- Publish to gallery ----

/** The publish dialog's form. componentId "" publishes a new work; an id
 *  publishes the deliverable as that work's next version (its name stays).
 *  The recipe part is the author's control over the captured recipe: keep
 *  it or not, its prompt as edited, and the references still ticked (by
 *  attachment id). */
export interface PublishWorkForm {
  readonly componentId: string;
  readonly name: string;
  readonly displayName: string;
  readonly description: string;
  readonly category: WorkCategory | "";
  // Comma- or newline-separated.
  readonly tags: string;
  readonly includeRecipe: boolean;
  readonly prompt: string;
  readonly unticked: readonly string[];
}

const NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;

/** workNameFrom suggests a component name for a deliverable's title
 *  (lower-case letters, digits and hyphens): what the server would derive
 *  (contentpkg.NameFrom) when the name is left out. */
export function workNameFrom(title: string): string {
  return title
    .trim()
    .toLowerCase()
    .replace(/\.[a-z0-9]{1,5}$/, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63)
    .replace(/^-+|-+$/g, "");
}

/** The form for a deliverable: a new work named after it, or the next
 *  version of the member's own work of the same display name; the
 *  captured recipe kept as captured. */
export function publishWorkForm(
  title: string,
  mine: readonly Pick<ContentComponent, "id" | "name" | "displayName" | "kind" | "mine">[],
  captured?: WorkRecipe,
): PublishWorkForm {
  const label = title.trim();
  const same = mine.find((c) => c.kind === "work" && c.mine && contentLabel(c) === label);
  return {
    componentId: same?.id ?? "",
    name: workNameFrom(label),
    displayName: label,
    description: "",
    category: "",
    tags: "",
    includeRecipe: true,
    prompt: captured?.prompt ?? "",
    unticked: [],
  };
}

/** publishWorkProblem is the first thing the form must fix (the server
 *  validates everything; this catches the obvious early). */
export function publishWorkProblem(f: PublishWorkForm): string | null {
  if (!f.componentId) {
    const name = f.name.trim();
    if (!name) return "Give it a name";
    if (!NAME.test(name)) return "The name uses lower-case letters, digits and hyphens";
  }
  return tagProblem(splitList(f.tags));
}

/** publishWorkRequest is the body of POST …/publish-work: the trimmed
 *  form, empty fields left out (a new version keeps what it leaves out),
 *  no name for a new version (the work's own stays), and the recipe: none
 *  when the author dropped it, the author's edit when they changed the
 *  prompt or unticked a reference, else left to the server's capture. */
export function publishWorkRequest(f: PublishWorkForm, captured?: WorkRecipe): WorkPublishRequest {
  const out: WorkPublishRequest = f.componentId ? { componentId: f.componentId } : { name: f.name.trim() };
  const displayName = f.displayName.trim();
  const description = f.description.trim();
  const tags = cleanTags(splitList(f.tags));
  if (displayName) out.displayName = displayName;
  if (description) out.description = description;
  if (f.category) out.category = f.category;
  if (tags.length > 0) out.tags = tags;
  if (!captured) return out;
  if (!f.includeRecipe) return { ...out, includeRecipe: false };
  const prompt = f.prompt.trim();
  if (prompt === (captured.prompt ?? "").trim() && f.unticked.length === 0) return out;
  const references = (captured.references ?? [])
    .filter((r) => r.attachmentId && !f.unticked.includes(r.attachmentId))
    .map((r) => ({ attachmentId: r.attachmentId, name: r.name, contentType: r.contentType, sizeBytes: r.sizeBytes }));
  const { prompt: _captured, references: _refs, ...rest } = captured;
  return { ...out, recipe: { ...rest, ...(prompt ? { prompt } : {}), ...(references.length > 0 ? { references } : {}) } };
}

// ---- A room for "Use this app" and "Make similar" ----

/** Where a copy or a draft goes: a room the member owns, or a new room
 *  (named after the work unless they rename it). */
export type GalleryRoomTarget = { readonly kind: "room"; readonly roomId: string } | { readonly kind: "new"; readonly name: string };

/** galleryRooms are the member's rooms a copy or a draft may go to: the
 *  ones they may post in (lib/roomRoles mayChat), by name. */
export function galleryRooms(rooms: readonly RoomSummary[]): RoomSummary[] {
  return rooms.filter((r) => mayChat({ role: r.role })).sort((a, b) => (a.name || a.roomId).localeCompare(b.name || b.roomId));
}

/** The default choice: a new room named after the work. */
export function defaultGalleryTarget(name: string): GalleryRoomTarget {
  return { kind: "new", name };
}

/** galleryRoomRequest is the room part (and version) of POST …/use-app
 *  and …/make-similar. */
export function galleryRoomRequest(target: GalleryRoomTarget, version?: string): MakeSimilarRequest {
  const v = version ? { version } : {};
  if (target.kind === "room") return { ...v, roomId: target.roomId };
  const name = target.name.trim();
  return { ...v, newRoom: name ? { name } : {} };
}

/** galleryRoomProblem: the target is not one the dialog can send. */
export function galleryRoomProblem(target: GalleryRoomTarget, rooms: readonly RoomSummary[]): string | null {
  if (target.kind === "room" && !galleryRooms(rooms).some((r) => r.roomId === target.roomId)) return "Pick a room you can post in";
  return null;
}

/** galleryErrorText words a refused copy or draft: a room the member does
 *  not own (403), a work they no longer see (404), else the server's
 *  reason. */
export function galleryErrorText(e: unknown, what: "use" | "similar"): string {
  if (e instanceof ApiError && e.status === 403) return "Pick a room you can post in, or create a new one.";
  if (e instanceof ApiError && e.status === 404) return "This work is no longer available to you.";
  return errorText(e, what === "use" ? "Could not use this app" : "Could not start a similar one");
}

// ---- Make similar ----

/** queueSimilarDraft hands a draft to its room's composer through the
 *  injected-draft owner: the text, the expert picked, the references
 *  attached. The room's composer takes each once it opens. */
export function queueSimilarDraft(d: MakeSimilarDraft): void {
  if (d.text) composerDrafts.queue(d.roomId, d.text);
  if (d.expert) composerExpertPicks.queue(d.roomId, { id: d.expert.id, ...(d.expert.version ? { version: d.expert.version } : {}) });
  if (d.attachments.length > 0) composerAttachments.queue(d.roomId, d.attachments as readonly ChatAttachment[]);
}
