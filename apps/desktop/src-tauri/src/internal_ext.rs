//! Desktop component sidecar supervision: resolve installed sidecar cores from
//! the gateway, download and pin their platform artifacts, then keep the local
//! binaries alive for the bridge to drive through core endpoint env overrides.
#[cfg(test)]
mod owners_tests;
#[cfg(test)]
mod reconcile_tests;
#[cfg(test)]
mod sandbox_live_tests;

pub(super) mod gateway_client;
pub(super) mod kind_runtime;
pub(super) mod loopback;
pub(super) mod mcp_tools;
pub(super) mod member_dir;
pub(super) mod pin;
pub(super) mod plugin_events;
pub(super) mod plugin_host;
pub(super) mod plugin_settings;
pub(super) mod previewer_bundle;
pub(super) mod previewer_cache;
pub(super) mod sandbox;
pub(super) mod sandbox_api;
pub(super) mod sandbox_bundle;
pub(super) mod sidecar_bridge_sync;
pub(super) mod sidecar_cache;
pub(super) mod sidecar_fetch;
pub(super) mod sidecar_model;
pub(super) mod sidecar_orphan;
pub(super) mod sidecar_runtime;
pub(super) mod sidecar_status;
pub(super) mod sidecar_sweep;
pub(super) mod sidecar_targets;
pub(super) mod sidecar_util;

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use parking_lot::Mutex;
use tauri::{AppHandle, Manager};

use gateway_client::GatewayClient;
pub use mcp_tools::McpToolSettings;
pub use plugin_settings::PluginLocalSettings;
pub use sidecar_model::AdapterStatus;
use sidecar_model::{GatewayAccess, Inner, ManagedSidecar, SidecarSpec};
pub use sidecar_targets::SidecarAuthStatus;
use sidecar_util::core_env_var;

use crate::child_process::{Backoff, Child, Stopping};

/// Steady-state catalog refresh cadence: the safety net behind the pushed
/// `components_changed` hints (plugin_events.rs), which refresh within
/// seconds of a change.
const POLL_INTERVAL: std::time::Duration = std::time::Duration::from_secs(60);
/// First retry after a failed refresh (gateway outage at boot, expired
/// session); doubles up to POLL_INTERVAL so sidecars come up soon after the
/// gateway does instead of a full poll period later.
const RETRY_MIN: std::time::Duration = std::time::Duration::from_secs(5);
/// Upper bound on one reconcile (catalog fetch + manifest write + apply).
const RECONCILE_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(60);

#[derive(Clone)]
pub struct Adapter {
    app: AppHandle,
    /// Where everything on disk lives (dirs.rs); an error when the app dirs
    /// could not be resolved.
    dirs: Arc<Result<crate::dirs::Dirs, String>>,
    /// The session the manager runs with. Its token also names the member
    /// scope (subject and tenant) whose directory holds that scope's state
    /// (member_dir; `session_scope`).
    gateway: GatewayClient,
    inner: Arc<Mutex<Inner>>,
    /// Serializes reconciles: the boot-time session handoff, the poll loop,
    /// and a post-update resume may all trigger one, and interleaved runs
    /// would race on the manifest dir and the sidecar map.
    reconcile_lock: Arc<tokio::sync::Mutex<()>>,
    /// The loopback host API plugin sidecars call as the member
    /// (plugin_host); it forwards with this adapter's gateway session.
    host: Arc<plugin_host::PluginHost>,
    /// The active sandbox component's launch and machine (sandbox.rs).
    sandbox: Arc<Mutex<sandbox::SandboxState>>,
    /// The installed MCP tools and their materialized binaries
    /// (mcp_tools.rs).
    mcp: Arc<mcp_tools::McpState>,
    /// Coalesces pushed catalog hints into one refresh (plugin_events.rs).
    catalog_hints: Arc<plugin_events::HintDebounce>,
    /// Stops of sidecar runs still in progress, by sidecar id: a relaunch
    /// waits for its own (a fixed port is free again, a data dir released).
    stopping: Stopping,
}

