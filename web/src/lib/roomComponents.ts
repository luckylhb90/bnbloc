// Room components (docs/prd/2026-10-20-rooms-as-components.md §2.1): a
// room's setup saved as a content component and installed as a new room,
// with the room's instructions, the apps made in it, the files its saver
// ticks and the tools its runs had (docs/prd/2026-10-20-complete-room-capture.md).
// The shapes are internal/wire/room_components.go's (wire.gen.ts); the
// helpers here are pure (the save dialog's state and its form, the AI
// draft onto it, the setup's summary), so they are unit-tested. The
// requests are lib/roomComponentsApi.ts.

import { cleanTags, splitList, tagProblem, type ContentComponent, type ContentForm, type ContentMCPRequirement, type ContentQuickPrompt } from "./contentComponents.ts";
import { formatBytes } from "./format.ts";
import { sourcesProblem } from "./roomSources.ts";
import { describeRecurrence, recipientLabel } from "./schedules.ts";
import { workNameFrom } from "./workGallery.ts";

export type { RoomComponentDraft, RoomInclude, RoomInstallReply, RoomInstallRequest, RoomPackageSchedule, RoomSpec } from "./wire.gen.ts";
import {
  MaxRoomCarryBytes,
  MaxRoomFiles,
  type ContentRequires,
  type RoomComponentDraft,
  type RoomDraftPart,
  type RoomInclude,
  type RoomPackageChannel,
  type RoomPackageSchedule,
  type RoomPackageSection,
  type RoomSourceAsk,
  type RoomSpec,
} from "./wire.gen.ts";

// MAX_ROOM_DESCRIPTION is a room's description bound (rooms.description):
// a room component's description becomes the new room's.
const MAX_ROOM_DESCRIPTION = 500;
const MAX_TAGS = 12;
const CATEGORY = /^[a-z0-9][a-z0-9-]{0,39}$/;

/** A part the save dialog lists: the reference a package names it by and
 *  its label. */
export interface RoomPart {
  readonly ref: string;
  readonly label: string;
}

/** A deliverable a package carries: one of the room's (key: its artifact
 *  id; a save includes it) or one the edited version carries (key: its
 *  folder or path; kept: true). sizeBytes is 0 when unknown. */
export interface RoomCarry {
  readonly key: string;
  readonly label: string;
  readonly sizeBytes: number;
  readonly kept: boolean;
}

/** An MCP tool the room's runs had, which the package requires, with the
 *  permission it declares for it. */
export interface RoomTool {
  readonly name: string;
  readonly permission: string;
}

/** The save dialog's state: the text the member edits (the room's
 *  instructions among it), every part of the room, and the keys of the
 *  parts they untick (partKey). */
export interface RoomSaveForm {
  name: string;
  displayName: string;
  description: string;
  category: string;
  tags: string;
  welcome: string;
  instructions: string;
  // The channel layout (C25): every section is kept; a channel is ticked
  // by its name (partKey("channel", name)).
  sections: readonly RoomPackageSection[];
  channels: readonly RoomPackageChannel[];
  schedules: readonly RoomPackageSchedule[];
  expert: RoomPart | null;
  skills: readonly RoomPart[];
  prompts: readonly RoomPart[];
  apps: readonly RoomPart[];
  madeApps: readonly RoomCarry[];
  files: readonly RoomCarry[];
  tools: readonly RoomTool[];
  sources: readonly RoomSourceAsk[];
  off: readonly string[];
  // Kept as they are, the room's or a component version's (the dialog
  // edits none: the room's settings edit a room's quick prompts).
  quickPrompts: readonly ContentQuickPrompt[];
  mcp: readonly ContentMCPRequirement[];
  permissions: readonly string[];
}

export type RoomPartKind = "channel" | "schedule" | "expert" | "skill" | "prompt" | "app" | "madeApp" | "file" | "tool";

/** partKey names one part of the form for its tick box. */
export function partKey(kind: RoomPartKind, id: string | number = ""): string {
  return `${kind}:${id}`;
}

const draftPart = (p: RoomDraftPart): RoomPart => ({ ref: p.ref, label: p.displayName || p.name });
const refPart = (ref: string): RoomPart => ({ ref, label: ref });

/** roomSaveFromDraft is the dialog's first state for a room's draft:
 *  everything the room holds, ticked (its welcome, quick prompts, its own
 *  asks for sources, the apps made in it and the tools its runs had too),
 *  but its files, which the saver ticks. */
