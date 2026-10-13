// The app gallery (docs/prd/2026-10-13-app-gallery.md §6–§8): Plaza →
// "Apps", an app's detail, "Use this app" and "Publish to gallery". The
// apps are the content listing's (GET /v1/content-components?kind=app: what
// contentuse.Visible lets the member see, each with its star, its star
// count and its cover); the helpers here are pure (no React, no DOM) and
// unit-tested: the category tabs, the gallery's filter, the author line,
// the publish request and the room choice of a copy. The requests are
// lib/appGalleryApi.ts, the views views/gallery/.

export type { AppCategory, AppDemo, AppPublishRequest, AppUseReply, AppUseRequest } from "./wire.gen.ts";
import { AppCategoryValues, type AppCategory, type AppPublishRequest, type AppUseRequest, type RoomSummary } from "./wire.gen.ts";
import { cleanTags, contentLabel, splitList, type ContentComponent } from "./contentComponents.ts";
import { ApiError, errorText } from "./errors.ts";
import { mayChat } from "./roomRoles.ts";

/** What the gallery calls each category (its tabs, a card's detail). */
export const APP_CATEGORY_LABELS: Readonly<Record<AppCategory, string>> = {
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

/** A gallery tab: every app, or one category. */
export type GalleryTab = "all" | AppCategory;

/** The tabs in the wire's order (wire.AppCategories), "All" first. */
export const GALLERY_TABS: readonly { readonly value: GalleryTab; readonly label: string }[] = [
  { value: "all", label: "All" },
  ...AppCategoryValues.map((c) => ({ value: c, label: APP_CATEGORY_LABELS[c] })),
];

/** The category's label, "" for none (or one this client does not know). */
export function appCategoryLabel(category: string | undefined): string {
  return category && category in APP_CATEGORY_LABELS ? APP_CATEGORY_LABELS[category as AppCategory] : "";
}

export interface GalleryFilter {
  readonly tab: GalleryTab;
  readonly query: string;
  readonly favoritesOnly: boolean;
}

/** matchesApp is the gallery's search: every word of the query in the
 *  app's name, display name, description or tags (case-insensitive). */
export function matchesApp(c: ContentComponent, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = [c.name, c.displayName, c.description, ...(c.tags ?? [])].filter(Boolean).join("\n").toLowerCase();
  return words.every((w) => hay.includes(w));
}

/** galleryApps is the grid: the listing's apps under the tab, the search
 *  and "My favorites", newest-updated first. An author's own app whose
 *  organization publication is listed too shows once, as the publication
 *  (what everyone sees and stars). */
export function galleryApps(list: readonly ContentComponent[], f: GalleryFilter): ContentComponent[] {
  const apps = list.filter((c) => c.kind === "app");
  const listed = new Set(apps.map((c) => c.id));
  return apps
    .filter((c) => !(c.publication?.orgComponentId && listed.has(c.publication.orgComponentId)))
    .filter((c) => f.tab === "all" || c.category === f.tab)
    .filter((c) => !f.favoritesOnly || c.favorite)
    .filter((c) => matchesApp(c, f.query))
    .sort((a, b) => b.updatedAtMs - a.updatedAtMs);
}

/** An app card's author: a person, or the platform (platform and built-in
 *  content, which no member authors). */
export type AppAuthor = { readonly kind: "platform" } | { readonly kind: "person"; readonly id: string };

/** appAuthor: the platform for platform scope; a shared copy's author;
 *  else the member who owns it (an organization publication's author). */
export function appAuthor(c: Pick<ContentComponent, "scope" | "owner" | "sharedBy">): AppAuthor {
  if (c.scope === "platform" || c.scope === "builtin") return { kind: "platform" };
  return { kind: "person", id: (c.scope === "shared" && c.sharedBy) || c.owner };
}

/** A star's new state on the listing: the member's own star and the count
 *  (never below zero). */
export function withFavorite<T extends Pick<ContentComponent, "favorite" | "favoriteCount">>(c: T, favorite: boolean): T {
  if (c.favorite === favorite) return c;
  return { ...c, favorite, favoriteCount: Math.max(0, (c.favoriteCount ?? 0) + (favorite ? 1 : -1)) };
}

// ---- Publish to gallery ----

/** The publish dialog's form. componentId "" publishes a new app; an id
 *  publishes the site as that app's next version (its name stays). */
export interface PublishAppForm {
  readonly componentId: string;
  readonly name: string;
  readonly displayName: string;
  readonly description: string;
  readonly category: AppCategory | "";
  // Comma- or newline-separated.
  readonly tags: string;
}

const NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;

/** appNameFrom suggests a component name for a site's title (lower-case
 *  letters, digits and hyphens): what the server would derive
 *  (contentpkg.NameFrom) when the name is left out. */
export function appNameFrom(title: string): string {
  return title
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63)
    .replace(/^-+|-+$/g, "");
}

