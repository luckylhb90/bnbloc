//! The accounts of this app, and what each keeps of its own.
//!
//! An **account** is a gateway and a subject (`AccountKey`); it has a current
//! tenant (its scope, which the account switches among its organizations).
//! Every account the person has signed in is live at once, whichever one a
//! window shows (docs/prd/2026-10-22-multi-account.md). What a person chose AS
//! that account (its default agent, the providers it turned off, its sync
//! secret and static gateway token, whether this computer serves its agents)
//! is its own `AccountSettings`, kept beside its member directories under
//! `<app config>/accounts/<key>/`. What belongs to the machine (the
//! configured cores and their endpoints, the dev single-host flag) stays in
//! `MachineSettings`, the one `settings.json`.
//!
//! The web and the bridge still read one merged shape, `Settings`: the
//! machine's part, the account's part, and what comes from the account itself
//! (gateway, subject as owner, tenant), so nothing past this module changes
//! for an account that has only its own settings.
use std::path::{Path, PathBuf};

use reqwest::Url;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::settings::{CoreConfig, DefaultAgent, Settings};

/// `<app config>/accounts`: one directory per account.
const ACCOUNTS_DIR: &str = "accounts";

/// The directory that holds every account's own directory.
pub(crate) fn accounts_dir(config: &Path) -> PathBuf {
    config.join(ACCOUNTS_DIR)
}
/// An account's own settings file inside its directory.
pub(crate) const ACCOUNT_SETTINGS_FILE: &str = "settings.json";

/// An account: a gateway and a subject. Two accounts of one person (or the
/// same subject on two gateways) are two keys.
#[derive(Clone, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub(crate) struct AccountKey {
    /// The gateway's origin (scheme, host, port), never a path or a trailing
    /// slash.
    gateway: String,
    subject: String,
}

/// The origin (scheme, host, port; no path, no trailing slash) of an http(s)
/// gateway URL: the one spelling of where a gateway is. None for anything
/// else.
pub(crate) fn gateway_origin(gateway_url: &str) -> Option<String> {
    let url = Url::parse(gateway_url.trim()).ok()?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return None;
    }
    Some(url.origin().ascii_serialization())
}

impl AccountKey {
    /// The account of `subject` on the gateway at `gateway_url`. None without
    /// an http(s) gateway or a subject.
    pub(crate) fn new(gateway_url: &str, subject: &str) -> Option<Self> {
        let gateway = gateway_origin(gateway_url)?;
        let subject = subject.trim();
        if subject.is_empty() {
            return None;
        }
        Some(Self {
            gateway,
            subject: subject.to_string(),
        })
    }

    pub(crate) fn gateway(&self) -> &str {
        &self.gateway
    }

    pub(crate) fn subject(&self) -> &str {
        &self.subject
    }

    /// The directory name of this account: a hash of gateway and subject, so
    /// no subject or origin is ever a path segment.
    pub(crate) fn dir_name(&self) -> String {
        self.digest(&[])
    }

    /// The 32-hex key of this account plus `more` (a tenant): the one place a
    /// directory name is derived, so a member directory (account and tenant)
    /// and an account directory never disagree on how an account is named.
    pub(crate) fn digest(&self, more: &[&str]) -> String {
        let mut hasher = Sha256::new();
        for part in [self.gateway.as_str(), self.subject.as_str()]
            .into_iter()
            .chain(more.iter().copied())
        {
            hasher.update(part.as_bytes());
            hasher.update(b"\n");
        }
        crate::internal_ext::sidecar_util::hex_lower(&hasher.finalize())[..32].to_string()
    }

    /// A short, stable name of the account for a window's label: the first
    /// characters of its directory name (never the subject or the origin).
    pub(crate) fn short(&self) -> String {
        self.dir_name().chars().take(8).collect()
    }

    /// The account's own directory under the app config dir.
    pub(crate) fn dir(&self, config: &Path) -> PathBuf {
        accounts_dir(config).join(self.dir_name())
    }
}

impl std::fmt::Display for AccountKey {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}@{}", self.subject, self.gateway)
    }
}

/// What belongs to the machine whichever account is in front: the configured
/// cores (their endpoints and tokens) and the dev single-host flag. The one
/// `settings.json`.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct MachineSettings {
    #[serde(default)]
    pub cores: Vec<CoreConfig>,
    /// Single-host mode (see `Settings::serve_all_owners`).
    #[serde(default)]
    pub serve_all_owners: bool,
}

