import { useState } from "react";
import { PlusIcon, TrashIcon, UsersThreeIcon } from "@phosphor-icons/react";
import type { MemberComponent } from "@/lib/apiTypes";
import { splitList, type ContentForm, type ContentFormRole, type ContentFormTeam, type ContentKind, type ContentQuickPrompt } from "@/lib/contentComponents";
import { addMcpTool, clearPromptRef, mcpPermission, pickPromptRef, removeMcpTool, settingChoices, toggleSetting, type PackPromptOption } from "@/lib/contentFormRefs";
import { defaultAgentLabel, formatDefaultAgent, parseDefaultAgent, type ContentTeam, type DefaultAgentKind } from "@/lib/teams";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Field, Input } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useDisplayNames } from "@/lib/useDisplayName";

// Library form fields beyond the text of a component
// (docs/prd/2026-10-12-experts-and-teams.md, step 6): tags, icon,
// requirements, and an expert's team roster.

// ListInput edits a comma-separated list; the text is the member's own
// while they type, the list is what saves.
function ListInput({
  value,
  onChange,
  placeholder,
  label,
}: {
  readonly value: readonly string[];
  readonly onChange: (v: string[]) => void;
  readonly placeholder: string;
  readonly label: string;
}) {
  const [text, setText] = useState(value.join(", "));
  return (
    <Input
      value={text}
      placeholder={placeholder}
      aria-label={label}
      onChange={(e) => {
        setText(e.target.value);
        onChange(splitList(e.target.value));
      }}
    />
  );
}

export function TagsIconFields({ form, patch }: { readonly form: ContentForm; readonly patch: (p: Partial<ContentForm>) => void }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Tags" hint="Comma separated, e.g. review, testing">
        <ListInput value={form.tags ?? []} onChange={(tags) => patch({ tags })} placeholder="review, testing" label="Tags" />
      </Field>
      <Field label="Icon" hint="An emoji or a short name">
        <Input value={form.icon ?? ""} onChange={(e) => patch({ icon: e.target.value })} placeholder="🧪" aria-label="Icon" />
      </Field>
    </div>
  );
}

// RequirementsFields edits what a component needs at use time: skill and
// prompt packs by name or name@range, the MCP tools it calls, and whether
// it runs scripts. MCP permissions follow the tools picked
// (contentFormRefs.ts); other declared permissions carry over unchanged.
export function RequirementsFields({
  form,
  patch,
  kind,
  tools,
}: {
  readonly form: ContentForm;
  readonly patch: (p: Partial<ContentForm>) => void;
  readonly kind: ContentKind;
  // The MCP tool components visible to the member (null while loading).
  readonly tools: readonly MemberComponent[] | null;
}) {
  const req = form.requires ?? {};
  const perms = req.permissions ?? [];
  const scripts = perms.includes("scripts");
  const others = perms.filter((p) => p !== "scripts" && p !== "mcp.stdio" && !p.startsWith("mcp.http:"));
  const setReq = (p: Partial<typeof req>) => patch({ requires: { ...req, ...p } });
  return (
    <div className="flex flex-col gap-3">
      <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Requirements</span>
      {kind === "expert" && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Skill packs" hint="name or name@range, e.g. lint-kit@^1">
            <ListInput value={req.skills ?? []} onChange={(skills) => setReq({ skills })} placeholder="lint-kit@^1" label="Required skill packs" />
          </Field>
          <Field label="Prompt packs" hint="For quick prompts that point at a pack's prompt">
            {/* Keyed by the list so a quick prompt's pick shows up here. */}
            <ListInput key={(req.prompts ?? []).join(",")} value={req.prompts ?? []} onChange={(prompts) => setReq({ prompts })} placeholder="release-notes@^1" label="Required prompt packs" />
          </Field>
        </div>
      )}
      <McpToolsEditor form={form} patch={patch} tools={tools} />
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={scripts}
          onChange={(e) => setReq({ permissions: e.target.checked ? [...perms, "scripts"] : perms.filter((p) => p !== "scripts") })}
        />
        Runs scripts (someone else's use runs in the agent sandbox)
      </label>
      {others.length > 0 && <p className="text-xs text-[var(--color-fg-subtle)]">Other declared permissions carry over: {others.join(", ")}</p>}
    </div>
  );
}

