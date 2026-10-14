import { fileRefOf, normalizeRoomEvent, readTurnMetrics, type RoomEvent } from "./roomEvents.ts";
import { agentIdentity } from "./roomAgents.ts";
// Golden fixtures written by internal/wire's golden tests: the exact JSON the
// server sends. A field Go stops sending fails here.
import sessionState from "../../../internal/wire/testdata/agent_session_state.json" with { type: "json" };
import agentMessages from "../../../internal/wire/testdata/agent_messages.json" with { type: "json" };
import historyPage from "../../../internal/wire/testdata/history_page.json" with { type: "json" };
import jobUpdated from "../../../internal/wire/testdata/agent_job_updated.json" with { type: "json" };
import turnUsage from "../../../internal/wire/testdata/turn_usage.json" with { type: "json" };
import jobNotice from "../../../internal/wire/testdata/job_notice.json" with { type: "json" };

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function normalized(ev: RoomEvent): RoomEvent[] {
  const out: RoomEvent[] = [];
  normalizeRoomEvent(structuredClone(ev), (e) => out.push(e));
  return out;
}

const reply = (core: string, notice?: string): RoomEvent =>
  normalized({
    kind: "agent_messages",
    sequence: 7,
    payload: {
      messages: [
        { payload: { role: "assistant", text: "done", ownerUserId: "maya", provider: "codex", core, ...(notice ? { notice } : {}) } },
      ],
    },
  })[0];

function testSessionStateFixtureKeysTheAgent(): void {
  const [ev] = normalized(sessionState as RoomEvent);
  assert(ev.agentPhase === "working", "the fixture is a working state");
  assert(ev.agentLabel === "acme/maya/codex", `label ${ev.agentLabel}`);
  assert(
    ev.agentKey === agentIdentity({ ownerId: "maya", core: "acme", provider: "codex" }),
    `the indicator is keyed by the agent's identity, got ${ev.agentKey}`,
  );
}

function testReplyClearsOnlyItsOwnAgent(): void {
  const [state] = normalized(sessionState as RoomEvent);
  assert(reply("acme").agentKey === state.agentKey, "the agent's reply clears its indicator");
  assert(reply("nimbus").agentKey !== state.agentKey, "the same harness under another core is another agent");
  assert(reply("acme", "progress").agentKey === undefined, "a notice line leaves the indicator alone");
}

// A failed turn's notice carries the agent's own error output apart from
// its sentence, and is no reply (it leaves the working indicator alone).
function testNoticeCarriesTheAgentErrorApart(): void {
  const [ev] = normalized(jobNotice as RoomEvent);
  assert(ev.notice === "turn-failed", `notice ${ev.notice}`);
  assert(ev.text === "The codex agent reported an error and stopped.", `text ${ev.text}`);
  assert(ev.noticeDetail === "Missing environment variable: `EXAMPLE_API_KEY`", `detail ${ev.noticeDetail}`);
  assert(ev.agentKey === undefined, "a notice is no reply");
  assert(reply("acme").noticeDetail === undefined, "a reply has no notice detail");
}

// A reply whose core reported only part of its metrics is billed by the
// server, so the feed shows its usage too (web M19).
function testPartialMetricsShowUsage(): void {
  const [ev] = normalized(agentMessages as RoomEvent);
  assert(ev.kind === "agent_messages" && ev.text === "Done.", "the fixture is one reply");
  assert(ev.metrics !== undefined && ev.metrics.inputTokens === 1200 && ev.metrics.outputTokens === 340, `metrics ${JSON.stringify(ev.metrics)}`);
  assert(ev.metrics?.costUsd === 0.0042 && ev.metrics.cacheReadTokens === 0, "optional keys default to 0");
  assert(ev.jobId === "job-1" && ev.sourceSeq === 11, "the job link rides the reply");
  assert(ev.agentKey === agentIdentity({ ownerId: "maya", core: "acme", provider: "codex" }), "keyed by its agent");
}

// The web reads a metrics map exactly as wire.TurnUsageFromMetrics does.
function testTurnUsageParity(): void {
  for (const c of turnUsage as { name: string; metrics: Record<string, unknown>; usage: { model: string; reported: boolean; inputTokens: number; outputTokens: number; costUsd: number | null } }[]) {
    const m = readTurnMetrics(c.metrics);
    if (Object.keys(c.metrics).length === 0) {
      assert(m === undefined, `${c.name}: an empty map is no usage`);
      continue;
    }
    assert(m !== undefined, `${c.name}: read`);
    assert((m?.model ?? "") === c.usage.model, `${c.name}: model ${m?.model}`);
    assert(m?.reported === c.usage.reported, `${c.name}: reported`);
    assert(m?.inputTokens === c.usage.inputTokens && m?.outputTokens === c.usage.outputTokens, `${c.name}: tokens ${m?.inputTokens}/${m?.outputTokens}`);
    assert((m?.costUsd ?? null) === c.usage.costUsd, `${c.name}: cost ${m?.costUsd}`);
  }
}

// A history row edited since it was posted keeps its "(edited)" stamp: the
// stamp rides the row (wire.FeedMessage), never the payload.
function testHistoryRowKeepsEditStamp(): void {
  const [row] = (historyPage as { messages: RoomEvent[] }).messages;
  const [ev] = normalized(row);
  assert(ev.kind === "chat_message" && ev.text === "Please fix the build" && ev.author === "maya", "the row is maya's chat");
  assert(ev.editedAtMs === 1760000005000, `edited ${ev.editedAtMs}`);
  assert(ev.clientMsgId === "c-1" && ev.channelId === "chan-1", "id and channel survive");
}

// The job events carry the job projection (wire.AgentJob): no tenant, lease
// or raw diagnostics, the failure text named.
function testJobEventIsTheProjection(): void {
  const job = (jobUpdated as { payload: Record<string, unknown> }).payload;
  for (const k of ["tenantId", "leaseBridgeId", "leaseExpiresAtMs", "diagnostics"]) assert(!(k in job), `job event carries ${k}`);
  assert(job.error === "the core exited" && Array.isArray(job.contentMissing), "the member-facing diagnostics are named fields");
}

// A request from a file's Ask AI panel names the file, its version and the
// part pointed at (the change card checks the edit against it).
function testFileRefKeepsTheSelectionLabel(): void {
  const ref = fileRefOf({ artifactId: "art_1", version: 2, name: "deck.pptx", selection: { label: "Slide 3", detail: "Q3 revenue", kind: "slide", keys: ["slide-258"] } });
  assert(
    JSON.stringify(ref) === JSON.stringify({ artifactId: "art_1", version: 2, name: "deck.pptx", selection: { label: "Slide 3", detail: "Q3 revenue", kind: "slide", keys: ["slide-258"] } }),
    `ref ${JSON.stringify(ref)}`,
  );
  assert(fileRefOf({ artifactId: "art_1", version: 2, selection: { label: "" } })?.selection === undefined, "an empty label is no selection");
  assert(fileRefOf({ artifactId: "art_1", version: 0 }) === undefined, "version 0 is no file");
}

testSessionStateFixtureKeysTheAgent();
testFileRefKeepsTheSelectionLabel();
testReplyClearsOnlyItsOwnAgent();
testNoticeCarriesTheAgentErrorApart();
testPartialMetricsShowUsage();
testTurnUsageParity();
testHistoryRowKeepsEditStamp();
testJobEventIsTheProjection();
