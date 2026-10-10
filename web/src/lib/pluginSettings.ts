// Desktop-local plugin settings (e.g. a personal bot's credentials). A plugin
// package declares the schema (`localSettings`); the desktop shell stores the
// values on the member's machine and hands them to the plugin at launch —
// they are never sent to the gateway. Shapes mirror the shell's Tauri
// commands (apps/desktop/src-tauri/src/internal_ext/plugin_settings.rs).

// One schema field with this desktop's state. A secret value is never
// returned: `set` says whether one is stored.
export interface LocalSettingView {
  key: string;
  label: string;
  help?: string;
  secret: boolean;
  required: boolean;
  set: boolean;
  value?: string;
}

// One installed plugin that declares local settings.
export interface PluginLocalSettings {
  plugin: string;
  version: string;
  // Every required field is set (the plugin runs).
  configured: boolean;
  // The plugin sidecar's state and message on this desktop.
  state: string;
  message: string;
  fields: LocalSettingView[];
}

// settingsUpdate builds the values to save from the form: secret fields left
// empty keep their stored value in the shell, so they are sent as "".
export function settingsUpdate(fields: readonly LocalSettingView[], values: Readonly<Record<string, string>>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of fields) out[f.key] = (values[f.key] ?? "").trim();
  return out;
}

// missingRequired lists the labels of required fields the form leaves empty
// (a stored secret counts as filled).
export function missingRequired(fields: readonly LocalSettingView[], values: Readonly<Record<string, string>>): string[] {
  return fields.filter((f) => f.required && !(values[f.key] ?? "").trim() && !(f.secret && f.set)).map((f) => f.label);
}

// initialValues pre-fills stored non-secret values; secret inputs start empty.
export function initialValues(fields: readonly LocalSettingView[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of fields) out[f.key] = f.secret ? "" : (f.value ?? "");
  return out;
}
