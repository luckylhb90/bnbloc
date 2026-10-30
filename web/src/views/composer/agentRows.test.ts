import { agentRows, rankRows, type AgentRow } from "./agentRows.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const t = (core: string, provider: string) => ({ id: `${core}/${provider}`, core, provider });

// An agent also offered through a wrapper lists after the wrapper, in every core,
// whatever the names are; the rest keep the order they came in.
const targets = [t("acme", "qwen"), t("acme", "wrap-qwen"), t("acme", "solo"), t("acme", "hermes-sandbox"), t("acme", "wrap-hermes-sandbox"), t("nimbus", "qwen")];
const groups = agentRows(targets, [], "maya");
const acme = groups.find((g) => g.group === "acme")!.rows.map((r) => r.provider);
assert(
  JSON.stringify(acme) === JSON.stringify(["wrap-qwen", "solo", "wrap-hermes-sandbox", "qwen", "hermes-sandbox"]),
  `wrapped first, originals after: ${acme.join(",")}`,
);
// A name that merely ends alike in another core, or another owner, ranks nothing.
const nimbus = groups.find((g) => g.group === "nimbus")!.rows.map((r) => r.provider);
assert(nimbus.join(",") === "qwen", "no wrapper in this core");
const rows: AgentRow[] = [
  { key: "a", group: "acme", core: "acme", provider: "qwen", ownerId: "maya", self: true },
  { key: "b", group: "acme", core: "acme", provider: "wrap-qwen", ownerId: "jack", self: false },
];
assert(rankRows(rows).map((r) => r.key).join("") === "ab", "another member's wrapper does not demote my agent");
