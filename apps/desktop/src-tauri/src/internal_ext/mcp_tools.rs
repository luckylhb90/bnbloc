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
//! A tool's member-scope settings are entered on the Components page and
//! kept in the member store (component_settings.rs, one 0600 JSON object
//! per component in the member's own directory, which the bridge reads for
//! its owner's own drives); they never leave this
//! machine and are forgotten with the tool. Its platform and organization
//! values reach the bridge with each job, never through here.
//!
//! An http tool declaring `auth.scheme = "oauth"` takes a sign-in instead:
//! the member signs in from the Components page (mcp_oauth.rs runs the
//! MCP authorization flow in their browser), the tokens are stored 0600 in
//! `<member>/mcp-sign-ins/<tool>.json` (`BNBLOC_MCP_SIGN_INS_DIR` for the
//! bridge) and refreshed here on every catalog refresh shortly before they
//! expire. Removing the tool forgets the sign-in.
use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::Arc;

use parking_lot::Mutex;

use crate::internal_ext::component_settings::{
    fields, load, missing, settings_path, ComponentSettingsView, Setting, Values,
};
use crate::internal_ext::pin::Sha256Pin;
use crate::internal_ext::sidecar_cache::ensure_verified;
use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::{
    ComponentArtifact, ComponentKind, ComponentsResponse, McpToolEntry,
};
use crate::internal_ext::sidecar_status::component_id;
use crate::internal_ext::sidecar_util::{cache_file_name, current_platform};
use crate::internal_ext::Adapter;

#[path = "mcp_oauth.rs"]
mod mcp_oauth;
pub(super) use mcp_oauth::SignIns;
use mcp_oauth::{OAuthSpec, RefreshOutcome};

/// The env var naming the tools dir the bridge reads (the member's index).
pub(super) const TOOLS_DIR_ENV: &str = "BNBLOC_MCP_TOOLS_DIR";
/// The env var naming the shared store the index's binaries are in.
pub(super) const TOOLS_STORE_ENV: &str = "BNBLOC_MCP_TOOLS_STORE";
/// The env var naming the member's MCP sign-ins dir the bridge reads.
pub(super) const SIGN_INS_DIR_ENV: &str = "BNBLOC_MCP_SIGN_INS_DIR";
/// The index the bridge reads in the member's `mcp-tools` directory.
const INDEX_FILE: &str = "index.json";

/// One installed MCP tool of the member catalog, as this desktop needs it.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct McpTool {
    pub(super) name: String,
    pub(super) version: String,
    pub(super) transport: String,
    /// The tool's settings declaration, and which platform and
    /// organization settings are set or missing for the member's
    /// organization.
    pub(super) settings: Vec<Setting>,
    pub(super) settings_set: Vec<String>,
    pub(super) settings_missing: Vec<String>,
    pub(super) tools: Vec<McpToolEntry>,
    /// A stdio tool's binary for this platform.
    pub(super) binary: Option<ComponentArtifact>,
    /// An http tool each member signs in to (auth scheme "oauth").
    pub(super) oauth: Option<OAuthSpec>,
}

/// What the uninstall sweep (sidecar_sweep) keeps of the member's MCP
/// files: the installed tools' member settings, and the sign-ins of those
/// that take one.
#[derive(Clone, Default)]
pub(super) struct McpKeep {
    pub(super) tools: HashSet<String>,
    pub(super) sign_ins: HashSet<String>,
    /// The cache file names of the stdio binaries this platform needs (the
    /// binaries live in the shared store, content-addressed: a sweep keeps
    /// what ANY signed-in account's catalog still lists).
    pub(super) files: HashSet<String>,
}

impl McpKeep {
    /// What every one of `keeps` keeps.
    pub(super) fn union<'a>(keeps: impl IntoIterator<Item = &'a McpKeep>) -> McpKeep {
        let mut out = McpKeep::default();
        for keep in keeps {
            out.tools.extend(keep.tools.iter().cloned());
            out.sign_ins.extend(keep.sign_ins.iter().cloned());
            out.files.extend(keep.files.iter().cloned());
        }
        out
    }
}

