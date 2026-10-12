// Platform agents (docs/prd/2026-10-10-zero-install-onboarding.md, v2 in
// docs/prd/2026-10-10-platform-agents-v2-onboarding.md): super admins' own
// agents published platform-wide. A platform agent is a (core, provider)
// published by one or more hosts. Any member can add one to a room; from then
// on it is driven exactly like a borrowed agent (recipient {ownerId: host,
// provider, core}, borrow flag) against the standing room authorization the
// add created for every host. The client picks the host per send
// (platformRouting.ts). Hosts join the room as hidden agent-host members,
// which are never people.
//
// This file is the one place the platform-agent wire shapes live.
import type { AgentJob, Member } from "./apiModels.ts";
import { defaultDisplayName } from "./displayNames.ts";
import { agentIdentity, borrowKey, mergeRoomAgents, type RosterAgent, type SharedAgent } from "./roomAgents.ts";
import { pickHost } from "./platformRouting.ts";
import type { RoomAgent } from "../views/composer/types.ts";
import { sameUser } from "./format.ts";

export interface PlatformAgentRef {
  readonly core: string;
  readonly provider: string;
}

// One published agent of a host's policy entry. defaultModel (optional) is
// the model a platform turn runs with when the member picked none.
export interface PolicyAgent extends PlatformAgentRef {
  readonly defaultModel?: string;
}

// One super admin's entry of the policy (GET /v1/platform/platform-agents).
export interface PlatformHostPolicy {
  readonly hostUserId: string;
  readonly enabled: boolean;
  readonly agents: readonly PolicyAgent[];
}

// The whole policy: every host's entry. Each super admin edits only their own.
export interface PlatformAgentsPolicy {
  readonly hosts: readonly PlatformHostPolicy[];
}

// PUT /v1/platform/platform-agents: the caller's entry only.
export interface PlatformAgentsPolicyInput {
  readonly enabled: boolean;
  readonly agents: readonly PolicyAgent[];
}

// One host of a platform agent as members see it. queued counts the host's
// claimable jobs not yet started; targetId is "" while offline. defaultModel
// (optional) is that host's policy default for the agent.
export interface PlatformHost {
  readonly hostUserId: string;
  readonly online: boolean;
  readonly queued: number;
  readonly targetId: string;
  readonly defaultModel?: string;
}

// A platform agent offered to the caller's organization (GET
// /v1/platform-agents): one entry per (core, provider). online = any host
// online (the client re-derives it from merged host states).
export interface PlatformAgent extends PlatformAgentRef {
  readonly online: boolean;
  readonly hosts: readonly PlatformHost[];
}

// A platform agent added to one room (GET /v1/rooms/{id}/platform-agents).
export interface RoomPlatformAgent extends PlatformAgent {
  readonly addedBy: string;
  readonly addedAtMs: number;
}

// One "Platform agents" row of the composer's picker.
export interface PlatformChoice {
  // The recipient as routed now (ownerId = the host a send would pick).
  readonly agent: RoomAgent;
  // The hosts that may serve the caller (never the caller themselves).
  readonly hosts: readonly PlatformHost[];
  // At least one host is online.
  readonly online: boolean;
  // Already added to this room: picking it drives it; otherwise the add is
  // confirmed first.
  readonly inRoom: boolean;
}

// Looks up the host this room last used for an agent (platformRouting.ts).
export type LastHostLookup = (agent: PlatformAgentRef) => string | null;

export const PLATFORM_AGENTS_GROUP = "Platform agents";

// The composer's refusal (and the 409 platform_agent_offline toast).
export const PLATFORM_OFFLINE_MESSAGE =
  "This platform agent is offline — its host's computer is not connected. Try again later.";

const noLastHost: LastHostLookup = () => null;

export function samePlatformAgent(a: PlatformAgentRef, b: PlatformAgentRef): boolean {
  return (a.core ?? "").toLowerCase() === (b.core ?? "").toLowerCase() && a.provider.toLowerCase() === b.provider.toLowerCase();
}


export function platformAgentOnline(a: PlatformAgent): boolean {
  return a.hosts.some((h) => h.online);
}

