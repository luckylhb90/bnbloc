import {
  aiSuggestionsOn,
  asksAbout,
  askedText,
  candidatesOf,
  decisionRequest,
  dismissSuggestion,
  dismissedKeys,
  eligibleForAsk,
  MIN_REQUEST_CHARS,
  setAiSuggestions,
  suggestionAction,
  suggestionFromDecision,
  suggestionText,
  type UseCandidate,
} from "./suggestUse.ts";
import { cardForRow, createCardStore, staleAnswer } from "./sentRequestCard.ts";
import { agentByIdentity, sameAgent } from "./roomAgents.ts";
import type { DecisionReply, LibraryCandidate } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const cand = (kind: "expert" | "skill" | "mcp", name: string, label: string, wears: string[] = []): LibraryCandidate =>
  ({ id: kind === "mcp" ? undefined : `cc_${name}`, kind, name, label, description: `${label} does its work.`, tags: [], wears, version: "1.0.0" }) as LibraryCandidate;

const catalog = candidatesOf({
  total: 6,
  candidates: [
    cand("expert", "app-builder", "App builder", ["interactive-apps"]),
    cand("skill", "interactive-apps", "Interactive apps"),
    cand("expert", "sql-helper", "SQL helper"),
    cand("expert", "code-reviewer", "Code reviewer"),
    cand("expert", "tech-design-reviewer", "Tech design reviewer"),
    cand("mcp", "browser", "browser"),
  ],
});
const by = (name: string) => catalog.find((c) => c.name === name) as UseCandidate;
const answer = (probs: Record<string, number>, type = "choice"): DecisionReply =>
  ({ model: "decision-1", usage: { input_tokens: 1, output_tokens: 1 }, answers: [{ type, name: "fit", probabilities: Object.entries(probs).map(([value, probability]) => ({ value, probability })) }] }) as unknown as DecisionReply;

// The candidates are what the server's shortlist says: experts, skill packs and tools.
assert(catalog.length === 6 && catalog.some((c) => c.key === "mcp/browser") && catalog.some((c) => c.key === "skill/cc_interactive-apps"), "the shortlist's experts, skill packs and tools");
assert(candidatesOf({ total: 0, candidates: [] }).length === 0 && candidatesOf({ total: 1, candidates: [{ kind: "folder", name: "x", label: "x" } as unknown as LibraryCandidate] }).length === 0, "an empty or unknown-kind shortlist has no candidates");
assert(by("app-builder").wears[0] === "interactive-apps", "an expert records the skill packs it wears, without their range");

// Which sent requests earn a question: to an agent, long enough, no expert chosen already.
assert(asksAbout({ text: "build a landing page with an AI assistant", toAgent: true }), "a request to an agent");
assert(!asksAbout({ text: "build a landing page with an AI assistant", toAgent: false }), "not a message to people only");
assert(!asksAbout({ text: "thanks!", toAgent: true }) && MIN_REQUEST_CHARS === 12, "not a short reply");
assert(!asksAbout({ text: "build a landing page with an AI assistant", toAgent: true, expertId: "cc_x" }), "not when the member already chose an expert");

// The question: every eligible candidate and "none", the request as the input.
{
  const eligible = eligibleForAsk(catalog, { exclude: new Set(["expert/cc_sql-helper"]), worn: new Set(["interactive-apps"]) });
  assert(!eligible.some((c) => c.name === "sql-helper" || c.name === "interactive-apps"), "excluded and worn are not asked about");
  assert(eligibleForAsk(catalog).length === catalog.length, "nothing is cut here: the server already bounded the shortlist");
  const body = decisionRequest("build me a page", eligible);
  const q = body.questions[0];
  assert(body.input === "build me a page" && q?.type === "choice", "one choice question over the request");
  assert(q?.choices?.length === eligible.length + 1 && q.choices.at(-1)?.value === "none", "every candidate and none");
  assert(new Set(q?.choices?.map((c) => c.value)).size === q?.choices?.length, "values are unique");
  assert(!(q?.instructions ?? "").includes("build me a page"), "the request is never part of the instructions");
  const long = "w".repeat(3000);
  assert(askedText(long).length < 1300 && askedText(long).includes("[…]") && askedText("  short  ") === "short", "a long request is cut to its start and end");
}

// The answer: a candidate the model is sure of and clearly prefers, or silence.
{
  const name = (reply: DecisionReply, eligible = catalog) => suggestionFromDecision(reply, eligible)?.candidate.name ?? null;
  assert(name(answer({ "expert/cc_app-builder": 0.86, "skill/cc_interactive-apps": 0.09, none: 0.02 })) === "app-builder", "a clear choice");
  assert(name(answer({ "skill/cc_interactive-apps": 0.37, none: 0.33, "expert/cc_app-builder": 0.26 })) === null, "an ambiguous request stays silent");
  assert(name(answer({ none: 1, "expert/cc_sql-helper": 0 })) === null, "none");
  assert(name(answer({ "expert/cc_code-reviewer": 0.69, "expert/cc_tech-design-reviewer": 0.29, none: 0.01 })) === "code-reviewer", "sure enough and twice the runner-up");
  assert(name(answer({ "expert/cc_code-reviewer": 0.55, none: 0.1 })) === null, "not sure enough");
  assert(name(answer({ "expert/cc_code-reviewer": 0.6, "expert/cc_tech-design-reviewer": 0.4 })) === null, "a close runner-up");
  assert(name(answer({ "skill/cc_interactive-apps": 0.99, none: 0 })) === "app-builder", "a skill pack an eligible expert wears is the expert");
  assert(name(answer({ "skill/cc_interactive-apps": 0.99, none: 0 }), catalog.filter((c) => c.name !== "app-builder")) === "interactive-apps", "the skill pack itself when its expert is not offered");
  assert(name(answer({ "expert/cc_app-builder": 0.5, "skill/cc_interactive-apps": 0.45, none: 0.05 })) === "app-builder", "the expert takes its skill pack's share");
  assert(name(answer({ "expert/cc_sql-helper": 1 }, "refusal")) === null, "a refusal");
  assert(name({ model: "m", usage: { input_tokens: 1, output_tokens: 1 }, answers: [] } as unknown as DecisionReply) === null, "no answer");
  assert(suggestionFromDecision(answer({ "expert/cc_sql-helper": 0.99, none: 0 }), catalog)?.probability === 0.99, "the probability is kept");
}