export function roomSaveFromDraft(d: RoomComponentDraft): RoomSaveForm {
  const files = d.files.map((x): RoomCarry => ({ key: x.artifactId, label: x.fileName || x.title, sizeBytes: x.sizeBytes, kept: false }));
  return {
    name: d.name,
    displayName: d.displayName,
    description: d.description.slice(0, MAX_ROOM_DESCRIPTION),
    category: "",
    tags: "",
    welcome: d.welcome,
    instructions: "",
    sections: d.sections,
    channels: d.channels,
    schedules: d.schedules,
    expert: d.expert ? draftPart(d.expert) : null,
    skills: d.skills.map(draftPart),
    prompts: d.prompts.map(draftPart),
    apps: d.apps.map(draftPart),
    madeApps: d.madeApps.map((x): RoomCarry => ({ key: x.artifactId, label: x.title, sizeBytes: x.sizeBytes, kept: false })),
    files,
    tools: d.tools,
    sources: d.sources,
    off: files.map((x) => partKey("file", x.key)),
    quickPrompts: d.quickPrompts.map((q) => ({ title: q.title, prompt: q.prompt })),
    mcp: [],
    permissions: [],
  };
}

/** roomSaveFromComponent is the dialog's state for a new version of a room
 *  component: its setup as the version holds it (its instructions are the
 *  version's file: the dialog reads them into instructions). */
export function roomSaveFromComponent(c: ContentComponent): RoomSaveForm {
  const room = c.room;
  return {
    name: c.name,
    displayName: c.displayName ?? "",
    description: c.description ?? "",
    category: c.category ?? "",
    tags: (c.tags ?? []).join(", "),
    welcome: c.welcome ?? "",
    instructions: "",
    sections: room?.sections ?? [],
    channels: room?.channels ?? [],
    schedules: room?.schedules ?? [],
    expert: c.requires.expert ? refPart(c.requires.expert) : null,
    skills: (c.requires.skills ?? []).map(refPart),
    prompts: (c.requires.prompts ?? []).map(refPart),
    apps: (room?.apps ?? []).map(refPart),
    madeApps: (room?.appSnapshots ?? []).map((a): RoomCarry => ({ key: a.dir, label: a.title, sizeBytes: 0, kept: true })),
    files: (room?.files ?? []).map((x): RoomCarry => ({ key: x.path, label: x.name, sizeBytes: x.sizeBytes, kept: true })),
    tools: [],
    sources: room?.sources ?? [],
    off: [],
    quickPrompts: c.quickPrompts ?? [],
    mcp: c.requires.mcp ?? [],
    permissions: c.requires.permissions ?? [],
  };
}

/** toggleRoomPart ticks a part on or off. */
export function toggleRoomPart(f: RoomSaveForm, key: string, on: boolean): RoomSaveForm {
  const off = f.off.filter((k) => k !== key);
  return { ...f, off: on ? off : [...off, key] };
}

export const isOn = (f: RoomSaveForm, key: string) => !f.off.includes(key);

/** carried is what the package carries of kind: the ticked ones. */
export function carried(f: RoomSaveForm, kind: "madeApp" | "file"): RoomCarry[] {
  return (kind === "madeApp" ? f.madeApps : f.files).filter((x) => isOn(f, partKey(kind, x.key)));
}

/** carrySize is the bytes the ticked apps and files add to the package
 *  (a kept one of unknown size counts 0). */
export function carrySize(f: RoomSaveForm): number {
  return [...carried(f, "madeApp"), ...carried(f, "file")].reduce((n, x) => n + x.sizeBytes, 0);
}

/** carryProblem is what the ticked apps and files are past: the package's
 *  count of files, or its bound of both together. */
export function carryProblem(f: RoomSaveForm): string | null {
  if (carried(f, "file").length > MaxRoomFiles) return `A room component carries at most ${MaxRoomFiles} files`;
  const size = carrySize(f);
  if (size > MaxRoomCarryBytes) return `The ticked apps and files are ${formatBytes(size)}: a room component carries at most ${formatBytes(MaxRoomCarryBytes)} of them`;
  return null;
}

/** roomSaveProblem is what the dialog must fix before it saves (the
 *  server's validator is the authority; this catches the obvious). */
