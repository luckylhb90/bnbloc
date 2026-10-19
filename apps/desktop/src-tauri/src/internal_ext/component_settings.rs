//! Component settings on the member's desktop
//! (docs/prd/2026-10-19-component-settings.md).
//!
//! A plugin or MCP tool package declares `settings`: each a key, a type, a
//! scope (`platform`, `org` or `member`) and how its value reaches the
//! component. This module is the desktop's one owner of them:
//!
//! - The value rules: a Rust copy of `core/pkgmeta/settings.go`
//!   (`check_setting_value`, `merge_settings`, `effective_settings`,
//!   `unset_settings`, `settings_env`), pinned to it by
//!   `core/pkgmeta/testdata/setting_values.json`.
//! - The member store: the member-scope values, entered in Settings →
//!   Components through the Tauri commands below, one owner-only (0600)
//!   JSON object per component in the signed-in member's own directory
//!   (`<member>/component-settings/<kind>@<name>.json`, member_dir.rs).
//!   They never reach the gateway. The bridge reads an MCP tool's for the
//!   member's own drives (`BNBLOC_COMPONENT_SETTINGS_DIR`).
//! - A plugin's launch: platform and organization values come from the
//!   gateway at every launch (`GET /v1/components/{name}/settings/resolved`,
//!   secrets included, never logged), are merged with the member's by key,
//!   and the plugin gets every value in an owner-only file named by
//!   `BNBLOC_COMPONENT_SETTINGS_FILE` (`<member>/component-settings/launch/`)
//!   plus each declared `deliver.env`.
//! - Needs setup: a component whose required settings (any scope) are not
//!   all set does not run. The catalog says which platform and organization
//!   ones are missing (`settingsMissing`); the member store says the rest. A
//!   plugin in that state is not launched; saving its settings restarts it.
use std::collections::{BTreeMap, HashSet};
use std::path::{Path, PathBuf};

use crate::internal_ext::gateway_client::API_TIMEOUT;
use crate::internal_ext::sidecar_model::{ComponentKind, McpToolEntry, SidecarSpec};
use crate::internal_ext::sidecar_status::component_id;
use crate::internal_ext::sidecar_util::cache_stem;
use crate::internal_ext::{member_dir, Adapter};

/// The env var naming the file with every value of a component's settings
/// (pkgmeta.ComponentSettingsFileEnv).
pub(super) const SETTINGS_FILE_ENV: &str = "BNBLOC_COMPONENT_SETTINGS_FILE";
/// The env var naming the member store dir the bridge reads an MCP tool's
/// member values from (`<dir>/mcp@<tool>.json`; services/bridge).
pub(super) const SETTINGS_DIR_ENV: &str = "BNBLOC_COMPONENT_SETTINGS_DIR";
/// Sub-directory of the member store holding the launch files.
const LAUNCH_DIR: &str = "launch";
/// Bound on one value (wire.MaxComponentSettingValueBytes).
const MAX_VALUE_BYTES: usize = 4 << 10;

/// The scope each member sets for themselves (wire.SettingScopeMember).
pub(super) const SCOPE_MEMBER: &str = "member";

/// One declared setting (wire.ComponentSetting). Every field is defaulted
/// so one odd entry never fails the whole catalog decode.
#[derive(Clone, Debug, Default, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Setting {
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub label: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub help: String,
    #[serde(default, rename = "type")]
    pub kind: String,
    #[serde(default)]
    pub scope: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub required: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub default: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub options: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub deliver: Option<Deliver>,
}

/// How a setting's value reaches the component (wire.ComponentSettingDeliver).
#[derive(Clone, Debug, Default, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Deliver {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub env: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub header: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub bearer: bool,
}

pub(super) type Values = BTreeMap<String, String>;

/// One declared setting with this desktop's state (wire.ComponentSettingField):
/// a member setting's stored value (never a secret's), whether a platform or
/// organization setting is set (never its value).
#[derive(Clone, Debug, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SettingField {
    #[serde(flatten)]
    pub setting: Setting,
    pub set: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub value: Option<String>,
}

