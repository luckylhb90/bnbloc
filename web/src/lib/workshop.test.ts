import type { ContentComponent } from "./contentComponents.ts";
import type { PlatformAgent } from "./platformAgents.ts";
import {
  MAX_WORKSHOP_PROBES,
  WORKSHOP_ROOM_NAME,
  cachedWorkshop,
  libraryTabOf,
  rememberWorkshop,
  tryItDraft,
  workshopAgent,
  workshopAgentPick,
  workshopCandidates,
  workshopDraft,
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
    removable: false,
    acked: true,
    runs: 0,
    members: 0,
    uses: 0,
    createdAtMs: 1,
    updatedAtMs: 1,
    ...over,
  };
}

// Prefills are the builder's own quick prompts (a stand-in builder here; the
// real one's titles are pinned by TestBuilderQuickPromptsServeTheWorkshop):
// per kind the "Create … <kind>" one; improve fills the first blank with the
// component and its id.
const builder = comp({
  quickPrompts: [
    { title: "Create an expert", prompt: " Make me an expert for ___ " },
    { title: "Create a skill pack", prompt: "Make me a skill pack for ___" },
    { title: "Create a prompt pack", prompt: "Make me a prompt pack for ___" },
    { title: "Improve one of mine", prompt: "Make my ___ better: ___" },
  ],
});
eq(workshopDraft(builder, { create: "expert" }), "Make me an expert for ___", "expert prefill");
eq(workshopDraft(builder, { create: "skill" }), "Make me a skill pack for ___", "skill prefill");
eq(workshopDraft(builder, { improve: comp({ id: "cc_9" }) }), 'Make my expert "Reviewer" (id cc_9) better: ___', "improve an expert");
eq(workshopDraft(builder, { improve: comp({ id: "cc_8", kind: "skill", name: "csv", displayName: "" }) }), 'Make my skill pack "csv" (id cc_8) better: ___', "improve a skill pack");
// The builder unreadable: create opens empty; improve still names the component.
eq(workshopDraft(null, { create: "expert" }), "", "no builder, no prefill");
eq(workshopDraft(null, { improve: comp({ id: "cc_9" }) }), 'expert "Reviewer" (id cc_9)', "no builder: the component alone");
eq(tryItDraft(comp({ quickPrompts: [{ title: "Review", prompt: " Review this diff " }] })), "Review this diff", "first quick prompt");
eq(tryItDraft(comp({})), "", "no quick prompt");
eq(tryItDraft(comp({ kind: "prompt", prompts: [{ id: "a", title: "A", body: "b", uses: 0 }, { id: "n", title: "Notes", body: "c", slash: "/notes", uses: 0 }] })), "/notes ", "a pack's first slash command");
// Prompt packs are authored by the same builder.
eq(workshopDraft(builder, { create: "prompt" }), "Make me a prompt pack for ___", "prompt pack prefill");
eq(workshopDraft(builder, { improve: comp({ id: "cc_7", kind: "prompt", name: "kit", displayName: "Kit" }) }), 'Make my prompt pack "Kit" (id cc_7) better: ___', "improve a prompt pack");
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
  { roomId: "r-shared", name: WORKSHOP_ROOM_NAME, visibility: "shared", role: "owner", createdAtMs: 9 },
  { roomId: "r-theirs", name: WORKSHOP_ROOM_NAME, visibility: "private", role: "member", createdAtMs: 9 },
  { roomId: "r-old", name: WORKSHOP_ROOM_NAME, visibility: "private", role: "owner", createdAtMs: 1 },
  { roomId: "r-new", name: "expert workshop", visibility: "private", role: "owner", createdAtMs: 5 },
  { roomId: "r-admin", name: WORKSHOP_ROOM_NAME, visibility: "private", role: "admin", createdAtMs: 6 },
  { roomId: "r-renamed", name: "My builder", visibility: "private", role: "owner", createdAtMs: 3 },
];
eq(workshopCandidates(rooms, null), ["r-new", "r-old", "r-renamed"], "named first, newest first");
eq(workshopCandidates(rooms, "r-renamed"), ["r-renamed", "r-new", "r-old"], "cached first");
eq(workshopCandidates(rooms, "r-theirs"), ["r-new", "r-old", "r-renamed"], "a cached room the member does not own is ignored");
const many = Array.from({ length: 20 }, (_, i) => ({ roomId: `r${i}`, visibility: "private", role: "owner", createdAtMs: i }));
eq(workshopCandidates(many, null).length, MAX_WORKSHOP_PROBES, "bounded probes");

assert(/^expert-workshop-[a-z2-9]{6}$/.test(workshopRoomId(() => 0.5)), "workshop room id");

// The cache is per member and tolerates missing storage.
const store = new Map<string, string>();
const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) };
rememberWorkshop(storage, "maya", "r-new");
eq(cachedWorkshop(storage, "maya"), "r-new", "cached per member");
eq(cachedWorkshop(storage, "Maya"), null, "Maya is another member");
eq(cachedWorkshop(storage, "jack"), null, "another member");
eq(cachedWorkshop(null, "maya"), null, "no storage");

