// Content components (docs/prd/2026-10-12-experts-and-teams.md): experts and
// skill packs. The shapes are internal/wire/content_components.go's (wire.gen.ts); the
// helpers here are pure (form <-> payload mapping, version and update logic,
// expert mentions, the chat payload marker) so they are unit-tested.

import { parseDefaultAgent } from "./teams.ts";

export type { ChatExpert, ContentComponent, ContentComponentDetail, ContentMCPRequirement, ContentPublishRequest, ContentQuickPrompt, ContentRequires, ContentUse, ContentUsesReply, ContentVersionDetail } from "./wire.gen.ts";
import type { ChatExpert, ContentComponent, ContentQuickPrompt, ContentRequires, ContentUse, ContentVersionDetail } from "./wire.gen.ts";

export type { ContentKind } from "./wire.gen.ts";
import type { AssistField, AssistSpec, ChannelPreset, ContentKind, RoomSpec } from "./wire.gen.ts";
import { ContentTagPattern, MaxContentTagRunes } from "./wire.gen.ts";

// CONTENT_KINDS is everything the client says about a content kind, one row
// per kind (web L5): a Record over the generated ContentKind, so a kind the
// server registers (contentpkg) fails to compile until it has its row. An
// app (docs/prd/2026-10-13-app-gallery.md) is published from a site and
// listed in the gallery, not in a Library tab of the workshop's. An AI
// assist (docs/prd/2026-10-13-ai-assist.md) is a task of the assist model,
// listed in its own tab and never used in a room. A room component
// (docs/prd/2026-10-20-rooms-as-components.md) is a room's setup, saved
// from a room and installed as a new room (lib/roomComponents.ts).
export const CONTENT_KINDS: Readonly<Record<ContentKind, { readonly word: string; readonly label: string; readonly libraryTab: ContentLibraryTab | null; readonly packageKind: string }>> = {
  expert: { word: "expert", label: "Expert", libraryTab: "experts", packageKind: "bnbloc.expert.package/v1" },
  skill: { word: "skill pack", label: "Skill pack", libraryTab: "skills", packageKind: "bnbloc.skill.package/v1" },
  prompt: { word: "prompt pack", label: "Prompt pack", libraryTab: "prompts", packageKind: "bnbloc.prompt.package/v1" },
  work: { word: "work", label: "Work", libraryTab: null, packageKind: "bnbloc.work.package/v1" },
  assist: { word: "AI assist", label: "AI assist", libraryTab: "assists", packageKind: "bnbloc.assist.package/v1" },
  room: { word: "room component", label: "Room", libraryTab: "rooms", packageKind: "bnbloc.room.package/v1" },
  // A channel preset (docs/prd/2026-10-28-channels-and-threads.md §2.1c): a
  // kind of channel, applied to a channel from its settings (lib/channelPresets.ts).
  "channel-preset": { word: "channel preset", label: "Channel preset", libraryTab: null, packageKind: "bnbloc.channel-preset.package/v1" },
};

/** The Library tabs that list content components of a kind. */
export type ContentLibraryTab = "experts" | "skills" | "prompts" | "assists" | "rooms";

// The short kind of a content package's manifest kind.
export const CONTENT_PACKAGE_KINDS: Readonly<Record<string, ContentKind>> = Object.fromEntries(
  (Object.entries(CONTENT_KINDS) as [ContentKind, (typeof CONTENT_KINDS)[ContentKind]][]).map(([k, m]) => [m.packageKind, k]),
);

export function contentKindWord(kind: ContentKind): string {
  return CONTENT_KINDS[kind].word;
}

// "room-expert" is a room's expert: one of its pinned experts that every
// agent turn in the room wears.
export type { ContentUseScope } from "./wire.gen.ts";

// ContentForm is the Library form (both kinds); the server lays it out into
// the kind's files (component.json, INSTRUCTIONS.md, <skill>/SKILL.md).
export interface ContentFormSkill {
  name: string;
  description: string;
  body: string;
}

export interface ContentForm {
  kind: ContentKind;
  name: string;
  displayName: string;
  description: string;
  category: string;
  instructions: string;
  welcome: string;
  quickPrompts: ContentQuickPrompt[];
  skills: ContentFormSkill[];
  tags?: string[];
  icon?: string;
  // Skill and prompt packs by name@range, and declared permissions.
  requires: ContentRequires;
  // An expert's team roster (absent or empty: not a team).
  team?: ContentFormTeam;
  // An AI assist's task (instructions is its system prompt).
  assist?: AssistSpec;
  // A room component's setup (its room expert and packs are requires).
  room?: RoomSpec;
  // A channel preset's channel setup (lib/channelPresets.ts).
  channel?: ChannelPreset;
}

