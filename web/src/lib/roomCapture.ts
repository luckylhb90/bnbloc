// What a room's way of working depends on
// (docs/prd/2026-10-20-complete-room-capture.md §2.1): the describe-room
// assist answers dependsOn, items of what, kind, included and why, and the
// save dialog shows them as a checklist. A dependency the package can
// carry (an app made in the room, a file, a tool) is matched to its tick
// box, so its row says what the package will really carry; the others say
// what the AI found and why it stays out. AI proposes ("Fill with AI" ticks
// what it says the room needs), the member decides. The helpers are pure.

import { isOn, partKey, type RoomSaveForm } from "./roomComponents.ts";

/** The kinds a dependency may be, in the order describe-room's item
 *  description names them (a test holds them together). */
export const DEPENDENCY_KINDS = ["instructions", "app", "file", "tool", "schedule", "agent", "data", "other"] as const;
export type DependencyKind = (typeof DEPENDENCY_KINDS)[number];

/** DEPENDENCY_LABELS are the kinds in words. */
export const DEPENDENCY_LABELS: Readonly<Record<DependencyKind, string>> = {
  instructions: "Instructions",
  app: "App",
  file: "File",
  tool: "Tool",
  schedule: "Schedule",
  agent: "Agent",
  data: "Data",
  other: "Other",
};

/** At most this many dependencies are shown. */
const MAX_DEPENDENCIES = 12;

/** A dependency as the assist answered it: included is its word that the
 *  package should carry it. */
export interface RoomDependency {
  readonly what: string;
  readonly kind: DependencyKind;
  readonly included: boolean;
  readonly why: string;
}

/** A row of the checklist: the dependency, the tick box it matches (null:
 *  none; the package cannot carry it, or it is not one of the room's
 *  parts) and whether the package carries it now. */
export interface ChecklistRow extends RoomDependency {
  readonly key: string | null;
  readonly carried: boolean;
}

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");

/** roomDependencies reads describe-room's dependsOn: an unknown kind is
 *  "other", included is a "yes", each named once, at most MAX_DEPENDENCIES. */
export function roomDependencies(fields: Readonly<Record<string, unknown>>): RoomDependency[] {
  const raw = Array.isArray(fields.dependsOn) ? fields.dependsOn : [];
  const out: RoomDependency[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const it = item as Record<string, unknown>;
    const what = str(it.what);
    if (!what || out.some((d) => d.what === what)) continue;
    const kind = (DEPENDENCY_KINDS as readonly string[]).includes(str(it.kind).toLowerCase()) ? (str(it.kind).toLowerCase() as DependencyKind) : "other";
    out.push({ what, kind, included: str(it.included).toLowerCase() === "yes", why: str(it.why) });
    if (out.length === MAX_DEPENDENCIES) break;
  }
  return out;
}

// norm is a name as it is matched: lower-cased, without a trailing
// "(included, 2 KB)" the input wrote beside it.
const norm = (s: string) =>
  s
    .replace(/\s*\([^)]*\)\s*$/, "")
    .trim()
    .toLowerCase();

/** dependencyPart is the tick box a dependency names: an app made in the
 *  room or from a work, a file or a tool of the form, by its name; null
 *  for anything else. */
export function dependencyPart(f: RoomSaveForm, d: RoomDependency): string | null {
  const what = norm(d.what);
  const same = (label: string) => norm(label) === what;
  switch (d.kind) {
    case "app": {
      const made = f.madeApps.find((x) => same(x.label));
      if (made) return partKey("madeApp", made.key);
      const work = f.apps.find((p) => same(p.label) || same(p.ref));
      return work ? partKey("app", work.ref) : null;
    }
    case "file": {
      const file = f.files.find((x) => same(x.label));
      return file ? partKey("file", file.key) : null;
    }
    case "tool": {
      const tool = f.tools.find((t) => same(t.name));
      return tool ? partKey("tool", tool.name) : null;
    }
  }
  return null;
}

/** roomChecklist is the dialog's checklist: each dependency with the tick
 *  box it matches and whether the package carries it now (the form's tick,
 *  the instructions' text, else the assist's word). */
export function roomChecklist(f: RoomSaveForm, deps: readonly RoomDependency[]): ChecklistRow[] {
  return deps.map((d) => {
    const key = dependencyPart(f, d);
    const carried = key ? isOn(f, key) : d.kind === "instructions" ? f.instructions.trim() !== "" : d.included;
    return { ...d, key, carried };
  });
}

/** applyDependencies ticks the parts the assist says the package should
 *  carry; what it says stays out keeps the member's tick. */
export function applyDependencies(f: RoomSaveForm, deps: readonly RoomDependency[]): RoomSaveForm {
  const on = new Set(deps.filter((d) => d.included).flatMap((d) => dependencyPart(f, d) ?? []));
  return on.size === 0 ? f : { ...f, off: f.off.filter((k) => !on.has(k)) };
}
