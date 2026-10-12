// The Library form's reference editors (docs/prd/2026-10-12-experts-and-
// teams.md, "Gap closure" 8): an expert quick prompt that points at a pack
// prompt, and the MCP tool components an expert or skill pack requires.
// Pure helpers; the gateway's save check stays the authority (it refuses a
// reference to a tool the author may not use, an undeclared variable, or a
// missing permission).

import type { ComponentMCPEnv, MemberComponent } from "./apiTypes.ts";
import type { ContentComponent, ContentMCPRequirement, ContentQuickPrompt, ContentRequires } from "./contentComponents.ts";

// ---- quick prompt refs

// PackPromptOption is one prompt of a prompt pack the member can see, as
// the quick prompt picker lists it.
export interface PackPromptOption {
  readonly ref: string; // "<pack name>/<prompt id>"
  readonly pack: string;
  readonly label: string; // "Pack · prompt title"
  readonly range: string; // "^<major>" of the pack's latest version
}

function packName(requirement: string): string {
  const at = requirement.indexOf("@");
  return (at < 0 ? requirement : requirement.slice(0, at)).trim();
}

function majorRange(version: string): string {
  const major = /^v?(\d+)/.exec(version.trim())?.[1];
  return major ? `^${major}` : "*";
}

// packPromptOptions lists the prompts of the visible prompt packs, each
// pack once (several visible packs may share a name: the first listed, the
// member's own first, wins, as the reference resolves by name).
export function packPromptOptions(packs: readonly ContentComponent[]): PackPromptOption[] {
  const seen = new Set<string>();
  const out: PackPromptOption[] = [];
  for (const p of [...packs].sort((a, b) => Number(!!b.mine) - Number(!!a.mine))) {
    if (p.kind !== "prompt" || seen.has(p.name)) continue;
    seen.add(p.name);
    for (const pr of p.prompts ?? []) {
      out.push({ ref: `${p.name}/${pr.id}`, pack: p.name, label: `${p.displayName || p.name} · ${pr.title}`, range: majorRange(p.latestVersion) });
    }
  }
  return out;
}

// pickPromptRef points quick prompt i at a pack prompt: its text is
// cleared (a ref saves instead of text; the title stays optional) and the
// pack is added to requires.prompts as name@^major unless it is already
// required (an existing range for that pack is kept).
export function pickPromptRef(
  quickPrompts: readonly ContentQuickPrompt[],
  requires: ContentRequires,
  i: number,
  option: PackPromptOption,
): { quickPrompts: ContentQuickPrompt[]; requires: ContentRequires } {
  const next = quickPrompts.map((q, j) => (j === i ? { title: q.title, prompt: "", ref: option.ref } : q));
  const prompts = requires.prompts ?? [];
  const required = prompts.some((r) => packName(r) === option.pack);
  return { quickPrompts: next, requires: required ? requires : { ...requires, prompts: [...prompts, `${option.pack}@${option.range}`] } };
}

// clearPromptRef turns quick prompt i back into a text quick prompt (the
// pack stays required: other quick prompts may use it, and the author
// edits the requirement list).
export function clearPromptRef(quickPrompts: readonly ContentQuickPrompt[], i: number): ContentQuickPrompt[] {
  return quickPrompts.map((q, j) => (j === i ? { title: q.title, prompt: q.prompt } : q));
}

// ---- MCP tool requirements

const MCP_STDIO = "mcp.stdio";
const MCP_HTTP = "mcp.http:";

// urlHost is a URL's host with its port when it names one, lower-cased
// (what the gateway's mcp.http:<host> names; userinfo dropped).
export function urlHost(raw: string): string {
  const rest = raw.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, "");
  const authority = rest.split(/[/?#]/, 1)[0] ?? "";
  return authority.slice(authority.lastIndexOf("@") + 1).toLowerCase();
}

// mcpPermission is the permission content declares for a tool: mcp.stdio
// for a stdio tool, mcp.http:<host> for an http tool ("" unknown).
export function mcpPermission(tool: MemberComponent): string {
  const http = tool.mcp?.transport === "http" || tool.runtime === "endpoint";
  if (!http) return MCP_STDIO;
  const host = urlHost(tool.endpoint?.baseUrl ?? "");
  return host ? MCP_HTTP + host : "";
}

function isMcpPermission(p: string): boolean {
  return p === MCP_STDIO || p.startsWith(MCP_HTTP);
}

// withMcp sets requires.mcp and recomputes the MCP permissions from the
// selected tools: the permission each visible tool needs is declared, one
// no tool needs any more is dropped, and other permissions (scripts) stay
// as they were. While an entry names a tool no longer visible, its
// permission cannot be known, so the declared MCP permissions are kept.
export function withMcp(requires: ContentRequires, mcp: readonly ContentMCPRequirement[], tools: readonly MemberComponent[]): ContentRequires {
  const byName = new Map(tools.map((t) => [t.name, t]));
  const perms = requires.permissions ?? [];
  const unknown = mcp.some((m) => !byName.has(m.name));
  const out = perms.filter((p) => !isMcpPermission(p) || unknown);
  for (const m of mcp) {
    const t = byName.get(m.name);
    const p = t ? mcpPermission(t) : "";
    if (p && !out.includes(p)) out.push(p);
  }
  const next: ContentRequires = { ...requires, mcp: [...mcp], permissions: out };
  return next;
}

// addMcpTool requires a tool (no variables ticked yet; its required ones
// are always needed).
export function addMcpTool(requires: ContentRequires, tool: MemberComponent, tools: readonly MemberComponent[]): ContentRequires {
  const mcp = requires.mcp ?? [];
  if (mcp.some((m) => m.name === tool.name)) return requires;
  return withMcp(requires, [...mcp, { name: tool.name }], tools);
}

// removeMcpTool drops a tool's requirement (and a permission only it needed).
export function removeMcpTool(requires: ContentRequires, name: string, tools: readonly MemberComponent[]): ContentRequires {
  return withMcp(
    requires,
    (requires.mcp ?? []).filter((m) => m.name !== name),
    tools,
  );
}

// envChoices are a tool's variables for the form: a required one is always
// needed (shown ticked, not editable), an optional one is needed when the
// content lists it.
export interface EnvChoice {
  readonly env: ComponentMCPEnv;
  readonly checked: boolean;
  readonly locked: boolean;
}

export function envChoices(tool: MemberComponent, req: ContentMCPRequirement): EnvChoice[] {
  const listed = new Set(req.env ?? []);
  return (tool.mcp?.env ?? []).map((e) => ({ env: e, checked: !!e.required || listed.has(e.name), locked: !!e.required }));
}

// toggleEnv ticks or unticks an optional variable of a required tool. The
// saved list holds the optional variables the content needs (required ones
// are always needed, so they are not repeated); names the tool no longer
// declares are dropped.
export function toggleEnv(requires: ContentRequires, tool: MemberComponent, name: string, on: boolean): ContentRequires {
  const optional = new Set((tool.mcp?.env ?? []).filter((e) => !e.required).map((e) => e.name));
  if (!optional.has(name)) return requires;
  const mcp = (requires.mcp ?? []).map((m) => {
    if (m.name !== tool.name) return m;
    const env = new Set((m.env ?? []).filter((e) => optional.has(e)));
    if (on) env.add(name);
    else env.delete(name);
    const list = [...(tool.mcp?.env ?? [])].map((e) => e.name).filter((e) => env.has(e));
    return list.length > 0 ? { name: m.name, env: list } : { name: m.name };
  });
  return { ...requires, mcp };
}
