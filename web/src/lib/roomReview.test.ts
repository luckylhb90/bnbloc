import { AssistContracts, AssistTaskDescribeRoom } from "./wire.gen.ts";
import { partKey, roomSaveFromDraft, type RoomComponentDraft, type RoomPackageSchedule, type RoomSaveForm } from "./roomComponents.ts";
import { FINDING_KINDS, findingId, findingWhere, fixFinding, openFindings, reviewKey, roomFindings, roomPackageTexts, roomReviewInput } from "./roomReview.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const task = (text: string, channel?: string): RoomPackageSchedule => ({ text, ...(channel ? { channel } : {}), recipient: { kind: "ai" }, recurrence: { kind: "daily", time: "09:00" }, timezone: "UTC" });
const draft: RoomComponentDraft = {
  roomId: "r1",
  name: "release-review",
  displayName: "Release review",
  description: "Reviews releases with maya.",
  welcome: "",
  quickPrompts: [],
  channels: ["reports", "acme-ops"],
  schedules: [task("Post the release notes to https://wiki.acme.internal/notes.", "reports"), task("Ask jack for the numbers.", "acme-ops")],
  skills: [],
  prompts: [],
  apps: [],
  madeApps: [],
  files: [],
  tools: [],
  sources: [],
  left: [],
};
const base: RoomSaveForm = {
  ...roomSaveFromDraft(draft),
  welcome: "Use the token sk-123 to read the dashboard.",
  quickPrompts: [{ title: "Numbers", prompt: "Call +1 555 0100 for the numbers." }],
  sources: [{ name: "runs", purpose: "Judge outcomes." }],
};

// The check reads every text the package would carry, by key; a part left
// out is not carried.
{
  const texts = roomPackageTexts(base);
  eq(
    Object.keys(texts),
    ["displayName", "description", "welcome", "quickPrompt:0", "schedule:0", "schedule:1", "channel:reports", "channel:acme-ops", "source:runs"],
    "the package's texts",
  );
  eq(texts["quickPrompt:0"], "Numbers: Call +1 555 0100 for the numbers.", "a quick prompt's text");
  const out = { ...base, off: [partKey("schedule", 1), partKey("channel", "acme-ops")] };
  assert(!("schedule:1" in roomPackageTexts(out)) && !("channel:acme-ops" in roomPackageTexts(out)), "a part left out is not checked");
  const input = JSON.parse(roomReviewInput(base)) as { setup: unknown; current: unknown; package: Record<string, string> };
  assert(!!input.setup && !!input.current && input.package.welcome === base.welcome, "describe-room reads the setup, the values and the package");
  assert(reviewKey(base) !== reviewKey({ ...base, welcome: "Welcome." }), "a change is checked again");
}

// A reply's findings: only in a text the package carries, the kind from
// the closed list, once each, at most ten.
{
  const fields = {
    findings: [
      { field: "welcome", quote: "sk-123", why: "secret: an API token." },
      { field: "welcome", quote: "sk-123", why: "secret: twice." },
      { field: "schedule:0", quote: "https://wiki.acme.internal/notes", why: "Internal-link: only the organization reaches it." },
      { field: "description", quote: "maya", why: "a person's name" },
      { field: "members", quote: "jack", why: "personal-name: not a package text." },
      "not an item",
    ],
  };
  const found = roomFindings(fields, base);
  eq(
    found.map((x) => [x.field, x.kind, x.why]),
    [
      ["welcome", "secret", "an API token."],
      ["schedule:0", "internal-link", "only the organization reaches it."],
      ["description", null, "a person's name"],
    ],
    "the findings read",
  );
  eq(roomFindings({ findings: Array.from({ length: 14 }, (_, i) => ({ field: "welcome", quote: `q${i}`, why: "secret: x" })) }, base).length, 10, "at most ten");
  eq(roomFindings({}, base), [], "no findings");

  // A finding holds while its words are in its text and it is not ignored.
  eq(openFindings(found, base, []).length, 3, "all open");
  eq(openFindings(found, base, [findingId(found[2])]).length, 2, "an ignored finding is closed");
  eq(openFindings(found, { ...base, welcome: "Read the dashboard." }, []).map((x) => x.field), ["schedule:0", "description"], "a fixed text closes its finding");
  eq(openFindings(found, { ...base, off: [partKey("schedule", 0)] }, []).map((x) => x.field), ["welcome", "description"], "a part left out closes its finding");

  // Fix: a field the dialog edits is focused; a part it does not edit is
  // left out (a channel with its tasks), a quick prompt removed.
  eq(fixFinding(base, found[0]), { edit: "welcome" }, "a welcome is edited");
  eq(fixFinding(base, { field: "source:runs", quote: "x", kind: "private-data", why: "" }), { edit: "source:runs" }, "a purpose is edited");
  const leftOut = fixFinding(base, found[1]);
  assert("form" in leftOut && leftOut.form.off.includes(partKey("schedule", 0)), "a task is left out");
  const channel = fixFinding(base, { field: "channel:acme-ops", quote: "acme-ops", kind: "internal-link", why: "" });
  assert("form" in channel && channel.form.off.includes(partKey("channel", "acme-ops")) && channel.form.off.includes(partKey("schedule", 1)), "a channel goes with its tasks");
  const prompt = fixFinding(base, { field: "quickPrompt:0", quote: "+1 555 0100", kind: "private-data", why: "" });
  assert("form" in prompt && prompt.form.quickPrompts.length === 0, "a quick prompt is removed");
  eq(findingWhere(base, found[1]), "Scheduled task 1", "where a finding is");
  eq(findingWhere(base, { field: "quickPrompt:0", quote: "", kind: null, why: "" }), 'Quick prompt "Numbers"', "a quick prompt by its title");
}

// The room's instructions are a text the package carries: checked, and a
// finding in them is fixed by editing them.
{
  const withInstructions = { ...base, instructions: "Ask maya@example.com when unsure." };
  eq(roomPackageTexts(withInstructions).instructions, "Ask maya@example.com when unsure.", "the instructions are checked");
  assert(!("instructions" in roomPackageTexts(base)), "no instructions, no text");
  const [x] = roomFindings({ findings: [{ field: "instructions", quote: "maya@example.com", why: "private-data: an email." }] }, withInstructions);
  eq([x.kind, fixFinding(withInstructions, x), findingWhere(withInstructions, x)], ["private-data", { edit: "instructions" }, "Instructions"], "a finding in them");
}

// The kinds are describe-room's output contract's (wire.AssistContracts),
// in its order: a finding's why starts with one.
{
  const findings = AssistContracts.find((c) => c.task === AssistTaskDescribeRoom)?.fields?.find((f) => f.name === "findings");
  eq(findings?.item?.map((i) => i.name), ["field", "quote", "why"], "a finding's fields are the contract's");
  eq(findings?.item?.find((i) => i.name === "why")?.values, [...FINDING_KINDS], "the kinds are the contract's");
}
