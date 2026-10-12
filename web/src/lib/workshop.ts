// The expert workshop (docs/prd/2026-10-12-experts-and-teams.md §5,
// authoring by conversation): "Create with AI" opens the member's private
// workshop room with the built-in builder expert picked and an editable
// prompt prefilled. The helpers here are pure (prefill texts, which agent
// runs the builder, which room is the workshop) so they
// are unit-tested; the view wiring lives in views/library/workshopFlow.ts.

import { contentKindWord, contentLabel, type ContentComponent, type ContentKind } from "./contentComponents.ts";
import { defaultPlatformChoice, type OwnAgent } from "./onboarding.ts";
import type { PlatformAgent, PlatformChoice } from "./platformAgents.ts";
import { isOwner } from "./roomRoles.ts";
import { StorageKey, keyFor } from "./storageKeys.ts";

export const WORKSHOP_ROOM_NAME = "Expert workshop";

// workshopPrompt is the prefilled, editable request of "Create with AI".
// The UI strings are English (the web has no i18n layer); the builder itself
// answers in the member's language.
export function workshopPrompt(kind: ContentKind): string {
  if (kind === "skill") return "Help me create a skill pack that teaches an agent to ___. It is used when ___. Example task: [an example]";
  if (kind === "prompt") return "Help me create a prompt pack for ___: prompts my team reuses when ___. Examples of what we ask: [an example]";
  return "Help me create a ___ expert that is good at ___. My background: [your field and experience]";
}

// improvePrompt is the prefilled request of "Improve with AI": it names the
// component id so the builder loads the current version.
export function improvePrompt(c: Pick<ContentComponent, "id" | "kind" | "name" | "displayName">): string {
  return `Improve the ${contentKindWord(c.kind)} "${contentLabel(c)}" (id ${c.id}): ___`;
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

// libraryTabOf is the Library tab that lists a kind.
export function libraryTabOf(kind: ContentKind): "experts" | "skills" | "prompts" {
  return kind === "skill" ? "skills" : kind === "prompt" ? "prompts" : "experts";
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
  return keyFor(StorageKey.workshopRoom, subject);
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
