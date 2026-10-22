// The expert workshop (docs/prd/2026-10-12-experts-and-teams.md §5,
// authoring by conversation): "Create with AI" opens the member's private
// workshop room with the Expert builder picked and an editable prompt
// prefilled. The helpers here are pure (the prefill, which agent
// runs the builder, which room is the workshop) so they
// are unit-tested; the view wiring lives in views/library/workshopFlow.ts.

import { CONTENT_KINDS, contentKindWord, contentLabel, type ContentComponent, type ContentKind, type ContentLibraryTab } from "./contentComponents.ts";
import { defaultPlatformChoice, type OwnAgent } from "./onboarding.ts";
import type { PlatformAgent, PlatformChoice } from "./platformAgents.ts";
import { isOwner } from "./roomRoles.ts";
import { gatewayBase } from "./config.ts";
import { StorageKey, memberKey } from "./storageKeys.ts";

export const WORKSHOP_ROOM_NAME = "Expert workshop";

// BUILDER_EXPERT_NAME names the Expert builder: ordinary content (the
// default content publishes it at platform scope), found by name, never by
// an id (docs/prd/2026-10-20-no-built-in-content.md §2.4).
export const BUILDER_EXPERT_NAME = "expert-builder";

// SCOPE_RANK orders scopes nearest first, as the server resolves a name
// (internal/assist scopeRank): the member's own, a copy shared with them,
// their organization's, the platform's.
const SCOPE_RANK: Readonly<Record<string, number>> = { personal: 0, shared: 1, org: 2, platform: 3 };

// isBuilder: an expert named expert-builder.
export function isBuilder(c: Pick<ContentComponent, "kind" | "name"> | undefined | null): boolean {
  return !!c && c.kind === "expert" && c.name === BUILDER_EXPERT_NAME;
}

// builderOf is the Expert builder among the member's listed content: the
// expert named expert-builder of the nearest scope (an organization's own
// replaces the default), or null when none is installed.
export function builderOf<T extends Pick<ContentComponent, "kind" | "name" | "scope">>(list: readonly T[]): T | null {
  let best: T | null = null;
  for (const c of list) {
    if (!isBuilder(c)) continue;
    if (!best || (SCOPE_RANK[c.scope] ?? 9) < (SCOPE_RANK[best.scope] ?? 9)) best = c;
  }
  return best;
}

// WorkshopRequest is what the workshop opens for: "Create with AI" of a
// kind, or "Improve with AI" of a component.
export type WorkshopRequest =
  | { readonly create: ContentKind }
  | { readonly improve: Pick<ContentComponent, "id" | "kind" | "name" | "displayName"> };

// workshopDraft is the prefilled, editable request: one of the builder's own
// quick prompts (the builder component's, read through the content API, so
// the builder owns the wording): per kind the one titled "Create … <kind>",
// and for a component the one titled "Improve …", whose first ___ names it
// with its id so the builder loads the current version
// (the default content's TestBuilderQuickPromptsServeTheWorkshop pins
// those titles).
export function workshopDraft(builder: Pick<ContentComponent, "quickPrompts"> | null, req: WorkshopRequest): string {
  const quick = builder?.quickPrompts ?? [];
  const titled = (prefix: string, word = "") =>
    quick.find((q) => {
      const t = (q.title ?? "").trim().toLowerCase();
      return t.startsWith(prefix) && t.includes(word);
    })?.prompt?.trim() ?? "";
  if ("create" in req) return titled("create", req.create);
  const c = req.improve;
  const named = `${contentKindWord(c.kind)} "${contentLabel(c)}" (id ${c.id})`;
  const improve = titled("improve");
  return improve.includes("___") ? improve.replace("___", named) : [improve, named].filter(Boolean).join(" ");
}

// tryItDraft is the composer text of "Try it now": an expert's first quick
// prompt, a prompt pack's first slash command (a pack is turned on for the
// room first, so the command works), else nothing.
export function tryItDraft(c: Pick<ContentComponent, "quickPrompts" | "prompts">): string {
  const first = c.quickPrompts?.find((q) => q.prompt?.trim());
  if (first?.prompt) return first.prompt.trim();
  const slash = c.prompts?.find((p) => p.slash)?.slash;
  return slash ? `${slash} ` : "";
}