/// One installed component with settings as Settings → Components renders
/// it: every installed MCP tool (its binary's state, its sign-in) and every
/// installed plugin that declares settings.
#[derive(Clone, Debug, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ComponentSettingsView {
    pub kind: ComponentKind,
    pub name: String,
    pub version: String,
    /// The plugin sidecar's state, or a stdio MCP tool's binary state
    /// ("" for an http tool), and its message.
    pub state: String,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub transport: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tools: Option<Vec<McpToolEntry>>,
    /// "oauth" when the member signs in to the tool ("" otherwise).
    pub auth: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sign_in: Option<crate::internal_ext::mcp_tools::McpSignInView>,
    pub fields: Vec<SettingField>,
    /// Keys of the required settings not set, any scope ("needs setup").
    pub missing: Vec<String>,
}

/// The platform and organization values of a component installed for the
/// member (wire.ComponentSettingsResolved).
#[derive(Debug, Default, serde::Deserialize)]
struct Resolved {
    #[serde(default)]
    values: Values,
    #[serde(default)]
    missing: Vec<String>,
}

// ---- the value rules (core/pkgmeta/settings.go) ----

/// A number value: JSON's number syntax (pkgmeta settingNumberRE).
fn number_syntax(v: &str) -> bool {
    fn digits(s: &str) -> usize {
        s.bytes().take_while(u8::is_ascii_digit).count()
    }
    let s = v.strip_prefix('-').unwrap_or(v);
    let int = digits(s);
    if int == 0 || (int > 1 && s.starts_with('0')) {
        return false;
    }
    let mut rest = &s[int..];
    if let Some(frac) = rest.strip_prefix('.') {
        let n = digits(frac);
        if n == 0 {
            return false;
        }
        rest = &frac[n..];
    }
    if let Some(exp) = rest.strip_prefix(['e', 'E']) {
        let exp = exp.strip_prefix(['+', '-']).unwrap_or(exp);
        let n = digits(exp);
        if n == 0 {
            return false;
        }
        rest = &exp[n..];
    }
    rest.is_empty()
}

/// The one rule for a value of setting `s` (trimmed, non-empty;
/// pkgmeta.CheckSettingValue).
pub(super) fn check_setting_value(s: &Setting, v: &str) -> Result<(), String> {
    if v.len() > MAX_VALUE_BYTES || v.contains('\0') {
        return Err(format!("must be text of at most {MAX_VALUE_BYTES} bytes"));
    }
    match s.kind.as_str() {
        "url" => {
            let ok = reqwest::Url::parse(v).is_ok_and(|u| {
                matches!(u.scheme(), "http" | "https")
                    && u.host_str().is_some_and(|h| !h.is_empty())
            }) && !v.contains([' ', '\t', '\r', '\n']);
            if !ok {
                return Err("must be an http(s) URL, such as https://api.example.com".into());
            }
        }
        "number" => {
            if !number_syntax(v) || !v.parse::<f64>().is_ok_and(f64::is_finite) {
                return Err("must be a number, such as 42 or 0.5".into());
            }
        }
        "boolean" => {
            if v != "true" && v != "false" {
                return Err("must be \"true\" or \"false\"".into());
            }
        }
        "choice" => {
            if !s.options.iter().any(|o| o == v) {
                return Err(format!("must be one of {}", s.options.join(", ")));
            }
        }
        _ => {}
    }
    Ok(())
}

