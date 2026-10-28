// A room's channels as the rail and the channel menus read them
// (docs/prd/2026-10-28-channels-and-threads.md, slice 1;
// docs/architecture/room-channels.md): the rail's grouping and order, the
// mirror of the room service's rules of who may change what (so a control
// shows only where the server allows the act; the server stays the
// authority), what a refusal's code says in words, and which agent the
// composer proposes in a channel (C8). Pure, node-tested; the rules are held
// to services/room/testdata/channel_rules.json, the cases the Go owners are
// tested against.

import { canInvite, mayChat, type RoomMembership } from "./roomRoles.ts";
import { sameUser } from "./format.ts";
import { agentByIdentity, type AgentRef } from "./roomAgents.ts";
import {
  ChannelAdminsOnly,
  ChannelArchived,
  ChannelCreationAdmins,
  ChannelCreationAnyone,
  ChannelCreationSections,
  ChannelSlowMode,
  RulesAgreementRequired,
  RulesChanged,
  type ChannelCreationPolicy,
  type ChannelGroup,
  type RoomChannel,
  type RoomSection,
} from "./wire.gen.ts";

// ---- The rules (mirrors of services/room's owners) ----

/** mayManageChannel mirrors the room service's mayManageChannel: the room's
 *  owner and admins, the channel's owner, the owners of its section; never
 *  a visitor. Rename, purpose, rules, section, position, settings, archive,
 *  restore. */
export function mayManageChannel(me: RoomMembership & { readonly userId?: string }, user: string | null, ch: Pick<RoomChannel, "ownerId">, sec: Pick<RoomSection, "owners"> | null): boolean {
  if (canInvite(me)) return true;
  if (!mayChat(me) || !user) return false;
  return (!!ch.ownerId && sameUser(ch.ownerId, user)) || isSectionOwner(sec, user);
}

/** mayPlaceChannel mirrors mayPlaceChannel: where the member may create or
 *  move a channel (sec null is the room's top level), by the room's channel
 *  creation policy (C15). */
export function mayPlaceChannel(me: RoomMembership, user: string | null, policy: ChannelCreationPolicy | "" | undefined, sec: Pick<RoomSection, "owners"> | null): boolean {
  if (canInvite(me)) return true;
  if (!mayChat(me) || !user) return false;
  switch (policy || ChannelCreationAnyone) {
    case ChannelCreationAnyone:
      return sec === null || isSectionOwner(sec, user);
    case ChannelCreationSections:
      return sec !== null && isSectionOwner(sec, user);
  }
  return false;
}

/** mayCreateSection mirrors mayCreateSection. */
export function mayCreateSection(me: RoomMembership, policy: ChannelCreationPolicy | "" | undefined): boolean {
  if (canInvite(me)) return true;
  return mayChat(me) && (policy || ChannelCreationAnyone) !== ChannelCreationAdmins;
}

/** mayManageSection mirrors mayManageSection: its owners and the room's
 *  owner and admins. */
export function mayManageSection(me: RoomMembership, user: string | null, sec: Pick<RoomSection, "owners">): boolean {
  return canInvite(me) || (mayChat(me) && !!user && isSectionOwner(sec, user));
}

/** mayDeleteChannel: the room's owner and admins (handleDeleteChannel); the
 *  default channel never. */
export function mayDeleteChannel(me: RoomMembership, ch: Pick<RoomChannel, "isDefault">): boolean {
  return canInvite(me) && !ch.isDefault;
}

function isSectionOwner(sec: Pick<RoomSection, "owners"> | null, user: string): boolean {
  return !!sec && sec.owners.some((o) => sameUser(o, user));
}

/** sectionOf is a channel's section, null at the top level or when its
 *  section is gone (its channels moved to the top level with it). */
export function sectionOf(ch: Pick<RoomChannel, "sectionId">, sections: readonly RoomSection[]): RoomSection | null {
  return ch.sectionId ? (sections.find((s) => s.sectionId === ch.sectionId) ?? null) : null;
}

// ---- The rail ----

/** One group of the rail: the top level (section and group null), one of the
 *  member's own groups (C24), or a section. */
export interface RailGroup {
  readonly section: RoomSection | null;
  readonly group: ChannelGroup | null;
  readonly channels: readonly RoomChannel[];
}