/** The form for a site: a new app named after it, or the next version of
 *  the member's own app of the same display name. */
export function publishAppForm(title: string, mine: readonly Pick<ContentComponent, "id" | "name" | "displayName" | "kind" | "mine">[]): PublishAppForm {
  const label = title.trim();
  const same = mine.find((c) => c.kind === "app" && c.mine && contentLabel(c) === label);
  return { componentId: same?.id ?? "", name: appNameFrom(label), displayName: label, description: "", category: "", tags: "" };
}

/** publishAppProblem is the first thing the form must fix (the server
 *  validates everything; this catches the obvious early). */
export function publishAppProblem(f: PublishAppForm): string | null {
  if (!f.componentId) {
    const name = f.name.trim();
    if (!name) return "Give it a name";
    if (!NAME.test(name)) return "The name uses lower-case letters, digits and hyphens";
  }
  for (const t of cleanTags(splitList(f.tags))) {
    if (!/^[a-z0-9][a-z0-9 _-]*$/.test(t) || t.length > 32) return `The tag "${t}" uses lower-case letters, digits, spaces, "_" and "-" (at most 32)`;
  }
  return null;
}

/** publishAppRequest is the body of POST …/publish-app: the trimmed form,
 *  empty fields left out (a new version keeps what it leaves out), and no
 *  name for a new version (the app's own stays). */
export function publishAppRequest(f: PublishAppForm): AppPublishRequest {
  const out: AppPublishRequest = f.componentId ? { componentId: f.componentId } : { name: f.name.trim() };
  const displayName = f.displayName.trim();
  const description = f.description.trim();
  const tags = cleanTags(splitList(f.tags));
  if (displayName) out.displayName = displayName;
  if (description) out.description = description;
  if (f.category) out.category = f.category;
  if (tags.length > 0) out.tags = tags;
  return out;
}

// ---- Use this app ----

/** Where "Use this app" puts the copy: a room the member owns, or a new
 *  room (named after the app unless they rename it). */
export type AppCopyTarget = { readonly kind: "room"; readonly roomId: string } | { readonly kind: "new"; readonly name: string };

/** appCopyRooms are the member's rooms a copy may go to: the ones they may
 *  post in (the app owner rule, lib/roomRoles mayChat), by name. */
export function appCopyRooms(rooms: readonly RoomSummary[]): RoomSummary[] {
  return rooms.filter((r) => mayChat({ role: r.role })).sort((a, b) => (a.name || a.roomId).localeCompare(b.name || b.roomId));
}

/** The default choice: a new room named after the app. */
export function defaultAppCopyTarget(appName: string): AppCopyTarget {
  return { kind: "new", name: appName };
}

/** appCopyRequest is the body of POST …/use-app for a target and version. */
export function appCopyRequest(target: AppCopyTarget, version?: string): AppUseRequest {
  const v = version ? { version } : {};
  if (target.kind === "room") return { ...v, roomId: target.roomId };
  const name = target.name.trim();
  return { ...v, newRoom: name ? { name } : {} };
}

/** appCopyProblem: the target is not one the dialog can send. */
export function appCopyProblem(target: AppCopyTarget, rooms: readonly RoomSummary[]): string | null {
  if (target.kind === "room" && !appCopyRooms(rooms).some((r) => r.roomId === target.roomId)) return "Pick a room you can post in";
  return null;
}

/** appCopyErrorText words a refused copy: a room the member does not own
 *  (403), an app they no longer see (404), else the server's reason. */
export function appCopyErrorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 403) return "You can only use an app in a room you can post in. Pick another room, or create a new one.";
  if (e instanceof ApiError && e.status === 404) return "This app is no longer available to you.";
  return errorText(e, "Could not use this app");
}