export function roomSaveProblem(f: RoomSaveForm): string | null {
  if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(f.name.trim())) return "The name uses lower-case letters, digits and hyphens";
  if ([...f.description.trim()].length > MAX_ROOM_DESCRIPTION) return `The description is at most ${MAX_ROOM_DESCRIPTION} characters (it becomes the room's)`;
  if (f.category.trim() && !CATEGORY.test(f.category.trim())) return "The category is one lower-case word (letters, digits and hyphens)";
  const tagIssue = tagProblem(splitList(f.tags));
  if (tagIssue) return tagIssue;
  const sourceIssue = sourcesProblem(f.sources);
  if (sourceIssue) return sourceIssue;
  if (f.instructions.trim() && f.expert && isOn(f, partKey("expert"))) {
    return "The instructions become the new room's expert: untick the room expert to carry them, or clear them to keep the expert";
  }
  const carryIssue = carryProblem(f);
  if (carryIssue) return carryIssue;
  for (const [i, s] of f.schedules.entries()) {
    if (isOn(f, partKey("schedule", i)) && s.channel && !isOn(f, partKey("channel", s.channel))) {
      return `A kept schedule runs in #${s.channel}: keep the channel too, or leave the schedule out`;
    }
  }
  return null;
}

/** roomContentForm is the content form a save sends: the kept parts (the
 *  ticked tools required with their permissions, the apps and files the
 *  edited version carries by folder and path; the room's own go in the
 *  save's include, roomInclude). */
export function roomContentForm(f: RoomSaveForm): ContentForm {
  const tools = f.tools.filter((t) => isOn(f, partKey("tool", t.name)));
  const requires: ContentRequires = {
    ...(f.expert && isOn(f, partKey("expert")) ? { expert: f.expert.ref } : {}),
    skills: f.skills.filter((p) => isOn(f, partKey("skill", p.ref))).map((p) => p.ref),
    prompts: f.prompts.filter((p) => isOn(f, partKey("prompt", p.ref))).map((p) => p.ref),
    mcp: [...f.mcp, ...tools.filter((t) => !f.mcp.some((m) => m.name === t.name)).map((t) => ({ name: t.name }))],
    permissions: [...new Set([...f.permissions, ...tools.map((t) => t.permission).filter(Boolean)])],
  };
  return {
    kind: "room",
    name: f.name.trim(),
    displayName: f.displayName,
    description: f.description,
    category: f.category,
    tags: splitList(f.tags),
    instructions: f.instructions.trim(),
    welcome: f.welcome,
    quickPrompts: f.quickPrompts.map((q) => ({ ...q })),
    skills: [],
    requires,
    room: {
      sections: [...f.sections],
      channels: f.channels.filter((c) => isOn(f, partKey("channel", c.name))),
      schedules: f.schedules.filter((_, i) => isOn(f, partKey("schedule", i))),
      apps: f.apps.filter((p) => isOn(f, partKey("app", p.ref))).map((p) => p.ref),
      appSnapshots: carried(f, "madeApp")
        .filter((x) => x.kept)
        .map((x) => ({ title: x.label, dir: x.key })),
      files: carried(f, "file")
        .filter((x) => x.kept)
        .map((x) => ({ name: x.label, path: x.key, sizeBytes: x.sizeBytes })),
      sources: f.sources.map((s) => ({ name: s.name, purpose: s.purpose.trim() })),
    },
  };
}

/** roomInclude is what a save of roomId's draft takes from the room: the
 *  ticked apps made in it and files, by id; null for none. */
export function roomInclude(f: RoomSaveForm, roomId: string): RoomInclude | null {
  const ids = (kind: "madeApp" | "file") =>
    carried(f, kind)
      .filter((x) => !x.kept)
      .map((x) => x.key);
  const [apps, files] = [ids("madeApp"), ids("file")];
  if (!roomId || apps.length + files.length === 0) return null;
  return { roomId, ...(apps.length ? { apps } : {}), ...(files.length ? { files } : {}) };
}

/** scheduleLine is one schedule in a line: who carries it out, when, and
 *  its text's start. */
export function scheduleLine(s: RoomPackageSchedule): string {
  const text = s.text.replace(/\s+/g, " ").trim();
  const head = text.length > 60 ? `${text.slice(0, 59)}…` : text;
  return `${recipientLabel(s.recipient)} · ${describeRecurrence(s.recurrence, s.timezone)}${s.channel ? ` · #${s.channel}` : ""}${head ? ` · ${head}` : ""}`;
}

