//! Desktop-local plugin settings.
//!
//! A plugin package may declare `localSettings` (e.g. the member's own bot
//! credentials). The values belong to the member's machine: they are entered
//! in Settings → Notifications through the Tauri commands below, stored here
//! as one owner-only (0600) JSON object per plugin in the signed-in member's
//! own directory (member_dir.rs), and handed to the sidecar at launch through
//! `BNBLOC_PLUGIN_SETTINGS_FILE` (the path of that same file). They are never
//! sent to the gateway.
//!
//! A plugin whose required settings are not all set is not launched (its
//! notifications then fall back to the server notifiers); saving or clearing
//! the settings restarts it.
//!
//! The store itself (one 0600 JSON object per component, the schema merge,
//! the secret-free views) is shared with the member credentials of MCP tools
//! (internal_ext/mcp_tools.rs, Settings → Tools).
use std::collections::{BTreeMap, HashSet};
use std::path::{Path, PathBuf};

use crate::internal_ext::sidecar_model::{ComponentKind, LocalSettingField};
use crate::internal_ext::sidecar_status::component_id;
use crate::internal_ext::{member_dir, Adapter};

/// The env var naming the settings file a plugin sidecar reads at launch.
pub(super) const SETTINGS_FILE_ENV: &str = "BNBLOC_PLUGIN_SETTINGS_FILE";
/// Bound on one value (a key or id, never a document).
const MAX_VALUE_BYTES: usize = 1024;

/// One schema field with this desktop's state; a secret value is never
/// returned (`set` says whether one is stored).
#[derive(Clone, Debug, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LocalSettingView {
    pub key: String,
    pub label: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub help: Option<String>,
    pub secret: bool,
    pub required: bool,
    pub set: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub value: Option<String>,
}

/// One installed plugin with local settings, as Settings → Notifications
/// renders it.
#[derive(Clone, Debug, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PluginLocalSettings {
    pub plugin: String,
    pub version: String,
    /// Every required field is set (the plugin may be launched).
    pub configured: bool,
    /// The sidecar's current state and message (status panel).
    pub state: String,
    pub message: String,
    pub fields: Vec<LocalSettingView>,
}

pub(super) type Values = BTreeMap<String, String>;

impl Adapter {
    /// The directory holding the signed-in member's per-plugin settings
    /// files (member_dir).
    pub(super) fn plugin_settings_dir(&self) -> Result<PathBuf, String> {
        Ok(self.member_dir()?.join(member_dir::PLUGIN_SETTINGS_DIR))
    }

    /// The settings file of `plugin` (it need not exist yet).
    pub(super) fn plugin_settings_file(&self, plugin: &str) -> Result<PathBuf, String> {
        settings_path(&self.plugin_settings_dir()?, plugin)
    }

    /// Why `plugin` may not be launched yet (None: every required local
    /// setting is set).
    pub(super) fn plugin_settings_blocker(
        &self,
        plugin: &str,
        schema: &[LocalSettingField],
    ) -> Option<String> {
        if schema.iter().all(|f| !f.required) {
            return None;
        }
        let ask = format!("{plugin} needs its settings: open Settings → Notifications");
        let values = match self
            .plugin_settings_file(plugin)
            .and_then(|path| load(&path))
        {
            Ok(values) => values,
            Err(e) => return Some(format!("{ask} ({e})")),
        };
        (!missing_required(schema, &values).is_empty()).then_some(ask)
    }

    /// Every installed plugin that declares local settings.
    pub fn plugin_local_settings(&self) -> Result<Vec<PluginLocalSettings>, String> {
        let dir = self.plugin_settings_dir()?;
        let inner = self.inner.lock();
        let mut out = Vec::new();
        for sidecar in inner.sidecars.values() {
            let Some(spec) = sidecar.spec.as_ref() else {
                continue;
            };
            if spec.kind != ComponentKind::Plugin || spec.plugin.local_settings.is_empty() {
                continue;
            }
            let (values, unreadable) = match load(&settings_path(&dir, &spec.name)?) {
                Ok(values) => (values, None),
                Err(e) => (Values::new(), Some(e)),
            };
            out.push(PluginLocalSettings {
                plugin: spec.name.clone(),
                version: spec.version.clone(),
                configured: missing_required(&spec.plugin.local_settings, &values).is_empty(),
                state: sidecar.status.state.clone(),
                message: unreadable.unwrap_or_else(|| sidecar.status.message.clone()),
                fields: views(&spec.plugin.local_settings, &values),
            });
        }
        out.sort_by(|a, b| a.plugin.cmp(&b.plugin));
        Ok(out)
    }