export interface ContentFormRole {
  role: string;
  expert: string;
  defaultAgent: string;
  description: string;
}

export interface ContentFormTeam {
  lead: string;
  members: ContentFormRole[];
}

export function emptyContentForm(kind: ContentKind): ContentForm {
  return {
    kind,
    name: "",
    displayName: "",
    description: "",
    category: "",
    instructions: "",
    welcome: "",
    quickPrompts: [],
    skills: kind === "skill" ? [{ name: "", description: "", body: "" }] : [],
    tags: [],
    icon: "",
    requires: {},
    team: { lead: "", members: [] },
    ...(kind === "assist" ? { assist: { output: "text" as const, fields: [], context: [] } } : {}),
  };
}

// formFromVersion prefills the form from a version detail (new version).
export function formFromVersion(d: ContentVersionDetail): ContentForm {
  const c = d.component;
  return {
    kind: c.kind,
    name: c.name,
    displayName: c.displayName ?? "",
    description: c.description ?? "",
    category: c.category ?? "",
    instructions: (d.instructions ?? "").trim(),
    welcome: c.welcome ?? "",
    quickPrompts: (c.quickPrompts ?? []).map((q) => ({ ...q })),
    skills: (c.skills ?? []).map((s) => ({ name: s.name, description: s.description, body: (d.skillBodies?.[s.name] ?? "").trim() })),
    tags: [...(c.tags ?? [])],
    icon: c.icon ?? "",
    requires: c.requires ?? {},
    team: {
      lead: c.team?.lead && c.team.lead !== "lead" ? c.team.lead : "",
      members: (c.team?.members ?? []).map((m) => ({ role: m.role, expert: m.expert, defaultAgent: m.defaultAgent, description: m.description ?? "" })),
    },
    ...(c.assist ? { assist: { output: c.assist.output, fields: (c.assist.fields ?? []).map((f) => ({ ...f, ...(f.item ? { item: f.item.map((y) => ({ ...y })) } : {}) })), context: [...(c.assist.context ?? [])], ...(c.assist.gate ? { gate: { ...c.assist.gate } } : {}) } } : {}),
  };
}

function trimmed(s: string): string {
  return s.trim();
}

// ASSIST_FIELD_NAME is a JSON assist's reply field name (and an item's).
const ASSIST_FIELD_NAME = /^[a-zA-Z][a-zA-Z0-9_]{0,39}$/;

// assistFieldPayload is one reply field as a save sends it: text is the
// default shape (left out), an items field carries its item fields.
function assistFieldPayload(x: AssistField): AssistField {
  const description = trimmed(x.description ?? "");
  const item = x.type === "items" ? (x.item ?? []).map(assistFieldPayload).filter((y) => y.name) : [];
  return {
    name: trimmed(x.name),
    ...(description ? { description } : {}),
    ...(x.type && x.type !== "text" ? { type: x.type } : {}),
    ...(item.length > 0 ? { item } : {}),
  };
}