/// Apply an update to the stored values of one scope (pkgmeta.MergeSettings):
/// an absent key is left as it is, an empty value clears it (an empty secret
/// keeps the stored one), a key in `clear` is cleared. Every key named must
/// be a setting of the scope, every value valid, every required setting of
/// the scope set in the end (a default counts); values of keys the
/// declaration no longer has in the scope are dropped. Returns the values to
/// store and the keys whose stored value changed.
pub(super) fn merge_settings(
    decl: &[Setting],
    scope: &str,
    current: &Values,
    update: &Values,
    clear: &[String],
) -> Result<(Values, Vec<String>), String> {
    let in_scope = |key: &str| decl.iter().find(|s| s.scope == scope && s.key == key);
    // BTreeMap keys come sorted, as pkgmeta checks them.
    for key in update.keys().chain(clear) {
        if in_scope(key).is_none() {
            return Err(format!(
                "{key:?} is not a {scope} setting of this component"
            ));
        }
    }
    let mut next = Values::new();
    for s in decl.iter().filter(|s| s.scope == scope) {
        let given = update.get(&s.key).map(|v| v.trim());
        let v = if clear.contains(&s.key) {
            String::new()
        } else {
            match given {
                None => current
                    .get(&s.key)
                    .map(|v| v.trim().to_string())
                    .unwrap_or_default(),
                Some("") if s.kind == "secret" => current
                    .get(&s.key)
                    .map(|v| v.trim().to_string())
                    .unwrap_or_default(),
                Some(v) => {
                    if !v.is_empty() {
                        check_setting_value(s, v).map_err(|e| format!("{} {e}", s.label))?;
                    }
                    v.to_string()
                }
            }
        };
        if !v.is_empty() {
            next.insert(s.key.clone(), v);
        }
    }
    if let Some(key) = unset_settings(decl, &next, &[scope]).first() {
        let label = in_scope(key).map(|s| s.label.as_str()).unwrap_or(key);
        return Err(format!("{label} is required"));
    }
    let mut changed: Vec<String> = current
        .iter()
        .filter(|(k, v)| next.get(*k) != Some(*v))
        .map(|(k, _)| k.clone())
        .chain(next.keys().filter(|k| !current.contains_key(*k)).cloned())
        .collect();
    changed.sort();
    Ok((next, changed))
}

/// What a component gets (pkgmeta.EffectiveSettings): each declared
/// setting's value (trimmed), else its default; neither, or an undeclared
/// key, is left out.
pub(super) fn effective_settings(decl: &[Setting], values: &Values) -> Values {
    decl.iter()
        .filter_map(|s| {
            let v = values.get(&s.key).map(|v| v.trim()).unwrap_or_default();
            let v = if v.is_empty() { s.default.as_str() } else { v };
            (!v.is_empty()).then(|| (s.key.clone(), v.to_string()))
        })
        .collect()
}

/// The "needs setup" rule (pkgmeta.UnsetSettings): the keys, in declaration
/// order, of the required settings of `scopes` (empty: every scope) without
/// an effective value.
pub(super) fn unset_settings(decl: &[Setting], values: &Values, scopes: &[&str]) -> Vec<String> {
    let eff = effective_settings(decl, values);
    decl.iter()
        .filter(|s| {
            s.required
                && !eff.contains_key(&s.key)
                && (scopes.is_empty() || scopes.contains(&s.scope.as_str()))
        })
        .map(|s| s.key.clone())
        .collect()
}

/// The environment a process component gets from its effective values
/// (pkgmeta.SettingsEnv): each `deliver.env` with a value.
pub(super) fn settings_env(decl: &[Setting], effective: &Values) -> Vec<(String, String)> {
    decl.iter()
        .filter_map(|s| {
            let env = &s.deliver.as_ref()?.env;
            let v = effective.get(&s.key)?;
            (!env.is_empty()).then(|| (env.clone(), v.clone()))
        })
        .collect()
}

/// What a launch hands a process component: the effective values of the
/// whole declaration (the settings file's content) and its env pairs. The
/// gateway's values hold only for platform and organization settings, the
/// member store's only for member ones.
pub(super) fn launch_settings(
    decl: &[Setting],
    resolved: &Values,
    member: &Values,
) -> (Values, Vec<(String, String)>) {
    let mut values = Values::new();
    for s in decl {
        let from = if s.scope == SCOPE_MEMBER {
            member
        } else {
            resolved
        };
        if let Some(v) = from.get(&s.key) {
            values.insert(s.key.clone(), v.clone());
        }
    }
    let effective = effective_settings(decl, &values);
    let env = settings_env(decl, &effective);
    (effective, env)
}