// libraryTabOf is the Library tab that lists a kind (null: none, an app).
export function libraryTabOf(kind: ContentKind): ContentLibraryTab | null {
  return CONTENT_KINDS[kind].libraryTab;
}

// WorkshopAgent is the agent the builder runs on: the member's own agent
// first (their machine, their setup), else a platform agent.
export type WorkshopAgent =
  | { readonly kind: "own"; readonly agent: OwnAgent }
  | { readonly kind: "platform"; readonly choice: PlatformChoice }
  | { readonly kind: "none" };

export function workshopAgent(offered: readonly PlatformAgent[], own: readonly OwnAgent[], subject: string | null): WorkshopAgent {
  const mine = own.find((a) => a.online);
  if (mine) return { kind: "own", agent: mine };
  const choice = defaultPlatformChoice(offered, subject);
  if (choice) return { kind: "platform", choice };
  return { kind: "none" };
}

// The composer pick of the workshop's agent (composerAgentPicks).
export function workshopAgentPick(a: WorkshopAgent): { core: string; provider: string; own: boolean } | null {
  if (a.kind === "own") return { core: a.agent.core, provider: a.agent.provider, own: true };
  if (a.kind === "platform") return { core: a.choice.agent.core ?? "", provider: a.choice.agent.provider, own: false };
  return null;
}

// A room as the workshop lookup sees it (a row of the member's room list,
// wire.RoomSummary: role is the member's own).
export interface WorkshopRoomFacts {
  readonly roomId: string;
  readonly name?: string;
  readonly visibility: string;
  readonly role: string;
  readonly createdAtMs?: number;
}

// At most this many rooms are probed for the builder pin before a new
// workshop is created: the lookup must stay cheap for members with many rooms.
export const MAX_WORKSHOP_PROBES = 8;

function ownedPrivate(r: WorkshopRoomFacts): boolean {
  return r.visibility === "private" && isOwner(r);
}

// workshopCandidates orders the rooms that may be the member's workshop:
// the cached id first, then owned private rooms named "Expert workshop"
// (newest first), then the other owned private rooms (newest first), at most
// MAX_WORKSHOP_PROBES. The caller verifies each by its builder pin.
export function workshopCandidates(rooms: readonly WorkshopRoomFacts[], cachedId: string | null): string[] {
  const owned = rooms.filter(ownedPrivate);
  const newest = (a: WorkshopRoomFacts, b: WorkshopRoomFacts) => (b.createdAtMs ?? 0) - (a.createdAtMs ?? 0);
  const named = owned.filter((r) => (r.name ?? "").trim().toLowerCase() === WORKSHOP_ROOM_NAME.toLowerCase()).sort(newest);
  const others = owned.filter((r) => !named.includes(r)).sort(newest);
  const out: string[] = [];
  const cached = cachedId ? owned.find((r) => r.roomId === cachedId) : undefined;
  for (const r of [...(cached ? [cached] : []), ...named, ...others]) {
    if (!out.includes(r.roomId)) out.push(r.roomId);
    if (out.length >= MAX_WORKSHOP_PROBES) break;
  }
  return out;
}

// workshopRoomId is a fresh global room id for a new workshop.
export function workshopRoomId(random: () => number = Math.random): string {
  const alphabet = "abcdefghijkmnpqrstuvwxyz23456789";
  let suffix = "";
  for (let i = 0; i < 6; i++) suffix += alphabet[Math.floor(random() * alphabet.length) % alphabet.length];
  return `expert-workshop-${suffix}`;
}

// The workshop room id is cached per member in local storage (a fast path;
// the server state — an owned private room pinning the builder — decides).
export function workshopStorageKey(subject: string): string {
  return memberKey(StorageKey.workshopRoom, { gateway: gatewayBase(), subject });
}

export function cachedWorkshop(storage: Pick<Storage, "getItem"> | null, subject: string | null): string | null {
  if (!storage || !subject) return null;
  try {
    return storage.getItem(workshopStorageKey(subject)) || null;
  } catch {
    return null;
  }
}

export function rememberWorkshop(storage: Pick<Storage, "setItem"> | null, subject: string | null, roomId: string): void {
  if (!storage || !subject) return;
  try {
    storage.setItem(workshopStorageKey(subject), roomId);
  } catch {
    // Storage full or blocked: the lookup finds the room again.
  }
}