// formPayload is the JSON form a save sends: empty optional fields and
// blank quick prompts / skills are left out; an expert sends no skill
// list fields it does not use and a skill pack no instructions.
export function formPayload(f: ContentForm): Record<string, unknown> {
  const out: Record<string, unknown> = { kind: f.kind, name: trimmed(f.name) };
  for (const key of ["displayName", "description", "category", "icon"] as const) {
    const v = trimmed(f[key] ?? "");
    if (v) out[key] = v;
  }
  const tags = cleanTags(f.tags ?? []);
  if (tags.length > 0) out.tags = tags;
  const req = f.requires ?? {};
  if ((req.expert ? 1 : 0) + (req.skills?.length ?? 0) + (req.prompts?.length ?? 0) + (req.mcp?.length ?? 0) + (req.permissions?.length ?? 0) > 0) {
    out.requires = {
      // A room component's room expert.
      ...(req.expert ? { expert: req.expert } : {}),
      ...(req.skills?.length ? { skills: req.skills } : {}),
      ...(req.prompts?.length ? { prompts: req.prompts } : {}),
      // MCP tools the content requires (the form edits them: contentFormRefs.ts).
      ...(req.mcp?.length ? { mcp: req.mcp } : {}),
      ...(req.permissions?.length ? { permissions: req.permissions } : {}),
    };
  }
  const skills = f.skills
    .map((s) => ({ name: trimmed(s.name), description: trimmed(s.description), body: s.body.trim() }))
    .filter((s) => s.name || s.description || s.body);
  if (skills.length > 0) out.skills = skills;
  if (f.kind === "assist") {
    const a = f.assist ?? { output: "text" };
    out.instructions = f.instructions.trim();
    out.output = a.output;
    const fields = (a.fields ?? []).map(assistFieldPayload).filter((x) => x.name);
    if (a.output === "json" && fields.length > 0) out.fields = fields;
    if ((a.context ?? []).length > 0) out.context = a.context;
    if (a.gate?.question.trim()) out.gate = { question: a.gate.question.trim(), threshold: a.gate.threshold };
  }
  if (f.kind === "expert") out.instructions = f.instructions.trim();
  if (f.kind === "expert" || f.kind === "room") {
    const welcome = trimmed(f.welcome);
    if (welcome) out.welcome = welcome;
    // A quick prompt that points at a pack's prompt keeps its ref.
    const qp = f.quickPrompts
      .map((q) => (q.ref && !q.prompt.trim() ? { title: trimmed(q.title), ref: q.ref } : { title: trimmed(q.title), prompt: q.prompt.trim() }))
      .filter((q) => q.title || ("prompt" in q ? q.prompt : q.ref));
    if (qp.length > 0) out.quickPrompts = qp;
  }
  if (f.kind === "room") {
    const room = f.room;
    // A room's instructions (INSTRUCTIONS.md) become its expert where it
    // is installed.
    if (f.instructions.trim()) out.instructions = f.instructions.trim();
    if (room?.sections.length) out.sections = room.sections;
    if (room?.channels.length) out.channels = room.channels;
    if (room?.schedules.length) out.schedules = room.schedules;
    if (room?.apps.length) out.apps = room.apps;
    if (room?.appSnapshots.length) out.appSnapshots = room.appSnapshots;
    if (room?.files.length) out.files = room.files;
    if (room?.sources.length) out.sources = room.sources;
  }
  if (f.kind === "channel-preset" && f.channel) out.channel = f.channel;
  if (f.kind === "expert") {
    const roles = (f.team?.members ?? [])
      .map((m) => ({ role: trimmed(m.role), expert: trimmed(m.expert), defaultAgent: trimmed(m.defaultAgent), description: trimmed(m.description) }))
      .filter((m) => m.role || m.expert);
    if (roles.length > 0) {
      const lead = trimmed(f.team?.lead ?? "");
      out.team = {
        ...(lead ? { lead } : {}),
        members: roles.map((m) => ({ role: m.role, expert: m.expert, defaultAgent: m.defaultAgent, ...(m.description ? { description: m.description } : {}) })),
      };
    }
  }
  return out;
}

// cleanTags lower-cases, trims and de-duplicates tags (the validator takes
// lower-case letters, digits, spaces, "_" and "-").
export function cleanTags(tags: readonly string[]): string[] {
  const out: string[] = [];
  for (const t of tags) {
    const v = t.trim().toLowerCase();
    if (v && !out.includes(v)) out.push(v);
  }
  return out;
}