/// The fields of a declaration as a settings view shows them: a member
/// setting with its stored state (a secret's value never), a platform or
/// organization one set when `set_elsewhere` names it.
pub(super) fn fields(
    decl: &[Setting],
    member: &Values,
    set_elsewhere: &[String],
) -> Vec<SettingField> {
    decl.iter()
        .map(|s| {
            if s.scope != SCOPE_MEMBER {
                return SettingField {
                    setting: s.clone(),
                    set: set_elsewhere.contains(&s.key),
                    value: None,
                };
            }
            let value = member.get(&s.key).filter(|v| !v.trim().is_empty());
            SettingField {
                setting: s.clone(),
                set: value.is_some(),
                value: if s.kind == "secret" {
                    None
                } else {
                    value.cloned()
                },
            }
        })
        .collect()
}

/// The keys, in declaration order, of the required settings not set: the
/// platform and organization ones the catalog names, and the member ones the
/// member store lacks.
pub(super) fn missing(
    decl: &[Setting],
    member: &Values,
    catalog_missing: &[String],
) -> Vec<String> {
    let member_unset = unset_settings(decl, member, &[SCOPE_MEMBER]);
    decl.iter()
        .filter(|s| catalog_missing.contains(&s.key) || member_unset.contains(&s.key))
        .map(|s| s.key.clone())
        .collect()
}

/// The labels of `keys` (the key itself for one the declaration lacks).
fn labels(decl: &[Setting], keys: &[String]) -> String {
    keys.iter()
        .map(|k| {
            decl.iter()
                .find(|s| &s.key == k)
                .map_or(k.as_str(), |s| s.label.as_str())
        })
        .collect::<Vec<_>>()
        .join(", ")
}

/// Why a component may not run yet (None: every required setting is set).
pub(super) fn needs_setup(name: &str, decl: &[Setting], keys: &[String]) -> Option<String> {
    (!keys.is_empty()).then(|| {
        format!(
            "{name} needs setup: {} (Settings → Components)",
            labels(decl, keys)
        )
    })
}

// ---- the member store ----

/// `dir/<name>.json`, refusing a name that is not a package name (the one
/// rule, sidecar_model::valid_package_name; never a path).
pub(super) fn name_path(dir: &Path, name: &str) -> Result<PathBuf, String> {
    if !crate::internal_ext::sidecar_model::valid_package_name(name) {
        return Err(format!("invalid component name {name:?}"));
    }
    Ok(dir.join(format!("{name}.json")))
}

/// The member store file of component `name` of `kind` in `dir`:
/// `<kind>@<name>.json`, the cache stem (sidecar_util::cache_stem).
pub(super) fn settings_path(
    dir: &Path,
    kind: ComponentKind,
    name: &str,
) -> Result<PathBuf, String> {
    name_path(dir, name)?;
    Ok(dir.join(format!("{}.json", cache_stem(&component_id(kind, name)))))
}

/// The files stored in `dir`: `(stem, path)` of every `<stem>.json` and of
/// its temps and quarantined copies (`files::owner_name`). Anything else in
/// the dir (sub-directories, foreign files) is not listed.
pub(super) fn stored_files(dir: &Path) -> Vec<(String, PathBuf)> {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return Vec::new();
    };
    entries
        .flatten()
        .filter(|entry| entry.file_type().is_ok_and(|t| t.is_file()))
        .filter_map(|entry| {
            let file = entry.file_name().into_string().ok()?;
            let name = crate::files::owner_name(&file)
                .strip_suffix(".json")?
                .to_string();
            Some((name, entry.path()))
        })
        .collect()
}

/// Delete the files of every component of `kind` in `dir` (the member store
/// and its launch files) not in `keep` (the uninstall sweep). Blocking.
pub(super) fn forget_unlisted(member: &Path, kind: ComponentKind, keep: &HashSet<String>) {
    let prefix = cache_stem(&component_id(kind, ""));
    let keep: HashSet<String> = keep
        .iter()
        .map(|name| cache_stem(&component_id(kind, name)))
        .collect();
    let store = member.join(member_dir::COMPONENT_SETTINGS_DIR);
    for dir in [store.join(LAUNCH_DIR), store] {
        for (stem, path) in stored_files(&dir) {
            if !stem.starts_with(&prefix) || keep.contains(&stem) {
                continue;
            }
            if let Err(e) = std::fs::remove_file(&path) {
                eprintln!("[sidecar-manager] could not forget {}: {e}", path.display());
            }
        }
    }
}

