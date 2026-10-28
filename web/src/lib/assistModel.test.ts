import { DRAFT_EXPERT, DRAFT_PROMPT_PACK, DRAFT_SCHEDULE, DRAFT_SKILL_PACK, PLAN_ROOM } from "./assistDrafts.ts";
import { assistRevision, assistTask, createSSEParser, DESCRIBE_ROOM, DESCRIBE_WORK, ENHANCE_PROMPT, refineJSON, workDraftPatch, type SSEEvent } from "./assistModel.ts";
import { DRAFT_CHANNEL_PURPOSE, DRAFT_ROOM_RULES, FIND_CHANNEL } from "./channelAssists.ts";
import { FIND_A_WAY } from "./deadEnd.ts";
import { EXPLAIN_SOURCE_USE } from "./roomSources.ts";
import { FILL_SETTINGS } from "./settingsFill.ts";
import { SUGGEST_SKILL_DIRS } from "./skillDirs.ts";
import { REVIEW_SKILL } from "./skillReview.ts";
import { publishWorkForm } from "./workGallery.ts";
import { AssistContracts, AssistTaskNameValues, MaxAssistProblemRunes, MaxAssistProblems, MaxAssistRounds, type AssistJSONReply, type AssistRequest, type AssistStatus } from "./wire.gen.ts";

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
  const task = { name: ENHANCE_PROMPT, componentId: "cc_enhance", output: "text" as const };
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

// Every task the web runs by name has an output contract, and every
// contract is a task the web (or the gateway) runs: one list, wire.AssistContracts.
{
  const web = [ENHANCE_PROMPT, DESCRIBE_WORK, DESCRIBE_ROOM, DRAFT_EXPERT, DRAFT_SKILL_PACK, DRAFT_PROMPT_PACK, PLAN_ROOM, DRAFT_SCHEDULE,
    EXPLAIN_SOURCE_USE, REVIEW_SKILL, FILL_SETTINGS, SUGGEST_SKILL_DIRS, FIND_A_WAY, DRAFT_CHANNEL_PURPOSE, DRAFT_ROOM_RULES, FIND_CHANNEL];
  eq([...web].sort(), [...AssistTaskNameValues].sort(), "the web's tasks are the contracts' names");
  eq(AssistContracts.map((c) => c.task).sort(), [...AssistTaskNameValues].sort(), "one contract per task");
}

// Checking its work: a failed check asks again with the previous answer
// and the problems, MaxAssistRounds in all; a passing answer stops; a
// failed revision keeps the last answer with its problems.
{
  const usage = { model: "m", promptTokens: 1, completionTokens: 1, latencyMs: 1 };
  const answers = (list: readonly Record<string, unknown>[]) => {
    const asked: AssistRequest[] = [];
    let i = 0;
    const run = async (body: AssistRequest): Promise<AssistJSONReply> => {
      asked.push(body);
      const f = list[Math.min(i++, list.length - 1)];
      if (!f) throw new Error("down");
      return { fields: f, usage };
    };
    return { asked, run };
  };
  const check = (f: Record<string, unknown>) => (f.name ? [] : ["Give it a name"]);
  void (async () => {
    const a = answers([{}, { name: "reviewer" }]);
    const r = await refineJSON(a.run, { text: "a reviewer" }, check);
    eq(r.problems, [], "the second answer passes");
    eq(a.asked.map((b) => b.revise ?? null), [null, { answer: "{}", problems: ["Give it a name"] }], "the revision carries the answer and its problems");
    eq(a.asked[1]?.text, "a reviewer", "a revision keeps the request");

    const b = answers([{}]);
    const s = await refineJSON(b.run, { text: "x" }, check);
    eq([b.asked.length, s.problems], [MaxAssistRounds, ["Give it a name"]], "at most MaxAssistRounds, then the last problems");

    const c = answers([{ draft: 1 }, null as unknown as Record<string, unknown>]);
    const t = await refineJSON(c.run, { text: "x" }, check);
    eq([t.reply.fields, t.problems], [{ draft: 1 }, ["Give it a name"]], "a failed revision keeps the last answer and its problems");
  })(); // a rejection here is unhandled: Node prints it and exits non-zero
  eq(assistRevision({}, ["  a \n b  ", "", ...Array.from({ length: 12 }, () => "x".repeat(900))])?.problems.length, MaxAssistProblems, "problems bounded");
  eq([...(assistRevision({}, ["x".repeat(900)])?.problems[0] ?? "")].length, MaxAssistProblemRunes, "each within its bound");
  eq(assistRevision({}, ["  a \n b  "])?.problems, ["a b"], "one line");
  eq(assistRevision({}, ["", " "]), null, "no problem, no revision");
}
