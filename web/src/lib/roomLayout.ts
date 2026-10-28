// A new room's channel layout (docs/prd/2026-10-28-channels-and-threads.md
// §2.5b, C25): the sections and channels a room package makes, or that a
// plan proposes, in one shape (wire RoomPackageSection / RoomPackageChannel).
// The create dialog previews a layout the way the rail will show it and
// makes it through the room's channel routes only when the member creates
// the room. Pure helpers here (unit-tested); makeLayout takes the API.

import { MaxChannelPurpose, MaxChannelRules, type ChannelCreateRequest, type ChannelPatchRequest, type RoomPackageChannel, type RoomPackageSection, type RoomSection, type SectionCreateRequest } from "./wire.gen.ts";

export type { RoomPackageChannel, RoomPackageSection } from "./wire.gen.ts";

/** A room's layout: its sections and its channels (the default channel is
 *  every room's and is never in it). */
export interface RoomLayout {
  readonly sections: readonly RoomPackageSection[];
  readonly channels: readonly RoomPackageChannel[];
}

export const emptyLayout: RoomLayout = { sections: [], channels: [] };

// The package's bounds the plan is held to (contentpkg/room.go): the
// counts and a name's length.
const MAX_SECTIONS = 10;
const MAX_CHANNELS = 20;
const MAX_NAME = 80;
const DEFAULT_CHANNEL = "general";

/** A preview group: the top level (section null) or a section, with its
 *  channels in the layout's order. */
export interface LayoutGroup {
  readonly section: RoomPackageSection | null;
  readonly channels: readonly RoomPackageChannel[];
}

/** layoutGroups is the rail a layout makes: the default channel and the
 *  top-level channels first, then each section with its channels, as the
 *  rail lists them (lib/channelRules railGroups). A channel naming a
 *  section the layout does not make sits at the top level, as the room
 *  would place it. */
export function layoutGroups(layout: RoomLayout): LayoutGroup[] {
  const known = new Set(layout.sections.map((s) => s.name.toLowerCase()));
  const inSection = (c: RoomPackageChannel) => !!c.section && known.has(c.section.toLowerCase());
  const top: RoomPackageChannel[] = [{ name: DEFAULT_CHANNEL, settings: {} }, ...layout.channels.filter((c) => !inSection(c))];
  return [
    { section: null, channels: top },
    ...layout.sections.map((s) => ({ section: s, channels: layout.channels.filter((c) => inSection(c) && c.section!.toLowerCase() === s.name.toLowerCase()) })),
  ];
}

/** layoutSize is a layout in a line ("2 sections · 5 channels"). */
export function layoutSize(layout: RoomLayout): string {
  const n = (count: number, one: string) => `${count} ${one}${count === 1 ? "" : "s"}`;
  return [layout.sections.length ? n(layout.sections.length, "section") : "", n(layout.channels.length, "channel")].filter(Boolean).join(" · ");
}

/** layoutFromPlan is the layout a plan-room reply proposes (its sections
 *  and channels items): names trimmed, one line and bounded, each once
 *  regardless of case, never the default channel, texts cut to the room's
 *  bounds, a channel's section one of the plan's (else the top level),
 *  within a package's counts. Anything else is dropped, never guessed. */
export function layoutFromPlan(sections: readonly Record<string, string>[], channels: readonly Record<string, string>[]): RoomLayout {
  const name = (v: string | undefined) => (v ?? "").replace(/\s+/g, " ").trim().slice(0, MAX_NAME);
  const seenS = new Set<string>();
  const outS: RoomPackageSection[] = [];
  for (const s of sections) {
    const n = name(s.name);
    if (!n || seenS.has(n.toLowerCase()) || outS.length >= MAX_SECTIONS) continue;
    seenS.add(n.toLowerCase());
    const rules = (s.rules ?? "").trim().slice(0, MaxChannelRules);
    outS.push(rules ? { name: n, rules } : { name: n });
  }
  const seenC = new Set<string>([DEFAULT_CHANNEL]);
  const outC: RoomPackageChannel[] = [];
  for (const c of channels) {
    const n = name(c.name).replace(/^#/, "");
    if (!n || seenC.has(n.toLowerCase()) || outC.length >= MAX_CHANNELS) continue;
    seenC.add(n.toLowerCase());
    const purpose = (c.purpose ?? "").replace(/\s+/g, " ").trim().slice(0, MaxChannelPurpose);
    const section = outS.find((s) => s.name.toLowerCase() === name(c.section).toLowerCase())?.name ?? "";
    outC.push({ name: n, ...(purpose ? { purpose } : {}), ...(section ? { section } : {}), settings: {} });
  }
  return { sections: outS, channels: outC };
}

/** The room's channel routes makeLayout calls (lib/channelsApi). */
export interface LayoutApi {
  createSection: (roomId: string, body: SectionCreateRequest) => Promise<RoomSection>;
  create: (roomId: string, body: ChannelCreateRequest) => Promise<{ channelId: string }>;
  patch: (roomId: string, channelId: string, body: ChannelPatchRequest) => Promise<unknown>;
}

/** makeLayout makes a layout in a new room the member owns: each section
 *  (open), then each channel with its purpose in its section, then its
 *  position (the layout's order), rules and settings. It stops at the
 *  first refusal and says which part failed; the room stays. */
export async function makeLayout(api: LayoutApi, roomId: string, layout: RoomLayout): Promise<void> {
  const ids = new Map<string, string>();
  for (const [i, s] of layout.sections.entries()) {
    const made = await api.createSection(roomId, { name: s.name, rules: s.rules ?? "", position: i, visibility: "open" }).catch((e: unknown) => {
      throw new Error(`Could not make the section ${s.name}: ${e instanceof Error ? e.message : String(e)}`);
    });
    ids.set(s.name.toLowerCase(), made.sectionId);
  }
  for (const [i, c] of layout.channels.entries()) {
    const fail = (e: unknown) => {
      throw new Error(`Could not make #${c.name}: ${e instanceof Error ? e.message : String(e)}`);
    };
    const made = await api.create(roomId, { name: c.name, purpose: c.purpose ?? "", sectionId: ids.get((c.section ?? "").toLowerCase()) ?? "" }).catch(fail);
    const set = c.settings ?? {};
    const hasSettings = !!set.whoPosts || !!set.noAgents || !!set.slowModeSeconds;
    const patch: ChannelPatchRequest = { position: i, ...(c.rules ? { rules: c.rules } : {}), ...(hasSettings ? { settings: { whoPosts: set.whoPosts, noAgents: set.noAgents, slowModeSeconds: set.slowModeSeconds } } : {}) };
    await api.patch(roomId, made.channelId, patch).catch(fail);
  }
}