/// The stored values (missing file: none). An unreadable file is moved
/// aside and reported (files::read_json).
pub(super) fn load(path: &Path) -> Result<Values, String> {
    crate::files::read_json(path).map(Option::unwrap_or_default)
}

/// Write the values owner-only (files::atomic_write).
pub(super) fn save(path: &Path, values: &Values) -> Result<(), String> {
    let raw = serde_json::to_vec(values).map_err(|e| e.to_string())?;
    crate::files::atomic_write(path, &raw)
}

/// The kind a settings command names.
fn parse_kind(kind: &str) -> Result<ComponentKind, String> {
    match kind {
        "plugin" => Ok(ComponentKind::Plugin),
        "mcp" => Ok(ComponentKind::Mcp),
        _ => Err(format!(
            "components of kind {kind:?} take no member settings here"
        )),
    }
}

impl Adapter {
    /// The signed-in member's settings store (member_dir).
    pub(super) fn component_settings_dir(&self) -> Result<PathBuf, String> {
        Ok(self.member_dir()?.join(member_dir::COMPONENT_SETTINGS_DIR))
    }

    /// The member's stored values of a component (an unreadable file
    /// reports its error).
    pub(super) fn member_settings(
        &self,
        kind: ComponentKind,
        name: &str,
    ) -> Result<Values, String> {
        load(&settings_path(&self.component_settings_dir()?, kind, name)?)
    }

    /// Why plugin `spec` may not be launched yet (None: every required
    /// setting is set).
    pub(super) fn plugin_settings_blocker(&self, spec: &SidecarSpec) -> Option<String> {
        let decl = &spec.plugin.settings;
        if decl.iter().all(|s| !s.required) {
            return None;
        }
        let member = match self.member_settings(ComponentKind::Plugin, &spec.name) {
            Ok(values) => values,
            Err(e) => return Some(format!("{} needs setup ({e})", spec.name)),
        };
        needs_setup(
            &spec.name,
            decl,
            &missing(decl, &member, &spec.plugin.settings_missing),
        )
    }

    /// Hand plugin `spec` its settings at launch: the platform and
    /// organization values from the gateway (when it declares any), the
    /// member's from the store, all in the settings file and each
    /// `deliver.env`. Never logs a value.
    pub(super) async fn plugin_settings_launch(
        &self,
        spec: &SidecarSpec,
        cmd: &mut std::process::Command,
    ) -> Result<(), String> {
        let decl = &spec.plugin.settings;
        let mut resolved = Resolved::default();
        if decl.iter().any(|s| s.scope != SCOPE_MEMBER) {
            let path = format!("/v1/components/{}/settings/resolved?kind=plugin", spec.name);
            let raw = self
                .gateway
                .session()
                .send(reqwest::Method::GET, &path, |r| r.timeout(API_TIMEOUT))
                .await
                .map_err(|e| format!("read {}'s settings: {e}", spec.name))?
                .bytes()
                .await
                .map_err(|e| format!("read {}'s settings: {}", spec.name, e.without_url()))?;
            // The answer holds secrets: a decode error names no content.
            resolved = serde_json::from_slice(&raw).map_err(|_| {
                format!(
                    "read {}'s settings: the gateway's answer is malformed",
                    spec.name
                )
            })?;
        }
        if let Some(why) = needs_setup(&spec.name, decl, &resolved.missing) {
            return Err(why);
        }
        let member = self.member_settings(ComponentKind::Plugin, &spec.name)?;
        let (effective, env) = launch_settings(decl, &resolved.values, &member);
        let file = settings_path(
            &self.component_settings_dir()?.join(LAUNCH_DIR),
            ComponentKind::Plugin,
            &spec.name,
        )?;
        save(&file, &effective)?;
        cmd.env(SETTINGS_FILE_ENV, &file);
        for (name, value) in env {
            cmd.env(name, value);
        }
        Ok(())
    }

