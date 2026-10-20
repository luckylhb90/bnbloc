import explainSourceUse from "../../../internal/contentbuiltin/packages/explain-source-use/component.json" with { type: "json" };
import { driftView, EXPLAIN_SOURCE_USE, explainReply, grantState, reviewLine, SCOPE_LABELS, sourceAsk, sourcesProblem, sourceState } from "./roomSources.ts";
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

// Where an ask stands, whose tasks read a grant, and what a reviewed
// room's members read.
eq(sourceState(undefined), "Waiting for your organization admin", "waiting");
eq(sourceState("none-private"), "Granted: all but private rooms", "granted");
eq(grantState({ scope: "all" }), "Granted: all rooms", "a grant as consented");
eq(grantState({ scope: "all", changed: 2 }), "Granted: all rooms · 2 tasks changed since granted", "a grant whose tasks changed");
eq(grantState({ changed: 1 }), "Waiting for your organization admin", "a waiting ask has no changes to show");
eq(reviewLine({ source: "runs", roomName: "Outcome review", grantee: "maya" }), "This organization reviews conversation outcomes in Outcome review", "the transparency line");
eq(Object.keys(SCOPE_LABELS).sort(), [...RoomSourceScopeValues].sort(), "every scope has its words");

// A changed task on the console: its words then and now, the mismatch
// first.
eq(
  driftView({ scheduleId: "s1", before: "Review yesterday.", after: "Rank everyone.", summary: "Ranks members.", mismatch: " It ranks people. ", atMs: 1 }),
  { scheduleId: "s1", change: "changed", before: "Review yesterday.", after: "Rank everyone.", explanation: "It ranks people.", beyond: true },
  "a change beyond the purpose",
);
eq(driftView({ scheduleId: "s2", before: "", after: "Post a digest.", summary: "Posts a digest.", atMs: 1 }).change, "added", "a task added since the grant");
eq(driftView({ scheduleId: "s2", before: "", after: "Post a digest.", summary: "Posts a digest.", atMs: 1 }).explanation, "Posts a digest.", "a change within the purpose");
assert(driftView({ scheduleId: "s3", before: "a", after: "b", atMs: 1 }).explanation.includes("No AI explanation"), "a change without an explanation says so");

// explain-source-use's reply is the package's fields.
eq(explainSourceUse.name, EXPLAIN_SOURCE_USE, "the built-in assist's name");
eq(explainSourceUse.fields.map((f) => f.name), ["summary", "uses", "mismatch"], "the reply's fields are the package's");
eq(explainReply({ summary: " Posts a digest. ", uses: ["Digest", 3, " "], mismatch: " Names people. " }), { summary: "Posts a digest.", uses: ["Digest"], mismatch: "Names people." }, "a reply read");
eq(explainReply({ summary: "", uses: [] }), null, "an empty reply");
