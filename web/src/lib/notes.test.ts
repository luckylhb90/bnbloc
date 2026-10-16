import {
  bodyLimit,
  byteLength,
  canEditNote,
  createRequest,
  emptyNoteForm,
  formOfNote,
  groupNotes,
  noteDates,
  noteDraftFromMessage,
  notePreview,
  replaceableNotes,
  sourceLabel,
  titleProblem,
  truncateBytes,
  updateRequest,
  type RoomNote,
} from "./notes.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function note(id: string, over: Partial<RoomNote> = {}): RoomNote {
  return {
    noteId: id,
    type: "knowledge",
    title: `Note ${id}`,
    body: "",
    status: "active",
    author: "maya",
    revision: 1,
    createdAtMs: 1000,
    updatedAtMs: 1000,
    ...over,
  };
}

// The panel: brief, pinned, knowledge, lessons, memory, each newest first;
// superseded and archived notes are history.
{
  const g = groupNotes([
    note("mem-old", { type: "memory", updatedAtMs: 10 }),
    note("know-old", { updatedAtMs: 20 }),
    note("brief", { type: "brief", updatedAtMs: 5 }),
    note("lesson", { type: "lesson", updatedAtMs: 30 }),
    note("know-new", { updatedAtMs: 40 }),
    note("mem-new", { type: "memory", updatedAtMs: 50 }),
    note("pinned-mem", { type: "memory", pinned: true, updatedAtMs: 1 }),
    note("pinned-know", { pinned: true, updatedAtMs: 2 }),
    note("gone", { status: "superseded", updatedAtMs: 60 }),
    note("shelved", { status: "archived", pinned: true, updatedAtMs: 70 }),
    note("old-brief", { type: "brief", status: "superseded", updatedAtMs: 3 }),
  ]);
  eq(
    g.sections.map((s) => [s.key, s.notes.map((n) => n.noteId)]),
    [
      ["brief", ["brief"]],
      ["pinned", ["pinned-know", "pinned-mem"]],
      ["knowledge", ["know-new", "know-old"]],
      ["lesson", ["lesson"]],
      ["memory", ["mem-new", "mem-old"]],
    ],
    "sections",
  );
  eq(g.history.map((n) => n.noteId), ["shelved", "gone", "old-brief"], "history, newest first");
  eq(g.sections.map((s) => s.label), ["Project brief", "Pinned", "Knowledge", "Lessons", "Memory"], "section labels");
  eq(groupNotes([]), { sections: [], history: [] }, "an empty room has no groups");
  eq(groupNotes([note("only", { type: "lesson" })]).sections.map((s) => s.key), ["lesson"], "empty groups are left out");
}

// A tie in time keeps the order stable.
eq(groupNotes([note("b"), note("a")]).sections[0].notes.map((n) => n.noteId), ["a", "b"], "stable order on equal times");

// The notes a new note may replace.
{
  const list = [
    note("a", { updatedAtMs: 1 }),
    note("b", { updatedAtMs: 2 }),
    note("c", { type: "lesson" }),
    note("d", { status: "archived" }),
    note("e", { type: "brief" }),
  ];
  eq(replaceableNotes(list, "knowledge").map((n) => n.noteId), ["b", "a"], "active notes of the same type, newest first");
  eq(replaceableNotes(list, "brief"), [], "a new brief replaces the old one on its own");
  eq(replaceableNotes(list, "memory"), [], "none of that type");
}

eq(
  [canEditNote(note("a")), canEditNote(note("b", { status: "archived" })), canEditNote(note("c", { status: "superseded" }))],
  [true, true, false],
  "a superseded note is history",
);

// Limits are by UTF-8 bytes.
eq([bodyLimit("knowledge"), bodyLimit("memory"), bodyLimit("lesson"), bodyLimit("brief")], [4096, 4096, 4096, 8192], "body limits");
eq(byteLength("é"), 2, "two bytes");
eq(byteLength("日本"), 6, "three bytes each");
eq(truncateBytes("日本語", 7), "日本", "cut on a character boundary");
eq(truncateBytes("abc", 10), "abc", "short text is kept");
eq(truncateBytes("ab 😀😀", 6), "ab", "an emoji is never split, the end is trimmed");

// A title: 1 to 120 characters, one line.
eq(titleProblem("  "), "A note needs a title", "blank title");
eq(titleProblem("a\nb"), "A title is one line", "multi-line title");
eq(titleProblem("a\tb"), "A title is one line", "a tab is a control character");
eq(titleProblem("x".repeat(120)), null, "120 characters fit");
eq(titleProblem("x".repeat(121)), "A title is at most 120 characters", "121 characters do not");
eq(titleProblem("é".repeat(120)), null, "characters, not bytes");
eq(titleProblem("😀".repeat(120)), null, "an emoji is one character");

