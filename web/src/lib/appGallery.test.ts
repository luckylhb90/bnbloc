import {
  APP_CATEGORY_LABELS,
  appAuthor,
  appCategoryLabel,
  appCopyErrorText,
  appCopyProblem,
  appCopyRequest,
  appCopyRooms,
  appNameFrom,
  defaultAppCopyTarget,
  GALLERY_TABS,
  galleryApps,
  publishAppForm,
  publishAppProblem,
  publishAppRequest,
  withFavorite,
} from "./appGallery.ts";
import { coverRefused } from "./appCover.ts";
import type { ContentComponent } from "./contentComponents.ts";
import { orgPublishCopy, orgPublishToast } from "./contentSharing.ts";
import { ApiError } from "./errors.ts";
import { WorkCategoryValues, type RoomSummary } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function app(over: Partial<ContentComponent>): ContentComponent {
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
assert(WorkCategoryValues.every((c) => APP_CATEGORY_LABELS[c]), "every category has a label");
eq([appCategoryLabel("life"), appCategoryLabel(""), appCategoryLabel("games"), appCategoryLabel(undefined)], ["Life", "", "", ""], "category labels");

// The gallery: apps only; the tab, the search (name, display name,
// description, tags; every word) and "My favorites" narrow it; newest
// first.
{
  const list = [
    app({ id: "poll", category: "life", description: "Vote on lunch", tags: ["team", "food"], updatedAtMs: 10, favorite: true }),
    app({ id: "budget", name: "budget-sheet", displayName: "Budget", category: "finance", description: "Track spend", updatedAtMs: 30 }),
    app({ id: "signup", name: "signup", displayName: "Sign-up sheet", category: "office", tags: ["events"], updatedAtMs: 20 }),
    app({ id: "expert", kind: "expert", name: "reviewer", displayName: "Lunch reviewer", updatedAtMs: 99 }),
  ];
  const ids = (f: Partial<Parameters<typeof galleryApps>[1]>) => galleryApps(list, { tab: "all", query: "", favoritesOnly: false, ...f }).map((c) => c.id);
  eq(ids({}), ["budget", "signup", "poll"], "all apps, newest first, no other kind");
  eq(ids({ tab: "finance" }), ["budget"], "a category tab");
  eq(ids({ tab: "learning" }), [], "an empty category");
  eq(ids({ query: "LUNCH" }), ["poll"], "search: display name and description, any case");
  eq(ids({ query: "food" }), ["poll"], "search: tags");
  eq(ids({ query: "budget-sheet" }), ["budget"], "search: name");
  eq(ids({ query: "sheet events" }), ["signup"], "search: every word");
  eq(ids({ query: "sheet lunch" }), [], "search: a word that matches nothing");
  eq(ids({ favoritesOnly: true }), ["poll"], "my favorites");
  eq(ids({ favoritesOnly: true, tab: "finance" }), [], "favorites within a tab");
}

// An author's own app shows once, as its organization publication.
{
  const list = [
    app({ id: "mine", scope: "personal", mine: true, publication: { orgComponentId: "org-row", orgStatus: "active", orgVersion: "1.0.0" } }),
    app({ id: "org-row", scope: "org" }),
    app({ id: "draft", scope: "personal", mine: true, publication: { orgComponentId: "not-listed" } }),
  ];
  eq(
    galleryApps(list, { tab: "all", query: "", favoritesOnly: false }).map((c) => c.id),
    ["org-row", "draft"],
    "the personal row of a listed publication is hidden",
  );
}

// The author line: a person, or the platform.
eq(appAuthor(app({ scope: "platform", owner: "ops" })), { kind: "platform" }, "platform scope");
eq(appAuthor(app({ scope: "builtin", owner: "" })), { kind: "platform" }, "built-in content");
eq(appAuthor(app({ scope: "org", owner: "maya" })), { kind: "person", id: "maya" }, "an organization app's author");
eq(appAuthor(app({ scope: "shared", owner: "jack", sharedBy: "maya" })), { kind: "person", id: "maya" }, "a shared copy's author");
eq(appAuthor(app({ scope: "personal", owner: "jack" })), { kind: "person", id: "jack" }, "a member's own app");

// A star toggles the member's star and the count, never below zero.
eq([withFavorite(app({ favoriteCount: 2 }), true).favorite, withFavorite(app({ favoriteCount: 2 }), true).favoriteCount], [true, 3], "star");
eq(withFavorite(app({ favorite: true, favoriteCount: 0 }), false).favoriteCount, 0, "unstar at zero");
{
  const c = app({ favorite: true, favoriteCount: 4 });
  assert(withFavorite(c, true) === c, "no change keeps the object");
}

// A cover the gateway does not serve (404 none, 413, 415) switches the card
// to the generated cover for good; other failures do not.
assert(coverRefused(new ApiError(404, "this app has no cover")), "404: no cover");
assert(coverRefused(new ApiError(413, "too large")) && coverRefused(new ApiError(415, "not an image")), "413/415: refused");
assert(!coverRefused(new ApiError(500, "boom")) && !coverRefused(new ApiError(401, "sign in")), "a server or session failure is not");
assert(!coverRefused(new TypeError("Failed to fetch")), "offline is not");

// Publish to gallery: a new app sends its name and what is filled in; a new
// version sends the component and no name; tags are cleaned.
{
  const form = publishAppForm("  Lunch Poll! ", []);
  eq(form, { componentId: "", name: "lunch-poll", displayName: "Lunch Poll!", description: "", category: "", tags: "" }, "a site's form");
  eq(publishAppRequest(form), { name: "lunch-poll", displayName: "Lunch Poll!" }, "a new app, blanks left out");
  eq(
    publishAppRequest({ ...form, name: " team-poll ", description: " Vote on lunch ", category: "life", tags: "Team, food,team\n" }),
    { name: "team-poll", displayName: "Lunch Poll!", description: "Vote on lunch", category: "life", tags: ["team", "food"] },
    "a new app, every field",
  );
  eq(publishAppRequest({ ...form, componentId: "cc_9", displayName: "" }), { componentId: "cc_9" }, "a new version keeps what it leaves out");
  eq(publishAppRequest({ ...form, componentId: "cc_9", category: "data" }), { componentId: "cc_9", displayName: "Lunch Poll!", category: "data" }, "a new version's changes");
  // A site whose title is one of the member's own apps publishes its next version.
  const mine = [app({ id: "cc_7", displayName: "Lunch Poll!", mine: true }), app({ id: "cc_8", displayName: "Lunch Poll!", mine: false })];
  eq(publishAppForm("Lunch Poll!", mine).componentId, "cc_7", "the member's own app of that name");
  eq(publishAppForm("Other", mine).componentId, "", "no app of that name");
  eq([publishAppProblem(form), publishAppProblem({ ...form, name: "" }), publishAppProblem({ ...form, name: "Bad Name" })], [null, "Give it a name", "The name uses lower-case letters, digits and hyphens"], "name problems");
  assert(publishAppProblem({ ...form, componentId: "cc_9", name: "" }) === null, "a new version needs no name");
  assert(publishAppProblem({ ...form, tags: "ok, Bad!" })?.includes("bad!") ?? false, "a bad tag");
  eq(publishAppProblem({ ...form, tags: "评审, 优先级 规划, q3-planning" }), null, "tags in any script (wire.ContentTagPattern)");
  assert(publishAppProblem({ ...form, tags: "评".repeat(33) }) !== null, "a tag over MaxContentTagRunes characters");
  eq([appNameFrom(" Team 2026 — Sign-up "), appNameFrom("***")], ["team-2026-sign-up", ""], "names from titles");
}

// Publishing to the organization: the copy follows its approval setting,
// the toast the server's answer.
assert(orgPublishCopy("required", "1.0.0").action === "Ask to publish", "approval required");
assert(orgPublishCopy(undefined, "1.0.0").action === "Ask to publish", "unknown setting reads as required");
assert(orgPublishCopy("none", "1.0.0").action === "Publish" && orgPublishCopy("none", "1.0.0").description.includes("at once"), "no approval");
eq(orgPublishToast({ request: { id: "r1" } as never }), "Sent for approval", "a publish request");
eq(orgPublishToast({}), "Published to your organization", "published at once");

// Use this app: the rooms a copy may go to are the member's rooms they may
// post in; the request names one room or a new one.
{
  const room = (roomId: string, role: string, name = roomId): RoomSummary =>
    ({ roomId, name, role, description: "", visibility: "private", syncMode: "none", memberCount: 1, createdAtMs: 1 }) as unknown as RoomSummary;
  const rooms = [room("r-z", "member", "Zeta"), room("r-v", "visitor", "Visited"), room("r-a", "owner", "Alpha"), room("r-x", "admin", "")];
  eq(appCopyRooms(rooms).map((r) => r.roomId), ["r-a", "r-x", "r-z"], "rooms a copy may go to, by name");
  eq(defaultAppCopyTarget("Lunch poll"), { kind: "new", name: "Lunch poll" }, "a new room named after the app by default");
  eq(appCopyRequest({ kind: "room", roomId: "r-a" }, "1.0.0"), { version: "1.0.0", roomId: "r-a" }, "an existing room");
  eq(appCopyRequest({ kind: "new", name: "  Lunch poll " }), { newRoom: { name: "Lunch poll" } }, "a new room");
  eq(appCopyRequest({ kind: "new", name: " " }), { newRoom: {} }, "a new room the server names");
  eq(appCopyProblem({ kind: "room", roomId: "r-v" }, rooms), "Pick a room you can post in", "a visitor's room");
  eq(appCopyProblem({ kind: "room", roomId: "r-a" }, rooms), null, "an owned room");
  eq(appCopyProblem({ kind: "new", name: "" }, rooms), null, "a new room");
  assert(appCopyErrorText(new ApiError(403, "use an app in a room you own")).startsWith("You can only use an app in a room you can post in"), "403: not an owner");
  eq(appCopyErrorText(new ApiError(404, "content component not found")), "This app is no longer available to you.", "404: invisible app");
  assert(appCopyErrorText(new ApiError(413, "over the room's cap")).includes("over the room's cap"), "other refusals keep the server's reason");
}