impl Adapter {
    /// A manager for the gateway at `gateway_url`, with no member session
    /// until the web hands one over (`update_gateway`).
    pub fn new(app: AppHandle, dirs: Result<crate::dirs::Dirs, String>, gateway_url: &str) -> Self {
        if let Ok(dirs) = &dirs {
            member_dir::remove_unkeyed_dirs(dirs.config(), dirs.data());
        }
        let gateway = GatewayClient::new(gateway_url);
        Self {
            app,
            dirs: Arc::new(dirs),
            host: plugin_host::PluginHost::new(gateway.clone()),
            gateway,
            inner: Arc::new(Mutex::new(Inner {
                desired_up: false,
                generation: 0,
                sidecars: HashMap::new(),
                bridge_cores: HashMap::new(),
            })),
            reconcile_lock: Arc::new(tokio::sync::Mutex::new(())),
            sandbox: Arc::new(Mutex::new(sandbox::SandboxState::default())),
            mcp: Arc::new(mcp_tools::McpState::default()),
            catalog_hints: Arc::new(plugin_events::HintDebounce::default()),
            stopping: Stopping::default(),
        }
    }

    /// Take over the web's session; an empty token is the web's sign-out,
    /// which leaves the manager no session at all. A session of another
    /// member scope (member, tenant or gateway, or none) switches the member
    /// directory, and the sidecars that read their scope's state (plugins
    /// with local settings, the sandbox with its data dir) restart with the
    /// new scope's.
    pub fn update_gateway(&self, gateway_url: &str, gateway_token: String) {
        let before = self.member_dir().ok();
        self.gateway.set(gateway_url, gateway_token);
        if self.member_dir().ok() != before {
            self.restart_member_sidecars();
        }
    }

    /// Where everything on disk lives (dirs.rs).
    pub(super) fn dirs(&self) -> Result<&crate::dirs::Dirs, String> {
        self.dirs.as_ref().as_ref().map_err(String::clone)
    }

    /// The member scope a session acts in (subject and tenant from its
    /// token) and that scope's directory (member_dir). None when the token
    /// names no subject or no tenant.
    fn session_scope(
        &self,
        gateway: &GatewayAccess,
    ) -> Option<(member_dir::SessionMember, std::path::PathBuf)> {
        let config = self.dirs().ok()?.config();
        let member = member_dir::token_member(&gateway.token)?;
        let dir =
            member_dir::member_dir(config, &gateway.base_url, &member.subject, &member.tenant)?;
        Some((member, dir))
    }

    /// The private directory of the member scope the manager's session acts
    /// in.
    pub(super) fn member_dir(&self) -> Result<std::path::PathBuf, String> {
        let gateway = self.gateway.session();
        self.session_scope(&gateway)
            .map(|(_, dir)| dir)
            .ok_or_else(|| "no member is signed in on this computer yet".to_string())
    }

    /// The member directory a bridge with these `settings` may read: the
    /// manager's, only when its session is the bridge's owner on the
    /// bridge's gateway, in the bridge's tenant (an empty one is the
    /// session's own). Anything else gets none: a bridge never reads another
    /// member's or another tenant's credentials.
    pub(super) fn bridge_member_dir_for(
        &self,
        settings: &crate::settings::Settings,
    ) -> Option<std::path::PathBuf> {
        let gateway = self.gateway.session();
        let (member, dir) = self.session_scope(&gateway)?;
        let tenant = settings.tenant.trim();
        if !tenant.is_empty() && tenant != member.tenant {
            return None;
        }
        let config = self.dirs().ok()?.config();
        let bridge = member_dir::member_dir(
            config,
            &settings.gateway_url,
            &settings.owner,
            &member.tenant,
        )?;
        (bridge == dir).then_some(dir)
    }

