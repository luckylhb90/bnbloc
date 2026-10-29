import { modelLabel, prettyModel } from "./modelLabel.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (a !== b) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

for (const [id, want] of [
  ["claude-opus-5-5[1m]", "Claude Opus 5.5 [1m]"],
  ["x-claude-opus-5-5", "X Claude Opus 5.5"],
  ["bedrock-claude-4-7-opus", "Bedrock Claude 4.7 Opus"],
  ["gpt-5.5", "GPT-5.5"],
  ["gpt-5.6-luna[1m]", "GPT-5.6 Luna [1m]"],
  ["claude-sonnet-4", "Claude Sonnet 4"],
  ["qwen3.8-max", "Qwen3.8 Max"],
  ["DeepSeek-V4.1-Flash[1m]", "DeepSeek V4.1 Flash [1m]"],
  ["", ""],
]) {
  eq(prettyModel(id), want, id);
}

// The agent's own name wins; a model with none (or a blank one) is rendered from its id.
eq(modelLabel("acme/x-gpt-6-luna", { "acme/x-gpt-6-luna": "GPT-6 Luna" }), "GPT-6 Luna", "a given name is shown as given");
eq(modelLabel("acme/glm-5.3", { "acme/x-gpt-6-luna": "GPT-6 Luna" }), prettyModel("acme/glm-5.3"), "no name for this model: the id's rendering");
eq(modelLabel("a-b", { "a-b": "   " }), prettyModel("a-b"), "a blank name is no name");
eq(modelLabel("a-b"), prettyModel("a-b"), "no names at all: today's behaviour");