// The card's words: what clicking does, in the member's terms.
assert(suggestionAction(by("sql-helper")) === "Ask again with it" && suggestionAction(by("interactive-apps")) === "Turn on and ask again" && suggestionAction(by("browser")) === "Add and ask again", "a button per kind");
assert(suggestionText(by("sql-helper")) === "SQL helper fits this request.", "the sentence names the candidate");

// The member's choice: on unless turned off.
{
  const data = new Map<string, string>();
  const storage = { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) };
  assert(aiSuggestionsOn(storage, "k") && aiSuggestionsOn(null, "k"), "on by default");
  setAiSuggestions(storage, "k", false);
  assert(!aiSuggestionsOn(storage, "k") && aiSuggestionsOn(storage, "other"), "off for that member only");
  setAiSuggestions(storage, "k", true);
  assert(aiSuggestionsOn(storage, "k"), "on again");
  setAiSuggestions(null, "k", false);
}

// Dismissals: per member key, a month, pruned.
{
  const data = new Map<string, string>();
  const storage = { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) };
  const now = 1_000_000;
  assert(dismissedKeys(storage, "k", now).size === 0, "nothing dismissed at first");
  dismissSuggestion(storage, "k", "expert/cc_a", now);
  dismissSuggestion(storage, "other", "expert/cc_b", now);
  assert(dismissedKeys(storage, "k", now + 1000).has("expert/cc_a") && !dismissedKeys(storage, "k", now).has("expert/cc_b"), "kept per key");
  assert(dismissedKeys(storage, "k", now + 29 * 86_400_000).has("expert/cc_a"), "quiet for a month");
  assert(!dismissedKeys(storage, "k", now + 31 * 86_400_000).has("expert/cc_a"), "suggested again after a month");
  data.set("bad", "{not json");
  assert(dismissedKeys(storage, "bad", now).size === 0 && dismissedKeys(null, "k", now).size === 0, "an unreadable or missing store is empty");
  dismissSuggestion(null, "k", "x", now);
}

// The card of a sent request: one per room, under its own message only, never stale.
{
  const store = createCardStore();
  let changes = 0;
  const off = store.subscribe(() => changes++);
  const card = { clientMsgId: "m1", request: "build a page", sentTo: { ownerId: "maya", provider: "claude-code", core: "nimbus" }, suggestion: { candidate: by("app-builder"), probability: 0.9 } };
  assert(store.of("r1") === null, "no card at first");
  store.show("r1", card);
  assert(store.of("r1") === card && store.of("r2") === null, "a card belongs to its room");
  assert(cardForRow(store.of("r1"), "m1") === card && cardForRow(store.of("r1"), "m2") === null && cardForRow(store.of("r1"), undefined) === null, "drawn only under its own message");
  store.clear("r1", "m2");
  assert(store.of("r1") === card, "an answer for another message never removes this card");
  store.clear("r1", "m1");
  assert(store.of("r1") === null && changes === 2, "cleared by its own message");
  store.show("r1", card);
  store.show("r1", { ...card, clientMsgId: "m3" });
  assert(store.of("r1")?.clientMsgId === "m3", "a newer card replaces the older");
  store.clear("r1");
  assert(store.of("r1") === null, "cleared when the room is left");
  off();
  store.show("r1", card);
  assert(changes === 5, "an unsubscribed listener is not called");
}
assert(!staleAnswer("m1", "m1", false), "the latest message's answer is wanted");
assert(staleAnswer("m1", "m2", false), "a newer message supersedes it");
assert(staleAnswer("m1", "m1", true), "an answer after leaving the room is dropped");
assert(!staleAnswer("m1", null, false), "no message sent since is fine");

// Asking again goes to the agent the request went to: a room holds one member's agents of
// several cores (the same provider under two of them), and the second send must not land on
// the first of them. A real room did exactly that (2026-10-26: a request sent to nimbus's
// claude-code was sent again to acme's claude, which was not even signed in).
{
  const agents = [
    { ownerId: "maya", provider: "claude", core: "acme", self: true },
    { ownerId: "maya", provider: "claude-code", core: "nimbus", self: true },
    { ownerId: "maya", provider: "claude-code", core: undefined, self: true },
    { ownerId: "jack", provider: "claude-code", core: "nimbus", self: false },
  ];
  assert(agentByIdentity(agents, { ownerId: "maya", provider: "claude-code", core: "nimbus" }) === agents[1], "the agent the request went to, not the first of mine");
  assert(agentByIdentity(agents, { ownerId: "maya", provider: "claude-code" }) === agents[2], "no core is its own agent");
  assert(agentByIdentity(agents, { ownerId: "jack", provider: "claude-code", core: "nimbus" }) === agents[3], "someone else's agent it was sent to");
  assert(agentByIdentity(agents, { ownerId: "maya", provider: "opencode", core: "nimbus" }) === null, "an agent that left the room is nobody");
  assert(!sameAgent({ ownerId: "maya", provider: "claude", core: "acme" }, { ownerId: "maya", provider: "claude", core: "nimbus" }), "the core tells two agents of one provider apart");
}
