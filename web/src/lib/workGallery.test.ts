import {
  defaultGalleryTarget,
  GALLERY_TABS,
  galleryErrorText,
  galleryRoomProblem,
  galleryRoomRequest,
  galleryRooms,
  galleryWorks,
  publishWorkForm,
  publishWorkProblem,
  publishWorkRequest,
  queueSimilarDraft,
  recipeChips,
  recipeHasContent,
  recipePromptPreview,
  TYPE_FILTERS,
  withFavorite,
  WORK_CATEGORY_LABELS,
  WORK_TYPE_LABELS,
  workAuthor,
  workCategoryLabel,
  workNameFrom,
  workTypeLabel,
  type GalleryFilter,
} from "./workGallery.ts";
import { coverRefused } from "./appCover.ts";
import type { ContentComponent } from "./contentComponents.ts";
import { orgPublishCopy, orgPublishToast } from "./contentSharing.ts";
import { ApiError } from "./errors.ts";
import { composerAttachments, composerDrafts, composerExpertPicks } from "./team.ts";
import { WorkCategoryValues, WorkTypeValues, type RoomSummary, type WorkRecipe } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function work(over: Partial<ContentComponent>): ContentComponent {
  return {
    id: "cc_1",
    kind: "work",
    workType: "app",
    name: "lunch-poll",
    displayName: "Lunch poll",
    owner: "maya",
    scope: "org",
    status: "active",
    latestVersion: "1.0.0",
    version: "1.0.0",
    permissions: [],
    requires: {},
    mine: false,
    removable: false,
    acked: true,
    runs: 0,
    members: 0,
    uses: 0,
    favorite: false,
    favoriteCount: 0,
    createdAtMs: 1,
    updatedAtMs: 1,
    ...over,
  };
}

// The tabs: "All", then every wire category in the wire's order, each with
// its label.
eq(
  GALLERY_TABS.map((t) => t.value),
  ["all", ...WorkCategoryValues],
  "tabs",
);
eq(GALLERY_TABS[1], { value: "office", label: "Office" }, "first category tab");
assert(WorkCategoryValues.every((c) => WORK_CATEGORY_LABELS[c]), "every category has a label");
eq([workCategoryLabel("life"), workCategoryLabel(""), workCategoryLabel("games"), workCategoryLabel(undefined)], ["Life", "", "", ""], "category labels");

// The type filters (works PRD §2): All, Apps, Images, Videos, Documents,
// Slides, Sites; every wire type has a badge.
eq(
  TYPE_FILTERS.map((t) => t.label),
  ["All", "Apps", "Images", "Videos", "Documents", "Slides", "Sites"],
  "type filters",
);
assert(WorkTypeValues.every((t) => WORK_TYPE_LABELS[t]), "every type has a badge");
eq([workTypeLabel("video"), workTypeLabel("other"), workTypeLabel("game"), workTypeLabel(undefined)], ["Video", "File", "", ""], "type badges");

