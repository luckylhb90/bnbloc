// Content components (docs/prd/2026-10-12-experts-and-teams.md): experts and
// skill packs. The shapes mirror internal/wire/content_components.go; the
// helpers here are pure (form <-> payload mapping, version and update logic,
// expert mentions, the chat payload marker) so they are unit-tested.

export type ContentKind = "expert" | "skill";

export interface ContentSkill {
  readonly name: string;
  readonly description: string;
}

export interface ContentQuickPrompt {
  readonly title: string;
  readonly prompt: string;
}

export interface ContentRequires {
  readonly skills?: readonly string[];
  readonly prompts?: readonly string[];
  readonly mcp?: readonly string[];
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
  readonly welcome?: string;
  readonly mine: boolean;
  readonly acked: boolean;
  readonly uses: number;
  readonly createdAtMs: number;
  readonly updatedAtMs: number;
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

export type ContentUseScope = "member" | "room";

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
  // Kept as they were: the form does not edit requirements or permissions,
  // but a new version must not drop what the base declared.
  requires: ContentRequires;
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
    requires: {},
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
    requires: c.requires ?? {},
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
  for (const key of ["displayName", "description", "category"] as const) {
    const v = trimmed(f[key]);
    if (v) out[key] = v;
  }
  const req = f.requires ?? {};
  if ((req.skills?.length ?? 0) + (req.prompts?.length ?? 0) + (req.permissions?.length ?? 0) > 0) {
    out.requires = {
      ...(req.skills?.length ? { skills: req.skills } : {}),
      ...(req.prompts?.length ? { prompts: req.prompts } : {}),
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
    const qp = f.quickPrompts.map((q) => ({ title: trimmed(q.title), prompt: q.prompt.trim() })).filter((q) => q.title || q.prompt);
    if (qp.length > 0) out.quickPrompts = qp;
  }
  return out;
}

// formProblem is the first thing the form must fix before saving (the
// server validates everything; this only catches the obvious early).
export function formProblem(f: ContentForm): string | null {
  const name = f.name.trim();
  if (!name) return "Give it a name";
  if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)) return "The name uses lower-case letters, digits and hyphens";
  if (f.kind === "expert" && !f.instructions.trim()) return "An expert needs instructions";
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
  const hay = [c.name, c.displayName, c.description, c.category, ...(c.tags ?? []), ...(c.skills ?? []).map((s) => s.name)]
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
