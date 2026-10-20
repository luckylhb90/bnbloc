import { proposalLabel, proposalShownTo, publishProposalOf } from "./publishProposal.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const card = { componentId: "cc_9", kind: "room", name: "weekly-review", displayName: "Weekly review", version: "1.2.0", scope: "org", note: "For reviews", for: "maya" };

// A whole proposal is a card; anything less (another scope, no one to show
// it to, an unknown kind, not an object) is none.
{
  const p = publishProposalOf(card);
  eq(p?.componentId, "cc_9", "a whole proposal");
  eq(p && proposalLabel(p), "room component “Weekly review” v1.2.0", "its label");
  for (const [name, v] of Object.entries({
    personal: { ...card, scope: "personal" },
    "no one": { ...card, for: "" },
    "no version": { ...card, version: "" },
    "unknown kind": { ...card, kind: "spaceship" },
    text: "publish it",
    none: undefined,
  })) {
    eq(publishProposalOf(v), undefined, name);
  }
}

// Only the person the agent asked for is shown it.
{
  const p = publishProposalOf(card);
  assert(!!p && proposalShownTo(p, "maya"), "maya sees it");
  assert(!!p && !proposalShownTo(p, "jack") && !proposalShownTo(p, null), "nobody else does");
}
