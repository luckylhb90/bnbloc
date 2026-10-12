// Content components (docs/prd/2026-10-12-experts-and-teams.md): experts and
// skill packs. The shapes mirror internal/wire/content_components.go; the
// helpers here are pure (form <-> payload mapping, version and update logic,
// expert mentions, the chat payload marker) so they are unit-tested.

import type { ContentPrompt, PromptRef } from "./promptPacks.ts";
import { parseDefaultAgent, type ContentTeam } from "./teams.ts";

export type ContentKind = "expert" | "skill" | "prompt";

// The built-in builder expert (shipped with the release, read-only, listed
// for every member with scope "builtin"): it interviews the member, drafts
// an expert or skill pack and saves it. A protocol constant of the product's
// own built-in content, not deployment data.
export const BUILDER_EXPERT_ID = "cc_builtin_expert-builder";

// isBuiltin: content that ships with the release (read-only, owned by no
// member).
export function isBuiltin(c: Pick<ContentComponent, "scope">): boolean {
  return c.scope === "builtin";
}

// The manifest kinds of content packages, by short kind.
export const CONTENT_PACKAGE_KINDS: Readonly<Record<string, ContentKind>> = {
  "bnbloc.expert.package/v1": "expert",
  "bnbloc.skill.package/v1": "skill",
  "bnbloc.prompt.package/v1": "prompt",
};

export function contentKindWord(kind: ContentKind): string {
  return kind === "skill" ? "skill pack" : kind === "prompt" ? "prompt pack" : "expert";
}

export interface ContentSkill {
  readonly name: string;
  readonly description: string;
}

// A quick prompt is its text, or ref ("pack/prompt-id") to a prompt of a
// pack the expert requires, resolved for the viewer when they may use it.
export interface ContentQuickPrompt {
  readonly title: string;
  readonly prompt: string;
  readonly ref?: string;
  readonly resolved?: PromptRef;
}

// An MCP tool component a package requires, and the tool's variables it
// needs set (docs/architecture/content-components.md, MCP tool components).
export interface ContentMCPRequirement {
  readonly name: string;
  readonly env?: readonly string[];
}

export interface ContentRequires {
  readonly skills?: readonly string[];
  readonly prompts?: readonly string[];
  readonly mcp?: readonly ContentMCPRequirement[];
  readonly permissions?: readonly string[];
}

export interface ContentComponent {
  readonly id: string;
  readonly kind: ContentKind;
  readonly name: string;
  readonly displayName?: string;
  readonly description?: string;
  readonly category?: string;
  readonly tags?: readonly string[];
  readonly icon?: string;
  readonly owner: string;
  readonly scope: string;
  readonly status: string;
  readonly latestVersion: string;
  readonly version: string;
  readonly permissions: readonly string[];
  readonly needsSandbox?: boolean;
  readonly requires: ContentRequires;
  readonly skills?: readonly ContentSkill[];
  readonly quickPrompts?: readonly ContentQuickPrompt[];
  // A prompt pack's prompts (lib/promptPacks.ts).
  readonly prompts?: readonly ContentPrompt[];
  readonly welcome?: string;
  // mine: the caller authors it (a personal component of theirs: new
  // versions, sharing, publishing). removable: they may delete it (theirs,
  // or a copy someone shared with them).
  readonly mine: boolean;
  readonly removable?: boolean;
  // A shared copy: its author (owner is the holder), the author's
  // component it came from, and the author's newer version when there is
  // one (lib/contentSharing.ts).
  readonly sharedBy?: string;
  readonly sourceId?: string;
  readonly sourceLatestVersion?: string;
  // The author's own component: its organization publication.
  readonly publication?: ContentPublication;
  readonly acked: boolean;
  readonly uses: number;
  // Usage: an expert's runs and their distinct requesters; a pack's
  // members who turned it on (capped server-side).
  readonly runs?: number;
  readonly members?: number;
  // A team expert's roster (lib/teams.ts).
  readonly team?: ContentTeam;
  readonly createdAtMs: number;
  readonly updatedAtMs: number;
}

export interface ContentPublishRequest {
  readonly id: string;
  readonly componentId: string;
  readonly kind: ContentKind;
  readonly name: string;
  readonly displayName?: string;
  readonly version: string;
  readonly requestedBy: string;
  readonly note?: string;
  readonly status: "pending" | "approved" | "declined" | "withdrawn";
  readonly decidedBy?: string;
  readonly decisionNote?: string;
  readonly decidedAtMs?: number;
  readonly createdAtMs: number;
  // The version the organization uses now ("" before a first approval).
  readonly approvedVersion?: string;
}

