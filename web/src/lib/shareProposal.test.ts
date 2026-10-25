import { shareProposalOf, shareProposalRequest, shareProposalShownTo, shareProposalWords } from "./shareProposal.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const whole = { roomId: "r1", artifactId: "art_1", title: "Landing page", kind: "site", for: "maya@example.com" };

// Only a whole proposal is a card: a half-written or foreign row never is.
assert(shareProposalOf(whole)?.artifactId === "art_1", "a whole proposal is read");
for (const [name, bad] of [
  ["no artifact", { ...whole, artifactId: "" }],
  ["no recipient", { ...whole, for: "" }],
  ["an unknown kind", { ...whole, kind: "folder" }],
  ["no title", { ...whole, title: "" }],
  ["a string", "x"],
  ["null", null],
] as const) assert(shareProposalOf(bad) === undefined, `${name} is not a card`);

// Only the person the agent asked for sees it (subjects compare exactly, as on the server).
const p = shareProposalOf(whole)!;
assert(shareProposalShownTo(p, "maya@example.com"), "its requester sees it");
assert(!shareProposalShownTo(p, "jack@example.com") && !shareProposalShownTo(p, "MAYA@example.com") && !shareProposalShownTo(p, null), "nobody else does");

// A click creates what the Share dialog's defaults create, and nothing wider.
const req = shareProposalRequest();
assert(req.access === "org" && req.version === 0 && req.expiresInHours === 7 * 24, `the default link (${JSON.stringify(req)})`);

// The card says what is offered and that nothing is shared yet.
assert(shareProposalWords(p).includes('the page "Landing page"') && shareProposalWords({ ...p, kind: "app" }).includes('the app "Landing page"'), "names what it offers");
assert(shareProposalWords(p).includes("nothing is shared until you create it"), "says nothing is shared yet");