    /// Every installed component with settings, as Settings → Components
    /// renders it. Secret values are never returned.
    pub fn component_settings(&self) -> Result<Vec<ComponentSettingsView>, String> {
        let dir = self.component_settings_dir()?;
        let mut out = self.mcp_settings_views()?;
        let plugins: Vec<(SidecarSpec, String, String)> = {
            let inner = self.inner.lock();
            inner
                .sidecars
                .values()
                .filter_map(|sidecar| {
                    let spec = sidecar.spec.as_ref()?;
                    (spec.kind == ComponentKind::Plugin && !spec.plugin.settings.is_empty()).then(
                        || {
                            (
                                spec.clone(),
                                sidecar.status.state.clone(),
                                sidecar.status.message.clone(),
                            )
                        },
                    )
                })
                .collect()
        };
        for (spec, state, message) in plugins {
            let decl = &spec.plugin.settings;
            let (member, unreadable) =
                match load(&settings_path(&dir, ComponentKind::Plugin, &spec.name)?) {
                    Ok(values) => (values, None),
                    Err(e) => (Values::new(), Some(e)),
                };
            out.push(ComponentSettingsView {
                kind: ComponentKind::Plugin,
                version: spec.version.clone(),
                state,
                message: unreadable.unwrap_or(message),
                transport: None,
                tools: None,
                auth: String::new(),
                sign_in: None,
                fields: fields(decl, &member, &spec.plugin.settings_set),
                missing: missing(decl, &member, &spec.plugin.settings_missing),
                name: spec.name,
            });
        }
        out.sort_by(|a, b| (kind_name(a.kind), &a.name).cmp(&(kind_name(b.kind), &b.name)));
        Ok(out)
    }

    /// Validate and store the member's values for a component (member
    /// scope only), then restart a plugin with them; the bridge reads an
    /// MCP tool's per session.
    pub fn save_component_settings(
        &self,
        kind: &str,
        name: &str,
        values: Values,
        clear: Vec<String>,
    ) -> Result<(), String> {
        let kind = parse_kind(kind)?;
        let decl = self
            .component_settings_decl(kind, name)
            .ok_or_else(|| format!("{name} is not installed or declares no settings"))?;
        let path = settings_path(&self.component_settings_dir()?, kind, name)?;
        let (next, _) = merge_settings(
            &decl,
            SCOPE_MEMBER,
            &load(&path).unwrap_or_default(),
            &values,
            &clear,
        )?;
        save(&path, &next)?;
        if kind == ComponentKind::Plugin {
            self.restart_sidecar(&component_id(kind, name));
        }
        Ok(())
    }

    /// The declaration of an installed plugin or MCP tool.
    fn component_settings_decl(&self, kind: ComponentKind, name: &str) -> Option<Vec<Setting>> {
        let decl = match kind {
            ComponentKind::Plugin => self
                .inner
                .lock()
                .sidecars
                .get(&component_id(kind, name))
                .and_then(|sidecar| sidecar.spec.as_ref())
                .map(|spec| spec.plugin.settings.clone()),
            ComponentKind::Mcp => self.mcp_settings_decl(name),
            _ => None,
        }?;
        (!decl.is_empty()).then_some(decl)
    }
}

