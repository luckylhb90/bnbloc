import type { ContentComponent } from "./contentComponents.ts";
import type { PlatformAgent } from "./platformAgents.ts";
import {
  MAX_WORKSHOP_PROBES,
  WORKSHOP_ROOM_NAME,
  cachedWorkshop,
  improvePrompt,
  libraryTabOf,
  rememberWorkshop,
  tryItDraft,
  workshopAgent,
  workshopAgentPick,
  workshopCandidates,
  workshopPrompt,
  workshopRoomId,
} from "./workshop.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function comp(over: Partial<ContentComponent>): ContentComponent {
  return {
    id: "cc_1",
    kind: "expert",
    name: "reviewer",
    displayName: "Reviewer",
    owner: "maya",
    scope: "personal",
    status: "active",
    latestVersion: "1.0.0",
    version: "1.0.0",
    permissions: [],
    requires: {},
    mine: true,
    acked: true,
    uses: 0,
    createdAtMs: 1,
    updatedAtMs: 1,
    ...over,
  };
}

// Prefills: editable placeholders per kind; improve names the id.
assert(workshopPrompt("expert").startsWith("Help me create a ___ expert that is good at ___."), "expert prefill");
assert(workshopPrompt("expert").includes("[your field and experience]"), "expert prefill asks for background");
assert(workshopPrompt("skill").startsWith("Help me create a skill pack"), "skill prefill");
eq(improvePrompt(comp({ id: "cc_9" })), 'Improve the expert "Reviewer" (id cc_9): ___', "improve an expert");
eq(improvePrompt(comp({ id: "cc_8", kind: "skill", name: "csv", displayName: "" })), 'Improve the skill pack "csv" (id cc_8): ___', "improve a skill pack");
eq(tryItDraft(comp({ quickPrompts: [{ title: "Review", prompt: " Review this diff " }] })), "Review this diff", "first quick prompt");
eq(tryItDraft(comp({})), "", "no quick prompt");
eq(tryItDraft(comp({ kind: "prompt", prompts: [{ id: "a", title: "A", body: "b", uses: 0 }, { id: "n", title: "Notes", body: "c", slash: "/notes", uses: 0 }] })), "/notes ", "a pack's first slash command");
// Prompt packs are authored by the same builder.
assert(workshopPrompt("prompt").startsWith("Help me create a prompt pack for ___"), "prompt pack prefill");
eq(improvePrompt(comp({ id: "cc_7", kind: "prompt", name: "kit", displayName: "Kit" })), 'Improve the prompt pack "Kit" (id cc_7): ___', "improve a prompt pack");
eq([libraryTabOf("expert"), libraryTabOf("skill"), libraryTabOf("prompt")], ["experts", "skills", "prompts"], "library tabs");

// The builder runs on the member's own online agent first, else a platform
// agent, else none.
const platform: PlatformAgent[] = [
  { core: "acme", provider: "codex", online: true, hosts: [{ ownerId: "jack", online: true, queued: 0 }] } as unknown as PlatformAgent,
];
const own = [
  { core: "nimbus", provider: "claude", online: false },
  { core: "acme", provider: "opencode", online: true },
];
eq(workshopAgentPick(workshopAgent(platform, own, "maya")), { core: "acme", provider: "opencode", own: true }, "own agent first");
eq(workshopAgentPick(workshopAgent(platform, [], "maya")), { core: "acme", provider: "codex", own: false }, "platform fallback");
eq(workshopAgent([], [], "maya").kind, "none", "no agent");

// Workshop candidates: cached first, then owned private rooms named
// "Expert workshop" (newest first), then other owned private rooms; never
// shared rooms or rooms someone else owns; bounded.
const rooms = [
  { roomId: "r-shared", name: WORKSHOP_ROOM_NAME, visibility: "shared", isOwner: true, createdAtMs: 9 },
  { roomId: "r-theirs", name: WORKSHOP_ROOM_NAME, visibility: "private", isOwner: false, createdBy: "jack", createdAtMs: 9 },
  { roomId: "r-old", name: WORKSHOP_ROOM_NAME, visibility: "private", isOwner: true, createdAtMs: 1 },
  { roomId: "r-new", name: "expert workshop", visibility: "private", createdBy: "maya", createdAtMs: 5 },
  { roomId: "r-case", name: WORKSHOP_ROOM_NAME, visibility: "private", createdBy: "Maya", createdAtMs: 6 },
  { roomId: "r-renamed", name: "My builder", visibility: "private", isOwner: true, createdAtMs: 3 },
];
eq(workshopCandidates(rooms, "maya", null), ["r-new", "r-old", "r-renamed"], "named first, newest first");
eq(workshopCandidates(rooms, "maya", "r-renamed"), ["r-renamed", "r-new", "r-old"], "cached first");
eq(workshopCandidates(rooms, "maya", "r-theirs"), ["r-new", "r-old", "r-renamed"], "a cached room the member does not own is ignored");
const many = Array.from({ length: 20 }, (_, i) => ({ roomId: `r${i}`, visibility: "private", isOwner: true, createdAtMs: i }));
eq(workshopCandidates(many, "maya", null).length, MAX_WORKSHOP_PROBES, "bounded probes");

assert(/^expert-workshop-[a-z2-9]{6}$/.test(workshopRoomId(() => 0.5)), "workshop room id");

// The cache is per member and tolerates missing storage.
const store = new Map<string, string>();
const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) };
rememberWorkshop(storage, "maya", "r-new");
eq(cachedWorkshop(storage, "maya"), "r-new", "cached per member");
eq(cachedWorkshop(storage, "Maya"), null, "Maya is another member");
eq(cachedWorkshop(storage, "jack"), null, "another member");
eq(cachedWorkshop(null, "maya"), null, "no storage");