/** railGroups is what a member's rail shows: the channels they joined (C18;
 *  the active one even when not joined, so the open channel is never
 *  missing), hidden ones left out, starred first then by position then by
 *  name. The top level comes first (the default channel leads it), then the
 *  member's own groups (C24) by their position, then the room's open
 *  sections by theirs, as Slack lists custom sections above channels
 *  (decision: pending owner, the PRD does not place them). A
 *  channel in one of the member's groups shows there and not in its section
 *  (for them only); a channel naming a group they no longer have shows where
 *  it would without one. Every group of the member is listed, empty or not
 *  (they made it to fill); a section with no channel for the member only
 *  when keepEmpty says so (its owner may add channels there). */
export function railGroups(
  channels: readonly RoomChannel[],
  sections: readonly RoomSection[],
  opts: { readonly active?: string | null; readonly keepEmpty?: (s: RoomSection) => boolean; readonly groups?: readonly ChannelGroup[] } = {},
): RailGroup[] {
  const shown = channels.filter((c) => c.channelId === opts.active || (c.joined && !c.hidden));
  const order = (a: RoomChannel, b: RoomChannel) =>
    Number(b.isDefault) - Number(a.isDefault) ||
    Number(b.starred) - Number(a.starred) ||
    a.position - b.position ||
    a.name.localeCompare(b.name) ||
    a.channelId.localeCompare(b.channelId);
  const groups = [...(opts.groups ?? [])].sort((a, b) => a.position - b.position || a.createdAtMs - b.createdAtMs || a.groupId.localeCompare(b.groupId));
  const mine = new Set(groups.map((g) => g.groupId));
  const grouped = (c: RoomChannel) => !!c.groupId && mine.has(c.groupId);
  const known = new Set(sections.map((s) => s.sectionId));
  const top = shown.filter((c) => !grouped(c) && (!c.sectionId || !known.has(c.sectionId))).sort(order);
  const out: RailGroup[] = [{ section: null, group: null, channels: top }];
  for (const g of groups) out.push({ section: null, group: g, channels: shown.filter((c) => c.groupId === g.groupId).sort(order) });
  const sorted = [...sections].sort((a, b) => a.position - b.position || a.createdAtMs - b.createdAtMs || a.sectionId.localeCompare(b.sectionId));
  for (const s of sorted) {
    const inSection = shown.filter((c) => !grouped(c) && c.sectionId === s.sectionId).sort(order);
    if (inSection.length > 0 || opts.keepEmpty?.(s)) out.push({ section: s, group: null, channels: inSection });
  }
  return out;
}

/** railGroupKey is a rail group's key for folding: "" the top level, the
 *  section's id, or "group:" and the member's group id (a group and a
 *  section never share a key). */
export function railGroupKey(g: Pick<RailGroup, "section" | "group">): string {
  return g.group ? `group:${g.group.groupId}` : (g.section?.sectionId ?? "");
}

/** foldedChannels is what a folded rail group still shows, as Slack does: the
 *  channel being read and those with unread messages, so folding never
 *  hides where the member is or what is waiting. An unfolded group shows
 *  all its channels. The one rule for sections and the member's groups. */
export function foldedChannels<C extends Pick<RoomChannel, "channelId">>(
  channels: readonly C[],
  folded: boolean,
  active: string | null,
  unread: Readonly<Record<string, number>> | undefined,
): readonly C[] {
  if (!folded) return channels;
  return channels.filter((c) => c.channelId === active || (unread?.[c.channelId] ?? 0) > 0);
}

/** directoryMatches filters the channel directory by a search over names and
 *  purposes (words in any order, case-insensitive: a search, never an act). */
