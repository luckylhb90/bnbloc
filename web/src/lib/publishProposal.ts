// A publish proposal (docs/prd/2026-10-20-room-setup-by-conversation.md
// §2.3): publish_component never shares a component by itself; it puts a
// card in the room, a notice row carrying payload.proposal
// (internal/wire/content_events.go PublishProposal), that only the person
// who asked the agent is shown. Their click on Publish calls the publish
// route as them. The helpers here are pure.

import { CONTENT_PACKAGE_KINDS, contentKindWord, type ContentKind } from "./contentComponents.ts";
import { sameUser } from "./format.ts";
import type { PublishProposal } from "./wire.gen.ts";

export type { PublishProposal } from "./wire.gen.ts";

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** publishProposalOf reads a notice's payload.proposal, or undefined when it
 *  is not a whole one (a row from elsewhere is never a card). */
export function publishProposalOf(v: unknown): PublishProposal | undefined {
  if (!v || typeof v !== "object") return undefined;
  const p = v as Record<string, unknown>;
  const out: PublishProposal = {
    componentId: str(p.componentId),
    kind: str(p.kind) as ContentKind,
    name: str(p.name),
    displayName: str(p.displayName) || undefined,
    version: str(p.version),
    scope: str(p.scope),
    note: str(p.note) || undefined,
    for: str(p.for),
  };
  const known = Object.values(CONTENT_PACKAGE_KINDS).includes(out.kind);
  return known && out.componentId && out.name && out.version && out.scope === "org" && out.for ? out : undefined;
}

/** proposalShownTo reports whether subject is shown the card: only the
 *  person the agent asked for. */
export function proposalShownTo(p: PublishProposal, subject: string | null): boolean {
  return sameUser(p.for, subject);
}

/** proposalLabel names what would be shared: "room component “Weekly
 *  review” v1.2.0". */
export function proposalLabel(p: PublishProposal): string {
  return `${contentKindWord(p.kind)} “${p.displayName || p.name}” v${p.version}`;
}
