// A share proposal (docs/prd/2026-10-25-agent-shares-from-conversation.md):
// room_setup share never creates a link by itself; it puts a card in the room,
// a notice row carrying payload.shareProposal (internal/wire/content_events.go
// ShareProposal), that only the person who asked the agent is shown. Their
// click on Create link calls the share route as them, with the dialog's own
// default (the organization's signed-in members, a week). The helpers here are
// pure; the request is the Share dialog's owner (lib/shares.ts).

import { sameUser } from "./format.ts";
import { DEFAULT_SHARE_ACCESS, DEFAULT_SHARE_DAYS, shareRequest } from "./shares.ts";
import type { ShareCreateRequest, ShareProposal } from "./wire.gen.ts";

export type { ShareProposal } from "./wire.gen.ts";

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const KINDS: readonly string[] = ["app", "site", "file"];

/** shareProposalOf reads a notice's payload.shareProposal, or undefined when it
 *  is not a whole one (a row from elsewhere is never a card). */
export function shareProposalOf(v: unknown): ShareProposal | undefined {
  if (!v || typeof v !== "object") return undefined;
  const p = v as Record<string, unknown>;
  const out: ShareProposal = { roomId: str(p.roomId), artifactId: str(p.artifactId), title: str(p.title), kind: str(p.kind), for: str(p.for) };
  return out.roomId && out.artifactId && out.title && out.for && KINDS.includes(out.kind) ? out : undefined;
}

/** shareProposalShownTo: only the person the agent asked for. */
export function shareProposalShownTo(p: ShareProposal, subject: string | null): boolean {
  return sameUser(p.for, subject);
}

/** shareProposalRequest is what a click creates: the latest version, for the
 *  organization's signed-in members, for the dialog's default lifetime. */
export function shareProposalRequest(): ShareCreateRequest {
  return shareRequest({ latest: true, version: 0, days: DEFAULT_SHARE_DAYS, access: DEFAULT_SHARE_ACCESS });
}

/** shareProposalWords: what the card says it is offering, and to whom. */
export function shareProposalWords(p: ShareProposal): string {
  const what = p.kind === "app" ? `the app "${p.title}"` : p.kind === "site" ? `the page "${p.title}"` : `"${p.title}"`;
  return `Your agent offers a link to ${what}. People in your organization open it after signing in. Only you see this; nothing is shared until you create it.`;
}