// The gallery: works only; the type, the tab, the search (name, display
// name, description, tags; every word) and "My favorites" narrow it;
// newest first.
{
  const list = [
    work({ id: "poll", category: "life", description: "Vote on lunch", tags: ["team", "food"], updatedAtMs: 10, favorite: true }),
    work({ id: "budget", name: "budget-sheet", displayName: "Budget", category: "finance", description: "Track spend", updatedAtMs: 30 }),
    work({ id: "signup", name: "signup", displayName: "Sign-up sheet", category: "office", tags: ["events"], updatedAtMs: 20 }),
    work({ id: "chart", workType: "image", name: "q3-chart", displayName: "Q3 chart", category: "finance", updatedAtMs: 40 }),
    work({ id: "clip", workType: "video", name: "launch-clip", displayName: "Launch clip", updatedAtMs: 50 }),
    work({ id: "deck", workType: "slides", name: "deck", displayName: "Deck", updatedAtMs: 60 }),
    work({ id: "plan", workType: "document", name: "plan", displayName: "Plan", updatedAtMs: 70 }),
    work({ id: "page", workType: "site", name: "page", displayName: "Landing page", updatedAtMs: 80 }),
    work({ id: "song", workType: "audio", name: "song", displayName: "Song", updatedAtMs: 90 }),
    work({ id: "expert", kind: "expert", workType: undefined, name: "reviewer", displayName: "Lunch reviewer", updatedAtMs: 99 }),
  ];
  const ids = (f: Partial<GalleryFilter>) => galleryWorks(list, { tab: "all", type: "all", query: "", favoritesOnly: false, ...f }).map((c) => c.id);
  eq(ids({}), ["song", "page", "plan", "deck", "clip", "chart", "budget", "signup", "poll"], "every work, newest first, no other kind");
  eq(ids({ type: "apps" }), ["budget", "signup", "poll"], "apps");
  eq(ids({ type: "images" }), ["chart"], "images");
  eq(ids({ type: "videos" }), ["clip"], "videos");
  eq(ids({ type: "documents" }), ["plan"], "documents");
  eq(ids({ type: "slides" }), ["deck"], "slides");
  eq(ids({ type: "sites" }), ["page"], "sites");
  eq(ids({ type: "images", tab: "finance" }), ["chart"], "a type within a category");
  eq(ids({ type: "videos", tab: "finance" }), [], "a type with nothing in the category");
  eq(ids({ tab: "finance" }), ["chart", "budget"], "a category tab");
  eq(ids({ tab: "learning" }), [], "an empty category");
  eq(ids({ query: "LUNCH" }), ["poll"], "search: display name and description, any case");
  eq(ids({ query: "food" }), ["poll"], "search: tags");
  eq(ids({ query: "budget-sheet" }), ["budget"], "search: name");
  eq(ids({ query: "sheet events" }), ["signup"], "search: every word");
  eq(ids({ query: "sheet lunch" }), [], "search: a word that matches nothing");
  eq(ids({ favoritesOnly: true }), ["poll"], "my favorites");
  eq(ids({ favoritesOnly: true, tab: "finance" }), [], "favorites within a tab");
}

// The author line: a person, or the platform.
eq(workAuthor(work({ scope: "platform", owner: "ops" })), { kind: "platform" }, "platform scope");
eq(workAuthor(work({ scope: "org", owner: "maya" })), { kind: "person", id: "maya" }, "an organization work's author");
eq(workAuthor(work({ scope: "shared", owner: "jack", sharedBy: "maya" })), { kind: "person", id: "maya" }, "a shared copy's author");
eq(workAuthor(work({ scope: "personal", owner: "jack" })), { kind: "person", id: "jack" }, "a member's own work");

// A star toggles the member's star and the count, never below zero.
eq([withFavorite(work({ favoriteCount: 2 }), true).favorite, withFavorite(work({ favoriteCount: 2 }), true).favoriteCount], [true, 3], "star");
eq(withFavorite(work({ favorite: true, favoriteCount: 0 }), false).favoriteCount, 0, "unstar at zero");
{
  const c = work({ favorite: true, favoriteCount: 4 });
  assert(withFavorite(c, true) === c, "no change keeps the object");
}

// A cover the gateway does not serve (404 none, 413, 415) switches the card
// to the generated cover for good; other failures do not.
assert(coverRefused(new ApiError(404, "this work has no cover")), "404: no cover");
assert(coverRefused(new ApiError(413, "too large")) && coverRefused(new ApiError(415, "not an image")), "413/415: refused");
assert(!coverRefused(new ApiError(500, "boom")) && !coverRefused(new ApiError(401, "sign in")), "a server or session failure is not");
assert(!coverRefused(new TypeError("Failed to fetch")), "offline is not");

// The recipe panel: the skills and the expert as chips (then the prompt
// packs), the prompt collapsed after a few lines.
const recipe: WorkRecipe = {
  prompt: "Draw the Q3 sales\nas a bar chart\nwith our colors\nand a title\nand a legend",
  expert: { id: "cc_e", name: "chart-maker", displayName: "Chart maker", version: "1.2.0" },
  skills: [{ id: "cc_s", name: "charts", version: "1.0.0" }],
  prompts: [{ id: "cc_p", name: "chart-prompts", displayName: "Chart prompts" }],
  references: [
    { attachmentId: "att_1", name: "brief.md", contentType: "text/markdown", sizeBytes: 24 },
    { attachmentId: "att_2", name: "sales.csv", contentType: "text/csv", sizeBytes: 13 },
  ],
  agent: { core: "acme", provider: "codex" },
};
eq(
  recipeChips(recipe),
  [
    { kind: "skill", label: "charts", version: "1.0.0" },
    { kind: "expert", label: "Chart maker", version: "1.2.0" },
    { kind: "prompt", label: "Chart prompts" },
  ],
  "chips: skills, the expert, prompt packs",
);
eq(recipeChips(undefined), [], "no recipe, no chips");
{
  const p = recipePromptPreview(recipe.prompt!);
  assert(p.more && p.text.split("\n").length === 4 && p.text.endsWith("…"), `a long prompt is collapsed: ${JSON.stringify(p)}`);
  eq(recipePromptPreview("Short"), { text: "Short", more: false }, "a short prompt is whole");
  const wide = recipePromptPreview("x".repeat(1000));
  assert(wide.more && wide.text.length < 500, "one very long line is cut too");
}
assert(recipeHasContent(recipe) && !recipeHasContent(undefined) && !recipeHasContent({ agent: { provider: "codex" } }), "the agent alone is no recipe");

