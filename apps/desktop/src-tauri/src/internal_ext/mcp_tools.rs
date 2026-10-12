//! MCP tool components on the member's desktop
//! (docs/prd/2026-10-12-experts-and-teams.md §6).
//!
//! An MCP tool is never a supervised process. For every installed stdio tool
//! of the member catalog (`GET /v1/components`, rollout and opt-in gated)
//! the manager materializes this platform's binary into the tools dir
//! through the shared verified download (`ensure_verified`: the sha256 pin
//! the gateway serves, the same cache file naming as sidecar binaries), then
//! rewrites `index.json` (`{tools: [{name, sha256, path}]}`) atomically. The
//! bridge (`BNBLOC_MCP_TOOLS_DIR`) attaches a stdio tool only from an entry
//! carrying the gateway's current pin, and the agent spawns it per session.
//! Binaries of tools that left the catalog (uninstalled, rollout off, opt-in
//! dropped) or were replaced by a new pin are deleted.
//!
//! Member credentials (`credentials: "member"`) are entered in Settings →
//! Tools and stored like plugin `localSettings` (one 0600 JSON object per
//! tool under the app config dir, `BNBLOC_MCP_SETTINGS_DIR` for the bridge);
//! they never leave this machine. They are forgotten with the tool.
//!
//! An http tool declaring `auth.scheme = "oauth"` takes a sign-in instead:
//! the member signs in from Settings → Tools (mcp_oauth.rs runs the MCP
//! authorization flow in their browser), the tokens are stored 0600 under
//! `<mcp-settings>/oauth/<tool>.json` for the bridge and refreshed here on
//! every catalog refresh shortly before they expire. Removing the tool
//! forgets the sign-in.
use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::Arc;

use parking_lot::Mutex;
use tauri::Manager;

use crate::internal_ext::plugin_settings::{
    load, merge, missing_required, save, settings_path, views, LocalSettingView, Values,
};
use crate::internal_ext::sidecar_cache::ensure_verified;
use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::{
    ComponentKind, ComponentsResponse, LocalSettingField, McpToolEntry,
};
use crate::internal_ext::sidecar_status::component_id;
use crate::internal_ext::sidecar_util::{cache_file_name, current_platform};
use crate::internal_ext::Adapter;

#[path = "mcp_oauth.rs"]
mod mcp_oauth;
use mcp_oauth::{OAuthSpec, TokenFailure};

/// The env var naming the tools dir the bridge reads.
pub(super) const TOOLS_DIR_ENV: &str = "BNBLOC_MCP_TOOLS_DIR";
/// The env var naming the member's MCP settings dir the bridge reads.
pub(super) const SETTINGS_DIR_ENV: &str = "BNBLOC_MCP_SETTINGS_DIR";
/// The index the bridge reads in the tools dir.
const INDEX_FILE: &str = "index.json";

/// One installed MCP tool of the member catalog, as this desktop needs it.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct McpTool {
    pub(super) name: String,
    pub(super) version: String,
    pub(super) transport: String,
    pub(super) credentials: String,
    /// The member-credential schema (empty unless credentials are "member").
    pub(super) fields: Vec<LocalSettingField>,
    pub(super) tools: Vec<McpToolEntry>,
    /// A stdio tool's binary for this platform: (url, sha256).
    pub(super) binary: Option<(String, String)>,
    /// An http tool each member signs in to (auth scheme "oauth").
    pub(super) oauth: Option<OAuthSpec>,
}

impl McpTool {
    /// The tool keeps something of the member's on this machine (settings
    /// or a sign-in), forgotten when it leaves the catalog.
    fn holds_member_credentials(&self) -> bool {
        !self.fields.is_empty() || self.oauth.is_some()
    }
}

/// What the manager last learned of the MCP tools, and the materialization
/// state of each stdio binary.
#[derive(Default)]
pub(super) struct McpState {
    tools: Mutex<Vec<McpTool>>,
    local: Mutex<HashMap<String, (String, String)>>,
    /// Serializes materializations: each pass owns the tools dir.
    pass: tokio::sync::Mutex<()>,
    /// Tools with a sign-in open in the browser.
    signing_in: Mutex<HashSet<String>>,
    /// Serializes sign-in refreshes (a rotated refresh token is used once).
    refresh: tokio::sync::Mutex<()>,
}

