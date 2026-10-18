import {
  DELIVERABLES_TAB,
  EMPTY_WORKBENCH,
  MAX_TABS,
  parseWorkbench,
  serializeWorkbench,
  workbenchReducer as r,
  type WorkbenchState,
} from "./workbenchTabs.ts";

// Local runner (the web tsconfig has no node types); a failure throws.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}
const keys = (s: WorkbenchState) => s.tabs.map((t) => t.key);

test("open adds after the active tab and activates; reopening only activates", () => {
  let s = r(EMPTY_WORKBENCH, { type: "open", artifactId: "a" });
  s = r(s, { type: "open", artifactId: "b" });
  s = r(s, { type: "activate", key: "a:a" });
  s = r(s, { type: "open", artifactId: "c" });
  eq(keys(s), ["a:a", "a:c", "a:b"], "inserted after active");
  eq(s.active, "a:c", "new tab active");
  s = r(s, { type: "open", artifactId: "b", version: 3 });
  eq(keys(s), ["a:a", "a:c", "a:b"], "no duplicate");
  eq([s.active, s.tabs[2].version], ["a:b", 3], "existing tab activated at the version");
});

test("close activates a neighbour; closeOthers keeps pinned tabs", () => {
  let s = EMPTY_WORKBENCH;
  for (const id of ["a", "b", "c"]) s = r(s, { type: "open", artifactId: id });
  s = r(s, { type: "close", key: "a:c" });
  eq([keys(s), s.active], [["a:a", "a:b"], "a:b"], "closing active picks neighbour");
  s = r(s, { type: "close", key: "a:a" });
  eq([keys(s), s.active], [["a:b"], "a:b"], "closing inactive keeps active");
  s = r(s, { type: "open", artifactId: "x" });
  s = r(s, { type: "open", artifactId: "y" });
  s = r(s, { type: "pin", key: "a:x", pinned: true });
  s = r(s, { type: "closeOthers", key: "a:y" });
  eq([keys(s), s.active], [["a:x", "a:y"], "a:y"], "close others keeps pinned + target");
  s = r(s, { type: "close", key: "a:y" });
  s = r(s, { type: "close", key: "a:x" });
  eq(s, { tabs: [], active: null }, "all closed");
});

test("pinned tabs group at the front and moves stay within their group", () => {
  let s = EMPTY_WORKBENCH;
  for (const id of ["a", "b", "c", "d"]) s = r(s, { type: "open", artifactId: id });
  s = r(s, { type: "pin", key: "a:c", pinned: true });
  eq(keys(s), ["a:c", "a:a", "a:b", "a:d"], "pinned moves to the front");
  s = r(s, { type: "move", key: "a:d", toIndex: 0 });
  eq(keys(s), ["a:c", "a:d", "a:a", "a:b"], "unpinned cannot enter the pinned group");
  s = r(s, { type: "move", key: "a:c", toIndex: 3 });
  eq(keys(s), ["a:c", "a:d", "a:a", "a:b"], "pinned cannot leave its group");
  s = r(s, { type: "move", key: "a:a", toIndex: 3 });
  eq(keys(s), ["a:c", "a:d", "a:b", "a:a"], "reorder within group");
  s = r(s, { type: "pin", key: "a:c", pinned: false });
  eq(keys(s)[0], "a:c", "unpinned tab starts the unpinned group");
});

test("eviction drops the oldest unpinned inactive tab", () => {
  let s = r(EMPTY_WORKBENCH, { type: "open", artifactId: "p" });
  s = r(s, { type: "pin", key: "a:p", pinned: true });
  for (let i = 0; i < MAX_TABS; i++) s = r(s, { type: "open", artifactId: `t${i}` });
  eq(s.tabs.length, MAX_TABS, "bounded");
  eq(keys(s).includes("a:p"), true, "pinned kept");
  eq(keys(s).includes("a:t0"), false, "oldest unpinned evicted");
  eq(s.active, `a:t${MAX_TABS - 1}`, "newest active");
});