// platformRoomAgent is the room recipient a platform agent is driven as on
// one host: the host's agent, borrowed, always in the host's sandbox, with
// the host's default model.
export function platformRoomAgent(a: PlatformAgentRef, host: PlatformHost, subject: string | null): RoomAgent {
  return {
    ownerId: host.hostUserId,
    provider: a.provider,
    core: a.core || undefined,
    self: sameUser(host.hostUserId, subject),
    sandboxed: true,
    platform: true,
    ...(host.defaultModel ? { defaultModel: host.defaultModel } : {}),
  };
}

// routedRoomAgent is the recipient a send to `a` would use now: the routed
// online host, else the first host (offline: the send is refused).
export function routedRoomAgent(a: PlatformAgentRef & { readonly hosts: readonly PlatformHost[] }, subject: string | null, lastHost: LastHostLookup = noLastHost): RoomAgent | null {
  const host = pickHost(a.hosts, lastHost(a)) ?? a.hosts[0];
  return host ? platformRoomAgent(a, host, subject) : null;
}

// splitAgentHosts separates the room's people from its agent-host
// memberships. Every member list goes through it, so an agent host is never
// shown, counted or mentioned as a person.
export function splitAgentHosts(members: readonly Member[]): {
  readonly people: Member[];
  readonly agentHosts: string[];
} {
  const people: Member[] = [];
  const agentHosts: string[] = [];
  for (const m of members) {
    if (m.agentHost) agentHosts.push(m.userId);
    else people.push({ userId: m.userId, role: m.role });
  }
  return { people, agentHosts };
}

// hostedIdentities is every (host, core, provider) identity of the platform
// agents, so no other group repeats one of them.
function hostedIdentities(agents: readonly PlatformAgent[]): Set<string> {
  return new Set(agents.flatMap((a) => a.hosts.map((h) => agentIdentity({ ownerId: h.hostUserId, core: a.core, provider: a.provider }))));
}

// composeRoomAgents builds the room's selectable agents: the live roster and
// borrow grants (mergeRoomAgents), where an agent-host member contributes
// only its added platform agents (its bridge may advertise more, and a
// platform grant must not surface again as an ordinary borrow), plus one
// recipient per room platform agent, routed to its current host.
export function composeRoomAgents(input: {
  readonly roster: readonly RosterAgent[];
  readonly shared: readonly SharedAgent[];
  readonly platform: readonly RoomPlatformAgent[];
  readonly agentHosts: readonly string[];
  readonly subject: string | null;
  readonly lastHost?: LastHostLookup;
}): RoomAgent[] {
  const hostOnly = (owner: string) => input.agentHosts.some((h) => sameUser(h, owner));
  const platformGrant = (s: SharedAgent) =>
    input.platform.some((p) => borrowKey(p) === s.provider && p.hosts.some((h) => sameUser(h.hostUserId, s.ownerUserId)));
  const base = mergeRoomAgents(
    input.roster.filter((a) => !hostOnly(a.ownerUserId)),
    input.shared.filter((s) => !hostOnly(s.ownerUserId) && !platformGrant(s)),
    input.subject,
  );
  const hosted = hostedIdentities(input.platform);
  // Another host serves the caller when one exists: their own hosting is
  // their own agent already.
  const servers = (p: RoomPlatformAgent): RoomPlatformAgent => {
    const others = p.hosts.filter((h) => !sameUser(h.hostUserId, input.subject));
    return others.length > 0 ? { ...p, hosts: others } : p;
  };
  const platform = input.platform
    .map((p) => routedRoomAgent(servers(p), input.subject, input.lastHost))
    .filter((a): a is RoomAgent => a !== null);
  return [...base.filter((a) => !hosted.has(agentIdentity(a))), ...platform];
}

// roomHosts are the hosts serving a room's platform agent (the room listing
// is authoritative for WHICH hosts), each with the fresher online/queued
// state of the organization's live offer when it lists that host.
function roomHosts(listed: readonly PlatformHost[], fresh: readonly PlatformHost[] | undefined): PlatformHost[] {
  return listed.map((h) => fresh?.find((x) => sameUser(x.hostUserId, h.hostUserId)) ?? h);
}

