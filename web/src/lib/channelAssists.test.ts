import assert from "node:assert/strict";
import test from "node:test";
import { channelListing, findAnswerOf, findAsk, purposeAsk, purposeOf, rulesAsk, rulesDraftOf } from "./channelAssists.ts";
import { AssistContracts, AssistTaskDraftChannelPurpose, AssistTaskDraftRoomRules, AssistTaskFindChannel, MaxAssistText, MaxChannelPurpose } from "./wire.gen.ts";

const chans = [
  { channelId: "ch_rel", name: "releases", purpose: "Ship notes", rules: "Admins post." },
  { channelId: "ch_help", name: "help", purpose: "Ask anything", rules: "" },
];

test("the fields the web reads are the contracts' fields", () => {
  const fields = (task: string) => AssistContracts.find((c) => c.task === task)?.fields?.map((f) => f.name) ?? [];
  assert.deepEqual(fields(AssistTaskDraftChannelPurpose), ["purpose"]);
  assert.deepEqual(fields(AssistTaskDraftRoomRules), ["rules", "welcome"]);
  assert.deepEqual(fields(AssistTaskFindChannel), ["answer", "channels"]);
  const items = AssistContracts.find((c) => c.task === AssistTaskFindChannel)?.fields?.find((f) => f.name === "channels")?.item?.map((f) => f.name);
  assert.deepEqual(items, ["channelId", "why"]);
});

test("an answer names only the channels the web listed, by id", () => {
  const got = findAnswerOf(
    { answer: " Ask in #help. ", channels: [{ channelId: "ch_help", why: "questions" }, { channelId: "ch_gone", why: "x" }, { channelId: "ch_help", why: "again" }, "help", { channelId: "ch_rel" }] },
    chans,
  );
  assert.deepEqual(got, { answer: "Ask in #help.", channels: [{ channelId: "ch_help", why: "questions" }, { channelId: "ch_rel", why: "" }] });
  assert.deepEqual(findAnswerOf({}, chans), { answer: "", channels: [] }, "nothing is guessed");
});

test("the texts the assists are asked with, within bounds", () => {
  assert.match(purposeAsk("releases", ""), /#releases\nCurrent purpose: \(none\)/);
  assert.equal(purposeOf({ purpose: "Ship\nnotes " }), "Ship notes");
  assert.ok(purposeOf({ purpose: "x".repeat(400) }).length <= MaxChannelPurpose);
  assert.match(findAsk("where do bugs go?", chans), /Question: where do bugs go\?\nChannels:\n- ch_rel #releases: Ship notes \(rules: Admins post\.\)\n- ch_help #help: Ask anything/);
  assert.match(rulesAsk({ name: "Team", description: "", rules: "", welcome: "" }, chans), /Description: \(none\)[\s\S]*- ch_help/);
  assert.deepEqual(rulesDraftOf({ rules: " Be kind ", welcome: "Hi" }), { rules: "Be kind", welcome: "Hi" });
  const many = Array.from({ length: 2000 }, (_, i) => ({ channelId: `ch_${i}`, name: `c${i}`, purpose: "p".repeat(40), rules: "" }));
  const listing = channelListing(many);
  assert.ok(listing.length <= MaxAssistText, "a long room stays within the text bound");
  assert.match(listing, /more channels left out\)$/, "and says what it left out");
});