    /// Validate and store `update` for `plugin`, then restart its sidecar.
    pub fn save_plugin_local_settings(
        &self,
        plugin: &str,
        update: BTreeMap<String, String>,
    ) -> Result<(), String> {
        let schema = self.plugin_schema(plugin)?;
        let path = self.plugin_settings_file(plugin)?;
        let next = merge(&schema, &load(&path).unwrap_or_default(), update)?;
        save(&path, &next)?;
        self.restart_sidecar(&component_id(ComponentKind::Plugin, plugin));
        Ok(())
    }

    /// Delete `plugin`'s stored settings; a plugin that requires settings is
    /// stopped until they are entered again.
    pub fn clear_plugin_local_settings(&self, plugin: &str) -> Result<(), String> {
        self.plugin_schema(plugin)?;
        let path = self.plugin_settings_file(plugin)?;
        match std::fs::remove_file(&path) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(format!("delete settings: {e}")),
        }
        self.restart_sidecar(&component_id(ComponentKind::Plugin, plugin));
        Ok(())
    }

    fn plugin_schema(&self, plugin: &str) -> Result<Vec<LocalSettingField>, String> {
        let inner = self.inner.lock();
        inner
            .sidecars
            .get(&component_id(ComponentKind::Plugin, plugin))
            .and_then(|sidecar| sidecar.spec.as_ref())
            .map(|spec| spec.plugin.local_settings.clone())
            .filter(|schema| !schema.is_empty())
            .ok_or_else(|| format!("plugin {plugin} is not installed or has no local settings"))
    }
}

/// `dir/<name>.json`, refusing a name that is not a package name (the one
/// rule, sidecar_model::valid_package_name; never a path).
pub(super) fn settings_path(dir: &Path, name: &str) -> Result<PathBuf, String> {
    if !crate::internal_ext::sidecar_model::valid_package_name(name) {
        return Err(format!("invalid component name {name:?}"));
    }
    Ok(dir.join(format!("{name}.json")))
}

/// The component files stored in `dir`: `(name, path)` of every
/// `<name>.json` and of its temps and quarantined copies
/// (`files::owner_name`). Anything else in the dir (sub-directories, foreign
/// files) is not listed.
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