// platformChoices lists the picker's platform agents: every agent published
// to this organization, plus any already in the room, one row per (core,
// provider). The caller's own hosting is theirs already: they are dropped
// from the hosts, and an agent only they host is not offered.
export function platformChoices(
  available: readonly PlatformAgent[],
  inRoom: readonly RoomPlatformAgent[],
  subject: string | null,
  lastHost: LastHostLookup = noLastHost,
): PlatformChoice[] {
  const out: PlatformChoice[] = [];
  for (const a of [...inRoom, ...available]) {
    if (out.some((c) => samePlatformAgent({ core: c.agent.core ?? "", provider: c.agent.provider }, a))) continue;
    const live = available.find((x) => samePlatformAgent(x, a));
    const added = inRoom.find((r) => samePlatformAgent(r, a));
    const all = added ? roomHosts(added.hosts, live?.hosts) : a.hosts;
    const hosts = all.filter((h) => !sameUser(h.hostUserId, subject));
    const agent = routedRoomAgent({ core: a.core, provider: a.provider, hosts }, subject, lastHost);
    if (!agent) continue;
    out.push({
      agent,
      hosts,
      online: hosts.some((h) => h.online),
      inRoom: !!added,
    });
  }
  return out;
}

// hostsLabel names an agent's hosts for the picker and the header popover by
// display name (`nameOf`, the resolver in the UI; the default rule otherwise).
export function hostsLabel(hosts: readonly PlatformHost[], nameOf: (id: string) => string = defaultDisplayName): string {
  if (hosts.length === 0) return "no host";
  return hosts
    .map((h) => {
      const name = nameOf(h.hostUserId);
      return !h.online ? `${name} (offline)` : h.queued > 0 ? `${name} (${h.queued} queued)` : name;
    })
    .join(", ");
}

// addFailureMessage turns a failed add (POST /v1/rooms/{id}/platform-agents)
// into the toast the member sees.
export function addFailureMessage(status: number, detail: string, provider: string): string {
  if (status === 403) return "Only room members who can chat may add agents to this room";
  if (status === 404) return `${provider} is no longer offered as a platform agent`;
  if (status === 429) return "You're doing that too fast — wait a moment";
  return detail ? `Could not add ${provider}: ${detail}` : `Could not add ${provider}`;
}

// isPlatformOffline recognizes the chat refusal for a platform agent whose
// host is offline (409 platform_agent_offline): no job was created.
export function isPlatformOffline(status: number, detail: string): boolean {
  return status === 409 && detail.includes("platform_agent_offline");
}

// jobQueueAhead is how many of the serving host's claimable jobs are ahead of
// a queued platform job. The host's queued count includes the job itself.
export function jobQueueAhead(job: Pick<AgentJob, "status" | "failureReason" | "ownerUserId" | "core" | "provider">, inRoom: readonly RoomPlatformAgent[]): number {
  if (job.status !== "queued" || job.failureReason) return 0;
  const agent = inRoom.find((a) => samePlatformAgent(a, job));
  const host = agent?.hosts.find((h) => sameUser(h.hostUserId, job.ownerUserId));
  return host ? Math.max(0, host.queued - 1) : 0;
}

// ---- Platform console (super admin)

// myHostPolicy is the caller's own policy entry (absent before they publish).
export function myHostPolicy(policy: PlatformAgentsPolicy, subject: string | null): PlatformHostPolicy | undefined {
  return policy.hosts.find((h) => sameUser(h.hostUserId, subject));
}

// otherHostPolicies are the other super admins' entries (read-only here).
export function otherHostPolicies(policy: PlatformAgentsPolicy, subject: string | null): PlatformHostPolicy[] {
  return policy.hosts.filter((h) => !sameUser(h.hostUserId, subject));
}

// toggleDraftAgent applies a published-agent checkbox to the caller's draft.
// Ticking the first agent of an empty, switched-off entry also switches it on:
// picking an agent to offer is the intent to offer it (re-saving after the
// policy reset left the switch off while agents were ticked, so members saw
// nothing).
export function toggleDraftAgent(draft: PlatformAgentsPolicyInput, agent: PlatformAgentRef, on: boolean): PlatformAgentsPolicyInput {
  const agents = togglePolicyAgent(draft.agents, agent, on);
  return { ...draft, agents, enabled: draft.enabled || (on && draft.agents.length === 0) };
}

// togglePolicyAgent adds or removes one agent from the caller's published
// set; re-adding keeps no stale default model.
export function togglePolicyAgent(agents: readonly PolicyAgent[], agent: PlatformAgentRef, on: boolean): PolicyAgent[] {
  const rest = agents.filter((a) => !samePlatformAgent(a, agent));
  return on ? [...rest, { core: agent.core, provider: agent.provider }] : rest;
}

