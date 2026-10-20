import explainSourceUse from "../../../internal/contentbuiltin/packages/explain-source-use/component.json" with { type: "json" };
import { EXPLAIN_SOURCE_USE, explainReply, explainSourceInput, reviewLine, SCOPE_LABELS, sourceAsk, sourcesProblem, sourceState, type RoomSourceGrant } from "./roomSources.ts";
import { RoomSourceScopeValues } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// A source is asked for with its purpose, and the admin decides.
assert(sourceAsk({ name: "runs", purpose: "judge outcomes" }).includes("agent conversations to: judge outcomes") && sourceAsk({ name: "runs", purpose: "x" }).includes("admin decides"), "an ask says what, why and who decides");
eq(sourcesProblem([]), null, "no source, nothing to fix");
assert((sourcesProblem([{ name: "runs", purpose: "  " }]) ?? "").includes("purpose"), "a source needs its purpose");
assert(sourcesProblem([{ name: "runs", purpose: "p".repeat(501) }]) !== null, "a purpose within its bound");
eq(sourcesProblem([{ name: "runs", purpose: "Judge outcomes." }]), null, "a source with its purpose");

// Where an ask stands, and what a reviewed room's members read.
eq(sourceState(undefined), "Waiting for your organization admin", "waiting");
eq(sourceState("none-private"), "Granted: all but private rooms", "granted");
eq(reviewLine({ source: "runs", roomName: "Outcome review" }), "This organization reviews conversation outcomes in Outcome review", "the transparency line");
eq(Object.keys(SCOPE_LABELS).sort(), [...RoomSourceScopeValues].sort(), "every scope has its words");

// explain-source-use reads the ask, the purpose and the tasks; its reply
// is the package's fields.
{
  const g: RoomSourceGrant = {
    roomId: "r1",
    roomName: "Review",
    source: "runs",
    purpose: "Judge outcomes.",
    componentId: "cc_1",
    from: "outcome-review@1.0.0",
    askedBy: "maya",
    askedAtMs: 1,
    schedules: [{ text: "Read yesterday's runs and post the misses.", recipient: { kind: "ai" }, recurrence: { kind: "daily", time: "09:00" }, timezone: "UTC" }],
  };
  const input = JSON.parse(explainSourceInput(g)) as { purpose: string; reads: string; scheduledTasks: { task: string; when: string }[] };
  eq([input.purpose, input.reads.includes("agent conversations"), input.scheduledTasks[0].task], ["Judge outcomes.", true, "Read yesterday's runs and post the misses."], "the AI's input");
  assert(input.scheduledTasks[0].when.length > 0, "each task says when it runs");
  eq(explainSourceUse.name, EXPLAIN_SOURCE_USE, "the built-in assist's name");
  eq(explainSourceUse.fields.map((f) => f.name), ["summary", "uses", "mismatch"], "the reply's fields are the package's");
  eq(explainReply({ summary: " Posts a digest. ", uses: ["Digest", 3, " "], mismatch: " Names people. " }), { summary: "Posts a digest.", uses: ["Digest"], mismatch: "Names people." }, "a reply read");
  eq(explainReply({ summary: "", uses: [] }), null, "an empty reply");
}