export function directoryMatches<C extends Pick<RoomChannel, "name" | "purpose">>(channels: readonly C[], query: string): C[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return [...channels];
  return channels.filter((c) => {
    const hay = `${c.name} ${c.purpose}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

/** sharedOrder is a section's channels ("" the top level) in the order
 *  everyone shares (position, then name), whoever follows them: what Move
 *  up and Move down change. The default channel always leads the room and
 *  is not moved; a member's stars are their own and do not count. */
export function sharedOrder<C extends Pick<RoomChannel, "channelId" | "sectionId" | "position" | "name" | "isDefault">>(channels: readonly C[], sectionId: string): C[] {
  return channels
    .filter((c) => !c.isDefault && (c.sectionId || "") === sectionId)
    .sort((a, b) => a.position - b.position || a.name.localeCompare(b.name) || a.channelId.localeCompare(b.channelId));
}

/** reorderedPositions is the positions that move channel `id` one place up
 *  (-1) or down (+1) among its group's channels as listed: each channel of
 *  the group gets its index, so the order is explicit after the move.
 *  Empty when it cannot move. */
export function reorderedPositions(group: readonly Pick<RoomChannel, "channelId" | "position">[], id: string, step: -1 | 1): { readonly channelId: string; readonly position: number }[] {
  const i = group.findIndex((c) => c.channelId === id);
  const j = i + step;
  if (i < 0 || j < 0 || j >= group.length) return [];
  const ids = group.map((c) => c.channelId);
  [ids[i], ids[j]] = [ids[j], ids[i]];
  return ids.flatMap((cid, k) => (group.find((c) => c.channelId === cid)?.position === k ? [] : [{ channelId: cid, position: k }]));
}

// ---- Refusals in words ----

/** A post the channel's setup refused (the room service's error body). */
export interface PostRefusal {
  readonly code: string;
  readonly message: string;
  readonly retryAfterMs: number;
}

/** postRefusalOf reads a failed post's body ({error, code, retryAfterMs});
 *  null when it is no channel refusal. */
export function postRefusalOf(body: { readonly code?: string; readonly error?: string; readonly retryAfterMs?: number } | null | undefined): PostRefusal | null {
  const code = body?.code ?? "";
  if (![ChannelArchived, ChannelAdminsOnly, ChannelSlowMode, RulesAgreementRequired, RulesChanged].includes(code)) return null;
  return { code, message: body?.error ?? "", retryAfterMs: Math.max(0, body?.retryAfterMs ?? 0) };
}

/** composerBlock is why the composer cannot post in a channel now, read from
 *  the channel's setup before a send (the server's gate decides; this only
 *  says it before the member types): an archived channel, an admins-only
 *  channel for a member who is not one. null: nothing blocks. */
export function composerBlock(ch: Pick<RoomChannel, "archivedAtMs" | "settings"> | null | undefined, me: RoomMembership | null | undefined): string | null {
  if (!ch) return null;
  if (ch.archivedAtMs) return "This channel is archived: it is read-only until an owner restores it.";
  if (ch.settings?.whoPosts === "admins" && !(me && canInvite(me))) return "Only the room's owner and admins post in this channel.";
  return null;
}

/** waitText is a slow mode wait in words ("12 s", "2 min"). */
export function waitText(ms: number): string {
  const s = Math.ceil(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.ceil(s / 60);
  return `${m} min`;
}

// ---- Which agent the composer proposes (C8) ----

/** An agent the member may drive here: their own, an authorized borrow, or
 *  a platform agent of the room (what the composer's roster lists). */
export type DrivableAgent = AgentRef & { readonly self?: boolean };

/** composerDefault is the agent the composer proposes in a channel for a
 *  member who has made no choice of their own there (decision: channels PRD
 *  C8): the member's own choice for the channel (handled before this: a
 *  stored pick is restored by useAgentPrefs and always wins), else the
 *  channel's default agent if the member may drive it (it is one of the
 *  agents the room lists for them, by its key, owner, core and provider),
 *  else the room's default (fallback, today's rule), else null: ask.
 *  Nothing is chosen by "first" or "online". It only proposes: the member
 *  confirms or replaces it in the picker before anything is sent. */
export function composerDefault<A extends DrivableAgent>(input: {
  readonly channelDefault: AgentRef | null | undefined;
  readonly drivable: readonly A[];
  readonly roomDefault: () => A | null;
}): { readonly agent: A | null; readonly from: "channel" | "room" | "ask" } {
  const ch = input.channelDefault;
  if (ch?.ownerId && ch.provider) {
    // which-one: by the channel default's key (owner, core, provider), never a search.
    const found = agentByIdentity(input.drivable, { ownerId: ch.ownerId, core: ch.core || undefined, provider: ch.provider });
    if (found) return { agent: found, from: "channel" };
  }
  const room = input.roomDefault();
  return room ? { agent: room, from: "room" } : { agent: null, from: "ask" };
}

/** sectionJoin is what a members-only section offers a reader who is not
 *  in it (C11, C17): "join" (open: at once), "ask" (a request its owners
 *  approve), "admin" (the room's owner or an admin: joins at once, which
 *  its members are told, C12) or null (an open section, or already in it).
 *  The server lists a members-only section only to those who may know it. */
export function sectionJoin(me: RoomMembership | null, sec: Pick<RoomSection, "visibility" | "joinPolicy" | "joined">): "join" | "ask" | "admin" | null {
  if (!me || sec.visibility !== "members" || sec.joined || !mayChat(me)) return null;
  if (canInvite(me)) return "admin";
  if (sec.joinPolicy === "open") return "join";
  if (sec.joinPolicy === "ask") return "ask";
  return null;
}
