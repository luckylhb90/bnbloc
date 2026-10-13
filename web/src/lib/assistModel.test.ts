import { assistTask, createSSEParser, DESCRIBE_WORK, ENHANCE_PROMPT, workDraftPatch, type SSEEvent } from "./assistModel.ts";
import { publishWorkForm } from "./workGallery.ts";
import type { AssistStatus } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function parse(chunks: readonly string[]): SSEEvent[] {
  const out: SSEEvent[] = [];
  const p = createSSEParser((e) => out.push(e));
  for (const c of chunks) p.push(c);
  p.end();
  return out;
}

// The SSE parser: events across chunk boundaries, in any line ending.
{
  const stream = 'event: delta\ndata: {"text":"Hel"}\n\nevent: delta\ndata: {"text":"lo"}\n\nevent: done\ndata: {"usage":{}}\n\n';
  const want = [
    { event: "delta", data: '{"text":"Hel"}' },
    { event: "delta", data: '{"text":"lo"}' },
    { event: "done", data: '{"usage":{}}' },
  ];
  eq(parse([stream]), want, "one chunk");
  eq(parse([...stream]), want, "one character at a time");
  eq(parse([stream.replace(/\n/g, "\r\n")]), want, "CRLF");
  eq(parse([...stream.replace(/\n/g, "\r\n")]), want, "CRLF split between CR and LF");
  eq(parse([stream.replace(/\n/g, "\r")]), want, "CR alone");
}
eq(parse(["data: one\ndata: two\n\n"]), [{ event: "message", data: "one\ntwo" }], "multi-line data joins with LF, an unnamed event is a message");
eq(parse([": keep-alive\n\nevent: delta\ndata:x\n\n"]), [{ event: "delta", data: "x" }], "comments are skipped, no space after the colon");
eq(parse(['event: delta\ndata: {"text":"cut']), [], "a cut stream dispatches nothing incomplete");
eq(parse(["event: delta\n\ndata: y\n\n"]), [{ event: "message", data: "y" }], "an event with no data resets its type");

// A task in the status: hidden when unavailable or missing.
{
  const task = { name: ENHANCE_PROMPT, componentId: "cc_builtin_enhance-prompt", output: "text" as const };
  const on: AssistStatus = { available: true, tasks: [task] };
  eq(assistTask(on, ENHANCE_PROMPT), task, "a runnable task");
  eq(assistTask(on, DESCRIBE_WORK), null, "a missing task");
  eq(assistTask({ ...on, available: false }, ENHANCE_PROMPT), null, "assists unavailable");
  eq(assistTask(null, ENHANCE_PROMPT), null, "still loading");
}

// describe-work onto the publish form.
{
  const form = publishWorkForm("poster.png", []);
  eq(
    workDraftPatch(
      { displayName: " Party Poster ", description: "A neon poster.", category: "content", tags: ["Party", "neon", "party", "Bad,Tag!", "海报"] },
      form,
    ),
    { displayName: "Party Poster", name: "party-poster", description: "A neon poster.", category: "content", tags: "party, neon, 海报" },
    "fields fill the form: tags cleaned, deduplicated and held to the tag rule",
  );
  eq(workDraftPatch({ category: "games", tags: "a, b" }, form), { tags: "a, b" }, "an unknown category is dropped; a tag string is split");
  eq(workDraftPatch({ displayName: "派对海报" }, form), { displayName: "派对海报" }, "a display name that gives no name leaves the name");
  eq(
    workDraftPatch({ displayName: "Party Poster" }, { ...form, name: "my-own-name" }),
    { displayName: "Party Poster" },
    "a name the member changed is kept",
  );
  eq(
    (workDraftPatch({ tags: Array.from({ length: 20 }, (_, i) => `t${i}`) }, form).tags ?? "").split(", ").length,
    12,
    "at most 12 tags",
  );
  eq((workDraftPatch({ description: "x".repeat(900) }, form).description ?? "").length, 500, "the description within the form's bound");
  eq(workDraftPatch({ displayName: 3, tags: [1, 2] }, form), {}, "values of the wrong type are ignored");
}