// McpToolsEditor picks the MCP tool components the content calls and the
// variables it needs set; the permission each tool's transport needs is
// declared with it.
function McpToolsEditor({
  form,
  patch,
  tools,
}: {
  readonly form: ContentForm;
  readonly patch: (p: Partial<ContentForm>) => void;
  readonly tools: readonly MemberComponent[] | null;
}) {
  const req = form.requires ?? {};
  const picked = req.mcp ?? [];
  const list = tools ?? [];
  const byName = new Map(list.map((t) => [t.name, t]));
  const addable = list.filter((t) => !picked.some((m) => m.name === t.name));
  return (
    <div className="flex flex-col gap-2">
      <span className="text-sm">Tools</span>
      {picked.length === 0 && (
        <p className="text-xs text-[var(--color-fg-subtle)]">
          {tools === null ? "Loading tools…" : list.length === 0 ? "No MCP tools are available to you." : "Pick the MCP tools this needs. Agents get them when it is used."}
        </p>
      )}
      {picked.map((m) => {
        const tool = byName.get(m.name);
        return (
          <div key={m.name} className="flex flex-col gap-1.5 rounded-[var(--radius)] border border-[var(--color-border)] p-3">
            <div className="flex items-center gap-2">
              <span className="font-mono text-sm">{m.name}</span>
              {tool ? (
                <span className="text-xs text-[var(--color-fg-subtle)]">{mcpPermission(tool) === "mcp.stdio" ? "runs on the agent's computer" : `calls ${mcpPermission(tool).slice("mcp.http:".length) || "a remote server"}`}</span>
              ) : (
                <Badge>{tools === null ? "loading" : "not available"}</Badge>
              )}
              <div className="flex-1" />
              <button
                type="button"
                onClick={() => patch({ requires: removeMcpTool(req, m.name, list) })}
                aria-label={`Remove tool ${m.name}`}
                title="Remove tool"
                className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]"
              >
                <TrashIcon size={16} />
              </button>
            </div>
            {tool ? (
              settingChoices(tool, m).map((c) => (
                <label key={c.setting.key} className="flex items-center gap-2 text-sm" title={c.setting.help}>
                  <input type="checkbox" checked={c.checked} disabled={c.locked} onChange={(e) => patch({ requires: toggleSetting(req, tool, c.setting.key, e.target.checked) })} />
                  <span className="text-xs">{c.setting.label}</span>
                  <span className="text-xs text-[var(--color-fg-subtle)]">
                    {c.locked ? "always needed" : "needed by this"} · set by {c.setting.scope === "member" ? "each member" : c.setting.scope === "org" ? "the organization" : "the platform"}
                  </span>
                </label>
              ))
            ) : (
              tools !== null && <p className="text-xs text-[var(--color-fg-subtle)]">This tool is no longer available to you. Remove it, or ask an admin.</p>
            )}
          </div>
        );
      })}
      {addable.length > 0 && (
        <Select
          className="h-9 w-64"
          value=""
          aria-label="Add a tool"
          onChange={(e) => {
            const t = byName.get(e.target.value);
            if (t) patch({ requires: addMcpTool(req, t, list) });
          }}
        >
          <option value="">Add a tool…</option>
          {addable.map((t) => (
            <option key={t.name} value={t.name}>
              {t.name}
              {t.description ? ` · ${t.description}` : ""}
            </option>
          ))}
        </Select>
      )}
    </div>
  );
}

