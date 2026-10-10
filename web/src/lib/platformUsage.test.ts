import { modelOptionLabel, normalizeUsage, roomLabel, runModelLabel, splitTrialModels, usageQuery, usageSummary, usageTiles, type PlatformAgentsUsage } from "./platformUsage.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function testNormalize(): void {
  const raw = { today: null, month: { runs: 1, members: 1, rooms: 1, tokens: 1, cost: 0 }, topMembers: null, topRooms: null, recent: null };
  const u = normalizeUsage(raw as unknown as PlatformAgentsUsage);
  assert(u.today.runs === 0 && u.topMembers.length === 0 && u.topRooms.length === 0 && u.recent.length === 0, "null lists become empty");
  assert(u.truncated === false, "absent truncated reads false");
}

function testFormatting(): void {
  const totals = { runs: 1200, members: 1, rooms: 3, tokens: 45600, cost: 0 };
  const tiles = usageTiles(totals);
  assert(tiles.map((t) => t.label).join(",") === "Runs,Members,Rooms,Tokens,Cost", "five tiles");
  assert(tiles.find((t) => t.label === "Cost")?.value === "$0.00", "a free model reports $0.00");
  assert(usageSummary({ ...totals, runs: 1 }) === "1 run · 1 member · $0.00", `got ${usageSummary({ ...totals, runs: 1 })}`);
  assert(runModelLabel("") === "default model" && runModelLabel("gpt-5-5") === "GPT-5.5", "model label");
  assert(runModelLabel("x-gpt-5-mini") === "x-gpt-5-mini", "a trial id stays verbatim");
  assert(roomLabel({ roomId: "r1", roomName: "Launch" }) === "Launch" && roomLabel({ roomId: "r1" }) === "r1", "room label");
}

function testQueryCarriesTheLocalOffset(): void {
  const fake = { getTimezoneOffset: () => -480 } as Date;
  assert(usageQuery(fake) === "tzOffsetMin=480", "minutes east of UTC");
}

function testTrialModels(): void {
  assert(modelOptionLabel("x-claude-free") === "x-claude-free — trial channel · free", "trial model is labelled free");
  assert(modelOptionLabel("gpt-5") === "gpt-5", "other models keep their id");
  const g = splitTrialModels(["gpt-5", "x-a", "opus", "x-b"]);
  assert(g.trial.join(",") === "x-a,x-b" && g.other.join(",") === "gpt-5,opus", "trial first, catalog order kept");
}

testNormalize();
testFormatting();
testQueryCarriesTheLocalOffset();
testTrialModels();