// splitList reads a comma- or newline-separated field (tags, requirements).
export function splitList(s: string): string[] {
  return s
    .split(/[,\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

// isTeam: an expert with a roster.
export function isTeam(c: Pick<ContentComponent, "team"> | null | undefined): boolean {
  return !!c?.team && c.team.members.length > 0;
}

// usageLine is a card's usage: "12 runs · 3 members" (experts), "4
// members" (packs); "" when unused. A count at the server's cap reads "+".
export function usageLine(c: Pick<ContentComponent, "kind" | "runs" | "members">, cap = 9999): string {
  const n = (v: number, word: string) => `${v}${v >= cap ? "+" : ""} ${word}${v === 1 ? "" : "s"}`;
  const parts: string[] = [];
  if (c.kind === "expert" && (c.runs ?? 0) > 0) parts.push(n(c.runs!, "run"));
  if ((c.members ?? 0) > 0) parts.push(n(c.members!, "member"));
  return parts.join(" · ");
}

// TAG is the server's one tag rule (wire.ContentTagPattern): lower-case
// letters of any script, digits, spaces, '_' and '-'.
const TAG = new RegExp(ContentTagPattern, "u");

// tagProblem is the first tag the server would refuse, worded for the form
// (every content form checks tags through it).
export function tagProblem(tags: readonly string[]): string | null {
  for (const t of cleanTags(tags)) {
    if (!TAG.test(t) || [...t].length > MaxContentTagRunes) {
      return `The tag "${t}" uses lower-case letters (any language), digits, spaces, "_" and "-" (at most ${MaxContentTagRunes} characters)`;
    }
  }
  return null;
}

// formProblem is the first thing the form must fix before saving (the
// server validates everything; this only catches the obvious early).
export function formProblem(f: ContentForm): string | null {
  const name = f.name.trim();
  if (!name) return "Give it a name";
  if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)) return "The name uses lower-case letters, digits and hyphens";
  if (f.kind === "expert" && !f.instructions.trim()) return "An expert needs instructions";
  if (f.kind === "assist") {
    if (!f.instructions.trim()) return "An AI assist needs instructions";
    const fields = (f.assist?.fields ?? []).filter((x) => x.name.trim() || x.description?.trim());
    if (f.assist?.output === "json" && fields.length === 0) return "A JSON assist names the fields of its reply";
    for (const x of fields) {
      if (!ASSIST_FIELD_NAME.test(x.name.trim())) return "Each field name uses letters, digits and _ (starting with a letter)";
      if (x.type !== "items") continue;
      const item = (x.item ?? []).filter((y) => y.name.trim());
      if (item.length === 0) return `The items field ${x.name.trim()} names its items' fields`;
      for (const y of item) if (!ASSIST_FIELD_NAME.test(y.name.trim())) return `Each item field of ${x.name.trim()} uses letters, digits and _ (starting with a letter)`;
    }
  }
  const tag = tagProblem(f.tags ?? []);
  if (tag) return tag;
  for (const r of [...(f.requires?.skills ?? []), ...(f.requires?.prompts ?? [])]) {
    if (!/^[a-z0-9][a-z0-9-]{0,62}(@\S+)?$/.test(r.trim())) return `The requirement "${r}" reads name or name@range`;
  }
  const roles = (f.team?.members ?? []).filter((m) => m.role.trim() || m.expert.trim());
  if (f.kind === "expert" && roles.length > 8) return "A team has at most 8 roles";
  const seen = new Set<string>();
  for (const m of roles) {
    const role = m.role.trim();
    if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(role)) return "Each team role needs a name of lower-case letters, digits and hyphens";
    if (seen.has(role)) return `The role ${role} is listed twice`;
    seen.add(role);
    if (!/^[a-z0-9][a-z0-9-]{0,62}(@\S+)?$/.test(m.expert.trim())) return `The role ${role} needs an expert as name or name@range`;
    if (!parseDefaultAgent(m.defaultAgent)) return `The role ${role} needs an agent: your own, a platform agent, or a member's`;
  }
  const skills = f.skills.filter((s) => s.name.trim() || s.description.trim() || s.body.trim());
  if (f.kind === "skill" && skills.length === 0) return "A skill pack needs at least one skill";
  for (const s of skills) {
    if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(s.name.trim())) return "Each skill needs a name of lower-case letters, digits and hyphens";
    if (!s.description.trim()) return `Describe when to use the skill ${s.name.trim()}`;
  }
  return null;
}

// compareVersions orders two semantic versions (prerelease below release).
export function compareVersions(a: string, b: string): number {
  const parse = (v: string) => {
    const [core, pre = ""] = v.replace(/^v/, "").split("+")[0].split(/-(.*)/s);
    return { nums: core.split(".").map((n) => Number.parseInt(n, 10) || 0), pre };
  };
  const pa = parse(a);
  const pb = parse(b);
  for (let i = 0; i < 3; i++) {
    const d = (pa.nums[i] ?? 0) - (pb.nums[i] ?? 0);
    if (d !== 0) return d < 0 ? -1 : 1;
  }
  if (pa.pre === pb.pre) return 0;
  if (!pa.pre) return 1;
  if (!pb.pre) return -1;
  return pa.pre < pb.pre ? -1 : 1;
}

// updateAvailable reports a pin behind the component's latest version.
export function updateAvailable(pinned: string | undefined, latest: string): boolean {
  return !!pinned && compareVersions(latest, pinned) > 0;
}