// Publish to gallery: a new work sends its name and what is filled in; a
// new version sends the component and no name; tags are cleaned.
{
  const form = publishWorkForm("  Lunch Poll! ", []);
  eq(
    form,
    { componentId: "", name: "lunch-poll", displayName: "Lunch Poll!", description: "", category: "", tags: "", includeRecipe: true, prompt: "", unticked: [] },
    "a deliverable's form",
  );
  eq(publishWorkRequest(form), { name: "lunch-poll", displayName: "Lunch Poll!" }, "a new work, blanks left out");
  eq(
    publishWorkRequest({ ...form, name: " team-poll ", description: " Vote on lunch ", category: "life", tags: "Team, food,team\n" }),
    { name: "team-poll", displayName: "Lunch Poll!", description: "Vote on lunch", category: "life", tags: ["team", "food"] },
    "a new work, every field",
  );
  eq(publishWorkRequest({ ...form, componentId: "cc_9", displayName: "" }), { componentId: "cc_9" }, "a new version keeps what it leaves out");
  eq(publishWorkRequest({ ...form, componentId: "cc_9", category: "data" }), { componentId: "cc_9", displayName: "Lunch Poll!", category: "data" }, "a new version's changes");
  // A deliverable whose title is one of the member's own works publishes its next version.
  const mine = [work({ id: "cc_7", displayName: "Lunch Poll!", mine: true }), work({ id: "cc_8", displayName: "Lunch Poll!", mine: false })];
  eq(publishWorkForm("Lunch Poll!", mine).componentId, "cc_7", "the member's own work of that name");
  eq(publishWorkForm("Other", mine).componentId, "", "no work of that name");
  eq([publishWorkProblem(form), publishWorkProblem({ ...form, name: "" }), publishWorkProblem({ ...form, name: "Bad Name" })], [null, "Give it a name", "The name uses lower-case letters, digits and hyphens"], "name problems");
  assert(publishWorkProblem({ ...form, componentId: "cc_9", name: "" }) === null, "a new version needs no name");
  assert(publishWorkProblem({ ...form, tags: "ok, Bad!" })?.includes("bad!") ?? false, "a bad tag");
  eq(publishWorkProblem({ ...form, tags: "评审, 优先级 规划, q3-planning" }), null, "tags in any script (wire.ContentTagPattern)");
  assert(publishWorkProblem({ ...form, tags: "评".repeat(33) }) !== null, "a tag over MaxContentTagRunes characters");
  eq([workNameFrom(" Team 2026 — Sign-up "), workNameFrom("***"), workNameFrom("Q3 chart.png")], ["team-2026-sign-up", "", "q3-chart"], "names from titles");
}

// The recipe under the author's control: as captured (left to the
// server), the prompt edited and a reference unticked (the author's
// recipe, its kept references by attachment), or dropped.
{
  const form = publishWorkForm("Q3 chart", [], recipe);
  eq(form.prompt, recipe.prompt, "the form starts from the captured prompt");
  eq(publishWorkRequest(form, recipe), { name: "q3-chart", displayName: "Q3 chart" }, "kept as captured: the server captures it");
  eq(publishWorkRequest({ ...form, includeRecipe: false }, recipe), { name: "q3-chart", displayName: "Q3 chart", includeRecipe: false }, "dropped");
  const edited = publishWorkRequest({ ...form, prompt: " Draw quarterly sales ", unticked: ["att_2"] }, recipe);
  eq(
    edited.recipe,
    {
      expert: recipe.expert,
      skills: recipe.skills,
      prompts: recipe.prompts,
      agent: recipe.agent,
      prompt: "Draw quarterly sales",
      references: [{ attachmentId: "att_1", name: "brief.md", contentType: "text/markdown", sizeBytes: 24 }],
    },
    "edited: the new prompt, the ticked references",
  );
  const cleared = publishWorkRequest({ ...form, prompt: "" }, recipe).recipe;
  assert(!!cleared && cleared.prompt === undefined && cleared.references?.length === 2, "a cleared prompt publishes none, the rest stays");
}