// QuickPromptsEditor edits an expert's quick prompts: text, or a pointer
// to a prompt of a prompt pack (the pack joins the requirements).
export function QuickPromptsEditor({
  form,
  patch,
  options,
}: {
  readonly form: ContentForm;
  readonly patch: (p: Partial<ContentForm>) => void;
  // The prompts of the prompt packs the member can see.
  readonly options: readonly PackPromptOption[];
}) {
  const qps = form.quickPrompts;
  const set = (i: number, q: Partial<ContentQuickPrompt>) => patch({ quickPrompts: qps.map((x, j) => (j === i ? { ...x, ...q } : x)) });
  const pick = (i: number, ref: string) => {
    if (!ref) {
      patch({ quickPrompts: clearPromptRef(qps, i) });
      return;
    }
    const o = options.find((x) => x.ref === ref);
    if (o) patch(pickPromptRef(qps, form.requires ?? {}, i, o));
  };
  return (
    <div className="flex flex-col gap-2">
      <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Quick prompts</span>
      {qps.map((q, i) => (
        <div key={i} className="flex flex-col gap-1.5">
          <div className="flex items-start gap-2">
            <Input className="w-40 shrink-0" placeholder={q.ref ? "Title (optional)" : "Title"} value={q.title} onChange={(e) => set(i, { title: e.target.value })} aria-label="Quick prompt title" />
            {q.ref ? (
              <span className="flex h-9 min-w-0 flex-1 items-center truncate text-sm text-[var(--color-fg-muted)]" title={q.ref}>
                Uses a pack prompt
              </span>
            ) : (
              <Input placeholder="Prompt" value={q.prompt} onChange={(e) => set(i, { prompt: e.target.value })} aria-label="Quick prompt" />
            )}
            <button type="button" aria-label="Remove quick prompt" className="tactile mt-2.5 text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]" onClick={() => patch({ quickPrompts: qps.filter((_, j) => j !== i) })}>
              <TrashIcon size={16} />
            </button>
          </div>
          <Select className="h-9" value={q.ref ?? ""} aria-label="Quick prompt source" onChange={(e) => pick(i, e.target.value)}>
            <option value="">Its own text</option>
            {q.ref && !options.some((o) => o.ref === q.ref) && <option value={q.ref}>{q.ref} (not available to you)</option>}
            {options.map((o) => (
              <option key={o.ref} value={o.ref}>
                {o.label}
              </option>
            ))}
          </Select>
        </div>
      ))}
      <Button variant="outline" size="sm" className="self-start" onClick={() => patch({ quickPrompts: [...qps, { title: "", prompt: "" }] })}>
        <PlusIcon size={14} /> Add quick prompt
      </Button>
    </div>
  );
}

const AGENT_KINDS: { value: DefaultAgentKind; label: string }[] = [
  { value: "requester", label: "The requester's own agent" },
  { value: "platform", label: "A platform agent" },
  { value: "member", label: "A member's agent" },
];

function RoleRow({ role, onChange, onRemove }: { readonly role: ContentFormRole; readonly onChange: (r: ContentFormRole) => void; readonly onRemove: () => void }) {
  const parsed = parseDefaultAgent(role.defaultAgent);
  const kind: DefaultAgentKind = parsed?.kind ?? (role.defaultAgent.startsWith("member:") ? "member" : "requester");
  const member = role.defaultAgent.startsWith("member:") ? role.defaultAgent.slice("member:".length) : "";
  return (
    <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] p-3">
      <div className="flex items-center gap-2">
        <Input className="w-40 shrink-0" placeholder="role" value={role.role} onChange={(e) => onChange({ ...role, role: e.target.value })} aria-label="Role" />
        <Input placeholder="expert, e.g. code-reviewer@^1" value={role.expert} onChange={(e) => onChange({ ...role, expert: e.target.value })} aria-label="Role expert" />
        <button type="button" onClick={onRemove} aria-label="Remove role" title="Remove role" className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]">
          <TrashIcon size={16} />
        </button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={kind}
          className="h-9 w-56"
          aria-label="Runs on"
          onChange={(e) => {
            const k = e.target.value as DefaultAgentKind;
            onChange({ ...role, defaultAgent: k === "member" ? `member:${member}` : formatDefaultAgent({ kind: k }) });
          }}
        >
          {AGENT_KINDS.map((a) => (
            <option key={a.value} value={a.value}>
              {a.label}
            </option>
          ))}
        </Select>
        {kind === "member" && (
          <Input
            className="w-56"
            placeholder="member id, or id/provider"
            value={member}
            onChange={(e) => onChange({ ...role, defaultAgent: `member:${e.target.value.trim()}` })}
            aria-label="Member's agent"
          />
        )}
      </div>
      <Input placeholder="What this role covers (the lead refuses what no role covers)" value={role.description} onChange={(e) => onChange({ ...role, description: e.target.value })} aria-label="Role description" />
    </div>
  );
}

