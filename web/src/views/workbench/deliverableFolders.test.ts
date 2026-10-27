import { test } from "node:test";
import assert from "node:assert/strict";
import { folderContents, folderCrumbs, folderSegments } from "./deliverableFolders.ts";

const items = [
  { title: "notes.md" },
  { title: "reports/q3/summary.md" },
  { title: "reports/q3/chart.png" },
  { title: "reports/overview.md" },
  { title: "./app/index.html" },
  { title: "Plan" },
];

test("a deliverable's folders are its path's", () => {
  assert.deepEqual(folderSegments("reports/q3/summary.md"), ["reports", "q3"]);
  assert.deepEqual(folderSegments("./app/index.html"), ["app"]);
  assert.deepEqual(folderSegments("Plan"), []);
});

test("the top holds its folders with their counts and the files without a folder", () => {
  const top = folderContents(items, "");
  assert.deepEqual(top.folders, [
    { name: "app", path: "app", count: 1 },
    { name: "reports", path: "reports", count: 3 },
  ]);
  assert.deepEqual(
    top.files.map((f) => f.title),
    ["notes.md", "Plan"],
  );
});

test("a folder holds its subfolders and its own files", () => {
  const reports = folderContents(items, "reports");
  assert.deepEqual(reports.folders, [{ name: "q3", path: "reports/q3", count: 2 }]);
  assert.deepEqual(
    reports.files.map((f) => f.title),
    ["reports/overview.md"],
  );
  assert.equal(folderContents(items, "reports/q3").files.length, 2);
  assert.deepEqual(folderContents(items, "nowhere"), { folders: [], files: [] });
});

test("the way back names each folder above", () => {
  assert.deepEqual(folderCrumbs(""), []);
  assert.deepEqual(folderCrumbs("reports/q3"), [
    { name: "reports", path: "reports" },
    { name: "q3", path: "reports/q3" },
  ]);
});
