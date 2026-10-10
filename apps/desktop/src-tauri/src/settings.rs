//! Persisted desktop settings: the configuration the shell feeds the embedded
//! bridge sidecar, stored as one JSON file under the app data dir.
//!
//! These map one-to-one to the bridge's environment configuration
//! (`services/bridge/cmd`), keeping the shell a thin supervisor: it holds no
//! lifecycle semantics, only the connection parameters the connector needs.
//!
//! The file carries credentials (gateway token, sync secret, core tokens), so
//! it is written owner-only (0600 on Unix) via write-to-temp + rename — never
//! world-readable, never half-written.

use std::io::Write;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use tauri::Manager;

/// The settings file name, under the app data dir.
const SETTINGS_FILE: &str = "settings.json";

/// One configured agent source (daemon backend). Empty `url`/`token` rely on
/// the bridge's per-source auto-discovery.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CoreConfig {
    pub name: String,
    /// Source kind. Only "local" today; the extension point for future remote
    /// sources. Defaults to "local" when absent.
    #[serde(default = "default_kind")]
    pub kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub token: Option<String>,
}

fn default_kind() -> String {
    "local".to_string()
}

/// The global default agent: used when a room chat names no provider.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct DefaultAgent {
    pub core: String,
    pub provider: String,
}

/// Persisted desktop settings. Every field defaults, so a partial or empty
/// file loads as a coreless, not-yet-configured client.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct Settings {
    /// Gateway base URL the UI and bridge talk to, e.g. `http://localhost:7090`.
    #[serde(default)]
    pub gateway_url: String,
    /// The member subject this client acts for; only its chat is claimed.
    #[serde(default)]
    pub owner: String,
    /// Tenant/workspace this client currently acts inside. The bridge switches
    /// its owner session to this tenant so local agents subscribe to the same
    /// rooms the UI is showing. Empty keeps the gateway's default login tenant.
    #[serde(default)]
    pub tenant: String,
    /// The configured cores (daemon backends). Translated to `BNBLOC_CORES`
    /// plus per-core endpoint env for the sidecar; empty url/token defer to
    /// per-core auto-discovery.
    #[serde(default)]
    pub cores: Vec<CoreConfig>,
    /// The global default agent (replaces the old claim-target string).
    #[serde(default)]
    pub default_agent: DefaultAgent,
    /// "core/provider" pairs the user turned off; not advertised or driveable.
    #[serde(default)]
    pub disabled_providers: Vec<String>,
    /// Deployment-wide file-sync secret (optional; empty disables sync auth).
    #[serde(default)]
    pub sync_secret: String,
    /// Single-host mode: let this one embedded bridge drive every member's
    /// agent from the one local core (for local multi-user testing). Only a
    /// dev gateway honors it (activity needs BNBLOC_ACTIVITY_ALLOW_SERVE_ALL=1;
    /// production refuses serve-all claims), so it is off in a real
    /// deployment, where each member runs their own bridge for their own
    /// agents. Mirrors `BNBLOC_BRIDGE_SERVE_ALL_OWNERS`.
    #[serde(default)]
    pub serve_all_owners: bool,
    /// Optional static bearer for gateway calls. Empty (default) makes the
    /// bridge mint an owner session token via the dev login. Set it when the
    /// gateway uses an enterprise IdP that rejects that login (remote
    /// deployments). Mirrors `BNBLOC_GATEWAY_TOKEN`.
    #[serde(default)]
    pub gateway_token: String,
    /// Which of this member's agent runs execute their commands in the local
    /// sandbox VM (when a sandbox component is installed and ready). Handed
    /// to the bridge as `BNBLOC_SANDBOX_MODE`; tenant policy may raise it.
    #[serde(default)]
    pub sandbox_mode: SandboxMode,
}

/// The member's sandbox setting (`BNBLOC_SANDBOX_MODE`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum SandboxMode {
    /// No sandbox VM runs; every run executes on the host.
    Off,
    /// Runs driven by another member (borrowed) execute in the VM.
    #[default]
    Borrowed,
    /// Every run of this member's agents executes in the VM.
    All,
}

impl SandboxMode {
    pub fn as_str(self) -> &'static str {
        match self {
            SandboxMode::Off => "off",
            SandboxMode::Borrowed => "borrowed",
            SandboxMode::All => "all",
        }
    }
}

impl Settings {
    /// True when the minimum fields to run the bridge are present. The bridge
    /// itself only requires a gateway and an owner (`services/bridge/cmd`);
    /// with no configured core it runs idle until cores are installed or
    /// configured, so a configured core is not required to launch.
    pub fn is_runnable(&self) -> bool {
        !self.gateway_url.trim().is_empty() && !self.owner.trim().is_empty()
    }
}

/// The settings file path for this app.
pub fn settings_path(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    app.path()
        .app_data_dir()
        .map(|dir| dir.join(SETTINGS_FILE))
        .map_err(|e| format!("resolve settings dir: {e}"))
}

/// Load settings; a missing or unreadable file is a fresh, unconfigured
/// client. An existing file is re-tightened to owner-only on load.
pub fn load(path: &Path) -> Settings {
    let Ok(raw) = std::fs::read(path) else {
        return Settings::default();
    };
    let _ = restrict_to_owner(path);
    serde_json::from_slice(&raw).unwrap_or_else(|e| {
        eprintln!("[settings] ignoring unreadable {}: {e}", path.display());
        Settings::default()
    })
}

/// Persist settings atomically with owner-only permissions.
pub fn save(path: &Path, settings: &Settings) -> Result<(), String> {
    let raw = serde_json::to_vec_pretty(settings).map_err(|e| e.to_string())?;
    write_owner_only(path, &raw)
}