/** roomSetupSummary is a one-line summary of what a room component sets up. */
export function roomSetupSummary(room: RoomSpec | undefined, requires: ContentRequires): string {
  const n = (count: number, one: string) => `${count} ${one}${count === 1 ? "" : "s"}`;
  const parts: string[] = [];
  if (room?.sections.length) parts.push(n(room.sections.length, "section"));
  if (room?.channels.length) parts.push(n(room.channels.length, "channel"));
  if (room?.schedules.length) parts.push(n(room.schedules.length, "scheduled task"));
  if (requires.expert) parts.push("an expert");
  const packs = (requires.skills?.length ?? 0) + (requires.prompts?.length ?? 0);
  if (packs) parts.push(n(packs, "pack"));
  const apps = (room?.apps.length ?? 0) + (room?.appSnapshots.length ?? 0);
  if (apps) parts.push(n(apps, "app"));
  if (room?.files.length) parts.push(n(room.files.length, "file"));
  return parts.length ? parts.join(" · ") : "An empty room";
}

/** describeRoomInput is describe-room's <text>: the setup being saved (the
 *  apps made in the room and its files with their sizes and whether they
 *  are included, the tools its runs had) and the member's current values,
 *  as JSON. */
export function describeRoomInput(f: RoomSaveForm): string {
  const form = roomContentForm(f);
  return JSON.stringify({
    setup: {
      sections: form.room?.sections.map((s) => s.name),
      channels: form.room?.channels.map((c) => (c.section ? `${c.section} / #${c.name}` : `#${c.name}`)),
      scheduledTasks: form.room?.schedules.map(scheduleLine),
      expert: f.expert && isOn(f, partKey("expert")) ? f.expert.label : undefined,
      skillPacks: f.skills.filter((p) => isOn(f, partKey("skill", p.ref))).map((p) => p.label),
      promptPacks: f.prompts.filter((p) => isOn(f, partKey("prompt", p.ref))).map((p) => p.label),
      apps: f.apps.filter((p) => isOn(f, partKey("app", p.ref))).map((p) => p.label),
      madeApps: f.madeApps.map((x) => carryLine(f, "madeApp", x)),
      files: f.files.map((x) => carryLine(f, "file", x)),
      tools: f.tools.map((t) => t.name),
    },
    current: { displayName: f.displayName, description: f.description, category: f.category, tags: f.tags, welcome: f.welcome },
  });
}

/** carryLine is an app or a file in describe-room's input: its name,
 *  whether the member included it and its size. */
function carryLine(f: RoomSaveForm, kind: "madeApp" | "file", x: RoomCarry): string {
  const size = x.sizeBytes > 0 ? `, ${formatBytes(x.sizeBytes)}` : "";
  return `${x.label} (${isOn(f, partKey(kind, x.key)) ? "included" : "not included"}${size})`;
}

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");

/** roomDraftPatch turns a describe-room reply into the dialog's fields: a
 *  category of one word (else dropped), the tags the tag rule admits, the
 *  description within a room's bound, the instructions as drafted. The
 *  name follows the display name only while the member has not changed it. */
export function roomDraftPatch(fields: Readonly<Record<string, unknown>>, f: RoomSaveForm): Partial<RoomSaveForm> {
  const patch: { -readonly [K in keyof RoomSaveForm]?: RoomSaveForm[K] } = {};
  const displayName = str(fields.displayName);
  if (displayName) {
    patch.displayName = displayName;
    const suggested = workNameFrom(displayName);
    if (suggested && f.name === workNameFrom(f.displayName)) patch.name = suggested;
  }
  const description = str(fields.description);
  if (description) patch.description = [...description].slice(0, MAX_ROOM_DESCRIPTION).join("");
  const category = str(fields.category).toLowerCase();
  if (CATEGORY.test(category)) patch.category = category;
  const raw = Array.isArray(fields.tags) ? fields.tags.filter((t): t is string => typeof t === "string") : splitList(str(fields.tags));
  const tags = cleanTags(raw)
    .filter((t) => tagProblem([t]) === null)
    .slice(0, MAX_TAGS);
  if (tags.length > 0) patch.tags = tags.join(", ");
  const welcome = str(fields.welcome);
  if (welcome) patch.welcome = welcome;
  const instructions = str(fields.instructions);
  if (instructions) patch.instructions = instructions;
  return patch;
}
