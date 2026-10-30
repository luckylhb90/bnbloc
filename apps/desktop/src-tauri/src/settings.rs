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

use serde::{Deserialize, Serialize};

/// One configured agent source (daemon backend). Empty `url`/`token` rely on
/// the bridge's per-source auto-discovery.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
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
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
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
    /// The model a person chose as the default of one agent, by
    /// "core/provider". Empty = the agent's own default.
    #[serde(default)]
    pub default_models: std::collections::BTreeMap<String, String>,
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
}

// The gateway a fresh install points at before the user configured one. It
// is deployment data, so it is never hardcoded: a deployment's build sets
// BNBLOC_DEFAULT_GATEWAY (see docs/desktop-update-release-runbook.md). An
// unset build has no default — the UI asks for a gateway instead of silently
// talking to someone else's deployment.
const DEFAULT_GATEWAY_URL: Option<&str> = option_env!("BNBLOC_DEFAULT_GATEWAY");

/// The gateway a window starts on before it is bound to an account: the
/// build's default, or empty (the person picks a gateway in the UI). An
/// account's own windows start on its gateway.
pub fn default_gateway() -> String {
    normalize_gateway_url(DEFAULT_GATEWAY_URL.unwrap_or_default())
}

/// The one spelling of a gateway origin: trimmed, no trailing slash.
pub fn normalize_gateway_url(raw: &str) -> String {
    raw.trim().trim_end_matches('/').to_string()
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
    fn a_window_without_an_account_starts_on_the_build_default() {
        match DEFAULT_GATEWAY_URL {
            Some(default) => assert_eq!(default_gateway(), normalize_gateway_url(default)),
            None => assert_eq!(default_gateway(), ""),
        }
        assert_eq!(
            normalize_gateway_url(" https://gw.example.com/ "),
            "https://gw.example.com"
        );
    }
}