pub(super) fn mcp_keep(tools: &[McpTool]) -> McpKeep {
    McpKeep {
        tools: tools.iter().map(|t| t.name.clone()).collect(),
        sign_ins: tools
            .iter()
            .filter(|t| t.oauth.is_some())
            .map(|t| t.name.clone())
            .collect(),
        files: tools
            .iter()
            .filter_map(|t| {
                let binary = t.binary.as_ref()?;
                Some(cache_file_name(
                    &component_id(ComponentKind::Mcp, &t.name),
                    binary.sha256.as_str(),
                ))
            })
            .collect(),
    }
}

/// What the manager last learned of the MCP tools, and the materialization
/// state of each stdio binary.
#[derive(Default)]
pub(super) struct McpState {
    tools: Mutex<Vec<McpTool>>,
    local: Mutex<HashMap<String, (String, String)>>,
    /// Materialization passes: one at a time (each owns the tools dir), and
    /// at most one waiting, for the newest catalog.
    passes: Coalesced<Vec<McpTool>>,
    /// Tools with a sign-in open in the browser.
    signing_in: Mutex<HashSet<String>>,
    /// Serializes sign-in refresh passes (a rotated refresh token is used
    /// once).
    refresh: tokio::sync::Mutex<()>,
    /// The stored sign-ins: every write and delete goes through it.
    pub(super) sign_ins: mcp_oauth::SignIns,
}

/// An oauth tool's sign-in as the Components page shows it.
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
                .cloned();
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
                settings: c.settings.clone(),
                settings_set: c.settings_set.clone(),
                settings_missing: c.settings_missing.clone(),
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
    pub(super) sha256: Sha256Pin,
    pub(super) path: String,
}

#[derive(serde::Serialize, serde::Deserialize)]
struct Index {
    tools: Vec<IndexEntry>,
}

/// The cache file of a tool's binary in the tools dir: the sidecar cache
/// naming (`mcp@<name>-<sha256>[.exe]`).
pub(super) fn tool_file(dir: &Path, name: &str, sha256: &Sha256Pin) -> PathBuf {
    dir.join(cache_file_name(
        &component_id(ComponentKind::Mcp, name),
        sha256.as_str(),
    ))
}

/// Write the index atomically (files::atomic_write): the bridge may read it
/// at any moment. An unchanged index is left alone.
pub(super) fn write_index(dir: &Path, entries: &[IndexEntry]) -> Result<(), String> {
    let raw = serde_json::to_vec_pretty(&Index {
        tools: entries.to_vec(),
    })
    .map_err(|e| e.to_string())?;
    let path = dir.join(INDEX_FILE);
    if std::fs::read(&path).is_ok_and(|current| current == raw) {
        return Ok(());
    }
    crate::files::atomic_write(&path, &raw)
}

/// Delete everything in the shared binaries dir but the files in `keep`
/// (the cache file names ANY account's catalog still lists): binaries of
/// removed or replaced tools and stale partial downloads. The dir is this
/// module's alone.
pub(super) fn collect_garbage(dir: &Path, keep: &HashSet<String>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path
            .file_name()
            .and_then(|n| n.to_str())
            .is_some_and(|name| keep.contains(name))
        {
            continue;
        }
        if let Err(e) = std::fs::remove_file(&path) {
            eprintln!("[mcp-tools] could not remove {}: {e}", path.display());
        }
    }
}

impl Adapter {
    /// The directory holding the signed-in member's MCP sign-ins
    /// (member_dir).
    pub(super) fn mcp_sign_ins_dir(&self) -> Result<PathBuf, String> {
        Ok(self
            .member_dir()?
            .join(crate::internal_ext::member_dir::MCP_SIGN_INS_DIR))
    }