    /// Restart every sidecar that reads its member scope's state: plugins
    /// with local settings and the sandbox (its data dir).
    fn restart_member_sidecars(&self) {
        let ids: Vec<String> = self
            .inner
            .lock()
            .sidecars
            .iter()
            .filter(|(_, sidecar)| {
                sidecar
                    .spec
                    .as_ref()
                    .is_some_and(|spec| kind_runtime::runtime(spec.kind).member_scoped(spec))
            })
            .map(|(id, _)| id.clone())
            .collect();
        for id in ids {
            self.restart_sidecar(&id);
        }
    }

    /// The configured gateway origin (no trailing slash; "" before one is
    /// known). The site scheme proxies ticketed site routes to it.
    pub(crate) fn gateway_base_url(&self) -> String {
        self.gateway.base_url()
    }

    async fn reconcile(&self) -> Result<(), String> {
        let _serialized = self.reconcile_lock.lock().await;
        let gateway = self.gateway.session();
        let catalog = self.fetch_components(&gateway).await?;
        for kind in kind_runtime::KINDS {
            kind_runtime::runtime(kind).sync(self, &catalog);
        }
        let (specs, discovered) = self.resolve_specs(&catalog);
        self.write_manifests(&specs).await?;
        self.apply_specs(specs, discovered);
        self.sweep_uninstalled(&gateway, &catalog).await;
        Ok(())
    }

    /// Forget what this desktop keeps for components the live catalog no
    /// longer has (sidecar_sweep). Member-scoped state is judged only
    /// against the catalog of its own scope: the member directory comes from
    /// the same session (`gateway`) the catalog was fetched with, never from
    /// the session current by the time the sweep runs.
    async fn sweep_uninstalled(
        &self,
        gateway: &GatewayAccess,
        catalog: &sidecar_model::ComponentsResponse,
    ) {
        let live = sidecar_sweep::Live::of(catalog, &sidecar_util::current_platform());
        let dirs = sidecar_sweep::SweepDirs {
            binaries: self.dirs().ok().map(crate::dirs::Dirs::binaries),
            sandbox_bundles: self.dirs().ok().map(crate::dirs::Dirs::sandbox_bundles),
            previewers: self.dirs().ok().map(crate::dirs::Dirs::previewers),
            member: self.session_scope(gateway).map(|(_, dir)| dir),
        };
        let mcp = Arc::clone(&self.mcp);
        let swept = tauri::async_runtime::spawn_blocking(move || {
            sidecar_sweep::sweep(&dirs, &live, &mcp.sign_ins)
        })
        .await;
        if let Err(e) = swept {
            eprintln!("[sidecar-manager] sweep failed: {e}");
        }
    }

    // reconcile_bounded is the only reconcile entrypoint callers should use:
    // an unresponsive gateway (or any other hang inside resolve/apply) cancels
    // and surfaces an error instead of parking the supervision loop forever —
    // the poll cycle then retries, which is always better than silent death.
    pub async fn reconcile_bounded(&self) -> Result<(), String> {
        match tokio::time::timeout(RECONCILE_TIMEOUT, self.reconcile()).await {
            Ok(result) => result,
            Err(_) => Err(format!(
                "sidecar reconcile timed out after {}s",
                RECONCILE_TIMEOUT.as_secs()
            )),
        }
    }

