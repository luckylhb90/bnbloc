import type { MessageTask, SearchResult } from "./teamModels.ts";
import {
  applyTaskUpdate,
  composerDrafts,
  groupResults,
  highlightParts,
  mergeDraft,
  messageJumps,
  parseTags,
} from "./team.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(parseTags(" Writing, ops  #ops,writing "), ["writing", "ops"], "tags normalized");
eq(parseTags(""), [], "no tags");

eq(
  highlightParts("Deploy the deploy", "DEPLOY"),
  [
    { text: "Deploy", match: true },
    { text: " the ", match: false },
    { text: "deploy", match: true },
  ],
  "highlight all matches",
);
eq(highlightParts("abc", ""), [{ text: "abc", match: false }], "empty query");

eq(mergeDraft("", "prompt"), "prompt", "empty draft replaced");
eq(mergeDraft("hello  \n", "prompt"), "hello\n\nprompt", "draft kept, prompt appended");

const t = (seq: number, status: "open" | "done" = "open"): MessageTask => ({
  roomId: "r",
  seq,
  status,
  title: `t${seq}`,
  createdBy: "a",
  createdAtMs: 1,
});
eq(applyTaskUpdate([t(5)], { seq: 2, task: t(2) }).map((x) => x.seq), [2, 5], "insert keeps order");
eq(applyTaskUpdate([t(2), t(5)], { seq: 5, task: t(5, "done") })[1].status, "done", "update in place");
eq(applyTaskUpdate([t(2), t(5)], { seq: 2, removed: true }).map((x) => x.seq), [5], "remove");

const r = (kind: SearchResult["kind"], snippet: string): SearchResult => ({ kind, snippet, atMs: 0 });
eq(
  groupResults([r("prompt", "p"), r("artifact", "a"), r("message", "m1"), r("message", "m2")]).map((g) => [g.kind, g.items.length]),
  [
    ["message", 2],
    ["artifact", 1],
    ["prompt", 1],
  ],
  "grouped by kind, messages first",
);

// Hand-offs: a queued value is taken exactly once, and listeners hear about it.
let heard = "";
const off = composerDrafts.subscribe((room) => (heard = room));
composerDrafts.queue("room-1", "draft");
eq(heard, "room-1", "listener notified");
eq(composerDrafts.take("room-1"), "draft", "draft taken");
eq(composerDrafts.take("room-1"), undefined, "draft taken once");
off();
messageJumps.queue("room-2", { seq: 7 });
eq(messageJumps.peek("room-2"), { seq: 7 }, "jump peek keeps it");
eq(messageJumps.take("room-2"), { seq: 7 }, "jump taken");

console.log("team helpers: ok");
