import assert from "node:assert/strict";
import test from "node:test";
import { applyPatch, capturePreset, presetChanges, presetFromFields, presetName, undoPatch } from "./channelPresets.ts";

const channel = {
  purpose: "Chat",
  rules: "",
  settings: { defaultAgent: { ownerId: "maya", core: "acme", provider: "codex" }, slowModeSeconds: 5 },
};
const desk = {
  purpose: "Ask anything about the product",
  rules: "One question per thread.",
  settings: { agentParticipation: "questions", slowModeSeconds: 5, postingStyle: "threads", defaultAgent: { ownerId: "x", provider: "y" } },
};

test("the preview lists what the preset changes, nothing it leaves", () => {
  const changes = presetChanges(channel, desk);
  assert.deepEqual(
    changes.map((c) => [c.field, c.from, c.to]),
    [
      ["purpose", "Chat", "Ask anything about the product"],
      ["rules", "—", "One question per thread."],
      ["agentParticipation", "—", "questions"],
      ["postingStyle", "—", "threads"],
    ],
  );
});

test("one PATCH applies it over the channel's settings; undo puts them back", () => {
  const patch = applyPatch(channel, desk);
  assert.equal(patch.purpose, desk.purpose);
  assert.deepEqual(patch.settings, {
    defaultAgent: channel.settings.defaultAgent,
    slowModeSeconds: 5,
    agentParticipation: "questions",
    postingStyle: "threads",
  });
  const back = undoPatch(channel, patch);
  assert.deepEqual(back, { purpose: "Chat", rules: "", settings: channel.settings });
  // A preset of a purpose alone touches neither rules nor settings.
  assert.deepEqual(undoPatch(channel, applyPatch(channel, { purpose: "Notes" })), { purpose: "Chat" });
});

test("a channel captured as a preset carries no default agent", () => {
  const p = capturePreset({ purpose: "Help", rules: "Be kind.", settings: { ...channel.settings, noAgents: false, whoPosts: "admins" } });
  assert.deepEqual(p, { purpose: "Help", rules: "Be kind.", settings: { slowModeSeconds: 5, whoPosts: "admins" } });
  assert.equal(presetName("Help desk 2"), "help-desk-2-preset");
  assert.equal(presetName("告警"), "channel-preset");
});

test("an assist's draft is read as a preset, a settings string parsed, a default agent dropped", () => {
  assert.deepEqual(presetFromFields({ purpose: " A help desk ", settings: '{"agentParticipation":"questions","defaultAgent":{"ownerId":"m"}}' }), {
    purpose: "A help desk",
    settings: { agentParticipation: "questions" },
  });
  assert.equal(presetFromFields({ purpose: " ", settings: "not json" }), null);
  assert.equal(presetFromFields(null), null);
});