    /// Follow a freshly fetched catalog: remember its MCP tools and
    /// materialize the stdio binaries in the background (a large download
    /// never holds up the reconcile). The settings and sign-ins of tools no
    /// longer installed are forgotten by the reconcile's sweep
    /// (sidecar_sweep).
    pub(super) fn sync_mcp_tools(&self, catalog: &ComponentsResponse) {
        let tools = installed_mcp_tools(catalog, &current_platform());
        *self.mcp.tools.lock() = tools.clone();
        let sup = self.clone();
        let signed = tools.clone();
        tauri::async_runtime::spawn(async move {
            sup.refresh_mcp_sign_ins(&signed).await;
        });
        // A pass while one runs (a slow download) only replaces the waiting
        // catalog: passes never pile up. Binaries verified before and
        // unchanged since are not hashed again (pin.rs), so a pass over an
        // unchanged catalog only stats them.
        if !self.mcp.passes.request(tools) {
            return;
        }
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            loop {
                while let Some(tools) = sup.mcp.passes.next() {
                    if let Err(e) = sup.materialize_mcp_tools(tools).await {
                        eprintln!("[mcp-tools] materialize failed: {e}");
                    }
                }
                if !sup.mcp.passes.finish() {
                    return;
                }
            }
        });
    }

    async fn materialize_mcp_tools(&self, tools: Vec<McpTool>) -> Result<(), String> {
        let state = Arc::clone(&self.mcp);
        let dir = self.dirs()?.mcp_tools();
        tokio::fs::create_dir_all(&dir)
            .await
            .map_err(|e| format!("create MCP tools dir: {e}"))?;
        let gateway = self.gateway.session();
        // The index of THIS scope's tools is its member's; the binaries it
        // lists are in the shared store.
        let index_dir = self
            .session_scope(&gateway)
            .map(|(_, member)| member.join(crate::internal_ext::member_dir::MCP_TOOLS_INDEX_DIR));
        let mut entries = Vec::new();
        for tool in &tools {
            if tool.transport != "stdio" {
                continue;
            }
            let Some(ComponentArtifact { url, sha256: sha }) = &tool.binary else {
                self.set_mcp_local(
                    &tool.name,
                    "unavailable",
                    format!("{} is not published for {}", tool.name, current_platform()),
                );
                continue;
            };
            let dest = tool_file(&dir, &tool.name, sha);
            let name = tool.name.clone();
            let result = ensure_verified(&gateway, url, sha, &dest, true, |p| {
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
                // The sidecars' reading of a fetch failure: a transient one
                // waits for the next pass, a refused artifact fails.
                Err(FetchError::Retry(m)) => {
                    eprintln!("[mcp-tools:{}] {m}", tool.name);
                    self.set_mcp_local(
                        &tool.name,
                        "waiting",
                        format!("Waiting to install {}: {m}", tool.name),
                    );
                }
                Err(FetchError::Mismatch(m) | FetchError::Refused(m)) => {
                    eprintln!("[mcp-tools:{}] {m}", tool.name);
                    self.set_mcp_local(&tool.name, "failed", m);
                }
            }
        }
        // What the shared store keeps: the binaries of every account's
        // catalog (this scope's was registered by its reconcile).
        let keep = self.live.union().mcp().files.clone();
        let machine_wide = self.live.settled();
        let dir_for_io = dir.clone();
        tauri::async_runtime::spawn_blocking(move || {
            if let Some(index_dir) = index_dir {
                std::fs::create_dir_all(&index_dir)
                    .map_err(|e| format!("create MCP tools index dir: {e}"))?;
                write_index(&index_dir, &entries)?;
            }
            // Only once every live account's catalog is registered
            // (LiveRegistry::settled): until then the union lacks what the
            // others keep.
            if machine_wide {
                collect_garbage(&dir_for_io, &keep);
            }
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

    /// Every installed MCP tool with this desktop's state for it (Settings →
    /// Components; component_settings.rs adds the plugins).
    pub(super) fn mcp_settings_views(&self) -> Result<Vec<ComponentSettingsView>, String> {
        let settings_dir = self.component_settings_dir()?;
        let sign_ins_dir = self.mcp_sign_ins_dir()?;
        let tools = self.mcp.tools.lock().clone();
        let local = self.mcp.local.lock().clone();
        let mut out = Vec::new();
        for tool in tools {
            let (member, unreadable) = match load(&settings_path(
                &settings_dir,
                ComponentKind::Mcp,
                &tool.name,
            )?) {
                Ok(values) => (values, None),
                Err(e) => (Values::new(), Some(e)),
            };
            let (state, message) = if tool.transport == "stdio" {
                local
                    .get(&tool.name)
                    .cloned()
                    .unwrap_or_else(|| ("pending".into(), String::new()))
            } else {
                (String::new(), String::new())
            };
            let now = mcp_oauth::now_ms();
            let sign_in = tool
                .oauth
                .as_ref()
                .and_then(|_| self.mcp.sign_ins.load(&sign_ins_dir, &tool.name))
                .map(|s| McpSignInView {
                    server: s.issuer_host(),
                    signed_in_at_ms: s.signed_in_at_ms,
                    expires_at_ms: s.expires_at_ms,
                    valid: s.valid_at(now) || !s.refresh_token.is_empty(),
                });
            out.push(ComponentSettingsView {
                kind: ComponentKind::Mcp,
                auth: if tool.oauth.is_some() {
                    "oauth".into()
                } else {
                    String::new()
                },
                sign_in,
                fields: fields(&tool.settings, &member, &tool.settings_set),
                missing: missing(&tool.settings, &member, &tool.settings_missing),
                tools: Some(tool.tools),
                transport: Some(tool.transport),
                name: tool.name,
                version: tool.version,
                state,
                message: unreadable.unwrap_or(message),
            });
        }
        Ok(out)
    }

    /// The settings declaration of installed tool `name`.
    pub(super) fn mcp_settings_decl(&self, name: &str) -> Option<Vec<Setting>> {
        self.mcp
            .tools
            .lock()
            .iter()
            .find(|t| t.name == name)
            .map(|t| t.settings.clone())
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
        let dir = self.mcp_sign_ins_dir()?;
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
            self.mcp.sign_ins.save(&dir, tool, &signed)
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
        self.mcp.sign_ins.forget(&self.mcp_sign_ins_dir()?, tool)
    }

    /// Refresh the sign-ins about to expire. A refresh the server refuses
    /// forgets the sign-in (the member signs in again); a failed request is
    /// retried on the next catalog refresh; a sign-in that changed while the
    /// request ran (signed out, signed in again) is left as it is.
    async fn refresh_mcp_sign_ins(&self, tools: &[McpTool]) {
        let Ok(dir) = self.mcp_sign_ins_dir() else {
            return;
        };
        let Ok(http) = mcp_oauth::oauth_client() else {
            return;
        };
        let _pass = self.mcp.refresh.lock().await;
        for tool in tools.iter().filter(|t| t.oauth.is_some()) {
            match mcp_oauth::refresh_stored(&http, &self.mcp.sign_ins, &dir, &tool.name).await {
                RefreshOutcome::NotDue | RefreshOutcome::Refreshed => {}
                RefreshOutcome::Forgotten(m) => {
                    eprintln!("[mcp-tools:{}] sign-in no longer valid: {m}", tool.name);
                }
                RefreshOutcome::Superseded => {
                    eprintln!(
                        "[mcp-tools:{}] sign-in changed during its refresh; kept as is",
                        tool.name
                    );
                }
                RefreshOutcome::Failed(m) => {
                    eprintln!("[mcp-tools:{}] refresh failed: {m}", tool.name);
                }
            }
        }
    }
}

/// Requests served by one runner at a time, newest wins: a request while the
/// runner works replaces the one waiting (at most one), so slow work never
/// queues up passes.
pub(super) struct Coalesced<T> {
    state: Mutex<(Option<T>, bool)>,
}

impl<T> Default for Coalesced<T> {
    fn default() -> Self {
        Self {
            state: Mutex::new((None, false)),
        }
    }
}

impl<T> Coalesced<T> {
    /// Queue `value` (replacing a waiting one). True when the caller must
    /// start the runner (none runs).
    pub(super) fn request(&self, value: T) -> bool {
        let mut state = self.state.lock();
        state.0 = Some(value);
        !std::mem::replace(&mut state.1, true)
    }

    /// The runner's next value.
    pub(super) fn next(&self) -> Option<T> {
        self.state.lock().0.take()
    }

    /// The runner found nothing waiting: it stops, unless a request came in
    /// since (then it goes on).
    pub(super) fn finish(&self) -> bool {
        let mut state = self.state.lock();
        if state.0.is_some() {
            return true;
        }
        state.1 = false;
        false
    }
}

#[cfg(test)]
#[path = "mcp_tools_tests.rs"]
mod mcp_tools_tests;