// TeamEditor edits an expert's team: the lead's title and the roles. An
// expert with at least one role is a team; its instructions are the SOP
// the lead follows.
export function TeamEditor({ team, onChange }: { readonly team: ContentFormTeam | undefined; readonly onChange: (t: ContentFormTeam) => void }) {
  const t = team ?? { lead: "", members: [] };
  const set = (i: number, r: ContentFormRole) => onChange({ ...t, members: t.members.map((m, j) => (j === i ? r : m)) });
  return (
    <div className="flex flex-col gap-2">
      <span className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
        <UsersThreeIcon size={14} /> Team (optional)
      </span>
      <p className="text-xs text-[var(--color-fg-subtle)]">
        Add roles to make this expert a team. The agent you address leads it: it follows the instructions above as the plan and asks each role&apos;s
        agent, wearing the role&apos;s expert. At most 8 roles.
      </p>
      {t.members.length > 0 && (
        <Field label="Lead's title" hint='Optional, e.g. "coordinator"'>
          <Input value={t.lead} onChange={(e) => onChange({ ...t, lead: e.target.value })} placeholder="lead" />
        </Field>
      )}
      {t.members.map((m, i) => (
        <RoleRow key={i} role={m} onChange={(r) => set(i, r)} onRemove={() => onChange({ ...t, members: t.members.filter((_, j) => j !== i) })} />
      ))}
      <Button
        variant="outline"
        size="sm"
        className="self-start"
        disabled={t.members.length >= 8}
        onClick={() => onChange({ ...t, members: [...t.members, { role: "", expert: "", defaultAgent: "requester", description: "" }] })}
      >
        <PlusIcon size={14} /> Add role
      </Button>
    </div>
  );
}

export function TeamBadge({ className }: { readonly className?: string }) {
  return (
    <Badge tone="accent" className={className} title="A team: the agent you address leads it and asks the roles' agents">
      <UsersThreeIcon size={12} />
      Team
    </Badge>
  );
}

// TeamRoster lists a team's roles: role, the expert it wears and the agent
// that takes it by default.
export function TeamRoster({ team, compact }: { readonly team: ContentTeam; readonly compact?: boolean }) {
  const nameOf = useDisplayNames(team.members.map((m) => parseDefaultAgent(m.defaultAgent)?.owner));
  return (
    <ul className={cn("flex flex-col gap-1", compact ? "text-xs" : "text-sm")}>
      {!compact && team.lead && <li className="text-[var(--color-fg-muted)]">Lead: the agent you address ({team.lead})</li>}
      {team.members.map((m) => (
        <li key={m.role} className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
          <span className="font-medium">{m.role}</span>
          <span className="font-mono text-[var(--color-fg-subtle)]">{m.expert}</span>
          <span className="text-[var(--color-fg-muted)]">on {defaultAgentLabel(m.defaultAgent, nameOf)}</span>
          {!compact && m.description && <span className="w-full text-[var(--color-fg-subtle)]">{m.description}</span>}
        </li>
      ))}
    </ul>
  );
}
