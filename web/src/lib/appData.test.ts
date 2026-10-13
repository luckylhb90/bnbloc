import {
  allDocs,
  appCollectionCounts,
  appDataJson,
  appDataLineShown,
  appDocsCsv,
  appExportFileName,
  appStatsAnswer,
  collectionsLabel,
  csvCell,
  documentsLabel,
} from "./appData.ts";
import { ApiError } from "./errors.ts";
import type { AppDataStats, AppDocPage } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const stats = (documents: number, collections: Record<string, number> = {}): AppDataStats => ({ documents, bytes: documents * 10, collections });

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

function testCsvCells(): void {
  eq(csvCell("plain"), "plain", "plain text");
  eq(csvCell('say "hi"'), '"say ""hi"""', "quotes doubled inside quotes");
  eq(csvCell("a,b"), '"a,b"', "a comma is quoted");
  eq(csvCell("two\nlines"), '"two\nlines"', "a line break is quoted");
  eq(csvCell("cr\r"), '"cr\r"', "a carriage return is quoted");
  eq(csvCell(null), "", "null is empty");
  eq(csvCell(undefined), "", "absent is empty");
  eq(csvCell(0), "0", "a number");
  eq(csvCell(-3), "-3", "a negative number is a number");
  eq(csvCell(false), "false", "a boolean");
  eq(csvCell("=HYPERLINK(1)"), "'=HYPERLINK(1)", "a formula is defused");
  eq(csvCell("+1"), "'+1", "a leading plus is defused");
  eq(csvCell("@x"), "'@x", "a leading at is defused");
  eq(csvCell({ a: 1, b: [1, 2] }), '"{""a"":1,""b"":[1,2]}"', "an object is its JSON text, quoted");
  eq(csvCell(["x", "y"]), '"[""x"",""y""]"', "an array is its JSON text, quoted");
}

function testCsvTable(): void {
  const csv = appDocsCsv([
    { choice: "B", _id: "d1", _by: "p1", _version: 1, _createdAt: 10, _updatedAt: 10, meta: { device: "phone" } },
    { _id: "d2", _by: "p2", _version: 2, _createdAt: 11, _updatedAt: 12, note: "hi, there", choice: "A", _author: { kind: "visitor", id: "vis_1" } },
  ]);
  eq(
    csv.split("\r\n"),
    [
      "_id,_by,_author,_createdAt,_updatedAt,_version,choice,meta,note",
      'd1,p1,,10,10,1,B,"{""device"":""phone""}",',
      'd2,p2,"{""kind"":""visitor"",""id"":""vis_1""}",11,12,2,A,,"hi, there"',
      "",
    ],
    "stamps first, then fields by first appearance; nested JSON-encoded; every row the same columns",
  );
  eq(appDocsCsv([]), "\r\n", "no documents is an empty header");
}

async function testPaging(): Promise<void> {
  const pages: Record<string, AppDocPage> = {
    "": { docs: [{ _id: "a" }, { _id: "b" }], next: "b" },
    b: { docs: [{ _id: "c" }], next: "c" },
    c: { docs: [] },
  };
  const asked: string[] = [];
  const docs = await allDocs(async (after) => {
    asked.push(after);
    return pages[after];
  });
  eq(docs.map((d) => d._id), ["a", "b", "c"], "every page, in order");
  eq(asked, ["", "b", "c"], "each page asked after the previous cursor");
  const stuck = await allDocs(async () => ({ docs: [{ _id: "x" }], next: "x" }));
  eq(stuck.length, 2, "a cursor that does not move ends the paging");
}

function testExportShapes(): void {
  const json = JSON.parse(appDataJson({ votes: [{ _id: "a" }] }));
  eq(json, { collections: { votes: [{ _id: "a" }] } }, "the JSON export is every collection by name");
  eq(appExportFileName("Friday poll", "json"), "Friday-poll-data.json", "json file name");
  eq(appExportFileName("Friday poll", "csv", "votes"), "Friday-poll-data-votes.csv", "csv file name names the collection");
  eq(appExportFileName("../..", "csv", "a/b"), "app-data-a-b.csv", "no path in a file name");
}

testDataLineVisibility();
testCounts();
testCsvCells();
testCsvTable();
await testPaging();
testExportShapes();