/// One installed MCP tool as Settings → Tools renders it. Secret values are
/// never returned.
#[derive(Clone, Debug, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct McpToolSettings {
    pub tool: String,
    pub version: String,
    pub transport: String,
    pub credentials: String,
    pub tools: Vec<McpToolEntry>,
    /// Every required member setting is set, or the member is signed in
    /// (always true for org or no credentials).
    pub configured: bool,
    /// "oauth" when the member signs in to the tool ("" otherwise).
    pub auth: String,
    /// The member's sign-in on this computer (oauth tools; never a token).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sign_in: Option<McpSignInView>,
    /// The stdio binary's state on this machine ("ready", "downloading",
    /// "failed", "unavailable"; "" for http tools) and its message.
    pub state: String,
    pub message: String,
    pub fields: Vec<LocalSettingView>,
}

/// An oauth tool's sign-in as Settings → Tools shows it.
#[derive(Clone, Debug, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct McpSignInView {
    /// The authorization server's host ("Signed in with …").
    pub server: String,
    pub signed_in_at_ms: i64,
    /// 0 when the server stated no expiry.
    pub expires_at_ms: i64,
    /// The access token is usable now, or can be refreshed.
    pub valid: bool,
}

/// The installed MCP tools of a catalog for `platform`.
pub(super) fn installed_mcp_tools(catalog: &ComponentsResponse, platform: &str) -> Vec<McpTool> {
    let mut out: Vec<McpTool> = catalog
        .components
        .iter()
        .filter(|c| c.kind == ComponentKind::Mcp && c.installed && c.rollout != "off")
        .filter_map(|c| {
            let mcp = c.mcp.as_ref()?;
            let binary = (c.runtime.as_deref() == Some("stdio"))
                .then(|| c.artifacts.as_ref()?.get(platform))
                .flatten()
                .map(|a| (a.url.clone(), a.sha256.to_ascii_lowercase()));
            let fields = if mcp.credentials == "member" {
                mcp.env
                    .iter()
                    .map(|e| LocalSettingField {
                        key: e.name.clone(),
                        label: e.name.clone(),
                        help: e.description.clone().filter(|d| !d.trim().is_empty()),
                        secret: e.secret,
                        required: e.required,
                    })
                    .collect()
            } else {
                Vec::new()
            };
            let oauth = mcp
                .http
                .as_ref()
                .filter(|h| h.auth.scheme == "oauth")
                .map(|h| OAuthSpec {
                    url: c
                        .endpoint
                        .as_ref()
                        .map(|e| e.base_url.clone())
                        .unwrap_or_default(),
                    client_id: h.auth.client_id.clone().filter(|id| !id.is_empty()),
                    scopes: h.auth.scopes.clone(),
                });
            Some(McpTool {
                name: c.name.clone(),
                version: c.version.clone(),
                transport: mcp.transport.clone(),
                credentials: mcp.credentials.clone(),
                fields,
                tools: mcp.tools.clone(),
                binary,
                oauth,
            })
        })
        .collect();
    out.sort_by(|a, b| a.name.cmp(&b.name));
    out
}

/// One materialized binary in the index.
#[derive(Clone, Debug, PartialEq, serde::Serialize, serde::Deserialize)]
pub(super) struct IndexEntry {
    pub(super) name: String,
    pub(super) sha256: String,
    pub(super) path: String,
}

#[derive(serde::Serialize, serde::Deserialize)]
struct Index {
    tools: Vec<IndexEntry>,
}

/// The cache file of a tool's binary in the tools dir: the sidecar cache
/// naming (`mcp-<name>-<sha256>[.exe]`).
pub(super) fn tool_file(dir: &Path, name: &str, sha256: &str) -> PathBuf {
    dir.join(cache_file_name(
        &component_id(ComponentKind::Mcp, name),
        sha256,
    ))
}