test("deliverables tab opens once and carries its filter", () => {
  let s = r(EMPTY_WORKBENCH, { type: "open", artifactId: "a" });
  s = r(s, { type: "openDeliverables", filter: { sourceSeq: 7 } });
  s = r(s, { type: "openDeliverables" });
  eq(keys(s), ["a:a", DELIVERABLES_TAB], "single deliverables tab");
  eq(s.tabs[1].filter, undefined, "filter replaced");
});

test("version and viewer overrides", () => {
  let s = r(EMPTY_WORKBENCH, { type: "open", artifactId: "a" });
  s = r(s, { type: "setVersion", key: "a:a", version: 2 });
  s = r(s, { type: "setViewer", key: "a:a", viewerId: "core.code" });
  eq([s.tabs[0].version, s.tabs[0].viewerId], [2, "core.code"], "set");
  s = r(s, { type: "setVersion", key: "a:a", version: undefined });
  eq(s.tabs[0].version, undefined, "follow current again");
  eq(r(s, { type: "setVersion", key: "nope", version: 1 }), s, "unknown key is a no-op");
});

test("persistence round-trips and rejects malformed storage", () => {
  let s = r(EMPTY_WORKBENCH, { type: "open", artifactId: "a", version: 2 });
  s = r(s, { type: "openDeliverables", filter: { sourceSeq: 9 } });
  s = r(s, { type: "pin", key: DELIVERABLES_TAB, pinned: true });
  eq(parseWorkbench(serializeWorkbench(s)), s, "round trip");
  eq(parseWorkbench(null), EMPTY_WORKBENCH, "missing");
  eq(parseWorkbench("{oops"), EMPTY_WORKBENCH, "bad json");
  eq(parseWorkbench(JSON.stringify({ v: 2, tabs: [] })), EMPTY_WORKBENCH, "unknown version");
  const dirty = JSON.stringify({
    v: 1,
    active: "a:gone",
    tabs: [
      { key: "a:x", artifactId: "x", pinned: false, version: -1 },
      { key: "a:y", artifactId: "z", pinned: false },
      { key: "a:x", artifactId: "x", pinned: true },
      { key: "a:w", artifactId: "w", pinned: true, viewerId: 5 },
      "junk",
    ],
  });
  eq(parseWorkbench(dirty), {
    tabs: [
      { key: "a:w", artifactId: "w", pinned: true },
      { key: "a:x", artifactId: "x", pinned: false },
    ],
    active: "a:w",
  }, "sanitized");
});

test("an attachment opens once as its own tab and persists", () => {
  const file = { id: "att_1", filename: "deck.pptx", contentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", size: 38422 };
  let s = r(EMPTY_WORKBENCH, { type: "open", artifactId: "a" });
  s = r(s, { type: "openAttachment", attachment: file });
  eq(keys(s), ["a:a", "f:att_1"], "after the active tab");
  eq(s.active, "f:att_1", "activated");
  s = r(s, { type: "activate", key: "a:a" });
  s = r(s, { type: "openAttachment", attachment: file });
  eq([keys(s), s.active], [["a:a", "f:att_1"], "f:att_1"], "reopening only activates");
  eq(parseWorkbench(serializeWorkbench(s)), s, "round trip");
  const dirty = JSON.stringify({
    v: 1,
    tabs: [
      { key: "f:x", attachment: { id: "y", filename: "a", contentType: "", size: 1 }, pinned: false },
      { key: "f:z", attachment: { id: "z", filename: "a" }, pinned: false },
      { key: "f:q", attachment: null, pinned: false },
    ],
  });
  eq(parseWorkbench(dirty), EMPTY_WORKBENCH, "mismatched key, missing fields and null are dropped");
});

test("a live view opens as a tab and is never persisted", () => {
  let s = r(EMPTY_WORKBENCH, { type: "open", artifactId: "a" });
  s = r(s, { type: "openLive", jobId: "job-1" });
  eq([keys(s), s.active], [["a:a", "l:job-1"], "l:job-1"], "opened beside the file");
  s = r(s, { type: "openLive", jobId: "job-1" });
  eq(keys(s).length, 2, "reopening only activates");
  eq(parseWorkbench(serializeWorkbench(s)), { tabs: [{ key: "a:a", artifactId: "a", pinned: false }], active: "a:a" }, "the live tab ends with the turn");
});
