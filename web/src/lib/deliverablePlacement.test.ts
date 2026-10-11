import { placeDeliverables, type PlacementRow } from "./deliverablePlacement.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const at = (m: Map<number, { seq: number; artifactIds: readonly string[] }[]>) =>
  [...m.entries()].map(([row, list]) => `${row}:${list.map((d) => `${d.seq}=${d.artifactIds.join("+")}`).join(",")}`).join(" ");

const ask = (seq: number): PlacementRow => ({ seq, author: "maya" });
const reply = (seq: number, label = "maya/claude-code"): PlacementRow => ({ seq, author: "agent", agentLabel: label });
const job = { ownerUserId: "maya", provider: "claude-code" };

// Regression: cards rendered under the request, above the agent's reply.
{
  const feed = [ask(10), reply(11), reply(11.001)];
  const placed = placeDeliverables(feed, { 10: ["a", "b"] }, { 10: [job] });
  assert(at(placed) === "2:10=a+b", `under the turn's last reply, got ${at(placed)}`);
}

// No reply yet (live turn): the cards stay under the request.
assert(at(placeDeliverables([ask(10)], { 10: ["a"] }, { 10: [job] })) === "0:10=a", "no reply yet");

// Another agent's reply and an unrelated human message in between: the cards
// follow the turn's own agent, and stop at that agent's next request.
{
  const feed = [ask(10), reply(11, "jack/codex"), ask(12), reply(13), ask(14), reply(15)];
  const placed = placeDeliverables(feed, { 10: ["a"] }, { 10: [job], 14: [job] });
  assert(at(placed) === "3:10=a", `own agent's reply before its next turn, got ${at(placed)}`);
}

// Without job data: the last of the replies right after the request.
{
  const feed = [ask(10), reply(11), reply(12), ask(13), reply(14)];
  assert(at(placeDeliverables(feed, { 10: ["a"] }, {})) === "2:10=a", "consecutive replies only");
}

// Agent reply rows never carry their own request cards.
assert(at(placeDeliverables([reply(10)], { 10: ["a"] }, {})) === "", "replies are not requests");
