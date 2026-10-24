// Host side of a component's own settings page
// (docs/architecture/settings-pages.md): where the page loads from, which
// messages it may send, and what it may read and save. Pure (no React, no
// DOM globals) so it is unit-tested under node; SettingsPageFrame wires it
// to the frame. The messages are the SDK's settings page protocol, the one
// definition both sides import; the frame is a previewer's (the same scheme
// and gateway serving, the same sandbox, the same sender rule).
//
// A page edits ONE scope (the dialog decides which) through the scope's own
// calls: the settings routes for the platform's and an organization's, the
// desktop's member store for a member's own. It reads only that scope's
// settings (a secret only as set or not) and the others' as set or not,
// names only keys of that scope, and every value it saves is checked by
// the platform against the declaration, as the one form's are.

import {
  fitReason,
  readPageMessage,
  SETTINGS_PAGE_PROTOCOL,
  type HostAnswer,
  type PageMessage,
  type SettingValue,
  type SettingsPageField,
  type SettingsPageView,
} from "../../previewer-sdk/src/settingsProtocol.ts";
import type { Theme } from "../../previewer-sdk/src/protocol.ts";
import { errorField, errorReason } from "./errors.ts";
import { frameSrc, fromFrame, validBundlePath, type FrameEnv } from "./previewerFrame.ts";
import { linesValue, parseLines, type ComponentSetting, type ComponentSettingField, type ComponentSettingScope, type ComponentSettingsUpdate, type ComponentSettingsView, type DesktopComponentSettings } from "./componentSettings.ts";

export type { SettingsPageView };

/** Where a scope's page loads from: its entry and bundle pin (the desktop
 *  serves the bundle by its pin) and, in a browser, the scope's ticketed
 *  frame base (ComponentSettingsView.page). */
export interface SettingsPageSource {
  readonly entry: string;
  readonly bundleSha256: string;
  readonly frameUrl?: string;
}

/** The iframe src of a page, or null where it cannot load (a browser
 *  without a frame ticket, a malformed entry). */
export function settingsPageSrc(page: SettingsPageSource, env: FrameEnv): string | null {
  if (!validBundlePath(page.entry) || (!env.convertFileSrc && !page.frameUrl)) return null;
  return frameSrc({ schemePath: `_page/${page.bundleSha256}/${page.entry}`, gatewayPath: `${page.frameUrl ?? ""}${page.entry}` }, env);
}

/** A page message from the page's own frame, validated; null otherwise. */
export function readSettingsPageMessage(ev: { origin: string; source: unknown; data: unknown }, frameWindow: unknown): PageMessage | null {
  return fromFrame(ev, frameWindow) ? readPageMessage(ev.data) : null;
}

/** The answer to the page's ready: the protocol, the theme, the scope. */
export function settingsPageInit(theme: Theme, scope: ComponentSettingScope): HostAnswer {
  return { type: "settings.init", protocol: SETTINGS_PAGE_PROTOCOL, theme, scope };
}

/** A stored value as a page reads it: a lines setting's as its lines. */
function pageValue(f: Pick<ComponentSetting, "type">, v: string): SettingValue {
  return f.type === "lines" ? parseLines(v) : v;
}

function pageField(f: ComponentSettingField): SettingsPageField {
  return {
    key: f.key,
    label: f.label,
    type: f.type,
    required: !!f.required,
    set: f.set,
    ...(f.help ? { help: f.help } : {}),
    ...(f.default ? { default: pageValue(f, f.default) } : {}),
    ...(f.options?.length ? { options: f.options } : {}),
    // A secret's value is never the page's.
    ...(f.type !== "secret" && f.value ? { value: pageValue(f, f.value) } : {}),
  };
}

/** The one projection of settings a page may read: the fields of the scope
 *  it edits, the other scopes' (never a value) and the keys still missing. */