    /// Refresh the catalog now without blocking the caller (the session
    /// handoff and the post-update resume); failures are retried by the poll
    /// loop.
    pub fn refresh_in_background(&self) {
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            if let Err(e) = sup.reconcile_bounded().await {
                eprintln!("[sidecar-manager] refresh failed: {e}");
            }
        });
    }

    pub fn start(&self) {
        let generation = {
            let mut inner = self.inner.lock();
            if inner.desired_up {
                return;
            }
            inner.desired_up = true;
            inner.generation += 1;
            inner.generation
        };
        self.start_known(generation);
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            sup.poll_for_updates(generation).await;
        });
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            sup.stabilize_bridge(generation).await;
        });
        // The member's own stream (plugin_events): plugin hooks for local
        // plugin sidecars and catalog hints that refresh before the poll.
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            sup.run_plugin_events(generation).await;
        });
    }

    /// Stop the manager loops, mark every sidecar stopped, and hand back the
    /// running children for the caller to terminate together with the bridge
    /// (app exit / update install).
    pub fn detach_all(&self) -> Vec<Child> {
        let children: Vec<(String, Child)> = {
            let mut inner = self.inner.lock();
            inner.desired_up = false;
            inner.generation += 1;
            for sidecar in inner.sidecars.values_mut() {
                sidecar.supervisor_active = false;
                sidecar.runtime_url = None;
                if sidecar.refused.is_none() {
                    sidecar.status.state = "stopped".into();
                    sidecar.status.message = "Stopped".into();
                }
                sidecar.generation += 1;
            }
            inner
                .sidecars
                .iter_mut()
                .filter_map(|(id, sidecar)| sidecar.child.take().map(|child| (id.clone(), child)))
                .collect()
        };
        // A clean stop: these children are terminated by the caller, so they
        // are not orphans for the next session.
        children
            .into_iter()
            .map(|(id, child)| {
                self.forget_spawn(&id, child.pid());
                child
            })
            .collect()
    }

    async fn poll_for_updates(&self, generation: u64) {
        let mut retry = Backoff::new(RETRY_MIN, POLL_INTERVAL, std::time::Duration::MAX);
        let mut delay = POLL_INTERVAL;
        loop {
            tokio::time::sleep(delay).await;
            if !self.manager_current(generation) {
                return;
            }
            delay = match self.reconcile_bounded().await {
                Ok(()) => {
                    retry.reset();
                    POLL_INTERVAL
                }
                Err(e) => {
                    eprintln!("[sidecar-manager] refresh failed: {e}");
                    retry.next(std::time::Duration::ZERO)
                }
            };
        }
    }

    fn manager_current(&self, generation: u64) -> bool {
        let inner = self.inner.lock();
        inner.desired_up && inner.generation == generation
    }

    /// Stop `id`'s sidecar and start it again with fresh launch inputs (its
    /// local settings changed). A plugin whose required settings are now
    /// missing stays stopped (start_known).
    pub(super) fn restart_sidecar(&self, id: &str) {
        let (child, generation) = {
            let mut inner = self.inner.lock();
            let generation = inner.desired_up.then_some(inner.generation);
            let child = inner.sidecars.get_mut(id).and_then(|sidecar| {
                sidecar.generation += 1;
                sidecar.supervisor_active = false;
                sidecar.runtime_url = None;
                sidecar.child.take()
            });
            (child, generation)
        };
        if let Some(child) = child {
            self.forget_spawn(id, child.pid());
            self.stopping.stop(id, vec![child]);
        }
        if let Some(generation) = generation {
            self.start_known(generation);
        }
    }

    fn apply_specs(&self, specs: Vec<SidecarSpec>, discovered: HashSet<String>) {
        let applied = {
            let mut inner = self.inner.lock();
            apply_specs_to_inner(&mut inner, specs, discovered)
        };
        // What an uninstalled component leaves on disk goes with the
        // reconcile's sweep (sidecar_sweep).
        for (id, child) in applied.removed {
            self.stopping.stop(&id, vec![child]);
        }
        if let Some(generation) = applied.generation_to_start {
            self.start_known(generation);
        }
    }
}