/// Write `raw` to `path` atomically (temp file + rename) and owner-only
/// (0600 on Unix): the one writer of every credential-bearing file the shell
/// keeps (these settings, plugin local settings).
pub(crate) fn write_owner_only(path: &Path, raw: &[u8]) -> Result<(), String> {
    let dir = path.parent().ok_or("settings path has no parent")?;
    std::fs::create_dir_all(dir).map_err(|e| format!("create settings dir: {e}"))?;
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| SETTINGS_FILE.to_string());
    let tmp = dir.join(format!(".{name}.tmp"));
    let _ = std::fs::remove_file(&tmp);
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options
        .open(&tmp)
        .map_err(|e| format!("write settings: {e}"))?;
    file.write_all(raw)
        .and_then(|_| file.sync_all())
        .map_err(|e| format!("write settings: {e}"))?;
    drop(file);
    std::fs::rename(&tmp, path).map_err(|e| format!("replace settings: {e}"))
}

#[cfg(unix)]
fn restrict_to_owner(path: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))
}

#[cfg(not(unix))]
fn restrict_to_owner(_path: &Path) -> std::io::Result<()> {
    // Windows: the app data dir is already per-user (ACL-scoped).
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn empty_settings_stay_coreless() {
        let s: Settings =
            serde_json::from_str(r#"{"gatewayUrl":"http://g","owner":"me"}"#).unwrap();
        assert!(s.cores.is_empty());
    }

    #[test]
    fn keeps_new_cores_list() {
        let s: Settings = serde_json::from_str(
            r#"{"gatewayUrl":"http://g","owner":"me",
                "cores":[{"name":"acme"},{"name":"orbit","url":"http://x"}]}"#,
        )
        .unwrap();
        assert_eq!(s.cores.len(), 2);
        assert_eq!(s.cores[1].url.as_deref(), Some("http://x"));
    }

    #[test]
    fn parses_default_agent_and_disabled() {
        let s: Settings = serde_json::from_str(
            r#"{"gatewayUrl":"http://g","owner":"me","cores":[{"name":"acme"}],
                "defaultAgent":{"core":"acme","provider":"codex"},
                "disabledProviders":["acme/cursor"]}"#,
        )
        .unwrap();
        assert_eq!(s.default_agent.core, "acme");
        assert_eq!(s.default_agent.provider, "codex");
        assert_eq!(s.disabled_providers, vec!["acme/cursor".to_string()]);
        assert_eq!(s.cores[0].kind, "local");
    }

    #[test]
    fn runnable_needs_only_gateway_and_owner() {
        let base = Settings {
            gateway_url: "http://g".into(),
            owner: "me".into(),
            ..Default::default()
        };
        // No configured core: the bridge runs idle until cores are installed
        // or configured, so this still runs.
        assert!(base.is_runnable());
        assert!(!Settings {
            gateway_url: String::new(),
            ..base.clone()
        }
        .is_runnable());
        assert!(!Settings {
            owner: "  ".into(),
            ..base
        }
        .is_runnable());
    }

    #[test]
    fn save_writes_owner_only_and_round_trips() {
        let dir = std::env::temp_dir().join(format!("bnbloc-settings-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let path = dir.join(SETTINGS_FILE);
        let settings = Settings {
            gateway_url: "https://gw.example".into(),
            owner: "me".into(),
            gateway_token: "secret".into(),
            ..Default::default()
        };

        save(&path, &settings).expect("save settings");
        // A second save replaces the file in place (temp + rename).
        save(&path, &settings).expect("re-save settings");

        let loaded = load(&path);
        assert_eq!(loaded.gateway_url, "https://gw.example");
        assert_eq!(loaded.gateway_token, "secret");
        assert!(!dir.join(format!(".{SETTINGS_FILE}.tmp")).exists());
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&path).expect("stat").permissions().mode();
            assert_eq!(mode & 0o777, 0o600, "settings must be owner-only");
        }
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }

    #[cfg(unix)]
    #[test]
    fn load_tightens_a_world_readable_file() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("bnbloc-settings-perm-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("mkdir");
        let path = dir.join(SETTINGS_FILE);
        std::fs::write(&path, br#"{"owner":"me"}"#).expect("write");
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).expect("chmod");

        assert_eq!(load(&path).owner, "me");
        let mode = std::fs::metadata(&path).expect("stat").permissions().mode();
        assert_eq!(mode & 0o777, 0o600);
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }

    #[test]
    fn sandbox_mode_defaults_to_borrowed_and_round_trips() {
        let s: Settings = serde_json::from_str(r#"{"gatewayUrl":"http://g","owner":"me"}"#).unwrap();
        assert_eq!(s.sandbox_mode, SandboxMode::Borrowed);
        for (raw, mode) in [("off", SandboxMode::Off), ("borrowed", SandboxMode::Borrowed), ("all", SandboxMode::All)] {
            let s: Settings = serde_json::from_str(&format!(r#"{{"sandboxMode":"{raw}"}}"#)).unwrap();
            assert_eq!(s.sandbox_mode, mode);
            assert_eq!(mode.as_str(), raw);
        }
        assert!(serde_json::from_str::<Settings>(r#"{"sandboxMode":"sometimes"}"#).is_err());

        let dir = std::env::temp_dir().join(format!("bnbloc-settings-sandbox-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let path = dir.join(SETTINGS_FILE);
        let settings = Settings {
            sandbox_mode: SandboxMode::All,
            ..Default::default()
        };
        save(&path, &settings).expect("save settings");
        assert_eq!(load(&path).sandbox_mode, SandboxMode::All);
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }

    #[test]
    fn missing_file_is_an_unconfigured_client() {
        let s = load(Path::new("/nonexistent/bnbloc/settings.json"));
        assert!(!s.is_runnable());
    }
}