/// What a person chose as one account.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct AccountSettings {
    /// The default agent of this account's room chats.
    #[serde(default)]
    pub default_agent: DefaultAgent,
    /// "core/provider" pairs this account turned off.
    #[serde(default)]
    pub disabled_providers: Vec<String>,
    /// The model this account chose as the default of an agent, by
    /// "core/provider".
    #[serde(default)]
    pub default_models: std::collections::BTreeMap<String, String>,
    /// The file-sync secret of this account's deployment.
    #[serde(default)]
    pub sync_secret: String,
    /// A static bearer that pins this account's bridge to that token
    /// (`BNBLOC_GATEWAY_TOKEN`).
    #[serde(default)]
    pub gateway_token: String,
    /// Whether this computer serves this account's agents (runs its bridge).
    /// On unless the person turned it off.
    #[serde(default = "serve_by_default")]
    pub serve: bool,
}

fn serve_by_default() -> bool {
    true
}

impl Default for AccountSettings {
    fn default() -> Self {
        Self {
            default_agent: DefaultAgent::default(),
            disabled_providers: Vec::new(),
            default_models: Default::default(),
            sync_secret: String::new(),
            gateway_token: String::new(),
            serve: true,
        }
    }
}

impl AccountSettings {
    /// Whether `other` differs in something the bridge is started with. The
    /// default agent and the default models are read by the web only, so a
    /// change of them needs no restart.
    pub(crate) fn bridge_differs(&self, other: &AccountSettings) -> bool {
        self.disabled_providers != other.disabled_providers
            || self.sync_secret != other.sync_secret
            || self.gateway_token != other.gateway_token
            || self.serve != other.serve
    }
}

impl Settings {
    /// The merged view of an account: the machine's settings, the account's,
    /// and what comes from the account itself (its gateway, its subject as
    /// owner, the tenant it acts in now).
    pub(crate) fn merged(
        machine: &MachineSettings,
        account: &AccountSettings,
        key: &AccountKey,
        tenant: &str,
    ) -> Self {
        Self {
            gateway_url: key.gateway().to_string(),
            owner: key.subject().to_string(),
            tenant: tenant.trim().to_string(),
            cores: machine.cores.clone(),
            default_agent: account.default_agent.clone(),
            disabled_providers: account.disabled_providers.clone(),
            default_models: account.default_models.clone(),
            sync_secret: account.sync_secret.clone(),
            serve_all_owners: machine.serve_all_owners,
            gateway_token: account.gateway_token.clone(),
        }
    }

    /// The two parts the web's form edited. The gateway, owner and tenant of
    /// the form are the account's own and are not settings: they are dropped.
    /// `serve` is the account's current switch (it is not part of the merged
    /// shape).
    pub(crate) fn split(&self, serve: bool) -> (MachineSettings, AccountSettings) {
        (
            MachineSettings {
                cores: self.cores.clone(),
                serve_all_owners: self.serve_all_owners,
            },
            AccountSettings {
                default_agent: self.default_agent.clone(),
                disabled_providers: self.disabled_providers.clone(),
                default_models: self.default_models.clone(),
                sync_secret: self.sync_secret.clone(),
                gateway_token: self.gateway_token.clone(),
                serve,
            },
        )
    }
}

/// Load an account's settings: a missing file is a fresh account; an
/// unreadable one is moved aside and reported (`Err`), as the machine's is.
pub(crate) fn load_account_settings(dir: &Path) -> Result<AccountSettings, String> {
    crate::files::read_json(&dir.join(ACCOUNT_SETTINGS_FILE)).map(Option::unwrap_or_default)
}

/// Persist an account's settings atomically, owner-only.
pub(crate) fn save_account_settings(dir: &Path, settings: &AccountSettings) -> Result<(), String> {
    let raw = serde_json::to_vec_pretty(settings).map_err(|e| e.to_string())?;
    crate::files::atomic_write(&dir.join(ACCOUNT_SETTINGS_FILE), &raw)
}

/// Load the machine's settings (the one `settings.json`).
pub(crate) fn load_machine_settings(path: &Path) -> Result<MachineSettings, String> {
    crate::files::read_json(path).map(Option::unwrap_or_default)
}

/// Persist the machine's settings atomically, owner-only.
pub(crate) fn save_machine_settings(path: &Path, settings: &MachineSettings) -> Result<(), String> {
    let raw = serde_json::to_vec_pretty(settings).map_err(|e| e.to_string())?;
    crate::files::atomic_write(path, &raw)
}

#[cfg(test)]
mod tests {
    use super::*;

    const GW: &str = "https://gw.example.com";

