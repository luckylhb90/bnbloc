//! The desktop's half of the local-skills seam (docs/prd/2026-10-16-local-skills.md).
//!
//! Local skills are an installable plugin component, never part of the app. A
//! plugin that declares a capability gets the matching launch inputs and call
//! rights, and nothing else of the desktop knows it exists:
//!
//! - `sandbox-mounts`: the package names two env vars; the desktop sets the
//!   first to the descriptor file the plugin writes its read-only sandbox mounts
//!   to (`<member>/bridge/mounts/<plugin>/mounts.json`, which the bridge reads
//!   before every sandboxed turn) and the second to the folder its copies must
//!   live in (`<workspaces root>/.mounts/<plugin>`, the one folder the machine
//!   shares).
//! - `skills-provider`: a fresh bearer in the package's `tokenEnv` (kept here,
//!   never handed to the web) and a private data dir. The web renders Settings →
//!   Skills over `bnbloc.skills.provider/v1` through the three commands below,
//!   which call the plugin's loopback URL and nothing else.
//!
//! Any plugin declaring a capability is served the same way, so a member's own
//! replacement component works unchanged. Uninstalling a plugin sweeps its
//! descriptor (its mounts disappear from the next turn), its data dir and its
//! copies.
use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::time::Duration;

use serde_json::Value;

use crate::internal_ext::member_dir::{MOUNTS_DIR, PLUGIN_DATA_DIR};
use crate::internal_ext::sidecar_model::{ComponentKind, SidecarSpec};
use crate::internal_ext::sidecar_status::component_id;
use crate::internal_ext::{loopback, sandbox, Adapter};
use crate::secret::Secret;

/// The plugin capability that earns the mount descriptor and the mirror folder.
pub(super) const CAP_MOUNTS: &str = "sandbox-mounts";
/// The plugin capability that earns the bearer and the Settings view.
pub(super) const CAP_PROVIDER: &str = "skills-provider";
/// The contract the Settings view renders (a plugin's `/v1/state` echoes it).
pub(super) const PROVIDER_CONTRACT: &str = "bnbloc.skills.provider/v1";
/// The descriptor file inside a plugin's folder under `MOUNTS_DIR`.
const MOUNTS_FILE: &str = "mounts.json";
/// The folder under the workspaces root that holds every component's copies.
const MIRROR_DIR: &str = ".mounts";

/// Scanning a big folder or packing a skill takes a moment.
const CALL_TIMEOUT: Duration = Duration::from_secs(60);
/// A packed skill is bounded by the provider's policy; this is the desktop's
/// own ceiling on what it relays.
const MAX_PACK_BYTES: usize = 32 * 1024 * 1024;
/// A state or settings answer is a document, never a file.
const MAX_JSON_BYTES: usize = 4 * 1024 * 1024;

/// A provider whose process runs: where it answers and its bearer.
#[derive(Clone)]
pub(super) struct ProviderLaunch {
    pub(super) seq: u64,
    pub(super) plugin: String,
    pub(super) url: String,
    pub(super) token: Secret,
}

/// The providers whose process runs; one launch per plugin.
#[derive(Default)]
pub(super) struct Providers {
    seq: u64,
    running: Vec<ProviderLaunch>,
}

impl Providers {
    /// `plugin`'s process runs at `url`; a launch it replaces is forgotten.
    /// Returns the launch's sequence for `exited`.
    fn spawned(&mut self, plugin: &str, url: &str, token: Secret) -> u64 {
        self.seq += 1;
        self.running.retain(|p| p.plugin != plugin);
        self.running.push(ProviderLaunch {
            seq: self.seq,
            plugin: plugin.to_string(),
            url: url.to_string(),
            token,
        });
        self.seq
    }

    /// Launch `seq` ended. A newer launch of the same plugin is left alone.
    fn exited(&mut self, seq: u64) {
        self.running.retain(|p| p.seq != seq);
    }

    fn find(&self, plugin: &str) -> Option<ProviderLaunch> {
        self.running.iter().find(|p| p.plugin == plugin).cloned()
    }

    fn names(&self) -> Vec<String> {
        self.running.iter().map(|p| p.plugin.clone()).collect()
    }
}

/// One installed plugin that provides skills, as the web lists it.
#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SkillsProviderView {
    pub plugin: String,
    pub version: String,
    pub state: String,
    pub message: String,
    /// The process runs and its API can be called.
    pub running: bool,
}

/// A packed skill as the web receives it, raw bytes (no base64 through the
/// IPC): the skill's hash, a line feed, then the tar. A hash is an HTTP
/// header value, so it never holds a line feed.
pub fn frame_packed_skill(hash: &str, archive: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(hash.len() + 1 + archive.len());
    out.extend_from_slice(hash.as_bytes());
    out.push(b'\n');
    out.extend_from_slice(archive);
    out
}

