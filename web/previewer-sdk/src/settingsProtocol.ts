// The settings page protocol (protocol 1): the ONE definition of every
// message between the host (the bnbloc web client) and a component's own
// settings page, a bundle the platform runs in the previewer's sandboxed
// opaque-origin frame (docs/architecture/settings-pages.md). Both sides
// import it: the host (web/src/lib/settingsPageFrame.ts) and the page SDK
// (./settings.ts), so the shapes cannot drift.
//
// Pure: no DOM globals, no dependencies, so it is unit-tested under node.
//
//   frame -> host : settings.ready                               (once, handlers registered)
//   host  -> frame: settings.init    {protocol, theme, scope}    (the answer to ready)
//   frame -> host : settings.get     {id}
//   frame -> host : settings.set     {id, values, clear?}
//   frame -> host : settings.check   {id}
//   host  -> frame: settings.view    {id, view}                  (a read, or a save done)
//   host  -> frame: settings.refused {id, reason, key?}          (a read or a save refused)
//   host  -> frame: settings.checked {id, supported}             (the component's own check)
//   frame -> host : settings.close
//
// A page edits one scope's settings (the one init names) and only through
// the host: it never reads a secret's value (a secret is only set or not),
// never names a setting the declaration lacks or another scope's, and never
// reaches another component's values. The platform checks every value it
// saves against the declaration, as it checks the one form's.

import type { Theme } from "./protocol.ts";

/** Protocol version carried in settings.init. */
export const SETTINGS_PAGE_PROTOCOL = 1;

/** Message bounds. They only bound a message: what a value may be is the
 *  platform's rule (the declaration), answered as a refusal. */
export const SETTINGS_KEYS_LIMIT = 64;
export const SETTING_VALUE_LIMIT = 16 * 1024;
export const REASON_LIMIT = 500;

/** One setting of the scope the page edits, as the page may know it: a
 *  secret's value never (`set` says whether one is stored). `type` and the
 *  scopes are the platform's vocabulary; a page shows a type it does not
 *  know as text. */
export interface SettingsPageField {
  readonly key: string;
  readonly label: string;
  readonly type: string;
  readonly help?: string;
  readonly required: boolean;
  readonly default?: string;
  readonly options?: readonly string[];
  readonly set: boolean;
  readonly value?: string;
}

/** A setting another scope sets (the platform's, the organization's), shown
 *  to the page only as set or not. */
export interface SettingsPageReadOnly {
  readonly key: string;
  readonly label: string;
  readonly type: string;
  readonly scope: string;
  readonly set: boolean;
}

/** What settings.get answers: the scope's settings, the other scopes' the
 *  host knows of, and the keys of the required settings not set yet. */
export interface SettingsPageView {
  readonly scope: string;
  readonly fields: readonly SettingsPageField[];
  readonly readOnly: readonly SettingsPageReadOnly[];
  readonly missing: readonly string[];
}

/** A save: values by key ("" clears one; a secret left out keeps the stored
 *  one), clear names stored values to remove. */
export interface SettingsPageUpdate {
  readonly values: Readonly<Record<string, string>>;
  readonly clear?: readonly string[];
}

export type PageMessage =
  | { readonly type: "settings.ready" }
  | { readonly type: "settings.get"; readonly id: number }
  | ({ readonly type: "settings.set"; readonly id: number } & SettingsPageUpdate)
  | { readonly type: "settings.check"; readonly id: number }
  | { readonly type: "settings.close" };

export type HostAnswer =
  | { readonly type: "settings.init"; readonly protocol: number; readonly theme: Theme; readonly scope: string }
  | { readonly type: "settings.view"; readonly id: number; readonly view: SettingsPageView }
  | { readonly type: "settings.refused"; readonly id: number; readonly reason: string; readonly key?: string }
  | { readonly type: "settings.checked"; readonly id: number; readonly supported: boolean };

type Rec = Readonly<Record<string, unknown>>;

