// Component settings (docs/prd/2026-10-19-component-settings.md): the
// settings a component declares (`settings` in its manifest), each set by the
// super admin (platform scope), an organization admin (org scope) or each
// member on their desktop (member scope). This is the one form model every
// settings view uses (views/settings/SettingsForm.tsx): the platform
// console, the organization's component settings and the member's Settings →
// Components. The gateway (core/pkgmeta) and the desktop shell decide what a
// value may be; these helpers only shape the form. Pure; the calls are in
// componentSettingsApi.ts and lib/desktop.ts.
import type { ComponentSetting, ComponentSettingField, ComponentSettingScope, ComponentSettingType, ComponentSettingsUpdate } from "./wire.gen.ts";

export type { ComponentSetting, ComponentSettingField, ComponentSettingScope, ComponentSettingsUpdate, ComponentSettingsView } from "./wire.gen.ts";

// DesktopComponentSettings is one installed component as the desktop shell
// reports it (`component_settings`, apps/desktop/src-tauri/src/internal_ext/
// component_settings.rs): every declared setting (member values from this
// computer, a secret's never; platform and organization ones only as set or
// not), what keeps it in needs setup, and its state on this computer.
export interface DesktopComponentSettings {
  kind: "plugin" | "mcp";
  name: string;
  version: string;
  // A plugin sidecar's state, or a stdio MCP tool's download state ("" for
  // an http tool), and its message.
  state: string;
  message: string;
  transport?: string;
  tools?: { name: string; description?: string }[];
  // "oauth" when the member signs in to the tool ("" otherwise).
  auth: string;
  // The member's sign-in on this computer (oauth tools; never a token).
  signIn?: McpSignIn;
  fields: ComponentSettingField[];
  // Keys of the required settings that are not set, any scope.
  missing: string[];
}

export interface McpSignIn {
  // The sign-in server's host.
  server: string;
  signedInAtMs: number;
  // 0 when the server stated no expiry.
  expiresAtMs: number;
  // Usable now, or refreshable.
  valid: boolean;
}

// The control a setting's type renders as.
export type SettingInput =
  | { readonly control: "input"; readonly type: "text" | "password" | "url" | "number" }
  | { readonly control: "switch" }
  | { readonly control: "select" };

const inputs: Record<ComponentSettingType, SettingInput> = {
  text: { control: "input", type: "text" },
  path: { control: "input", type: "text" },
  secret: { control: "input", type: "password" },
  url: { control: "input", type: "url" },
  number: { control: "input", type: "number" },
  boolean: { control: "switch" },
  choice: { control: "select" },
};

// settingInput is the control a setting renders as.
export function settingInput(type: ComponentSettingType): SettingInput {
  return inputs[type];
}

const scopeWords: Record<ComponentSettingScope, string> = {
  platform: "Set by the platform",
  org: "Set by your organization",
  member: "Yours, on this computer",
};

// scopeText says who sets a setting.
export function scopeText(scope: ComponentSettingScope): string {
  return scopeWords[scope];
}

// isSecret: a write-only value (never shown back, only "set").
export function isSecret(s: Pick<ComponentSetting, "type">): boolean {
  return s.type === "secret";
}

// initialValues pre-fills stored non-secret values; secret inputs start
// empty. A default is never pre-filled (a field left empty keeps getting
// the component's default, also when a later version changes it): the form
// shows it as a placeholder.
export function initialValues(fields: readonly ComponentSettingField[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of fields) out[f.key] = isSecret(f) ? "" : (f.value ?? "");
  return out;
}

// shownValue is what a field holds as the component sees it: the form's
// value, else the default.
export function shownValue(f: Pick<ComponentSetting, "default">, value: string | undefined): string {
  return (value ?? "").trim() || (f.default ?? "");
}

// settingsUpdate builds the update a form sends for its fields: a secret
// left empty is not sent (the stored one stays), any other field is sent
// trimmed ("" clears it); `clear` names the stored secrets to remove.
export function settingsUpdate(
  fields: readonly ComponentSettingField[],
  values: Readonly<Record<string, string>>,
  clear: readonly string[] = [],
): ComponentSettingsUpdate {
  const out: Record<string, string> = {};
  for (const f of fields) {
    if (clear.includes(f.key)) continue;
    const v = (values[f.key] ?? "").trim();
    if (isSecret(f) && v === "") continue;
    out[f.key] = v;
  }
  return clear.length > 0 ? { values: out, clear: [...clear] } : { values: out };
}

// missingRequired lists the labels of required fields the form leaves
// empty (a stored secret or a default counts as filled).
export function missingRequired(fields: readonly ComponentSettingField[], values: Readonly<Record<string, string>>): string[] {
  return fields
    .filter((f) => f.required && !(values[f.key] ?? "").trim() && !(isSecret(f) && f.set) && !f.default)
    .map((f) => f.label);
}

// settingLabels maps keys to their labels (a key the declaration lacks
// stays as it is).
export function settingLabels(keys: readonly string[], settings: readonly Pick<ComponentSetting, "key" | "label">[]): string[] {
  return keys.map((k) => settings.find((s) => s.key === k)?.label ?? k);
}

// needsSetupText is the one line every settings view shows while required
// settings are unset ("" when none is).
export function needsSetupText(missing: readonly string[], settings: readonly Pick<ComponentSetting, "key" | "label">[]): string {
  return missing.length === 0 ? "" : `Needs setup: ${settingLabels(missing, settings).join(", ")}`;
}

// ofScope is the part of a declaration a scope's owner sets.
export function ofScope<T extends Pick<ComponentSetting, "scope">>(settings: readonly T[] | undefined, scope: ComponentSettingScope): T[] {
  return (settings ?? []).filter((s) => s.scope === scope);
}

// settingsSummary lists a declaration as a short line
// ("apiKey (secret, org) *, region (choice, member)").
export function settingsSummary(settings: readonly ComponentSetting[] | undefined): string {
  return (settings ?? []).map((s) => `${s.key} (${s.type}, ${s.scope})${s.required ? " *" : ""}`).join(", ");
}