/// `<member>/bridge/mounts`: every component's descriptor folder.
pub(super) fn mounts_dir(member: &Path) -> PathBuf {
    member
        .join(crate::internal_ext::member_dir::BRIDGE_STATE_DIR)
        .join(MOUNTS_DIR)
}

/// `<workspaces root>/.mounts/<account>`: the copies of every component of
/// one account (`scope` is its account directory's name): two accounts'
/// plugins never write one folder, and one account's sweep never judges
/// another's.
pub(super) fn mirror_root(scope: &str) -> Option<PathBuf> {
    sandbox::workspaces_root().map(|root| root.join(MIRROR_DIR).join(scope))
}

/// Forget the descriptors, data dirs and copies of plugins `live` no longer
/// lists (`mirror` is the account's copies folder, `mirror_root`).
pub(super) fn sweep_member(member: &Path, mirror: Option<&Path>, live: &HashSet<String>) {
    sweep_dirs(member, mirror, live);
}

fn sweep_dirs(member: &Path, mirror: Option<&Path>, live: &HashSet<String>) {
    use crate::internal_ext::sidecar_sweep::forget_entries_except;
    forget_entries_except(&mounts_dir(member), live);
    forget_entries_except(&member.join(PLUGIN_DATA_DIR), live);
    if let Some(mirror) = mirror {
        forget_entries_except(mirror, live);
    }
}

/// Create `dir` readable by its owner only.
fn private_dir(dir: &Path) -> Result<(), String> {
    std::fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))
            .map_err(|e| format!("restrict {}: {e}", dir.display()))?;
    }
    Ok(())
}

/// What a plugin's capabilities add to its launch: env pairs, and the bearer of
/// a `skills-provider` launch.
#[derive(Debug, Default, PartialEq, Eq)]
pub(super) struct JobEnv {
    pub(super) env: Vec<(String, PathBuf)>,
    pub(super) token: Option<(String, Secret)>,
}

/// Create the folders `spec`'s capabilities need under `member` (and `mirror`,
/// the folder of every component's copies) and name them in the env vars the
/// package declares.
pub(super) fn job_env(
    spec: &SidecarSpec,
    member: &Path,
    mirror: Option<&Path>,
) -> Result<JobEnv, String> {
    let plugin = &spec.plugin;
    let mut out = JobEnv::default();
    if !plugin.data_dir_env.is_empty() {
        let dir = member.join(PLUGIN_DATA_DIR).join(&spec.name);
        private_dir(&dir)?;
        out.env.push((plugin.data_dir_env.clone(), dir));
    }
    if plugin.has(CAP_MOUNTS) {
        let folder = mounts_dir(member).join(&spec.name);
        private_dir(&folder)?;
        let mirror = mirror
            .ok_or_else(|| "this computer has no home folder to share".to_string())?
            .join(&spec.name);
        std::fs::create_dir_all(&mirror)
            .map_err(|e| format!("create {}: {e}", mirror.display()))?;
        out.env
            .push((plugin.mounts_env.clone(), folder.join(MOUNTS_FILE)));
        out.env.push((plugin.mount_root_env.clone(), mirror));
    }
    if plugin.has(CAP_PROVIDER) {
        out.token = Some((plugin.token_env.clone(), Secret::generate()?));
    }
    Ok(out)
}

/// Call `provider`'s loopback API (`path` with its query); the answer's body
/// is read up to `limit` bytes. A non-2xx answer is its `error` text.
async fn call(
    provider: &ProviderLaunch,
    method: reqwest::Method,
    path: &str,
    body: Option<Value>,
    limit: usize,
) -> Result<(reqwest::header::HeaderMap, Vec<u8>), String> {
    let plugin = &provider.plugin;
    loopback::LoopbackHttp::parse(&provider.url)?;
    let url = format!("{}{path}", provider.url.trim_end_matches('/'));
    let mut req = loopback::client()?
        .request(method, url)
        .timeout(CALL_TIMEOUT)
        .bearer_auth(provider.token.expose());
    if let Some(body) = body {
        req = req
            .header(reqwest::header::CONTENT_TYPE, "application/json")
            .body(body.to_string());
    }
    let transport = |e: reqwest::Error| e.without_url().to_string();
    let mut resp = req.send().await.map_err(transport)?;
    let status = resp.status();
    let headers = resp.headers().clone();
    let mut bytes = Vec::new();
    while let Some(chunk) = resp.chunk().await.map_err(transport)? {
        if bytes.len() + chunk.len() > limit {
            return Err(format!("{plugin} answered with more than {limit} bytes"));
        }
        bytes.extend_from_slice(&chunk);
    }
    if !status.is_success() {
        let message = serde_json::from_slice::<Value>(&bytes)
            .ok()
            .and_then(|v| v.get("error").and_then(Value::as_str).map(str::to_string))
            .unwrap_or_else(|| format!("status {}", status.as_u16()));
        return Err(message);
    }
    Ok((headers, bytes))
}

