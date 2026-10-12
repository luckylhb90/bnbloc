//! Persisted desktop settings: the configuration the shell feeds the embedded
//! bridge sidecar, stored as one JSON file under the app data dir.
//!
//! These map one-to-one to the bridge's environment configuration
//! (`services/bridge/cmd`), keeping the shell a thin supervisor: it holds no
//! lifecycle semantics, only the connection parameters the connector needs.
//!
//! The file carries credentials (gateway token, sync secret, core tokens), so
//! it is written and read through `files` (owner-only, atomic; an unreadable
//! file is moved aside, never reset and overwritten).

use std::path::Path;

use serde::{Deserialize, Serialize};

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

// The gateway a fresh install points at before the user configured one. It
// is deployment data, so it is never hardcoded: a deployment's build sets
// BNBLOC_DEFAULT_GATEWAY (see docs/desktop-update-release-runbook.md). An
// unset build has no default — the UI asks for a gateway instead of silently
// talking to someone else's deployment.
const DEFAULT_GATEWAY_URL: Option<&str> = option_env!("BNBLOC_DEFAULT_GATEWAY");

/// The gateway base URL every part of the shell uses (the page's init
/// script, the bridge env, the updater, pop-out windows): the configured
/// one, else the build's default, else empty (not configured); normalized
/// by `normalize_gateway_url`.
pub fn gateway_url(settings: &Settings) -> String {
    let configured = settings.gateway_url.trim();
    normalize_gateway_url(if configured.is_empty() {
        DEFAULT_GATEWAY_URL.unwrap_or_default()
    } else {
        configured
    })
}

/// The one spelling of a gateway origin: trimmed, no trailing slash.
pub fn normalize_gateway_url(raw: &str) -> String {
    raw.trim().trim_end_matches('/').to_string()
}

impl Settings {
    /// Take the web's settings form: every field but the ones the shell
    /// owns, which only their own command changes (`sandbox_mode`:
    /// `sandbox_mode_set`). The web writes whole snapshots, which may be
    /// stale; a stale snapshot must not revert a shell-owned field.
    pub fn apply_web(&mut self, from_web: Settings) {
        let sandbox_mode = self.sandbox_mode;
        *self = from_web;
        self.sandbox_mode = sandbox_mode;
    }

    /// True when the minimum fields to run the bridge are present. The bridge
    /// itself only requires a gateway and an owner (`services/bridge/cmd`);
    /// with no configured core it runs idle until cores are installed or
    /// configured, so a configured core is not required to launch.
    pub fn is_runnable(&self) -> bool {
        !self.gateway_url.trim().is_empty() && !self.owner.trim().is_empty()
    }
}

/// Load settings: a missing file is a fresh, unconfigured client; an
/// unreadable one is moved aside and reported (`Err`), so the caller shows it
/// instead of saving defaults over the member's credentials. An existing file
/// is re-tightened to owner-only on load.
pub fn load(path: &Path) -> Result<Settings, String> {
    let _ = restrict_to_owner(path);
    crate::files::read_json(path).map(Option::unwrap_or_default)
}

/// Persist settings atomically with owner-only permissions.
pub fn save(path: &Path, settings: &Settings) -> Result<(), String> {
    let raw = serde_json::to_vec_pretty(settings).map_err(|e| e.to_string())?;
    crate::files::atomic_write(path, &raw)
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

    const SETTINGS_FILE: &str = "settings.json";

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

        let loaded = load(&path).expect("load");
        assert_eq!(loaded.gateway_url, "https://gw.example");
        assert_eq!(loaded.gateway_token, "secret");
        assert_eq!(
            std::fs::read_dir(&dir).expect("list").count(),
            1,
            "no temp left"
        );
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

        assert_eq!(load(&path).expect("load").owner, "me");
        let mode = std::fs::metadata(&path).expect("stat").permissions().mode();
        assert_eq!(mode & 0o777, 0o600);
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }

    #[test]
    fn sandbox_mode_defaults_to_borrowed_and_round_trips() {
        let s: Settings =
            serde_json::from_str(r#"{"gatewayUrl":"http://g","owner":"me"}"#).unwrap();
        assert_eq!(s.sandbox_mode, SandboxMode::Borrowed);
        for (raw, mode) in [
            ("off", SandboxMode::Off),
            ("borrowed", SandboxMode::Borrowed),
            ("all", SandboxMode::All),
        ] {
            let s: Settings =
                serde_json::from_str(&format!(r#"{{"sandboxMode":"{raw}"}}"#)).unwrap();
            assert_eq!(s.sandbox_mode, mode);
            assert_eq!(mode.as_str(), raw);
        }
        assert!(serde_json::from_str::<Settings>(r#"{"sandboxMode":"sometimes"}"#).is_err());

        let dir =
            std::env::temp_dir().join(format!("bnbloc-settings-sandbox-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let path = dir.join(SETTINGS_FILE);
        let settings = Settings {
            sandbox_mode: SandboxMode::All,
            ..Default::default()
        };
        save(&path, &settings).expect("save settings");
        assert_eq!(load(&path).expect("load").sandbox_mode, SandboxMode::All);
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }

    /// The sandbox mode is set through its own command; the web's form,
    /// written as a whole and possibly stale, never reverts it.
    #[test]
    fn set_settings_preserves_shell_owned_fields() {
        let mut current = Settings {
            owner: "maya".into(),
            sandbox_mode: SandboxMode::All,
            ..Default::default()
        };
        let stale_form = Settings {
            owner: "maya".into(),
            tenant: "t2".into(),
            sandbox_mode: SandboxMode::Borrowed,
            ..Default::default()
        };
        current.apply_web(stale_form);
        assert_eq!(current.sandbox_mode, SandboxMode::All);
        assert_eq!(current.tenant, "t2", "the web's fields are taken");
    }

    #[test]
    fn unconfigured_gateway_follows_the_build_default() {
        let resolved = gateway_url(&Settings::default());
        match DEFAULT_GATEWAY_URL {
            Some(default) => assert_eq!(resolved, normalize_gateway_url(default)),
            None => assert_eq!(resolved, ""),
        }
        let s = Settings {
            gateway_url: " https://gw.example.com/ ".into(),
            ..Default::default()
        };
        assert_eq!(gateway_url(&s), "https://gw.example.com");
    }

    #[test]
    fn missing_file_is_an_unconfigured_client() {
        let s = load(Path::new("/nonexistent/bnbloc/settings.json")).expect("missing");
        assert!(!s.is_runnable());
    }

    /// A corrupt settings file is reported and kept (moved aside), never
    /// reset to defaults and saved over: it carries the member's tokens.
    #[test]
    fn corrupt_settings_are_moved_aside_and_reported() {
        let dir = std::env::temp_dir().join(format!("bnbloc-settings-bad-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("mkdir");
        let path = dir.join(SETTINGS_FILE);
        std::fs::write(&path, br#"{"gatewayToken":"keep"#).expect("write");
        let err = load(&path).expect_err("corrupt");
        assert!(err.contains("moved aside"), "{err}");
        let kept = std::fs::read_dir(&dir).expect("list").count();
        assert_eq!(kept, 1, "the corrupt copy is kept beside");
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }
}