export function contentLabel(c: Pick<ContentComponent, "displayName" | "name">): string {
  return c.displayName?.trim() || c.name;
}

// The permission vocabulary, in member words.
export function permissionLabel(p: string): string {
  if (p === "scripts") return "Runs scripts";
  if (p === "mcp.stdio") return "Runs a local tool";
  if (p.startsWith("mcp.http:")) return `Calls ${p.slice("mcp.http:".length)}`;
  return p;
}

// needsConfirmation: someone else's component that declares permissions
// the member has not confirmed for this version yet.
export function needsConfirmation(c: Pick<ContentComponent, "mine" | "acked" | "permissions">): boolean {
  return !c.mine && !c.acked && c.permissions.length > 0;
}

// matchesContent is the Library search: name, display name, description,
// category, tags and skill names, case-insensitive.
export function matchesContent(c: ContentComponent, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  const hay = [c.name, c.displayName, c.description, c.category, ...(c.tags ?? []), ...(c.skills ?? []).map((s) => s.name), ...(c.prompts ?? []).map((p) => p.title)]
    .filter(Boolean)
    .join("\n")
    .toLowerCase();
  return hay.includes(needle);
}

// A composer expert choice: the component and the version a send pins
// (a room's pinned version, else the latest, which the server pins).
export interface ExpertChoice {
  readonly component: ContentComponent;
  readonly version?: string;
  // Where it came from: the member's own Library or the room's pins.
  readonly source: "mine" | "room";
}

// expertChoices merges the room's pinned experts (first: the room chose
// them) with the member's own, one entry per component.
export function expertChoices(mine: readonly ContentComponent[], roomUses: readonly ContentUse[]): ExpertChoice[] {
  const out: ExpertChoice[] = [];
  const seen = new Set<string>();
  for (const u of roomUses) {
    if (u.kind !== "expert" || !u.component || seen.has(u.componentId)) continue;
    seen.add(u.componentId);
    out.push({ component: u.component, version: u.pinnedVersion || undefined, source: "room" });
  }
  for (const c of mine) {
    if (c.kind !== "expert" || seen.has(c.id)) continue;
    seen.add(c.id);
    out.push({ component: c, source: "mine" });
  }
  return out;
}

// expertMentionMatches filters expert choices whose name or display name
// starts with the typed query (case-insensitive), at most `limit`.
export function expertMentionMatches(choices: readonly ExpertChoice[], query: string, limit = 5): ExpertChoice[] {
  const q = query.toLowerCase();
  if (q.includes("/")) return [];
  return choices
    .filter((e) => e.component.name.toLowerCase().startsWith(q) || contentLabel(e.component).toLowerCase().startsWith(q))
    .slice(0, limit);
}

export function chatExpert(choice: ExpertChoice): ChatExpert {
  return choice.version ? { id: choice.component.id, version: choice.version } : { id: choice.component.id };
}

// ExpertMarker is payload.expert of a chat message an agent was asked to
// answer as an expert.
export interface ExpertMarker {
  readonly id: string;
  readonly name: string;
  readonly displayName?: string;
  readonly version: string;
  // An unsaved draft worn for a quick try (the builder's components try).
  readonly draft?: boolean;
}

export function expertMarker(payload: unknown): ExpertMarker | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const e = (payload as { expert?: unknown }).expert;
  if (!e || typeof e !== "object") return undefined;
  const { id, name, displayName, version, draft } = e as Record<string, unknown>;
  if (typeof id !== "string" || !id || typeof name !== "string") return undefined;
  return {
    id,
    name,
    displayName: typeof displayName === "string" && displayName ? displayName : undefined,
    version: typeof version === "string" ? version : "",
    ...(draft === true ? { draft: true } : {}),
  };
}

// expertBadge is how a message or run names its expert: "Reviewer v1.2.0".
export function expertBadge(e: { readonly name: string; readonly displayName?: string; readonly version?: string }): string {
  const label = e.displayName?.trim() || e.name;
  return e.version ? `${label} v${e.version}` : label;
}

// archiveContentType is the upload Content-Type of an import file.
export function archiveContentType(fileName: string): string {
  const n = fileName.toLowerCase();
  if (n.endsWith(".zip")) return "application/zip";
  if (n.endsWith(".tar.gz") || n.endsWith(".tgz")) return "application/gzip";
  return "application/x-tar";
}

export const ARCHIVE_ACCEPT = ".zip,.tar,.tar.gz,.tgz";