fn apply_specs_to_inner(
    inner: &mut Inner,
    specs: Vec<SidecarSpec>,
    discovered: HashSet<String>,
) -> AppliedSpecs {
    let mut removed = Vec::new();
    let runnable = specs.iter().map(SidecarSpec::id).collect::<HashSet<_>>();
    let uninstalled = inner
        .sidecars
        .keys()
        .filter(|name| !discovered.contains(*name))
        .cloned()
        .collect::<Vec<_>>();
    for name in uninstalled {
        if let Some(mut sidecar) = inner.sidecars.remove(&name) {
            sidecar.generation += 1;
            removed.extend(sidecar.child.take().map(|child| (name.clone(), child)));
        }
    }

    for name in discovered.difference(&runnable) {
        if let Some(sidecar) = inner.sidecars.get_mut(name) {
            sidecar.generation += 1;
            sidecar.spec = None;
            sidecar.runtime_url = None;
            sidecar.supervisor_active = false;
            sidecar.refused = None;
            removed.extend(sidecar.child.take().map(|child| (name.clone(), child)));
        }
    }

    for spec in specs {
        let status = AdapterStatus::ready(&spec);
        let id = spec.id();
        match inner.sidecars.get_mut(&id) {
            Some(sidecar) if sidecar.spec.as_ref() == Some(&spec) => {
                sidecar.status.version = Some(spec.version.clone());
                // A supervised sidecar's status is its supervisor's; a
                // refused one stays failed until the catalog publishes a
                // different spec. Only an idle one reads "ready" again.
                if sidecar.refused.is_none() && !sidecar.supervisor_active {
                    sidecar.status = status;
                }
            }
            Some(sidecar) => {
                sidecar.generation += 1;
                removed.extend(sidecar.child.take().map(|child| (id.clone(), child)));
                sidecar.spec = Some(spec);
                sidecar.runtime_url = None;
                sidecar.supervisor_active = false;
                sidecar.refused = None;
                sidecar.status = status;
            }
            None => {
                let mut sidecar = ManagedSidecar::pending(status);
                sidecar.spec = Some(spec);
                inner.sidecars.insert(id, sidecar);
            }
        }
    }
    AppliedSpecs {
        generation_to_start: inner.desired_up.then_some(inner.generation),
        removed,
    }
}

struct AppliedSpecs {
    generation_to_start: Option<u64>,
    /// Children of removed/replaced sidecars (with their ids), to stop.
    removed: Vec<(String, Child)>,
}

/// Extend the bridge env (bridge::env_for) with the managed cores and MCP
/// tools. The bridge's MCP credentials are its owner's in its tenant
/// (`Adapter::bridge_member_dir`); the bridge-stabilizing watcher restarts
/// the bridge when that directory changes (a member or tenant switch).
pub fn extend_core_env(
    app: Option<&AppHandle>,
    env: &mut std::collections::HashMap<String, String>,
    names: Vec<String>,
    settings: &crate::settings::Settings,
) -> Vec<String> {
    let Some(app) = app else {
        return names;
    };
    let Some(adapter) = app.try_state::<Adapter>() else {
        return names;
    };
    let mut out = names;
    if let Some(manifest_dir) = adapter.manifest_dir() {
        env.insert("BNBLOC_CORE_MANIFESTS_DIR".into(), manifest_dir);
    }
    // MCP tools: the materialized stdio binaries and the member's own tool
    // credentials, read by the bridge per session (no restart on change).
    if let Ok(dirs) = adapter.dirs() {
        env.insert(
            mcp_tools::TOOLS_DIR_ENV.into(),
            dirs.mcp_tools().to_string_lossy().into_owned(),
        );
    }
    if let Some(dir) = adapter.bridge_member_dir_for(settings) {
        env.insert(
            mcp_tools::SETTINGS_DIR_ENV.into(),
            dir.join(member_dir::MCP_SETTINGS_DIR)
                .to_string_lossy()
                .into_owned(),
        );
        // Which workspace each of the member's agents uses in a room, kept
        // across bridge restarts (the bridge's workspaces.json).
        env.insert(
            member_dir::BRIDGE_STATE_DIR_ENV.into(),
            dir.join(member_dir::BRIDGE_STATE_DIR)
                .to_string_lossy()
                .into_owned(),
        );
    }
    // The managed cores: exactly the set the bridge-stabilizing watcher
    // compares (sidecar_bridge_sync::bridge_cores), so the env and the
    // restart decision never disagree. A URL only for a daemon that answers
    // now; manifest cores resolve their own endpoint via discovery.
    for core in adapter.bridge_cores() {
        if let Some(url) = &core.url {
            env.insert(core_env_var(&core.name, "URL"), url.clone());
        }
        if !out.contains(&core.name) {
            out.push(core.name);
        }
    }
    out
}
