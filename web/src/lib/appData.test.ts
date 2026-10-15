import { appCollectionCounts, appDataLineShown, appExportFileName, appStatsAnswer, collectionsLabel, documentsLabel } from "./appData.ts";
import { ApiError } from "./errors.ts";
import type { AppDataStats } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const stats = (documents: number, collections: Record<string, number> = {}): AppDataStats => ({ documents, bytes: documents * 10, collections, files: 0, fileBytes: 0 });

function testDataLineVisibility(): void {
  const owner = appStatsAnswer({ data: stats(3, { votes: 3 }), error: undefined });
  assert(appDataLineShown(owner), "an owner's app with data shows the line");
  assert(!appDataLineShown(appStatsAnswer({ data: stats(0), error: undefined })), "an owner's empty app shows none");
  const notOwner = appStatsAnswer({ data: undefined, error: new ApiError(403, "only the app's owners may do this") });
  assert(notOwner.kind === "hidden" && !appDataLineShown(notOwner), "a non-owner (403) sees no line");
  const notApp = appStatsAnswer({ data: undefined, error: new ApiError(404, "app not found") });
  assert(notApp.kind === "hidden", "not an app of this room (404) is no line either");
  const failed = appStatsAnswer({ data: undefined, error: new ApiError(502, "") });
  assert(failed.kind === "failed" && !appDataLineShown(failed), "another failure is a failure (shown with Retry), not an empty line");
  const offline = appStatsAnswer({ data: undefined, error: new TypeError("Failed to fetch") });
  assert(offline.kind === "failed", "a network failure is a failure");
  assert(appStatsAnswer({ data: undefined, error: undefined }).kind === "loading", "no answer yet is loading");
}

function testCounts(): void {
  const s = stats(13, { votes: 10, comments: 2, answers: 2, empty: 0 });
  eq(
    appCollectionCounts(s).map((c) => c.name),
    ["votes", "answers", "comments"],
    "largest first, then by name, empty left out",
  );
  eq(collectionsLabel(s), "votes 10 · answers 2 · comments 2", "per-collection line");
  eq(documentsLabel(1), "1 document", "singular");
  eq(documentsLabel(1204), "1,204 documents", "plural, grouped");
}

// The export itself (paging, JSON, CSV, escaping) is the server's
// (internal/appdata export.go, services/room appdata_export.go): the web
// only names the file.
function testExportFileNames(): void {
  eq(appExportFileName("Friday poll", "json"), "Friday-poll-data.json", "json file name");
  eq(appExportFileName("Friday poll", "csv", "votes"), "Friday-poll-data-votes.csv", "csv file name names the collection");
  eq(appExportFileName("../..", "csv", "a/b"), "app-data-a-b.csv", "no path in a file name");
}

testDataLineVisibility();
testCounts();
testExportFileNames();
