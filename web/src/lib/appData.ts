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
// permission (the gateway's rule for the room entry's writes).

import { ApiError } from "./errors.ts";
import type { AppDataStats, AppDocPage } from "./wire.gen.ts";

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

/** A document as the API reads it: its fields plus the server's stamps. */
export type AppDoc = Record<string, unknown>;

/** The server's stamps (internal/appdata doc.go), the export's first
 *  columns in this order. `_author` (who wrote it) is rendered for owners. */
export const APP_DOC_STAMPS = ["_id", "_by", "_author", "_createdAt", "_updatedAt", "_version"] as const;

/** allDocs pages a find to its end (each page's `next` is the cursor of the
 *  following one). */
export async function allDocs(page: (after: string) => Promise<AppDocPage>): Promise<AppDoc[]> {
  const out: AppDoc[] = [];
  let after = "";
  for (;;) {
    const p = await page(after);
    out.push(...(p.docs ?? []));
    if (!p.next || p.next === after) return out;
    after = p.next;
  }
}

/** The JSON export: every collection's documents, by collection name. */
export function appDataJson(collections: Readonly<Record<string, readonly AppDoc[]>>): string {
  return JSON.stringify({ collections }, null, 2) + "\n";
}

// A text cell a spreadsheet would run as a formula (the data is anyone's
// with the link) is quoted with a leading apostrophe.
const FORMULA_START = /^[=+\-@\t\r]/;

/** csvCell is one value as a CSV cell. A top-level field's value is written
 *  as is (text, number, boolean); null or absent is empty; anything nested
 *  (an object or an array, at any depth) is its JSON text in the one cell,
 *  so every row has the same columns. A cell with a comma, quote or line
 *  break is quoted, quotes doubled. */
export function csvCell(value: unknown): string {
  let text: string;
  if (value === null || value === undefined) text = "";
  else if (typeof value === "string") text = FORMULA_START.test(value) ? `'${value}` : value;
  else if (typeof value === "number" || typeof value === "boolean") text = String(value);
  else text = JSON.stringify(value);
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/** appDocsCsv is one collection as CSV: the stamps present first, then
 *  every other top-level field in order of first appearance; one row per
 *  document; CRLF line ends. */
export function appDocsCsv(docs: readonly AppDoc[]): string {
  const seen = new Set<string>();
  const fields: string[] = [];
  for (const d of docs) {
    for (const k of Object.keys(d)) {
      if (!seen.has(k)) {
        seen.add(k);
        fields.push(k);
      }
    }
  }
  const stamps: readonly string[] = APP_DOC_STAMPS;
  const columns = [...stamps.filter((s) => seen.has(s)), ...fields.filter((f) => !stamps.includes(f))];
  const rows = [columns.map(csvCell).join(","), ...docs.map((d) => columns.map((c) => csvCell(d[c])).join(","))];
  return rows.join("\r\n") + "\r\n";
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