/// Delete the files of every component in `dir` not in `keep` (the
/// uninstall sweep). Blocking.
pub(super) fn forget_unlisted(dir: &Path, keep: &HashSet<String>) {
    for (name, path) in stored_files(dir) {
        if keep.contains(&name) {
            continue;
        }
        if let Err(e) = std::fs::remove_file(&path) {
            eprintln!("[sidecar-manager] could not forget {}: {e}", path.display());
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

/// Apply a form update to the stored values: only schema keys, a secret left
/// empty keeps its stored value, required fields must end up set.
pub(super) fn merge(
    schema: &[LocalSettingField],
    current: &Values,
    update: BTreeMap<String, String>,
) -> Result<Values, String> {
    for (key, value) in &update {
        if !schema.iter().any(|f| &f.key == key) {
            return Err(format!("unknown setting {key:?}"));
        }
        if value.len() > MAX_VALUE_BYTES {
            return Err(format!("{key} is too long"));
        }
    }
    let mut next = Values::new();
    for field in schema {
        let mut value = update
            .get(&field.key)
            .map(|v| v.trim().to_string())
            .unwrap_or_default();
        if value.is_empty() && field.secret {
            value = current.get(&field.key).cloned().unwrap_or_default();
        }
        if value.is_empty() {
            if field.required {
                return Err(format!("{} is required", field.label));
            }
            continue;
        }
        next.insert(field.key.clone(), value);
    }
    Ok(next)
}

/// Labels of required fields without a value.
pub(super) fn missing_required(schema: &[LocalSettingField], values: &Values) -> Vec<String> {
    schema
        .iter()
        .filter(|f| f.required && !values.get(&f.key).is_some_and(|v| !v.is_empty()))
        .map(|f| f.label.clone())
        .collect()
}

pub(super) fn views(schema: &[LocalSettingField], values: &Values) -> Vec<LocalSettingView> {
    schema
        .iter()
        .map(|f| {
            let value = values.get(&f.key).filter(|v| !v.is_empty());
            LocalSettingView {
                key: f.key.clone(),
                label: f.label.clone(),
                help: f.help.clone(),
                secret: f.secret,
                required: f.required,
                set: value.is_some(),
                value: if f.secret { None } else { value.cloned() },
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn schema() -> Vec<LocalSettingField> {
        vec![
            LocalSettingField {
                key: "appId".into(),
                label: "App ID".into(),
                help: None,
                secret: false,
                required: true,
            },
            LocalSettingField {
                key: "appKey".into(),
                label: "App key".into(),
                help: Some("stays here".into()),
                secret: true,
                required: true,
            },
            LocalSettingField {
                key: "note".into(),
                label: "Note".into(),
                help: None,
                secret: false,
                required: false,
            },
        ]
    }

    fn update(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn merge_validates_keeps_secrets_and_requires_fields() {
        let s = schema();
        assert!(merge(&s, &Values::new(), update(&[("appId", "a")]))
            .unwrap_err()
            .contains("App key is required"));
        assert!(merge(&s, &Values::new(), update(&[("x", "y")]))
            .unwrap_err()
            .contains("unknown setting"));
        let long = "k".repeat(MAX_VALUE_BYTES + 1);
        assert!(merge(
            &s,
            &Values::new(),
            update(&[("appId", "a"), ("appKey", &long)])
        )
        .unwrap_err()
        .contains("too long"));
        let first = merge(
            &s,
            &Values::new(),
            update(&[("appId", " a "), ("appKey", "s3cr3t")]),
        )
        .expect("valid");
        assert_eq!(first.get("appId").map(String::as_str), Some("a"));
        assert!(
            !first.contains_key("note"),
            "empty optional fields are not stored"
        );
        // An empty secret keeps the stored one; a non-secret empty is cleared.
        let second = merge(&s, &first, update(&[("appId", "b"), ("appKey", "")])).expect("valid");
        assert_eq!(second.get("appKey").map(String::as_str), Some("s3cr3t"));
        assert_eq!(second.get("appId").map(String::as_str), Some("b"));
    }

    #[test]
    fn views_never_return_secret_values() {
        let values = merge(
            &schema(),
            &Values::new(),
            update(&[("appId", "a"), ("appKey", "s3cr3t")]),
        )
        .unwrap();
        let v = views(&schema(), &values);
        assert_eq!(v[0].value.as_deref(), Some("a"));
        assert!(v[1].set && v[1].value.is_none());
        assert!(!v[2].set);
        let json = serde_json::to_string(&v).unwrap();
        assert!(!json.contains("s3cr3t"), "{json}");
        assert!(missing_required(&schema(), &values).is_empty());
        assert_eq!(
            missing_required(&schema(), &Values::new()),
            vec!["App ID".to_string(), "App key".to_string()]
        );
    }

    #[test]
    fn settings_file_is_owner_only_and_round_trips() {
        let dir = std::env::temp_dir().join(format!(
            "bnbloc-plugin-settings-test-{}",
            std::process::id()
        ));
        let path = settings_path(&dir, "personal-bot").expect("path");
        let values = update(&[("appKey", "s3cr3t")]);
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
        for bad in ["", "../evil", "a/b", ".hidden", "Upper", "a\\b"] {
            assert!(settings_path(dir, bad).is_err(), "{bad:?} accepted");
        }
        assert!(settings_path(dir, "personal-bot.v2_x").is_ok());
    }
}