// setPolicyDefaultModel sets (or clears, with "") a published agent's default
// model. An unpublished agent is left alone.
export function setPolicyDefaultModel(agents: readonly PolicyAgent[], agent: PlatformAgentRef, model: string): PolicyAgent[] {
  return agents.map((a) =>
    samePlatformAgent(a, agent) ? { core: a.core, provider: a.provider, ...(model ? { defaultModel: model } : {}) } : a,
  );
}

// samePolicyInput reports whether two editable entries publish the same
// agents with the same defaults (order-insensitive).
export function samePolicyInput(a: PlatformAgentsPolicyInput, b: PlatformAgentsPolicyInput): boolean {
  const norm = (x: PlatformAgentsPolicyInput) =>
    JSON.stringify({
      enabled: x.enabled,
      agents: x.agents
        .map((p) => `${p.core.toLowerCase()}/${p.provider.toLowerCase()}=${p.defaultModel ?? ""}`)
        .sort(),
    });
  return norm(a) === norm(b);
}

// One of the caller's own agents (GET /v1/my/agents) as the console reads it.
export interface ConsoleOwnAgent extends PlatformAgentRef {
  readonly online: boolean;
  readonly models: readonly string[];
}

// One row of the Platform console card.
export interface ConsoleAgentRow extends PlatformAgentRef {
  // Advertised by the admin's own bridge (GET /v1/my/agents) right now.
  readonly mine: boolean;
  readonly online: boolean;
  readonly published: boolean;
  // The host's own catalog for the agent (empty when not advertised now).
  readonly models: readonly string[];
}

// consoleAgentRows lists the admin's own agents, published ones checked. A
// published agent the admin's bridge does not advertise right now (desktop
// offline) stays listed, so it can still be unpublished.
export function consoleAgentRows(mine: readonly ConsoleOwnAgent[], published: readonly PolicyAgent[]): ConsoleAgentRow[] {
  const rows: ConsoleAgentRow[] = mine.map((a) => ({
    core: a.core,
    provider: a.provider,
    mine: true,
    online: a.online,
    published: published.some((p) => samePlatformAgent(p, a)),
    models: a.models,
  }));
  for (const p of published) {
    if (rows.some((r) => samePlatformAgent(r, p))) continue;
    rows.push({ core: p.core, provider: p.provider, mine: false, online: false, published: true, models: [] });
  }
  return rows;
}

// defaultModelOptions are the default-model select's choices for one agent:
// the host's catalog, plus a saved default the catalog no longer lists (so
// the select never silently shows another value).
export function defaultModelOptions(models: readonly string[], saved: string | undefined): string[] {
  return saved && !models.includes(saved) ? [...models, saved] : [...models];
}

// defaultPlatformRecipient is the composer's fallback recipient for a member
// who has no agent of their own here: the room's first platform agent. A
// member with a local agent keeps their own default (null).
export function defaultPlatformRecipient(roomAgents: readonly RoomAgent[], hasOwnAgent: boolean): RoomAgent | null {
  if (hasOwnAgent) return null;
  return roomAgents.find((a) => a.platform && !a.self) ?? null;
}

// SendRoute is where a send to a picked recipient goes now: a platform
// agent's recipient is re-routed to the host routing picks (offline: every
// host is down and nothing may be sent); any other recipient goes as picked.
export interface SendRoute {
  readonly recipient: RoomAgent | null;
  readonly host: PlatformHost | null;
  readonly offline: boolean;
}

// routeSend is the one place a send's recipient is resolved: the composer's
// send and its team plan (the lead the plan staffs and estimates) both call
// it, so the plan names the host the send will use.
export function routeSend(picked: RoomAgent | null, route: ((agent: PlatformAgentRef) => PlatformHost | null) | undefined, subject: string | null): SendRoute {
  if (!picked?.platform) return { recipient: picked, host: null, offline: false };
  const host = route?.({ core: picked.core ?? "", provider: picked.provider }) ?? null;
  if (!host) return { recipient: picked, host: null, offline: true };
  return {
    recipient: { ...picked, ownerId: host.hostUserId, self: sameUser(host.hostUserId, subject), defaultModel: host.defaultModel },
    host,
    offline: false,
  };
}