impl Adapter {
    /// Set the launch env a plugin's capabilities earn. Returns the bearer of a
    /// `skills-provider` launch for `on_spawned` to register.
    pub(super) fn plugin_job_env(
        &self,
        spec: &SidecarSpec,
        cmd: &mut std::process::Command,
    ) -> Result<Option<Secret>, String> {
        let wants = !spec.plugin.data_dir_env.is_empty()
            || spec.plugin.has(CAP_MOUNTS)
            || spec.plugin.has(CAP_PROVIDER);
        if !wants {
            return Ok(None);
        }
        let member = self.member_dir()?;
        let job = job_env(spec, &member, mirror_root(&self.scope).as_deref())?;
        for (key, value) in job.env {
            cmd.env(key, value);
        }
        Ok(job.token.map(|(key, token)| {
            cmd.env(key, token.expose());
            token
        }))
    }

    /// A provider's process runs at `url`: the web may call it until it exits.
    /// Returns the launch's sequence for `provider_exited`.
    pub(super) fn provider_spawned(&self, plugin: &str, url: &str, token: Secret) -> u64 {
        self.providers.lock().spawned(plugin, url, token)
    }

    pub(super) fn provider_exited(&self, seq: u64) {
        self.providers.lock().exited(seq);
    }

    /// Every installed plugin with the `skills-provider` capability.
    pub fn skills_providers(&self) -> Vec<SkillsProviderView> {
        let running = self.providers.lock().names();
        let inner = self.inner.lock();
        let mut out: Vec<SkillsProviderView> = inner
            .sidecars
            .values()
            .filter_map(|sidecar| {
                let spec = sidecar.spec.as_ref()?;
                (spec.kind == ComponentKind::Plugin && spec.plugin.has(CAP_PROVIDER)).then(|| {
                    SkillsProviderView {
                        plugin: spec.name.clone(),
                        version: spec.version.clone(),
                        state: sidecar.status.state.clone(),
                        message: sidecar.status.message.clone(),
                        running: running.contains(&spec.name),
                    }
                })
            })
            .collect();
        out.sort_by(|a, b| a.plugin.cmp(&b.plugin));
        out
    }

    fn provider(&self, plugin: &str) -> Result<ProviderLaunch, String> {
        self.providers.lock().find(plugin).ok_or_else(|| {
            let id = component_id(ComponentKind::Plugin, plugin);
            let inner = self.inner.lock();
            match inner.sidecars.get(&id) {
                Some(s) if s.spec.as_ref().is_some_and(|s| s.plugin.has(CAP_PROVIDER)) => {
                    format!("{plugin} is not running: {}", s.status.message)
                }
                _ => format!("{plugin} is not an installed skills provider"),
            }
        })
    }

    async fn provider_call(
        &self,
        plugin: &str,
        method: reqwest::Method,
        path: &str,
        body: Option<Value>,
        limit: usize,
    ) -> Result<(reqwest::header::HeaderMap, Vec<u8>), String> {
        call(&self.provider(plugin)?, method, path, body, limit).await
    }

    /// `GET /v1/state`: what the provider has.
    pub async fn skills_provider_state(&self, plugin: &str) -> Result<Value, String> {
        let (_, body) = self
            .provider_call(
                plugin,
                reqwest::Method::GET,
                "/v1/state",
                None,
                MAX_JSON_BYTES,
            )
            .await?;
        let state: Value =
            serde_json::from_slice(&body).map_err(|e| format!("decode state: {e}"))?;
        if state.get("contract").and_then(Value::as_str) != Some(PROVIDER_CONTRACT) {
            return Err(format!("{plugin} does not speak {PROVIDER_CONTRACT}"));
        }
        Ok(state)
    }

    /// `PUT /v1/settings`: replace the provider's settings; answers its state.
    pub async fn skills_provider_apply(
        &self,
        plugin: &str,
        settings: Value,
    ) -> Result<Value, String> {
        let (_, body) = self
            .provider_call(
                plugin,
                reqwest::Method::PUT,
                "/v1/settings",
                Some(settings),
                MAX_JSON_BYTES,
            )
            .await?;
        serde_json::from_slice(&body).map_err(|e| format!("decode state: {e}"))
    }

    /// `POST /v1/pack?name=`: one skill as a tar and its hash
    /// (`frame_packed_skill`).
    pub async fn skills_provider_pack(&self, plugin: &str, name: &str) -> Result<Vec<u8>, String> {
        let mut query =
            reqwest::Url::parse("http://localhost/v1/pack").map_err(|e| e.to_string())?;
        query.query_pairs_mut().append_pair("name", name);
        let path = format!("{}?{}", query.path(), query.query().unwrap_or_default());
        let (headers, body) = self
            .provider_call(plugin, reqwest::Method::POST, &path, None, MAX_PACK_BYTES)
            .await?;
        let hash = headers
            .get("x-skill-hash")
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default()
            .to_string();
        Ok(frame_packed_skill(&hash, &body))
    }
}

#[cfg(test)]
#[path = "skills_provider_tests.rs"]
mod tests;