/// Write the index atomically (temp file + rename): the bridge may read it
/// at any moment.
pub(super) fn write_index(dir: &Path, entries: &[IndexEntry]) -> Result<(), String> {
    std::fs::create_dir_all(dir).map_err(|e| format!("create MCP tools dir: {e}"))?;
    let raw = serde_json::to_vec_pretty(&Index {
        tools: entries.to_vec(),
    })
    .map_err(|e| e.to_string())?;
    let tmp = dir.join(format!(".{INDEX_FILE}.tmp"));
    std::fs::write(&tmp, raw).map_err(|e| format!("write MCP tools index: {e}"))?;
    std::fs::rename(&tmp, dir.join(INDEX_FILE)).map_err(|e| format!("replace MCP tools index: {e}"))
}

/// Delete everything in the tools dir but the index and `keep` (the current
/// binaries): binaries of removed or replaced tools and stale partial
/// downloads. The dir is this module's alone.
pub(super) fn collect_garbage(dir: &Path, keep: &HashSet<PathBuf>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.file_name().and_then(|n| n.to_str()) == Some(INDEX_FILE) || keep.contains(&path) {
            continue;
        }
        if let Err(e) = std::fs::remove_file(&path) {
            eprintln!("[mcp-tools] could not remove {}: {e}", path.display());
        }
    }
}

impl Adapter {
    /// The directory stdio MCP binaries are materialized into.
    pub(super) fn mcp_tools_dir(&self) -> Result<PathBuf, String> {
        self.app
            .path()
            .app_cache_dir()
            .map(|base| base.join("mcp-tools"))
            .map_err(|e| format!("cache dir: {e}"))
    }

    /// The directory holding the member's MCP tool credentials.
    pub(super) fn mcp_settings_dir(&self) -> Result<PathBuf, String> {
        self.app
            .path()
            .app_config_dir()
            .map(|dir| dir.join("mcp-settings"))
            .map_err(|e| format!("resolve app config dir: {e}"))
    }

