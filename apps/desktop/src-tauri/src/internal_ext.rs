//! Desktop component sidecar supervision: resolve installed sidecar cores from
//! the gateway, download and pin their platform artifacts, then keep the local
//! binaries alive for the bridge to drive through core endpoint env overrides.
#[cfg(test)]
mod account_scope_tests;
#[cfg(test)]
mod owners_tests;
#[cfg(test)]
mod reconcile_tests;
#[cfg(test)]
mod sandbox_live_tests;
#[cfg(test)]
mod shared_core_tests;

pub(super) mod bridge_control;
pub(super) mod component_settings;
pub(super) mod core_host;
pub(super) mod gateway_client;
pub(super) mod kind_runtime;
pub(super) mod loopback;
pub(super) mod mcp_tools;
pub(super) mod member_dir;
pub(super) mod pin;
pub(super) mod plugin_events;
pub(super) mod plugin_host;
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
pub(super) mod skills_provider;
pub(super) mod update_gate;

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use parking_lot::Mutex;

pub(crate) use bridge_control::BridgeControl;

pub use component_settings::{ComponentSettingsView, SettingsSaveError};
use gateway_client::GatewayClient;
pub use sidecar_model::AdapterStatus;
use sidecar_model::{GatewayAccess, Inner, ManagedSidecar, SidecarSpec};
pub use sidecar_targets::SidecarAuthStatus;
use sidecar_util::core_env_var;
pub use skills_provider::{PanelAnswer, SkillsProviderView};

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
    /// The skills providers whose process runs (skills_provider.rs).
    providers: Arc<Mutex<skills_provider::Providers>>,
    /// The installed MCP tools and their materialized binaries
    /// (mcp_tools.rs).
    mcp: Arc<mcp_tools::McpState>,
    /// Coalesces pushed catalog hints into one refresh (plugin_events.rs).
    catalog_hints: Arc<plugin_events::HintDebounce>,
    /// The previewer pins this account's catalog resolved and the bundles it
    /// serves from memory (previewer_cache.rs).
    previewers: Arc<previewer_cache::PreviewerBundles>,
    /// The live catalog of every signed-in account, by scope (shared by all
    /// adapters): the machine-wide caches are swept by the union
    /// (sidecar_sweep), so this adapter's reconcile never deletes what another
    /// account lists.
    live: Arc<sidecar_sweep::LiveRegistry>,
    /// The core daemons of this computer, held by every account that
    /// installs them (core_host.rs), and this adapter's hold on them.
    cores: Arc<core_host::CoreHost>,
    holder: core_host::HolderId,
    /// The name of this adapter's account for the machine-wide directories it
    /// keeps a directory in (the spawn records of its own sidecars): the
    /// account directory's name (`AccountKey::dir_name`).
    scope: String,
    /// Whether this account serves agents from this computer. One that does
    /// not (a test account, which only looks) runs no component: it follows
    /// its catalog for what its windows show (previewers) and nothing else.
    serves: bool,
    /// The bridge that serves this adapter's account (bridge_control).
    bridge: Arc<dyn BridgeControl>,
    /// Stops of sidecar runs still in progress, by sidecar id: a relaunch
    /// waits for its own (a fixed port is free again, a data dir released).
    stopping: Stopping,
    /// Until when the member's "apply now" lets a pending update (and the
    /// bridge restart it causes) go ahead while turns run (update_gate).
    apply_now_until: Arc<Mutex<Option<std::time::Instant>>>,
}

/// Remove what earlier layouts left under the app's directories (once, at
/// app start, before any account's adapter runs). Blocking, best-effort.
pub(crate) fn remove_retired_state(dirs: &crate::dirs::Dirs) {
    member_dir::remove_retired_dirs(dirs.config(), dirs.data());
    let _ = std::fs::remove_dir_all(dirs.retired_core_manifests());
}

/// What every account's adapter shares on this computer.
#[derive(Clone)]
pub(crate) struct Shared {
    pub(crate) live: Arc<sidecar_sweep::LiveRegistry>,
    pub(crate) cores: Arc<core_host::CoreHost>,
}

impl Shared {
    pub(crate) fn new(dirs: Result<crate::dirs::Dirs, String>) -> Self {
        Self {
            live: Arc::default(),
            cores: core_host::CoreHost::new(dirs),
        }
    }
}