// The create form maps to a request.
{
  const f = { ...emptyNoteForm("knowledge"), title: "  Use Postgres ", body: "Because.", pinned: true, supersedes: "old1" };
  const source = [{ kind: "message" as const, ref: "12" }];
  eq(
    createRequest(f, source).request,
    { type: "knowledge", title: "Use Postgres", body: "Because.", pinned: true, supersedes: "old1", sources: source },
    "create request",
  );
  eq(createRequest({ ...f, pinned: false, supersedes: "" }).request, { type: "knowledge", title: "Use Postgres", body: "Because." }, "unset options are omitted");
  eq(createRequest({ ...f, type: "brief" }).request, { type: "brief", title: "Use Postgres", body: "Because." }, "a brief is never pinned and never names a replacement");
  assert(createRequest({ ...f, title: "" }).error === "A note needs a title", "an empty title is refused");
  assert(createRequest({ ...f, body: "x".repeat(4097) }).error !== undefined, "a body over 4 KiB is refused");
  assert(createRequest({ ...f, body: "é".repeat(2049) }).error !== undefined, "the body limit counts bytes");
  assert(createRequest({ ...f, type: "brief", body: "x".repeat(8192) }).error === undefined, "a brief may be 8 KiB");
  assert(createRequest({ ...f, type: "brief", body: "x".repeat(8193) }).error !== undefined, "but not more");
}

// An edit sends only what changed, with the revision it read.
{
  const n = note("n1", { title: "T", body: "B", revision: 4 });
  const f = formOfNote(n);
  eq(updateRequest(n, f), { unchanged: true }, "nothing changed");
  eq(updateRequest(n, { ...f, title: " T " }), { unchanged: true }, "a title is compared trimmed");
  eq(updateRequest(n, { ...f, body: "B2" }).request, { expectedRevision: 4, body: "B2" }, "body only");
  eq(updateRequest(n, { ...f, title: "T2", pinned: true }).request, { expectedRevision: 4, title: "T2", pinned: true }, "title and pin");
  assert(updateRequest(n, { ...f, title: "" }).error === "A note needs a title", "an emptied title is refused");
  const brief = note("b1", { type: "brief", revision: 2 });
  eq(updateRequest(brief, { ...formOfNote(brief), pinned: true }), { unchanged: true }, "a brief's pin is not a change");
  assert(updateRequest(brief, { ...formOfNote(brief), body: "x".repeat(5000) }).request !== undefined, "a brief's body limit is its own");
}

// One-line preview and dates.
eq(notePreview("First line\n\n  second   line"), "First line second line", "whitespace collapses");
eq(notePreview("x".repeat(200), 10), "xxxxxxxxx…", "cut with an ellipsis");
eq(notePreview(""), "", "no body");
{
  const now = 100 * 3600 * 1000;
  const d = noteDates(note("a", { updatedAtMs: now - 3 * 3600 * 1000, verifiedAtMs: now - 2 * 60 * 1000 }), now);
  eq([d.updated, d.verified], ["updated 3 hours ago", "verified 2 minutes ago"], "relative labels");
  assert(d.verifiedTitle !== "", "an absolute time");
  const u = noteDates(note("b", { updatedAtMs: now }), now);
  eq([u.updated, u.verified], ["updated just now", "not verified"], "not verified");
}

eq(sourceLabel({ kind: "message", ref: "123" }), "message #123", "message source");
eq(sourceLabel({ kind: "artifact", ref: "art1", version: 2 }), "deliverable art1, version 2", "artifact source");

// "Save to memory" from a message.
{
  const d = noteDraftFromMessage("\n  ## Decision: ship on Friday  \nBecause QA is done.\n", 42);
  eq(d.title, "Decision: ship on Friday", "title is the first line");
  eq(d.body, "## Decision: ship on Friday  \nBecause QA is done.", "body is the trimmed text");
  eq(d.sources, [{ kind: "message", ref: "42" }], "the message is the source");
  const long = noteDraftFromMessage("é".repeat(200), 1);
  eq(Array.from(long.title).length, 120, "a long title is cut to 120 characters");
  assert(long.title.endsWith("…"), "and ends with an ellipsis");
  assert(titleProblem(long.title) === null, "the proposed title is valid");
  const big = noteDraftFromMessage("日".repeat(2000), 1);
  assert(byteLength(big.body) <= 4096 && byteLength(big.body) > 4090, "the body is cut to the byte limit");
  eq(Array.from(big.body).length, 1365, "on a character boundary");
  eq(noteDraftFromMessage("a\tb", 1).title, "a b", "control characters become a space");
  assert(createRequest({ ...emptyNoteForm("memory"), title: d.title, body: d.body }).request !== undefined, "a draft makes a valid note");
}