function isRecord(value: unknown): value is Rec {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requestId(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

function text(value: unknown): value is string {
  return typeof value === "string" && value.length <= SETTING_VALUE_LIMIT;
}

function keyList(value: unknown): readonly string[] | null {
  if (value === undefined) return [];
  return Array.isArray(value) && value.length <= SETTINGS_KEYS_LIMIT && value.every(text) ? value : null;
}

/** Reads a save's values and clear list: string values by key, within the
 *  message bounds; null when malformed. */
export function readUpdate(values: unknown, clear: unknown): SettingsPageUpdate | null {
  if (!isRecord(values)) return null;
  const entries = Object.entries(values);
  const keys = keyList(clear);
  if (entries.length > SETTINGS_KEYS_LIMIT || !keys || !entries.every(([k, v]) => text(k) && text(v))) return null;
  const out: Record<string, string> = {};
  for (const [k, v] of entries) out[k] = v as string;
  return keys.length > 0 ? { values: out, clear: keys } : { values: out };
}

/** Validates a frame -> host message (the host also checks the sender: see
 *  web/src/lib/settingsPageFrame.ts). Null for anything unknown or
 *  malformed. */
export function readPageMessage(data: unknown): PageMessage | null {
  if (!isRecord(data)) return null;
  switch (data.type) {
    case "settings.ready":
    case "settings.close":
      return { type: data.type };
    case "settings.get":
    case "settings.check": {
      const id = requestId(data.id);
      return id === null ? null : { type: data.type, id };
    }
    case "settings.set": {
      const id = requestId(data.id);
      const update = readUpdate(data.values, data.clear);
      return id === null || !update ? null : { type: "settings.set", id, ...update };
    }
    default:
      return null;
  }
}

function readField(value: unknown): SettingsPageField | null {
  if (!isRecord(value)) return null;
  const { key, label, type, help, required, set, value: v, options } = value;
  if (!text(key) || !text(label) || !text(type) || typeof set !== "boolean" || typeof required !== "boolean") return null;
  if ((help !== undefined && !text(help)) || (value.default !== undefined && !text(value.default)) || (v !== undefined && !text(v))) return null;
  const opts = keyList(options);
  if (!opts) return null;
  return {
    key,
    label,
    type,
    required,
    set,
    ...(help !== undefined ? { help } : {}),
    ...(value.default !== undefined ? { default: value.default as string } : {}),
    ...(opts.length > 0 ? { options: opts } : {}),
    // A secret's value is never the page's, whatever arrives.
    ...(v !== undefined && type !== "secret" ? { value: v } : {}),
  };
}

function readReadOnly(value: unknown): SettingsPageReadOnly | null {
  if (!isRecord(value)) return null;
  const { key, label, type, scope, set } = value;
  return text(key) && text(label) && text(type) && text(scope) && typeof set === "boolean" ? { key, label, type, scope, set } : null;
}

function readList<T>(value: unknown, read: (v: unknown) => T | null): T[] | null {
  if (!Array.isArray(value) || value.length > SETTINGS_KEYS_LIMIT) return null;
  const out: T[] = [];
  for (const v of value) {
    const item = read(v);
    if (!item) return null;
    out.push(item);
  }
  return out;
}

/** Validates a settings.view's view. */
export function readView(value: unknown): SettingsPageView | null {
  if (!isRecord(value) || !text(value.scope)) return null;
  const fields = readList(value.fields, readField);
  const readOnly = readList(value.readOnly, readReadOnly);
  const missing = keyList(value.missing);
  return fields && readOnly && missing ? { scope: value.scope, fields, readOnly, missing } : null;
}

/** Validates a host -> frame message (the page SDK also checks the sender).
 *  Null for anything unknown or malformed. */
export function readHostAnswer(data: unknown): HostAnswer | null {
  if (!isRecord(data)) return null;
  switch (data.type) {
    case "settings.init":
      if (data.protocol !== SETTINGS_PAGE_PROTOCOL || (data.theme !== "light" && data.theme !== "dark") || !text(data.scope)) return null;
      return { type: "settings.init", protocol: SETTINGS_PAGE_PROTOCOL, theme: data.theme, scope: data.scope };
    case "settings.view": {
      const id = requestId(data.id);
      const view = readView(data.view);
      return id === null || !view ? null : { type: "settings.view", id, view };
    }
    case "settings.refused": {
      const id = requestId(data.id);
      if (id === null || typeof data.reason !== "string" || [...data.reason].length > REASON_LIMIT) return null;
      if (data.key !== undefined && !text(data.key)) return null;
      return { type: "settings.refused", id, reason: data.reason, ...(data.key !== undefined ? { key: data.key } : {}) };
    }
    case "settings.checked": {
      const id = requestId(data.id);
      return id === null || typeof data.supported !== "boolean" ? null : { type: "settings.checked", id, supported: data.supported };
    }
    default:
      return null;
  }
}

/** Cuts a refusal's reason to the protocol bound. */
export function fitReason(reason: string): string {
  return [...reason].slice(0, REASON_LIMIT).join("");
}