impl Adapter {
    /// A manager for the gateway at `gateway_url`, with no member session
    /// until the web hands one over (`update_gateway`). `scope` names its
    /// account where the machine keeps a directory per account; `serves` says
    /// whether the account serves agents from this computer.
    pub fn new(
        dirs: Result<crate::dirs::Dirs, String>,
        gateway_url: &str,
        shared: Shared,
        scope: &str,
        serves: bool,
        bridge: Arc<dyn BridgeControl>,
    ) -> Self {
        let gateway = GatewayClient::new(gateway_url);
        let holder = shared.cores.register();
        shared.live.expect(holder);
        let adapter = Self {
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
            providers: Arc::new(Mutex::new(skills_provider::Providers::default())),
            mcp: Arc::new(mcp_tools::McpState::default()),
            catalog_hints: Arc::new(plugin_events::HintDebounce::default()),
            previewers: Arc::new(previewer_cache::PreviewerBundles::default()),
            live: shared.live,
            cores: shared.cores,
            holder,
            scope: scope.to_string(),
            serves,
            bridge,
            stopping: Stopping::default(),
            apply_now_until: Arc::new(Mutex::new(None)),
        };
        // The core host asks this adapter's bridge for its turns: the update
        // of a core waits for every account's. `leave` ends the loop of
        // references.
        let counted = adapter.clone();
        adapter
            .cores
            .set_turns(holder, Arc::new(move || counted.running_turns()));
        adapter
    }

    /// This adapter's account is leaving: it holds no core and counts no
    /// turns.
    pub(crate) fn leave(&self) {
        self.cores.unregister(self.holder);
        self.live.leave(self.holder);
    }

