import { cardShownTo, componentCardOf } from "./componentCard.ts";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(msg);
}

// A whole card is read with its valid actions only; anything else is no card.
const card = componentCardOf({
  component: "connections",
  for: "maya",
  title: "Connect the wiki",
  text: "Your agent tried to read a wiki page.",
  actions: [
    { kind: "open", label: "Connect", page: "connections", query: "system=wiki" },
    { kind: "ask-again", label: "Ask again" },
    { kind: "run", label: "Run" },
    { kind: "open", label: "Bad", page: "Not A Page" },
  ],
});
assert(!!card && card.actions?.length === 2 && card.actions[0].page === "connections", "valid actions are kept");
assert(componentCardOf({ component: "c", title: "t" }) === undefined, "a card for nobody is none");
assert(componentCardOf("x") === undefined, "a string is no card");
assert(!!card && cardShownTo(card, "maya") && !cardShownTo(card, "jack"), "only its person sees it");
const calls = componentCardOf({
  component: "connections",
  for: "maya",
  title: "Allow a change?",
  actions: [
    { kind: "call", label: "Allow", page: "approve", query: "id=h1&decision=allow" },
    { kind: "call", label: "Bad", page: "Not A Page" },
  ],
});
assert(!!calls && calls.actions?.length === 1 && calls.actions[0].kind === "call" && calls.actions[0].query === "id=h1&decision=allow", "a call acts on a valid page");
console.log("componentCard ok");