// Publishing to the organization: the copy follows its approval setting,
// the toast the server's answer.
assert(orgPublishCopy("required", "1.0.0").action === "Ask to publish", "approval required");
assert(orgPublishCopy(undefined, "1.0.0").action === "Ask to publish", "unknown setting reads as required");
assert(orgPublishCopy("none", "1.0.0").action === "Publish" && orgPublishCopy("none", "1.0.0").description.includes("at once"), "no approval");
eq(orgPublishToast({ request: { id: "r1" } as never }), "Sent for approval", "a publish request");
eq(orgPublishToast({}), "Published to your organization", "published at once");

// A room for "Use this app" and "Make similar": the member's rooms they
// may post in; the request names one room or a new one.
{
  const room = (roomId: string, role: string, name = roomId): RoomSummary =>
    ({ roomId, name, role, description: "", visibility: "private", syncMode: "none", memberCount: 1, createdAtMs: 1 }) as unknown as RoomSummary;
  const rooms = [room("r-z", "member", "Zeta"), room("r-v", "visitor", "Visited"), room("r-a", "owner", "Alpha"), room("r-x", "admin", "")];
  eq(galleryRooms(rooms).map((r) => r.roomId), ["r-a", "r-x", "r-z"], "rooms a work may go to, by name");
  eq(defaultGalleryTarget("Lunch poll"), { kind: "new", name: "Lunch poll" }, "a new room named after the work by default");
  eq(galleryRoomRequest({ kind: "room", roomId: "r-a" }, "1.0.0"), { version: "1.0.0", roomId: "r-a" }, "an existing room");
  eq(galleryRoomRequest({ kind: "new", name: "  Lunch poll " }), { newRoom: { name: "Lunch poll" } }, "a new room");
  eq(galleryRoomRequest({ kind: "new", name: " " }), { newRoom: {} }, "a new room the server names");
  eq(galleryRoomProblem({ kind: "room", roomId: "r-v" }, rooms), "Pick a room you can post in", "a visitor's room");
  eq(galleryRoomProblem({ kind: "room", roomId: "r-a" }, rooms), null, "an owned room");
  eq(galleryRoomProblem({ kind: "new", name: "" }, rooms), null, "a new room");
  assert(galleryErrorText(new ApiError(403, "pick a room you own"), "use").startsWith("Pick a room you can post in"), "403: not an owner");
  eq(galleryErrorText(new ApiError(404, "content component not found"), "similar"), "This work is no longer available to you.", "404: invisible work");
  assert(galleryErrorText(new ApiError(413, "over the room's cap"), "use").includes("over the room's cap"), "other refusals keep the server's reason");
}

// Make similar: the draft fills the room's composer through the
// injected-draft owner (the text, the expert, the attachments), each taken
// once.
{
  const draft = {
    roomId: "room-q4",
    createdRoom: true,
    text: "Draw the Q3 sales as a bar chart",
    expert: { id: "cc_e", name: "chart-maker", version: "1.2.0" },
    attachmentIds: ["att_9"],
    attachments: [{ id: "att_9", filename: "brief.md", contentType: "text/markdown", size: 24 }],
    skipped: ["skill pack charts", "prompt pack Chart prompts"],
  };
  queueSimilarDraft(draft);
  eq(composerDrafts.take("room-q4"), draft.text, "the composer's text");
  eq(composerExpertPicks.take("room-q4"), { id: "cc_e", version: "1.2.0" }, "the composer's expert");
  eq(composerAttachments.take("room-q4"), draft.attachments, "the composer's attachments");
  eq([composerDrafts.take("room-q4"), composerExpertPicks.take("room-q4"), composerAttachments.take("room-q4")], [undefined, undefined, undefined], "each taken once");
  queueSimilarDraft({ roomId: "room-empty", text: "", attachmentIds: [], attachments: [] });
  eq([composerDrafts.peek("room-empty"), composerExpertPicks.peek("room-empty"), composerAttachments.peek("room-empty")], [undefined, undefined, undefined], "an empty draft queues nothing");
}