    #[test]
    fn an_account_is_a_gateway_origin_and_a_subject() {
        let a = AccountKey::new("https://gw.example.com/", "maya").unwrap();
        assert_eq!(
            a,
            AccountKey::new("https://gw.example.com", " maya ").unwrap()
        );
        assert_eq!(a.gateway(), "https://gw.example.com");
        assert_ne!(
            a,
            AccountKey::new("https://gw.example.com", "jack").unwrap(),
            "another subject"
        );
        assert_ne!(
            a,
            AccountKey::new("https://other.example.com", "maya").unwrap(),
            "another gateway"
        );
        assert_ne!(
            a,
            AccountKey::new("http://gw.example.com", "maya").unwrap(),
            "another scheme"
        );
        assert!(AccountKey::new("", "maya").is_none());
        assert!(AccountKey::new("not a url", "maya").is_none());
        assert!(AccountKey::new("ftp://gw.example.com", "maya").is_none());
        assert!(AccountKey::new(GW, "  ").is_none());
    }

    /// The directory name never carries the subject or the origin, and is
    /// stable per account.
    #[test]
    fn an_account_directory_is_a_hash() {
        let key = AccountKey::new(GW, "maya@example.com").unwrap();
        let dir = key.dir(Path::new("/config"));
        let name = dir.file_name().unwrap().to_string_lossy().into_owned();
        assert_eq!(name.len(), 32);
        assert!(!name.contains("maya") && !name.contains("example"));
        assert_eq!(dir, key.dir(Path::new("/config")));
        assert!(dir.starts_with("/config/accounts"));
        assert_ne!(
            dir,
            AccountKey::new(GW, "jack")
                .unwrap()
                .dir(Path::new("/config"))
        );
    }

    #[test]
    fn a_fresh_account_serves_its_agents() {
        assert!(AccountSettings::default().serve);
        let parsed: AccountSettings = serde_json::from_str("{}").unwrap();
        assert!(parsed.serve, "a settings file without the switch serves");
        let off: AccountSettings = serde_json::from_str(r#"{"serve":false}"#).unwrap();
        assert!(!off.serve);
    }

    /// The merged view carries the machine's cores, the account's own choices
    /// and the account itself; splitting it back gives the same two parts and
    /// drops what is not a setting (gateway, owner, tenant).
    #[test]
    fn the_merged_view_round_trips_through_split() {
        let machine = MachineSettings {
            cores: vec![CoreConfig {
                name: "acme".into(),
                kind: "local".into(),
                url: Some("http://127.0.0.1:1".into()),
                token: None,
            }],
            serve_all_owners: true,
        };
        let account = AccountSettings {
            default_agent: DefaultAgent {
                core: "acme".into(),
                provider: "codex".into(),
            },
            disabled_providers: vec!["acme/cursor".into()],
            default_models: [("acme/codex".to_string(), "gpt-x".to_string())].into(),
            sync_secret: "s".into(),
            gateway_token: "t".into(),
            serve: false,
        };
        let key = AccountKey::new(GW, "maya").unwrap();
        let merged = Settings::merged(&machine, &account, &key, " org-a ");
        assert_eq!(
            (
                merged.gateway_url.as_str(),
                merged.owner.as_str(),
                merged.tenant.as_str()
            ),
            (GW, "maya", "org-a")
        );
        assert!(merged.is_runnable());
        assert_eq!(merged.cores.len(), 1);
        let (m, a) = merged.split(false);
        assert_eq!(m, machine);
        assert_eq!(a, account);
        // The web's form changing only the gateway changes nothing stored.
        let edited = Settings {
            gateway_url: "https://elsewhere.example.com".into(),
            owner: "someone-else".into(),
            tenant: "org-z".into(),
            ..merged
        };
        let (m2, a2) = edited.split(false);
        assert_eq!((m2, a2), (machine, account));
    }

    #[test]
    fn both_settings_files_round_trip_owner_only() {
        let dir =
            std::env::temp_dir().join(format!("bnbloc-account-settings-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let account = AccountSettings {
            sync_secret: "s".into(),
            serve: false,
            ..Default::default()
        };
        save_account_settings(&dir, &account).unwrap();
        assert_eq!(load_account_settings(&dir).unwrap(), account);
        assert_eq!(
            load_account_settings(&dir.join("nope")).unwrap(),
            AccountSettings::default()
        );
        let machine_file = dir.join("machine.json");
        let machine = MachineSettings {
            serve_all_owners: true,
            ..Default::default()
        };
        save_machine_settings(&machine_file, &machine).unwrap();
        assert_eq!(load_machine_settings(&machine_file).unwrap(), machine);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(dir.join(ACCOUNT_SETTINGS_FILE))
                .unwrap()
                .permissions()
                .mode();
            assert_eq!(
                mode & 0o777,
                0o600,
                "an account's settings carry its gateway token"
            );
        }
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
