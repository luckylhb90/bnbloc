// Sharing and publishing content components (docs/prd/2026-10-12-experts-
// and-teams.md, step 4): the wire shapes of shares, publish requests,
// reviews and admin listings, and the pure helpers the Library and the
// consoles render them with. Calls are in contentSharingApi.ts.
import { contentLabel, isBuiltin, type ContentComponent, type ContentPublishRequest } from "./contentComponents.ts";

export type { ContentAdminComponent, ContentCopy, ContentFileChange, ContentPublishReply, ContentReview, ContentRoomShare, ContentShareReply, ContentShares } from "./wire.gen.ts";
import type { ContentFileChange, ContentShareReply } from "./wire.gen.ts";

// tenantScopeLabel describes where platform content reaches.
export function tenantScopeLabel(tenants: readonly string[] | undefined): string {
  if (!tenants || tenants.length === 0) return "Every organization";
  return tenants.length === 1 ? `Only ${tenants[0]}` : `${tenants.length} organizations: ${tenants.join(", ")}`;
}

// tenantScopeBody is the PUT /v1/platform/components/{name}/tenants body
// for a list typed into a field: an empty list lifts the limit explicitly
// (allTenants), never an empty enabledTenants, which the gateway refuses.
export function tenantScopeBody(text: string): { allTenants: true } | { enabledTenants: string[] } {
  const ids = parseMembers(text);
  return ids.length === 0 ? { allTenants: true } : { enabledTenants: ids };
}

// A Library card's actions: only the author shares and publishes their
// own component; a copy is updated from its author; built-in content does
// neither.
export function canShare(c: Pick<ContentComponent, "mine" | "scope">): boolean {
  return c.mine && !isBuiltin(c);
}

export function updateAvailable(c: Pick<ContentComponent, "scope" | "sourceLatestVersion">): boolean {
  return c.scope === "shared" && !!c.sourceLatestVersion;
}

// parseMembers reads member ids typed into a field: separated by commas,
// spaces, semicolons or new lines, each once, in order.
export function parseMembers(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/[\s,;]+/)) {
    const m = raw.trim();
    if (!m || seen.has(m)) continue;
    seen.add(m);
    out.push(m);
  }
  return out;
}

function listNames(names: readonly string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

// shareSummary is the toast after a share: who got it, who already had
// it, who was skipped and why.
export function shareSummary(r: ContentShareReply): string {
  const given = r.shared.filter((s) => s.status !== "unchanged").map((s) => s.member);
  const had = r.shared.filter((s) => s.status === "unchanged").map((s) => s.member);
  const parts: string[] = [];
  if (given.length > 0) parts.push(`Shared version ${r.version} with ${listNames(given)}`);
  if (had.length > 0) parts.push(`${listNames(had)} already ${had.length === 1 ? "has" : "have"} it`);
  if (r.skipped.length > 0) parts.push(`skipped ${r.skipped.map((s) => `${s.member} (${s.reason})`).join(", ")}`);
  if (r.room) parts.push("people who join the room later get it too");
  return parts.length > 0 ? `${parts.join("; ")}.` : "Nobody to share with.";
}

// resubmittedMessage is the toast after saving a new version of a
// component the organization uses: the gateway sent it for approval.
export function resubmittedMessage(r: Pick<ContentPublishRequest, "version" | "approvedVersion"> | undefined | null): string | null {
  if (!r) return null;
  const keep = r.approvedVersion ? `; members keep using ${r.approvedVersion} until then` : "";
  return `Version ${r.version} was sent to your organization's admins for approval${keep}.`;
}

export type PublicationTone = "accent" | "warning" | "muted";

// publicationStatus is the line an author sees on their own component:
// what the organization uses, and the latest request.
export function publicationStatus(c: Pick<ContentComponent, "publication">): { text: string; tone: PublicationTone } | null {
  const p = c.publication;
  if (!p) return null;
  const req = p.request;
  const live = p.orgStatus === "active" && p.orgVersion;
  if (req?.status === "pending") {
    return { text: live ? `Version ${req.version} waits for approval; your organization uses ${p.orgVersion}` : `Version ${req.version} waits for an admin's approval`, tone: "warning" };
  }
  if (req?.status === "declined") {
    const note = req.decisionNote ? `: ${req.decisionNote}` : "";
    return { text: `Version ${req.version} was not approved${note}${live ? `. Your organization uses ${p.orgVersion}` : ""}`, tone: "warning" };
  }
  if (live) return { text: `In your organization (version ${p.orgVersion})`, tone: "accent" };
  if (p.orgStatus === "retired") return { text: "Unpublished from your organization", tone: "muted" };
  return null;
}

// isPublishedVersion: the organization already uses the latest version
// (publishing again would be refused).
export function isPublishedVersion(c: Pick<ContentComponent, "publication" | "latestVersion">): boolean {
  const p = c.publication;
  return !!p && p.orgStatus === "active" && p.orgVersion === c.latestVersion;
}

export function hasPendingRequest(c: Pick<ContentComponent, "publication">): boolean {
  return c.publication?.request?.status === "pending";
}

// requestTitle names a request in the console: "Reviewer v1.2.0".
export function requestTitle(r: Pick<ContentPublishRequest, "displayName" | "name" | "version">): string {
  return `${contentLabel({ displayName: r.displayName, name: r.name })} v${r.version}`;
}

export function fileChangeLabel(f: Pick<ContentFileChange, "change">): string {
  return f.change === "added" ? "Added" : f.change === "removed" ? "Removed" : "Changed";
}

// promoteCommands are the offline steps a platform admin runs to promote
// one version to every organization (the console never signs).
export function promoteCommands(c: Pick<ContentComponent, "id" | "name" | "latestVersion">, version = c.latestVersion): string[] {
  const file = `${c.name}-${version}.package.json`;
  return [
    `bnbloc content promote ${c.id} --version ${version} --publisher <your-publisher-id>`,
    `# or, signing with your own tool:`,
    `bnbloc content export ${c.id} --version ${version} --out ${file}`,
    `bnbloc cores install --package ${file} --signature ${file}.minisig`,
  ];
}

export interface SharedPrompt {
  readonly packId: string;
  readonly packName: string;
  readonly author: string;
  readonly id: string;
  readonly title: string;
  readonly body: string;
  readonly slash?: string;
}

// sharedPrompts are the prompts of the prompt packs published to the
// member's organization or the platform (the Plaza's showcase), most
// recently updated packs first, at most limit.
export function sharedPrompts(packs: readonly ContentComponent[], limit: number): SharedPrompt[] {
  const out: SharedPrompt[] = [];
  const shared = packs.filter((p) => p.kind === "prompt" && (p.scope === "org" || p.scope === "platform")).sort((a, b) => b.updatedAtMs - a.updatedAtMs);
  for (const p of shared) {
    for (const q of p.prompts ?? []) {
      if (out.length >= limit) return out;
      out.push({ packId: p.id, packName: contentLabel(p), author: p.owner, id: q.id, title: q.title, body: q.body, ...(q.slash ? { slash: q.slash } : {}) });
    }
  }
  return out;
}
