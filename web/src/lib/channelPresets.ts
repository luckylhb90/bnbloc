// Channel presets (docs/prd/2026-10-28-channels-and-threads.md §2.1c): a kind
// of channel as content. The pure side of applying one: what it changes on a
// channel (the preview), the one PATCH that applies it, the PATCH that undoes
// it, and a channel captured as a preset. The room service is the one owner
// of what a setting means: a preset's settings are its channel route's
// settings document, merged over the channel's by key, so a key the room
// service learns needs nothing here. The requests are channelsApi and
// contentApi.
import type { ChannelPatchRequest, ChannelPreset, ChannelSettings, RoomChannel } from "./wire.gen.ts";

/** A preset's settings document as an object (an absent or malformed one is
 *  none). A default agent is a person's: a preset never carries one. */
export function presetSettings(p: Pick<ChannelPreset, "settings"> | null | undefined): Record<string, unknown> {
  const s = p?.settings;
  if (!s || typeof s !== "object" || Array.isArray(s)) return {};
  const { defaultAgent: _drop, ...rest } = s as Record<string, unknown>;
  return rest;
}

/** One line of a preset's preview: a field, what the channel has and what
 *  the preset sets. */
export interface PresetChange {
  readonly field: string;
  readonly from: string;
  readonly to: string;
}

const shown = (v: unknown): string => {
  if (v === undefined || v === null || v === "" || v === false || (Array.isArray(v) && v.length === 0)) return "—";
  if (Array.isArray(v)) return v.map((x) => (x && typeof x === "object" && "name" in x ? String((x as { name: unknown }).name) : JSON.stringify(x))).join(", ");
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
};

/** presetChanges is a preset's preview on a channel: each purpose, rules and
 *  settings key it sets to something other than the channel has, in that
 *  order. A preset leaves what it does not name as the channel has it. */
export function presetChanges(ch: Pick<RoomChannel, "purpose" | "rules" | "settings">, preset: ChannelPreset): PresetChange[] {
  const out: PresetChange[] = [];
  if (preset.purpose && preset.purpose !== ch.purpose) out.push({ field: "purpose", from: shown(ch.purpose), to: shown(preset.purpose) });
  if (preset.rules && preset.rules !== ch.rules) out.push({ field: "rules", from: shown(ch.rules), to: shown(preset.rules) });
  const have = (ch.settings ?? {}) as Record<string, unknown>;
  for (const [k, v] of Object.entries(presetSettings(preset))) {
    if (JSON.stringify(have[k] ?? null) !== JSON.stringify(v ?? null) && shown(have[k]) !== shown(v)) {
      out.push({ field: k, from: shown(have[k]), to: shown(v) });
    }
  }
  return out;
}

/** applyPatch is the one PATCH that applies a preset: its purpose and rules,
 *  and the channel's settings with the preset's keys over them (the route
 *  replaces the whole settings document, so the channel's default agent and
 *  any key the preset does not name stay). */
export function applyPatch(ch: Pick<RoomChannel, "settings">, preset: ChannelPreset): ChannelPatchRequest {
  const patch: { purpose?: string; rules?: string; settings?: ChannelSettings } = {};
  if (preset.purpose) patch.purpose = preset.purpose;
  if (preset.rules) patch.rules = preset.rules;
  const keys = presetSettings(preset);
  if (Object.keys(keys).length > 0) patch.settings = { ...(ch.settings ?? {}), ...keys } as ChannelSettings;
  return patch;
}

/** undoPatch puts back what applyPatch changed: the purpose, rules and
 *  settings the channel had before. */
export function undoPatch(before: Pick<RoomChannel, "purpose" | "rules" | "settings">, applied: ChannelPatchRequest): ChannelPatchRequest {
  const patch: { purpose?: string; rules?: string; settings?: ChannelSettings } = {};
  if (applied.purpose !== undefined) patch.purpose = before.purpose;
  if (applied.rules !== undefined) patch.rules = before.rules;
  if (applied.settings !== undefined) patch.settings = before.settings ?? {};
  return patch;
}

/** capturePreset is a channel as a preset: its purpose, rules and settings,
 *  without its default agent (a person's agent; a preset names no people). */
export function capturePreset(ch: Pick<RoomChannel, "purpose" | "rules" | "settings">): ChannelPreset {
  const { defaultAgent: _drop, ...settings } = (ch.settings ?? {}) as Record<string, unknown>;
  const out: { purpose?: string; rules?: string; settings?: Record<string, unknown> } = {};
  if (ch.purpose) out.purpose = ch.purpose;
  if (ch.rules) out.rules = ch.rules;
  const kept = Object.fromEntries(Object.entries(settings).filter(([, v]) => v !== undefined && v !== null && v !== "" && v !== false && v !== 0));
  if (Object.keys(kept).length > 0) out.settings = kept;
  return out;
}

/** presetName is a content name from a channel's name ("Help desk" →
 *  "help-desk-preset"): lower-case letters, digits and hyphens. */
export function presetName(channelName: string): string {
  const base = channelName
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
  return `${base || "channel"}-preset`;
}

/** presetFromFields reads a draft-channel-preset answer (purpose, rules,
 *  settings), or null when it holds nothing a preset sets. */
export function presetFromFields(fields: Record<string, unknown> | null | undefined): ChannelPreset | null {
  if (!fields) return null;
  const purpose = typeof fields.purpose === "string" ? fields.purpose.trim().replace(/\s+/g, " ") : "";
  const rules = typeof fields.rules === "string" ? fields.rules.trim() : "";
  let settings: unknown = fields.settings;
  if (typeof settings === "string") {
    try {
      settings = JSON.parse(settings);
    } catch {
      settings = undefined;
    }
  }
  const out: ChannelPreset = { ...(purpose ? { purpose } : {}), ...(rules ? { rules } : {}) };
  const keys = presetSettings({ settings });
  if (Object.keys(keys).length > 0) out.settings = keys;
  return out.purpose || out.rules || out.settings ? out : null;
}