export function settingsPageView(
  scope: ComponentSettingScope,
  fields: readonly ComponentSettingField[],
  others: readonly (ComponentSetting & { readonly set: boolean })[],
  missing: readonly string[],
): SettingsPageView {
  return {
    scope,
    fields: fields.map(pageField),
    readOnly: others.map((s) => ({ key: s.key, label: s.label, type: s.type, scope: s.scope, set: s.set })),
    missing: [...missing],
  };
}

/** A platform or organization view (the settings routes): the other
 *  scopes' settings it knows of are the missing ones. */
export function scopePageView(view: ComponentSettingsView): SettingsPageView {
  const others = view.missing.filter((s) => s.scope !== view.scope).map((s) => ({ ...s, set: false }));
  return settingsPageView(view.scope, view.fields, others, view.missing.map((s) => s.key));
}

/** A member's own view (the desktop): the organization's and the
 *  platform's settings only as set or not. */
export function memberPageView(item: DesktopComponentSettings): SettingsPageView {
  const others = item.fields.filter((f) => f.scope !== "member");
  return settingsPageView("member", item.fields.filter((f) => f.scope === "member"), others, item.missing);
}

/** How a page frame reads and saves its scope. */
export interface SettingsPageScope {
  readonly load: () => Promise<SettingsPageView>;
  readonly save: (update: ComponentSettingsUpdate) => Promise<SettingsPageView>;
}

function refused(id: number, reason: string, key = ""): HostAnswer {
  return { type: "settings.refused", id, reason: fitReason(reason), ...(key ? { key } : {}) };
}

/** A page's values as the platform stores them: a lines setting's list as
 *  its lines (the form's rule: trimmed, none empty, joined by "\n"), every
 *  other value a string. The key of a list given for a setting that is not
 *  a list, or null when every value fits. */
export function storedValues(fields: readonly Pick<SettingsPageField, "key" | "type">[], values: Readonly<Record<string, SettingValue>>): { values: Record<string, string> } | { notList: string } {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(values)) {
    const lines = fields.find((f) => f.key === k)?.type === "lines";
    if (typeof v !== "string" && !lines) return { notList: k };
    out[k] = lines ? linesValue(typeof v === "string" ? [v] : v) : (v as string);
  }
  return { values: out };
}

/** The host of one page frame: answers each request of the page for the
 *  one scope it edits (ready and close are the frame component's). A save
 *  naming a key that is not a field of the scope is refused here, before
 *  anything is sent; a refusal of the platform comes back with its reason
 *  and the field it names. */
export function settingsPageHost(scope: SettingsPageScope): { answer(msg: PageMessage): Promise<HostAnswer | null> } {
  let view: SettingsPageView | null = null;
  const fieldKey = (key: string) => (view?.fields.some((f) => f.key === key) ? key : "");
  return {
    async answer(msg) {
      switch (msg.type) {
        case "settings.get":
          try {
            view = await scope.load();
            return { type: "settings.view", id: msg.id, view };
          } catch (e) {
            return refused(msg.id, errorReason(e) || "Could not read the settings");
          }
        case "settings.set":
          try {
            view ??= await scope.load();
            const outside = [...Object.keys(msg.values), ...(msg.clear ?? [])].find((k) => !fieldKey(k));
            if (outside !== undefined) return refused(msg.id, `"${outside}" is not a setting this page edits`);
            const stored = storedValues(view.fields, msg.values);
            if ("notList" in stored) return refused(msg.id, `"${stored.notList}" takes a string, not a list`, stored.notList);
            view = await scope.save(msg.clear?.length ? { values: stored.values, clear: [...msg.clear] } : { values: stored.values });
            return { type: "settings.view", id: msg.id, view };
          } catch (e) {
            return refused(msg.id, errorReason(e) || "Could not save the settings", fieldKey(errorField(e)));
          }
        case "settings.check":
          // A component's own check (PRD §2.5) is not built yet.
          return { type: "settings.checked", id: msg.id, supported: false };
        default:
          return null;
      }
    },
  };
}