/// The kind's catalog name (sort order of the settings views).
fn kind_name(kind: ComponentKind) -> &'static str {
    match kind {
        ComponentKind::Plugin => "plugin",
        ComponentKind::Mcp => "mcp",
        _ => "other",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// core/pkgmeta/testdata/setting_values.json, shared with Go.
    #[derive(serde::Deserialize)]
    struct Fixture {
        values: Vec<ValueCase>,
        merges: Vec<MergeCase>,
        effective: Vec<EffectiveCase>,
    }

    #[derive(serde::Deserialize)]
    struct ValueCase {
        setting: Setting,
        value: String,
        ok: bool,
    }

    #[derive(serde::Deserialize)]
    struct MergeCase {
        settings: Vec<Setting>,
        scope: String,
        #[serde(default)]
        current: Values,
        #[serde(default)]
        update: Values,
        #[serde(default)]
        clear: Vec<String>,
        #[serde(default)]
        next: Values,
        #[serde(default)]
        changed: Vec<String>,
        #[serde(default)]
        error: String,
    }

    #[derive(serde::Deserialize)]
    struct EffectiveCase {
        settings: Vec<Setting>,
        values: Values,
        effective: Values,
        unset: Vec<String>,
        env: Vec<(String, String)>,
    }

    fn fixture() -> Fixture {
        serde_json::from_str(include_str!(
            "../../../../../core/pkgmeta/testdata/setting_values.json"
        ))
        .expect("fixture")
    }

    /// The value rules agree with pkgmeta's, case by case.
    #[test]
    fn value_rules_match_pkgmeta() {
        let f = fixture();
        for c in &f.values {
            assert_eq!(
                check_setting_value(&c.setting, &c.value).is_ok(),
                c.ok,
                "{} {:?}",
                c.setting.kind,
                c.value
            );
        }
        for (i, c) in f.merges.iter().enumerate() {
            let got = merge_settings(&c.settings, &c.scope, &c.current, &c.update, &c.clear);
            if !c.error.is_empty() {
                let err = got.expect_err(&format!("merge {i} must fail"));
                assert!(
                    err.contains(&c.error),
                    "merge {i}: {err:?} lacks {:?}",
                    c.error
                );
                continue;
            }
            assert_eq!(got, Ok((c.next.clone(), c.changed.clone())), "merge {i}");
        }
        for (i, c) in f.effective.iter().enumerate() {
            let eff = effective_settings(&c.settings, &c.values);
            assert_eq!(eff, c.effective, "effective {i}");
            assert_eq!(
                unset_settings(&c.settings, &c.values, &[]),
                c.unset,
                "unset {i}"
            );
            assert_eq!(settings_env(&c.settings, &eff), c.env, "env {i}");
        }
    }

    fn setting(key: &str, kind: &str, scope: &str, required: bool, env: &str) -> Setting {
        Setting {
            key: key.into(),
            label: key.to_uppercase(),
            kind: kind.into(),
            scope: scope.into(),
            required,
            deliver: (!env.is_empty()).then(|| Deliver {
                env: env.into(),
                ..Deliver::default()
            }),
            ..Setting::default()
        }
    }

    fn values(pairs: &[(&str, &str)]) -> Values {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    fn decl() -> Vec<Setting> {
        vec![
            setting("apiBase", "url", "platform", true, "BNBLOC_BOT_API_BASE"),
            setting("orgKey", "secret", "org", true, "BOT_ORG_KEY"),
            setting("appKey", "secret", SCOPE_MEMBER, true, "BOT_APP_KEY"),
            setting("note", "text", SCOPE_MEMBER, false, ""),
        ]
    }

    /// A launch takes the gateway's values for platform and organization
    /// settings and the member's for theirs, never the other way round.
    #[test]
    fn launch_merges_scopes_by_key() {
        let resolved = values(&[
            ("apiBase", "https://api.example.com"),
            ("orgKey", "org-s3cr3t"),
            ("appKey", "forged"),
        ]);
        let member = values(&[("appKey", "mine"), ("note", "hi"), ("orgKey", "forged")]);
        let (file, env) = launch_settings(&decl(), &resolved, &member);
        assert_eq!(
            file,
            values(&[
                ("apiBase", "https://api.example.com"),
                ("orgKey", "org-s3cr3t"),
                ("appKey", "mine"),
                ("note", "hi")
            ])
        );
        assert_eq!(
            env,
            vec![
                (
                    "BNBLOC_BOT_API_BASE".to_string(),
                    "https://api.example.com".to_string()
                ),
                ("BOT_ORG_KEY".to_string(), "org-s3cr3t".to_string()),
                ("BOT_APP_KEY".to_string(), "mine".to_string()),
            ]
        );
    }

    /// The view never carries a secret's value nor any platform or
    /// organization value; needs setup names every scope's missing keys.
    #[test]
    fn views_never_return_secrets_and_say_what_is_missing() {
        let member = values(&[("appKey", "s3cr3t"), ("note", "hi")]);
        let f = fields(&decl(), &member, &["apiBase".to_string()]);
        assert!(
            f[0].set && f[0].value.is_none(),
            "a platform setting: set, no value"
        );
        assert!(!f[1].set);
        assert!(
            f[2].set && f[2].value.is_none(),
            "a secret's value never comes back"
        );
        assert_eq!(f[3].value.as_deref(), Some("hi"));
        let json = serde_json::to_string(&f).unwrap();
        assert!(!json.contains("s3cr3t"), "{json}");
        assert!(
            json.contains("\"type\":\"url\"") && json.contains("\"scope\":\"platform\""),
            "{json}"
        );
        assert_eq!(
            missing(&decl(), &member, &["orgKey".to_string()]),
            vec!["orgKey"]
        );
        assert_eq!(
            missing(&decl(), &Values::new(), &[]),
            vec!["appKey"],
            "member keys the store lacks"
        );
        assert_eq!(
            needs_setup(
                "bot",
                &decl(),
                &["orgKey".to_string(), "appKey".to_string()]
            )
            .as_deref(),
            Some("bot needs setup: ORGKEY, APPKEY (Settings → Components)")
        );
        assert_eq!(needs_setup("bot", &decl(), &[]), None);
    }

    #[test]
    fn settings_file_is_owner_only_and_round_trips() {
        let dir = std::env::temp_dir().join(format!(
            "bnbloc-component-settings-test-{}",
            std::process::id()
        ));
        let path = settings_path(&dir, ComponentKind::Plugin, "personal-bot").expect("path");
        assert_eq!(path, dir.join("plugin@personal-bot.json"));
        let values = values(&[("appKey", "s3cr3t")]);
        save(&path, &values).expect("save");
        save(&path, &values).expect("replace in place");
        assert_eq!(load(&path), Ok(values));
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&path).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o600);
        }
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn settings_path_refuses_non_package_names() {
        let dir = Path::new("/tmp/x");
        for bad in ["", "../evil", "a/b", ".hidden", "Upper", "a\\b", "a@b"] {
            assert!(
                settings_path(dir, ComponentKind::Mcp, bad).is_err(),
                "{bad:?} accepted"
            );
            assert!(name_path(dir, bad).is_err(), "{bad:?} accepted");
        }
        assert_eq!(
            settings_path(dir, ComponentKind::Mcp, "codeword.v2_x").unwrap(),
            dir.join("mcp@codeword.v2_x.json")
        );
    }

    /// The sweep forgets the store and launch files of one kind's
    /// components no longer installed, and nothing of another kind's.
    #[test]
    fn forget_unlisted_keeps_installed_and_other_kinds() {
        let member = std::env::temp_dir().join(format!(
            "bnbloc-component-settings-sweep-{}",
            std::process::id()
        ));
        let _ = std::fs::remove_dir_all(&member);
        let store = member.join(member_dir::COMPONENT_SETTINGS_DIR);
        let launch = store.join(LAUNCH_DIR);
        let files = [
            store.join("plugin@kept.json"),
            store.join("plugin@gone.json"),
            store.join(".plugin@gone.json.0123456789abcdef.tmp"),
            launch.join("plugin@gone.json"),
            launch.join("plugin@kept.json"),
            store.join("mcp@gone.json"),
        ];
        for f in &files {
            std::fs::create_dir_all(f.parent().unwrap()).unwrap();
            std::fs::write(f, b"{}").unwrap();
        }
        forget_unlisted(
            &member,
            ComponentKind::Plugin,
            &HashSet::from(["kept".to_string()]),
        );
        let exists: Vec<bool> = files.iter().map(|f| f.exists()).collect();
        assert_eq!(exists, vec![true, false, false, false, true, true]);
        let _ = std::fs::remove_dir_all(&member);
    }
}
