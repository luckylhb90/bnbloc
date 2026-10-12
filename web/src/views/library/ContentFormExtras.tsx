import { useState } from "react";
import { PlusIcon, TrashIcon, UsersThreeIcon } from "@phosphor-icons/react";
import { splitList, type ContentForm, type ContentFormRole, type ContentFormTeam, type ContentKind } from "@/lib/contentComponents";
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
// prompt packs by name or name@range, and whether it runs scripts. Other
// declared permissions (local or remote tools) carry over unchanged.
export function RequirementsFields({ form, patch, kind }: { readonly form: ContentForm; readonly patch: (p: Partial<ContentForm>) => void; readonly kind: ContentKind }) {
  const req = form.requires ?? {};
  const perms = req.permissions ?? [];
  const scripts = perms.includes("scripts");
  const others = perms.filter((p) => p !== "scripts");
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
            <ListInput value={req.prompts ?? []} onChange={(prompts) => setReq({ prompts })} placeholder="release-notes@^1" label="Required prompt packs" />
          </Field>
        </div>
      )}
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={scripts}
          onChange={(e) => setReq({ permissions: e.target.checked ? [...others, "scripts"] : others })}
        />
        Runs scripts (someone else's use runs in the agent sandbox)
      </label>
      {others.length > 0 && <p className="text-xs text-[var(--color-fg-subtle)]">Other declared permissions carry over: {others.join(", ")}</p>}
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