    /// Follow a freshly fetched catalog: remember its MCP tools, forget the
    /// credentials of tools no longer installed, and materialize the stdio
    /// binaries in the background (a large download never holds up the
    /// reconcile).
    pub(super) fn sync_mcp_tools(&self, catalog: &ComponentsResponse) {
        let tools = installed_mcp_tools(catalog, &current_platform());
        let previous = std::mem::replace(&mut *self.mcp.tools.lock(), tools.clone());
        for gone in previous.iter().filter(|t| t.holds_member_credentials()) {
            let now = tools.iter().find(|t| t.name == gone.name);
            if !gone.fields.is_empty() && now.is_none_or(|t| t.fields.is_empty()) {
                self.forget_mcp_settings(&gone.name);
            }
            if gone.oauth.is_some() && now.is_none_or(|t| t.oauth.is_none()) {
                self.forget_mcp_sign_in(&gone.name);
            }
        }
        let sup = self.clone();
        let signed = tools.clone();
        tauri::async_runtime::spawn(async move {
            sup.refresh_mcp_sign_ins(&signed).await;
        });
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            if let Err(e) = sup.materialize_mcp_tools(tools).await {
                eprintln!("[mcp-tools] materialize failed: {e}");
            }
        });
    }

    async fn materialize_mcp_tools(&self, tools: Vec<McpTool>) -> Result<(), String> {
        let state = Arc::clone(&self.mcp);
        let _pass = state.pass.lock().await;
        let dir = self.mcp_tools_dir()?;
        tokio::fs::create_dir_all(&dir)
            .await
            .map_err(|e| format!("create MCP tools dir: {e}"))?;
        let gateway = self.gateway.lock().clone();
        let client = self.http()?;
        let mut entries = Vec::new();
        let mut keep = HashSet::new();
        for tool in &tools {
            if tool.transport != "stdio" {
                continue;
            }
            let Some((url, sha)) = &tool.binary else {
                self.set_mcp_local(
                    &tool.name,
                    "unavailable",
                    format!("{} is not published for {}", tool.name, current_platform()),
                );
                continue;
            };
            let dest = tool_file(&dir, &tool.name, sha);
            keep.insert(dest.clone());
            let name = tool.name.clone();
            let result = ensure_verified(&client, &gateway, url, sha, &dest, true, |p| {
                let (state, message) = p.status(&name);
                self.set_mcp_local(&name, state, message);
            })
            .await;
            match result {
                Ok(_) => {
                    self.set_mcp_local(&tool.name, "ready", format!("{} is ready", tool.name));
                    entries.push(IndexEntry {
                        name: tool.name.clone(),
                        sha256: sha.clone(),
                        path: dest.to_string_lossy().into_owned(),
                    });
                }
                Err(FetchError::Retry(m) | FetchError::Mismatch(m) | FetchError::Refused(m)) => {
                    eprintln!("[mcp-tools:{}] {m}", tool.name);
                    self.set_mcp_local(&tool.name, "failed", m);
                }
            }
        }
        let dir_for_io = dir.clone();
        tauri::async_runtime::spawn_blocking(move || {
            write_index(&dir_for_io, &entries)?;
            collect_garbage(&dir_for_io, &keep);
            Ok::<(), String>(())
        })
        .await
        .map_err(|e| format!("write MCP tools index: {e}"))??;
        let names: HashSet<&str> = tools.iter().map(|t| t.name.as_str()).collect();
        state
            .local
            .lock()
            .retain(|name, _| names.contains(name.as_str()));
        Ok(())
    }

    fn set_mcp_local(&self, name: &str, state: &str, message: String) {
        self.mcp
            .local
            .lock()
            .insert(name.to_string(), (state.to_string(), message));
    }

    /// Every installed MCP tool with this desktop's state for it.
    pub fn mcp_tool_settings(&self) -> Result<Vec<McpToolSettings>, String> {
        let dir = self.mcp_settings_dir()?;
        let tools = self.mcp.tools.lock().clone();
        let local = self.mcp.local.lock().clone();
        let mut out = Vec::new();
        for tool in tools {
            let values = if tool.fields.is_empty() {
                Values::new()
            } else {
                load(&settings_path(&dir, &tool.name)?)
            };
            let (state, message) = if tool.transport == "stdio" {
                local
                    .get(&tool.name)
                    .cloned()
                    .unwrap_or_else(|| ("pending".into(), String::new()))
            } else {
                (String::new(), String::new())
            };
            let sign_in = tool.oauth.as_ref().map(|_| {
                mcp_oauth::sign_in_path(&dir, &tool.name)
                    .ok()
                    .and_then(|p| mcp_oauth::load_sign_in(&p))
            });
            let now = mcp_oauth::now_ms();
            let sign_in_view = sign_in.flatten().map(|s| McpSignInView {
                server: s.issuer_host(),
                signed_in_at_ms: s.signed_in_at_ms,
                expires_at_ms: s.expires_at_ms,
                valid: s.valid_at(now) || !s.refresh_token.is_empty(),
            });
            let configured = if tool.oauth.is_some() {
                sign_in_view.as_ref().is_some_and(|v| v.valid)
            } else {
                missing_required(&tool.fields, &values).is_empty()
            };
            out.push(McpToolSettings {
                configured,
                auth: if tool.oauth.is_some() {
                    "oauth".into()
                } else {
                    String::new()
                },
                sign_in: sign_in_view,
                fields: views(&tool.fields, &values),
                tools: tool.tools,
                tool: tool.name,
                version: tool.version,
                transport: tool.transport,
                credentials: tool.credentials,
                state,
                message,
            });
        }
        Ok(out)
    }

    /// Validate and store the member's values for `tool` (an empty secret
    /// keeps the stored one). The bridge reads them per session: nothing
    /// restarts.
    pub fn save_mcp_tool_settings(
        &self,
        tool: &str,
        update: BTreeMap<String, String>,
    ) -> Result<(), String> {
        let schema = self.mcp_schema(tool)?;
        let path = settings_path(&self.mcp_settings_dir()?, tool)?;
        let next = merge(&schema, &load(&path), update)?;
        save(&path, &next)
    }

    /// Delete the member's values for `tool`.
    pub fn clear_mcp_tool_settings(&self, tool: &str) -> Result<(), String> {
        self.mcp_schema(tool)?;
        let path = settings_path(&self.mcp_settings_dir()?, tool)?;
        match std::fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(format!("delete settings: {e}")),
        }
    }

    /// Sign the member in to an oauth tool in their browser and store the
    /// tokens for the bridge. One sign-in per tool at a time.
    pub async fn sign_in_mcp_tool(&self, tool: &str) -> Result<(), String> {
        let spec = self
            .mcp
            .tools
            .lock()
            .iter()
            .find(|t| t.name == tool)
            .and_then(|t| t.oauth.clone())
            .ok_or_else(|| format!("MCP tool {tool} is not installed or takes no sign-in"))?;
        let path = mcp_oauth::sign_in_path(&self.mcp_settings_dir()?, tool)?;
        if !self.mcp.signing_in.lock().insert(tool.to_string()) {
            return Err(format!(
                "A sign-in to {tool} is already open in your browser"
            ));
        }
        let result = async {
            let http = mcp_oauth::oauth_client()?;
            let open = |url: String| {
                open::that_detached(&url).map_err(|e| format!("could not open the browser: {e}"))
            };
            let signed =
                mcp_oauth::sign_in(&http, tool, &spec, open, mcp_oauth::SIGN_IN_TIMEOUT).await?;
            mcp_oauth::save_sign_in(&path, &signed)
        }
        .await;
        self.mcp.signing_in.lock().remove(tool);
        result
    }

    /// Forget the member's sign-in to an oauth tool on this computer.
    pub fn sign_out_mcp_tool(&self, tool: &str) -> Result<(), String> {
        let known = self
            .mcp
            .tools
            .lock()
            .iter()
            .any(|t| t.name == tool && t.oauth.is_some());
        if !known {
            return Err(format!(
                "MCP tool {tool} is not installed or takes no sign-in"
            ));
        }
        mcp_oauth::forget_sign_in(&mcp_oauth::sign_in_path(&self.mcp_settings_dir()?, tool)?)
    }

    /// Refresh the sign-ins about to expire. A refresh the server refuses
    /// forgets the sign-in (the member signs in again); a failed request is
    /// retried on the next catalog refresh.
    async fn refresh_mcp_sign_ins(&self, tools: &[McpTool]) {
        let Ok(dir) = self.mcp_settings_dir() else {
            return;
        };
        let Ok(http) = mcp_oauth::oauth_client() else {
            return;
        };
        let _pass = self.mcp.refresh.lock().await;
        for tool in tools.iter().filter(|t| t.oauth.is_some()) {
            let Ok(path) = mcp_oauth::sign_in_path(&dir, &tool.name) else {
                continue;
            };
            let Some(current) = mcp_oauth::load_sign_in(&path) else {
                continue;
            };
            if !current.needs_refresh(mcp_oauth::now_ms()) {
                continue;
            }
            match mcp_oauth::refresh(&http, &current).await {
                Ok(next) => {
                    if let Err(e) = mcp_oauth::save_sign_in(&path, &next) {
                        eprintln!("[mcp-tools:{}] store refreshed sign-in: {e}", tool.name);
                    }
                }
                Err(TokenFailure::Refused(m)) => {
                    eprintln!("[mcp-tools:{}] sign-in no longer valid: {m}", tool.name);
                    let _ = mcp_oauth::forget_sign_in(&path);
                }
                Err(TokenFailure::Failed(m)) => {
                    eprintln!("[mcp-tools:{}] refresh failed: {m}", tool.name);
                }
            }
        }
    }

    fn forget_mcp_sign_in(&self, tool: &str) {
        if let Ok(path) = self
            .mcp_settings_dir()
            .and_then(|dir| mcp_oauth::sign_in_path(&dir, tool))
        {
            let _ = mcp_oauth::forget_sign_in(&path);
        }
    }

    fn forget_mcp_settings(&self, tool: &str) {
        if let Ok(path) = self
            .mcp_settings_dir()
            .and_then(|dir| settings_path(&dir, tool))
        {
            let _ = std::fs::remove_file(path);
        }
    }

    fn mcp_schema(&self, tool: &str) -> Result<Vec<LocalSettingField>, String> {
        self.mcp
            .tools
            .lock()
            .iter()
            .find(|t| t.name == tool)
            .map(|t| t.fields.clone())
            .filter(|fields| !fields.is_empty())
            .ok_or_else(|| format!("MCP tool {tool} is not installed or takes no member settings"))
    }
}

#[cfg(test)]
#[path = "mcp_tools_tests.rs"]
mod mcp_tools_tests;