export interface ContentPublication {
  readonly orgComponentId?: string;
  readonly orgVersion?: string;
  readonly orgStatus?: string;
  readonly request?: ContentPublishRequest;
}

export interface ContentVersion {
  readonly version: string;
  readonly sha256: string;
  readonly bundleKey: string;
  readonly bundleUrl: string;
  readonly sizeBytes: number;
  readonly createdBy: string;
  readonly createdAtMs: number;
}

export interface ContentComponentDetail {
  readonly component: ContentComponent;
  readonly versions: readonly ContentVersion[];
}

export interface ContentFile {
  readonly path: string;
  readonly sizeBytes: number;
  readonly text?: string;
  readonly binary?: boolean;
}

export interface ContentVersionDetail {
  readonly component: ContentComponent;
  readonly version: ContentVersion;
  readonly instructions?: string;
  readonly skillBodies?: Readonly<Record<string, string>>;
  readonly files: readonly ContentFile[];
}

// "room-expert" is a room's expert: one of its pinned experts that every
// agent turn in the room wears.
export type ContentUseScope = "member" | "room" | "room-expert";

export interface ContentUse {
  readonly scope: ContentUseScope;
  readonly scopeId: string;
  readonly componentId: string;
  readonly kind: ContentKind;
  readonly pinnedVersion?: string;
  readonly createdBy: string;
  readonly createdAtMs: number;
  readonly component?: ContentComponent;
  readonly updateAvailable?: boolean;
}

export interface ContentUsesReply {
  readonly member: readonly ContentUse[];
  readonly room?: readonly ContentUse[];
  // The id of the room's expert, when it has one.
  readonly roomExpert?: string;
}

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
  };
}

function trimmed(s: string): string {
  return s.trim();
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
  if ((req.skills?.length ?? 0) + (req.prompts?.length ?? 0) + (req.mcp?.length ?? 0) + (req.permissions?.length ?? 0) > 0) {
    out.requires = {
      ...(req.skills?.length ? { skills: req.skills } : {}),
      ...(req.prompts?.length ? { prompts: req.prompts } : {}),
      // MCP tools carry over unchanged (the form does not edit them).
      ...(req.mcp?.length ? { mcp: req.mcp } : {}),
      ...(req.permissions?.length ? { permissions: req.permissions } : {}),
    };
  }
  const skills = f.skills
    .map((s) => ({ name: trimmed(s.name), description: trimmed(s.description), body: s.body.trim() }))
    .filter((s) => s.name || s.description || s.body);
  if (skills.length > 0) out.skills = skills;
  if (f.kind === "expert") {
    out.instructions = f.instructions.trim();
    const welcome = trimmed(f.welcome);
    if (welcome) out.welcome = welcome;
    // A quick prompt that points at a pack's prompt keeps its ref.
    const qp = f.quickPrompts
      .map((q) => (q.ref && !q.prompt.trim() ? { title: trimmed(q.title), ref: q.ref } : { title: trimmed(q.title), prompt: q.prompt.trim() }))
      .filter((q) => q.title || ("prompt" in q ? q.prompt : q.ref));
    if (qp.length > 0) out.quickPrompts = qp;
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

// formProblem is the first thing the form must fix before saving (the
// server validates everything; this only catches the obvious early).
export function formProblem(f: ContentForm): string | null {
  const name = f.name.trim();
  if (!name) return "Give it a name";
  if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)) return "The name uses lower-case letters, digits and hyphens";
  if (f.kind === "expert" && !f.instructions.trim()) return "An expert needs instructions";
  for (const t of cleanTags(f.tags ?? [])) {
    if (!/^[a-z0-9][a-z0-9 _-]*$/.test(t) || t.length > 32) return `The tag "${t}" uses lower-case letters, digits, spaces, "_" and "-" (at most 32)`;
  }
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

// The @-mention handle of an expert ("@reviewer"): agent handles always
// carry a "/", member ids are chosen by people, so an expert's handle is
// its bare component name and is resolved before members.
export function expertHandle(c: Pick<ContentComponent, "name">): string {
  return c.name;
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

// The chat send's expert field.
export interface ChatExpert {
  readonly id: string;
  readonly version?: string;
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
}

export function expertMarker(payload: unknown): ExpertMarker | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const e = (payload as { expert?: unknown }).expert;
  if (!e || typeof e !== "object") return undefined;
  const { id, name, displayName, version } = e as Record<string, unknown>;
  if (typeof id !== "string" || !id || typeof name !== "string") return undefined;
  return {
    id,
    name,
    displayName: typeof displayName === "string" && displayName ? displayName : undefined,
    version: typeof version === "string" ? version : "",
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

// formatBytes for bundle and file sizes.
export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