    /// Take over the web's session; an empty token is the web's sign-out,
    /// which leaves the manager no session at all. A session of another
    /// member scope (member, tenant or gateway, or none) switches the member
    /// directory, and the sidecars that read their scope's state (plugins
    /// with settings, the sandbox with its data dir) restart with the
    /// new scope's.
    pub fn update_gateway(&self, gateway_url: &str, gateway_token: String) {
        let before = self.member_dir().ok();
        self.gateway.set(gateway_url, gateway_token);
        if self.member_dir().ok() != before {
            // The scope left is no longer in force: its catalog stops
            // holding the machine-wide caches.
            if let Some(dir) = &before {
                self.live.remove(&live_scope(Some(dir)));
            }
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

    /// The subject the manager's own session acts as ("" when none): the
    /// sandbox machines are labelled with it, not with whatever the app's
    /// settings slot holds.
    pub(super) fn session_subject(&self) -> String {
        member_dir::token_member(&self.gateway.session().token)
            .map(|m| m.subject)
            .unwrap_or_default()
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
    /// with settings and the sandbox (its data dir).
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
        // This scope's catalog is in force before anything sweeps or
        // materializes: the machine-wide caches keep what it lists.
        let live = sidecar_sweep::Live::of(&catalog, &sidecar_util::current_platform());
        let scope = self.session_scope(&gateway).map(|(_, dir)| dir);
        self.live.set(&live_scope(scope.as_ref()), live);
        self.live.arrived(self.holder);
        for kind in kind_runtime::KINDS {
            if self.serves || !kind_runtime::runtime(kind).for_agents() {
                kind_runtime::runtime(kind).sync(self, &catalog);
            }
        }
        if self.serves {
            let (specs, discovered) = self.resolve_specs(&catalog);
            self.write_manifests(&specs).await?;
            self.apply_specs(specs, discovered);
        }
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
        let own = sidecar_sweep::Live::of(catalog, &sidecar_util::current_platform());
        // The machine-wide caches are judged by what every account's catalog
        // lists, this one included (set in reconcile).
        // Plus the builds of the cores that run: they restart from their
        // binary, whatever a catalog pins by now.
        let machine = self.live.union().with_running(self.cores.held_builds());
        // The machine-wide caches are swept only once every live account's
        // catalog is registered (LiveRegistry::settled).
        let machine_wide = self.live.settled();
        let dirs = sidecar_sweep::SweepDirs {
            binaries: self
                .dirs()
                .ok()
                .filter(|_| machine_wide)
                .map(crate::dirs::Dirs::binaries),
            sandbox_bundles: self
                .dirs()
                .ok()
                .filter(|_| machine_wide)
                .map(crate::dirs::Dirs::sandbox_bundles),
            previewers: self
                .dirs()
                .ok()
                .filter(|_| machine_wide)
                .map(crate::dirs::Dirs::previewers),
            member: self.session_scope(gateway).map(|(_, dir)| dir),
            mirror: skills_provider::mirror_root(&self.scope),
        };
        let mcp = Arc::clone(&self.mcp);
        let swept = tauri::async_runtime::spawn_blocking(move || {
            sidecar_sweep::sweep(&dirs, &own, &machine, &mcp.sign_ins)
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
        // The shared sidecars this account held are let go of (the core host
        // keeps them for the accounts that still hold them).
        self.cores.nudge();
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
    /// settings changed). A plugin whose required settings are now
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
        let running = self.gate_turns();
        let apply_now = self.apply_now();
        let applied = {
            let mut inner = self.inner.lock();
            apply_specs_to_inner(&mut inner, specs, discovered, running, apply_now)
        };
        self.finish_applied(applied);
    }

    /// Stop what an apply removed or replaced and start the rest.
    fn finish_applied(&self, applied: AppliedSpecs) {
        // A shared sidecar this replaced or removed is let go of by the
        // follower that held it: it looks now.
        self.cores.nudge();
        // What an uninstalled component leaves on disk goes with the
        // reconcile's sweep (sidecar_sweep).
        for (id, child) in applied.removed {
            self.stopping.stop(&id, vec![child]);
        }
        if let Some(generation) = applied.generation_to_start {
            self.start_known(generation);
        }
    }

    /// Apply the updates that waited for an idle moment, when there is one
    /// (or the member applied them now). Called on the recheck cadence.
    pub(super) fn apply_pending(&self) {
        let has_pending = self
            .inner
            .lock()
            .sidecars
            .values()
            .any(|s| s.pending.is_some());
        if !has_pending {
            return;
        }
        let running = self.gate_turns();
        let apply_now = self.apply_now();
        let applied = {
            let mut inner = self.inner.lock();
            apply_pending_in(&mut inner, running, apply_now)
        };
        self.finish_applied(applied);
    }

    /// The member's "apply now": pending updates (and the bridge restart
    /// they cause) go ahead even though turns run.
    pub fn apply_pending_now(&self) {
        *self.apply_now_until.lock() =
            Some(std::time::Instant::now() + update_gate::APPLY_NOW_WINDOW);
        self.apply_pending();
    }

    pub(super) fn apply_now(&self) -> bool {
        update_gate::apply_now_active(*self.apply_now_until.lock(), std::time::Instant::now())
    }

    /// The turns every account's bridge drives now: what an update of a core
    /// (one process under all of them) waits for (update_gate).
    pub(super) fn gate_turns(&self) -> u32 {
        self.cores.running_turns()
    }

    /// The turns this account's bridge drives now (update_gate).
    pub fn running_turns(&self) -> u32 {
        let (file, pid) = self.bridge_turns();
        update_gate::running_turns(file.as_ref(), pid)
    }

    /// The turns the bridge this app runs drives on the own sandbox machine
    /// now (update_gate).
    pub(super) fn own_machine_turns(&self) -> u32 {
        let (file, pid) = self.bridge_turns();
        update_gate::own_machine_turns(file.as_ref(), pid)
    }

    /// The bridge's running-turns file and the pid of the bridge this app
    /// runs.
    fn bridge_turns(&self) -> (Option<update_gate::RunningTurns>, Option<u32>) {
        let settings = self.bridge.settings();
        let pid = self.bridge.status().pid;
        let Some(dir) = self.bridge_member_dir_for(&settings) else {
            return (None, pid);
        };
        let file = update_gate::read_running_turns(&dir.join(member_dir::BRIDGE_STATE_DIR));
        (file, pid)
    }
}

/// The registry key of a scope's catalog: its member directory (a scope
/// without one, a legacy token, is one entry of its own).
fn live_scope(member: Option<&std::path::PathBuf>) -> String {
    member
        .map(|dir| dir.to_string_lossy().into_owned())
        .unwrap_or_default()
}

fn apply_specs_to_inner(
    inner: &mut Inner,
    specs: Vec<SidecarSpec>,
    discovered: HashSet<String>,
    running: u32,
    apply_now: bool,
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
            sidecar.pending = None;
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
                // The catalog is back to what runs: nothing waits any more.
                sidecar.pending = None;
                sidecar.status.version = Some(spec.version.clone());
                // A supervised sidecar's status is its supervisor's; a
                // refused one stays failed until the catalog publishes a
                // different spec. Only an idle one reads "ready" again.
                if sidecar.refused.is_none() && !sidecar.supervisor_active {
                    sidecar.status = status;
                }
            }
            Some(sidecar) => {
                // A running sidecar's update waits while turns run on it
                // (update_gate); a stopped one has nothing to interrupt.
                let live = sidecar.spec.is_some()
                    && (sidecar.child.is_some() || sidecar.runtime_url.is_some());
                // It keeps running meanwhile; its status shows the waiting
                // version (sidecar_status::status).
                if live
                    && update_gate::gate(spec.kind, running, apply_now) != update_gate::Gate::Apply
                {
                    sidecar.pending = Some(spec);
                    continue;
                }
                replace_spec(sidecar, &id, spec, status, &mut removed);
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

/// Put `spec` in place of what `sidecar` runs: its child stops (into
/// `removed`) and the next start launches the new one.
fn replace_spec(
    sidecar: &mut ManagedSidecar,
    id: &str,
    spec: SidecarSpec,
    status: AdapterStatus,
    removed: &mut Vec<(String, Child)>,
) {
    sidecar.generation += 1;
    removed.extend(sidecar.child.take().map(|child| (id.to_string(), child)));
    sidecar.spec = Some(spec);
    sidecar.pending = None;
    sidecar.runtime_url = None;
    sidecar.supervisor_active = false;
    sidecar.refused = None;
    sidecar.status = status;
}

/// Apply the waiting updates the gate now lets through (no turn runs, or
/// the member applied them now); the others keep waiting.
fn apply_pending_in(inner: &mut Inner, running: u32, apply_now: bool) -> AppliedSpecs {
    let mut removed = Vec::new();
    let mut applied_any = false;
    for (id, sidecar) in inner.sidecars.iter_mut() {
        let Some(spec) = sidecar.pending.take() else {
            continue;
        };
        if update_gate::gate(spec.kind, running, apply_now) == update_gate::Gate::Apply {
            let status = AdapterStatus::ready(&spec);
            replace_spec(sidecar, id, spec, status, &mut removed);
            applied_any = true;
        } else {
            sidecar.pending = Some(spec);
        }
    }
    AppliedSpecs {
        generation_to_start: (applied_any && inner.desired_up).then_some(inner.generation),
        removed,
    }
}

struct AppliedSpecs {
    generation_to_start: Option<u64>,
    /// Children of removed/replaced sidecars (with their ids), to stop.
    removed: Vec<(String, Child)>,
}

/// Extend the bridge env (bridge::env_for) with the managed cores and MCP
/// tools. The bridge's MCP settings and sign-ins are its owner's in its tenant
/// (`Adapter::bridge_member_dir`); the bridge-stabilizing watcher restarts
/// the bridge when that directory changes (a member or tenant switch).
pub fn extend_core_env(
    adapter: Option<&Adapter>,
    env: &mut std::collections::HashMap<String, String>,
    names: Vec<String>,
    settings: &crate::settings::Settings,
) -> Vec<String> {
    let Some(adapter) = adapter else {
        return names;
    };
    let mut out = names;
    // What the member's catalog decides (the manifest cores, the MCP tools
    // index, the MCP settings and sign-ins) is the member's own, read from
    // their directory per session (no restart on change), and only the
    // bridge that runs for that member gets it.
    if let Some(dir) = adapter.bridge_member_dir_for(settings) {
        env.insert(
            "BNBLOC_CORE_MANIFESTS_DIR".into(),
            dir.join(member_dir::CORE_MANIFESTS_DIR)
                .to_string_lossy()
                .into_owned(),
        );
        env.insert(
            mcp_tools::TOOLS_DIR_ENV.into(),
            dir.join(member_dir::MCP_TOOLS_INDEX_DIR)
                .to_string_lossy()
                .into_owned(),
        );
        env.insert(
            component_settings::SETTINGS_DIR_ENV.into(),
            dir.join(member_dir::COMPONENT_SETTINGS_DIR)
                .to_string_lossy()
                .into_owned(),
        );
        env.insert(
            mcp_tools::SIGN_INS_DIR_ENV.into(),
            dir.join(member_dir::MCP_SIGN_INS_DIR)
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
    for core in adapter.bridge_cores_for(settings) {
        if let Some(url) = &core.url {
            env.insert(core_env_var(&core.name, "URL"), url.clone());
        }
        if !out.contains(&core.name) {
            out.push(core.name);
        }
    }
    out
}
