import type { Artifact } from "../../lib/wire.gen.ts";
import { fileCategory, versionFacts, type FileCategory } from "../../lib/artifactFiles.ts";

// The deliverables view's filters (DeliverablesView): type, author, date and
// a name search. Pure, unit-tested under node. "Apps" is the server's rule
// (an artifact's isApp: a site whose data space exists or that loads the app
// SDK, store.Artifact.IsApp): the view also asks the server for apps only,
// so paging stays whole, and never re-derives which sites are apps.

export type TypeFilter = "all" | "apps" | "images" | "documents" | "code" | "tables" | "web" | "media" | "other";
export type DateFilter = "any" | "day" | "week" | "month";

export const TYPE_FILTERS: readonly { readonly value: TypeFilter; readonly label: string; readonly categories: readonly FileCategory[] }[] = [
  { value: "all", label: "All types", categories: [] },
  { value: "apps", label: "Apps", categories: [] },
  { value: "images", label: "Images", categories: ["image"] },
  { value: "documents", label: "Documents", categories: ["markdown", "pdf", "text"] },
  { value: "code", label: "Code", categories: ["code", "json"] },
  { value: "tables", label: "Tables", categories: ["table"] },
  { value: "web", label: "Web pages & sites", categories: ["web"] },
  { value: "media", label: "Audio & video", categories: ["audio", "video"] },
  { value: "other", label: "Other files", categories: ["archive", "file"] },
];

export const DATE_FILTERS: readonly { readonly value: DateFilter; readonly label: string; readonly ms: number }[] = [
  { value: "any", label: "Any time", ms: 0 },
  { value: "day", label: "Last 24 hours", ms: 86_400_000 },
  { value: "week", label: "Last 7 days", ms: 7 * 86_400_000 },
  { value: "month", label: "Last 30 days", ms: 30 * 86_400_000 },
];

export interface DeliverableFilter {
  readonly type: TypeFilter;
  readonly author: string;
  readonly date: DateFilter;
  // The search text, already trimmed.
  readonly q: string;
}

/** The server-side part of a filter: what the list request narrows. */
export function deliverableQuery(f: DeliverableFilter): { q?: string; author?: string; mime?: string; apps?: boolean } {
  return {
    q: f.q || undefined,
    author: f.author || undefined,
    mime: f.type === "images" ? "image/*" : undefined,
    apps: f.type === "apps" || undefined,
  };
}

/** deliverableMatches applies every filter to one listed artifact (a page
 *  from a server that ignored one still shows only matching items). */
export function deliverableMatches(a: Artifact, f: DeliverableFilter, nowMs: number): boolean {
  if (f.type === "apps") {
    if (!a.isApp) return false;
  } else {
    const categories = TYPE_FILTERS.find((t) => t.value === f.type)?.categories ?? [];
    if (categories.length > 0 && !categories.includes(fileCategory(versionFacts(a)))) return false;
  }
  if (f.author && a.createdBy !== f.author) return false;
  const since = DATE_FILTERS.find((d) => d.value === f.date)?.ms ?? 0;
  if (since && nowMs - a.updatedAtMs > since) return false;
  const needle = f.q.toLowerCase();
  return !needle || a.title.toLowerCase().includes(needle) || (a.fileName ?? "").toLowerCase().includes(needle);
}

/** Whether any filter narrows the list (the empty state says so). */
export function deliverableFiltered(f: DeliverableFilter): boolean {
  return !!(f.q || f.author || f.type !== "all" || f.date !== "any");
}
