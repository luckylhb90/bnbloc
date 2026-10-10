//! Desktop component sidecar supervision: resolve installed sidecar cores from
//! the gateway, download and pin their platform artifacts, then keep the local
//! binaries alive for the bridge to drive through core endpoint env overrides.
#[cfg(test)]
mod reconcile_tests;
#[cfg(test)]
mod sandbox_live_tests;

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
pub(super) mod sidecar_targets;
pub(super) mod sidecar_util;

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use parking_lot::Mutex;
use tauri::{AppHandle, Manager};

pub use plugin_settings::PluginLocalSettings;
pub use sidecar_model::AdapterStatus;
use sidecar_model::{GatewayAccess, Inner, ManagedSidecar, SidecarSpec};
pub use sidecar_targets::SidecarAuthStatus;
use sidecar_util::env_key;

use crate::child_process::{self, Backoff, Child, STOP_GRACE};

/// Steady-state catalog refresh cadence.
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
    gateway: Arc<Mutex<GatewayAccess>>,
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
}

impl Adapter {
    pub fn new(app: AppHandle, gateway_url: String, gateway_token: String) -> Self {
        let gateway = Arc::new(Mutex::new(GatewayAccess {
            base_url: gateway_url.trim_end_matches('/').to_string(),
            token: gateway_token,
        }));
        Self {
            app,
            host: plugin_host::PluginHost::new(Arc::clone(&gateway)),
            gateway,
            inner: Arc::new(Mutex::new(Inner {
                desired_up: false,
                generation: 0,
                sidecars: HashMap::new(),
                bridge_cores: HashMap::new(),
            })),
            reconcile_lock: Arc::new(tokio::sync::Mutex::new(())),
            sandbox: Arc::new(Mutex::new(sandbox::SandboxState::default())),
        }
    }

    pub fn update_gateway(&self, gateway_url: String, gateway_token: String) {
        let mut gateway = self.gateway.lock();
        gateway.base_url = gateway_url.trim_end_matches('/').to_string();
        gateway.token = gateway_token;
    }

    /// The configured gateway origin (no trailing slash; "" before one is
    /// known). The site scheme proxies ticketed site routes to it.
    pub(crate) fn gateway_base_url(&self) -> String {
        self.gateway.lock().base_url.clone()
    }

    async fn reconcile(&self) -> Result<(), String> {
        let _serialized = self.reconcile_lock.lock().await;
        let (specs, discovered) = self.resolve_specs().await?;
        self.write_manifests(&specs).await?;
        self.apply_specs(specs, discovered);
        Ok(())
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
        // Plugin hooks reach local plugin sidecars only through this
        // member's own stream (plugin_events).
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
            child_process::terminate_detached(vec![child], STOP_GRACE);
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
        child_process::terminate_detached(applied.removed, STOP_GRACE);
        for id in applied.uninstalled {
            self.remove_cached_binaries(&id, None);
            // An uninstalled plugin's local settings (its credentials) go
            // with it.
            if let Some(plugin) = id.strip_prefix("plugin/") {
                self.forget_plugin_settings(plugin);
            }
            // An uninstalled sandbox's bundles and data dir go with it.
            if let Some(name) = id.strip_prefix("sandbox/") {
                self.forget_sandbox(name);
            }
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
    for name in &uninstalled {
        if let Some(mut sidecar) = inner.sidecars.remove(name) {
            sidecar.generation += 1;
            removed.extend(sidecar.child.take());
        }
    }

    for name in discovered.difference(&runnable) {
        if let Some(sidecar) = inner.sidecars.get_mut(name) {
            sidecar.generation += 1;
            sidecar.spec = None;
            sidecar.runtime_url = None;
            sidecar.supervisor_active = false;
            sidecar.refused = None;
            removed.extend(sidecar.child.take());
        }
    }

    for spec in specs {
        let status = AdapterStatus::ready(&spec);
        let id = spec.id();
        match inner.sidecars.get_mut(&id) {
            Some(sidecar) if sidecar.spec.as_ref() == Some(&spec) => {
                sidecar.status.version = Some(spec.version.clone());
                if sidecar.refused.is_some() {
                    // Refused artifact for this exact spec: stays failed until
                    // the catalog publishes a different one.
                } else if sidecar.child.is_some() || sidecar.supervisor_active {
                    sidecar.status.state = "running".into();
                    if let Some(url) = &sidecar.runtime_url {
                        sidecar.status.message = format!("{} is running at {url}", spec.name);
                    }
                } else if sidecar.status.state != "running" {
                    sidecar.status = status;
                }
            }
            Some(sidecar) => {
                sidecar.generation += 1;
                removed.extend(sidecar.child.take());
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
        uninstalled,
    }
}

struct AppliedSpecs {
    generation_to_start: Option<u64>,
    /// Children of removed/replaced sidecars, to stop.
    removed: Vec<Child>,
    /// Component ids no longer in the member catalog: their cached binaries
    /// are deleted.
    uninstalled: Vec<String>,
}

pub fn extend_core_env(
    app: Option<&AppHandle>,
    env: &mut std::collections::HashMap<String, String>,
    names: Vec<String>,
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
    for status in adapter.status() {
        if status.kind != sidecar_model::ComponentKind::Core {
            continue;
        }
        // A URL env is only for cores with a local daemon the manager runs;
        // manifest cores resolve their own endpoint via discovery.
        if let Some(local_url) = adapter
            .local_url(&status.name)
            .filter(|url| !url.trim().is_empty())
        {
            env.insert(
                format!("BNBLOC_CORE_{}_URL", env_key(&status.name)),
                local_url,
            );
        }
        if !out.iter().any(|name| name == &status.name) {
            out.push(status.name);
        }
    }
    out
}
