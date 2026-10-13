// An interactive app's data as its owners see it on the deliverable card and
// in the viewer (docs/prd/2026-10-12-interactive-apps.md §6, "Card data
// line"): the document count per collection, Export (JSON, CSV) and Clear
// data. Pure (no React, no DOM); the requests are lib/appDataApi.ts, the
// line views/workbench/AppDataLine.tsx.
//
// Who is an owner is the room service's rule (services/room/appdata.go
// appOwner: the room's members who may chat, and its agents): the owners'
// stats route answers 403 to anyone else, so the line asks it and never
// re-decides. Clearing additionally needs the organization's drive_agent
// permission (the gateway's rule for the room entry's writes). An export is
// the server's (the room service streams it, writes its CSV and audits it):
// the web only names the file.

import { ApiError } from "./errors.ts";
import type { AppDataStats } from "./wire.gen.ts";

/** The stats route's answer as the data line reads it. */
export type AppStatsAnswer =
  | { readonly kind: "loading" }
  | { readonly kind: "stats"; readonly stats: AppDataStats }
  // Not an owner (403), or not an app of this room (404): no line at all.
  | { readonly kind: "hidden" }
  | { readonly kind: "failed"; readonly error: unknown };

/** appStatsAnswer reads a stats query's state. */
export function appStatsAnswer(q: { readonly data: AppDataStats | undefined; readonly error: unknown }): AppStatsAnswer {
  if (q.data) return { kind: "stats", stats: q.data };
  if (q.error instanceof ApiError && (q.error.status === 403 || q.error.status === 404)) return { kind: "hidden" };
  if (q.error) return { kind: "failed", error: q.error };
  return { kind: "loading" };
}

/** appDataLineShown: the line shows for an owner whose app has data. */
export function appDataLineShown(a: AppStatsAnswer): a is { readonly kind: "stats"; readonly stats: AppDataStats } {
  return a.kind === "stats" && a.stats.documents > 0;
}

/** The app's collections with their document counts, largest first (then
 *  by name), empty ones left out. */
export function appCollectionCounts(stats: AppDataStats): { readonly name: string; readonly count: number }[] {
  return Object.entries(stats.collections ?? {})
    .filter(([, count]) => count > 0)
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
}

/** "1 document" / "1,204 documents". */
export function documentsLabel(n: number): string {
  return n === 1 ? "1 document" : `${n.toLocaleString("en-US")} documents`;
}

/** "votes 10 · comments 2". */
export function collectionsLabel(stats: AppDataStats): string {
  return appCollectionCounts(stats)
    .map((c) => `${c.name} ${c.count.toLocaleString("en-US")}`)
    .join(" · ");
}

/** An export's file name: the app's name (made file-safe), the collection
 *  for a CSV, and the extension. */
export function appExportFileName(appName: string, ext: "json" | "csv", collection?: string): string {
  const safe = (s: string) =>
    s
      .trim()
      .replace(/[^\p{L}\p{N}._-]+/gu, "-")
      .replace(/^[-.]+|[-.]+$/g, "");
  const parts = [safe(appName) || "app", "data", ...(collection ? [safe(collection) || "collection"] : [])];
  return `${parts.join("-")}.${ext}`;
}
