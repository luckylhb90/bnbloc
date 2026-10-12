//! What differs per component kind, in one place.
//!
//! The generic machinery (the catalog reconcile, sidecar supervision, the
//! uninstall sweep) never branches on a component's kind: it asks the kind's
//! `KindRuntime`, and `runtime(kind)` is the one dispatch by kind.
//!
//! - Every kind: `sync` follows a freshly fetched catalog (previewers drop
//!   uninstalled bundles from memory, MCP tools materialize their binaries),
//!   and `sweep` forgets on disk what the live catalog no longer lists.
//! - Supervised sidecars (core, plugin, sandbox): `blocker` (may it launch
//!   now), `prepare` (its verified executable), `listener` (where it
//!   listens), `reuses_listener` (may a compatible listener already on its
//!   port be driven in place), `launch` (its kind-only launch env, as a
//!   `LaunchGuard`), `on_spawned`, and `member_scoped` (restart it when the
//!   member scope changes).
//!
//! A `LaunchGuard` holds what one launch hands out beyond its process (a
//! plugin's host API grant, the sandbox's registered launch) and releases it
//! when the launch ends, on every path: a failed spawn, a spawn superseded
//! before it ran, the process exit.
use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use futures_util::future::BoxFuture;

use crate::internal_ext::mcp_tools::SignIns;
use crate::internal_ext::member_dir::{MCP_SETTINGS_DIR, PLUGIN_SETTINGS_DIR, SANDBOX_DATA_DIR};
use crate::internal_ext::plugin_host::{self, PluginHost};
use crate::internal_ext::plugin_settings::{self, forget_unlisted};
use crate::internal_ext::sandbox_bundle;
use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::{ComponentKind, ComponentsResponse, SidecarSpec};
use crate::internal_ext::sidecar_sweep::{forget_entries_except, Live, SweepDirs};
use crate::internal_ext::sidecar_util::{cache_stem, runtime_listener_for, RuntimeListener};
use crate::internal_ext::Adapter;
use crate::secret::Secret;

/// The kinds the desktop knows, in the order a reconcile visits them.
pub(super) const KINDS: [ComponentKind; 5] = [
    ComponentKind::Core,
    ComponentKind::Plugin,
    ComponentKind::Sandbox,
    ComponentKind::Previewer,
    ComponentKind::Mcp,
];

/// The runtime of `kind`: the one dispatch by kind.
pub(super) fn runtime(kind: ComponentKind) -> &'static dyn KindRuntime {
    match kind {
        ComponentKind::Core => &Core,
        ComponentKind::Plugin => &Plugin,
        ComponentKind::Sandbox => &Sandbox,
        ComponentKind::Previewer => &Previewer,
        ComponentKind::Mcp => &Mcp,
        // Dropped when the catalog is decoded; inert if one ever got here.
        ComponentKind::Unknown => &Inert,
    }
}

pub(super) trait KindRuntime: Sync {
    /// Follow a freshly fetched catalog (in memory; quick).
    fn sync(&self, _adapter: &Adapter, _catalog: &ComponentsResponse) {}

    /// Forget on disk what `live` no longer lists. Blocking.
    fn sweep(&self, _dirs: &SweepDirs, _live: &Live, _sign_ins: &SignIns) {}

    /// Whether this sidecar reads its member scope's state (it restarts
    /// when the member, tenant or gateway changes).
    fn member_scoped(&self, _spec: &SidecarSpec) -> bool {
        false
    }

    /// Why this sidecar may not launch now (None: it may).
    fn blocker(&self, _adapter: &Adapter, _spec: &SidecarSpec) -> Option<String> {
        None
    }

    /// The verified executable to run.
    fn prepare<'a>(
        &'a self,
        adapter: &'a Adapter,
        spec: &'a SidecarSpec,
    ) -> BoxFuture<'a, Result<PathBuf, FetchError>> {
        Box::pin(adapter.ensure_binary(spec))
    }

    /// Where the sidecar listens: the package's fixed `localUrl`, reserved.
    fn listener<'a>(
        &'a self,
        _adapter: &'a Adapter,
        spec: &'a SidecarSpec,
    ) -> BoxFuture<'a, Result<RuntimeListener, String>> {
        Box::pin(async move { runtime_listener_for(&spec.local_url) })
    }

    /// Whether a compatible listener already on the fixed port may be
    /// driven in place (it speaks a protocol the desktop can probe).
    fn reuses_listener(&self, _spec: &SidecarSpec) -> bool {
        false
    }

    /// The kind-only launch env.
    fn launch<'a>(
        &'a self,
        _adapter: &'a Adapter,
        _spec: &'a SidecarSpec,
        _exe: &'a Path,
        _cmd: &'a mut std::process::Command,
    ) -> BoxFuture<'a, Result<LaunchGuard, String>> {
        Box::pin(async { Ok(LaunchGuard::default()) })
    }

    /// The launch's process is running at `url`.
    fn on_spawned(
        &self,
        _adapter: &Adapter,
        _spec: &SidecarSpec,
        _url: &str,
        _exe: &Path,
        _guard: &mut LaunchGuard,
    ) {
    }
}

/// What runs when a launch ends, given the process's last output line.
type LaunchEnd = Box<dyn FnOnce(&str) + Send>;

/// What one launch holds beyond its process. Dropping it ends the launch:
/// every registered end runs once, with the process's last output line
/// ("" when it never ran).
#[derive(Default)]
pub(super) struct LaunchGuard {
    /// A per-launch secret handed to the process that `on_spawned` may take
    /// over (the sandbox bearer).
    pub(super) secret: Option<Secret>,
    ends: Vec<LaunchEnd>,
    tail: String,
}

impl LaunchGuard {
    /// Run `end` when the launch ends, whatever the path.
    pub(super) fn on_end(&mut self, end: impl FnOnce(&str) + Send + 'static) {
        self.ends.push(Box::new(end));
    }

    /// The process exited; `tail` is its last output line.
    pub(super) fn exited(mut self, tail: &str) {
        self.tail = tail.to_string();
    }
}

impl Drop for LaunchGuard {
    fn drop(&mut self) {
        let tail = std::mem::take(&mut self.tail);
        for end in self.ends.drain(..) {
            end(&tail);
        }
    }
}

/// Grant a launch of `plugin` the host API (its URL and a per-launch token
/// in `cmd`); the grant is revoked when `guard` ends.
async fn grant_host(
    host: &Arc<PluginHost>,
    plugin: &str,
    permissions: &[String],
    cmd: &mut std::process::Command,
    guard: &mut LaunchGuard,
) -> Result<(), String> {
    let url = host.ensure_started().await?;
    let token = host.grant(plugin, permissions)?;
    cmd.env(plugin_host::HOST_URL_ENV, url);
    cmd.env(plugin_host::HOST_TOKEN_ENV, token.expose());
    let host = Arc::clone(host);
    guard.on_end(move |_| host.revoke(&token));
    Ok(())
}

/// A core sidecar (a Core Protocol daemon) or a manifest core.
struct Core;

impl KindRuntime for Core {
    /// A daemon without a manifest speaks Core Protocol (`/core/v1/discover`
    /// identifies it); a REST-manifest core is never probed for reuse.
    fn reuses_listener(&self, spec: &SidecarSpec) -> bool {
        spec.manifest.is_none()
    }
}

/// A plugin sidecar: host API grants and member-local settings.
struct Plugin;

impl KindRuntime for Plugin {
    fn sweep(&self, dirs: &SweepDirs, live: &Live, _sign_ins: &SignIns) {
        if let Some(member) = &dirs.member {
            forget_unlisted(
                &member.join(PLUGIN_SETTINGS_DIR),
                live.names(ComponentKind::Plugin),
            );
        }
    }

    fn member_scoped(&self, spec: &SidecarSpec) -> bool {
        !spec.plugin.local_settings.is_empty()
    }

    /// A plugin missing required local settings is not launched: the member
    /// enters them in Settings → Notifications (which restarts it).
    fn blocker(&self, adapter: &Adapter, spec: &SidecarSpec) -> Option<String> {
        adapter.plugin_settings_blocker(&spec.name, &spec.plugin.local_settings)
    }

    fn launch<'a>(
        &'a self,
        adapter: &'a Adapter,
        spec: &'a SidecarSpec,
        _exe: &'a Path,
        cmd: &'a mut std::process::Command,
    ) -> BoxFuture<'a, Result<LaunchGuard, String>> {
        Box::pin(async move {
            let mut guard = LaunchGuard::default();
            if !spec.plugin.local_settings.is_empty() {
                cmd.env(
                    plugin_settings::SETTINGS_FILE_ENV,
                    adapter.plugin_settings_file(&spec.name)?,
                );
            }
            if !spec.plugin.host.is_empty() {
                grant_host(
                    &adapter.host,
                    &spec.name,
                    &spec.plugin.host,
                    cmd,
                    &mut guard,
                )
                .await?;
            }
            Ok(guard)
        })
    }
}

/// The sandbox runtime: a per-version bundle, a fresh port, a per-launch
/// bearer and a member-scoped data dir (sandbox.rs).
struct Sandbox;

impl KindRuntime for Sandbox {
    fn sweep(&self, dirs: &SweepDirs, live: &Live, _sign_ins: &SignIns) {
        let keep: HashSet<String> = live
            .names(ComponentKind::Sandbox)
            .iter()
            .map(|name| cache_stem(name))
            .collect();
        if let Some(bundles) = &dirs.sandbox_bundles {
            forget_entries_except(bundles, &keep);
        }
        if let Some(member) = &dirs.member {
            forget_entries_except(&member.join(SANDBOX_DATA_DIR), &keep);
        }
    }

    fn member_scoped(&self, _spec: &SidecarSpec) -> bool {
        true
    }

    fn prepare<'a>(
        &'a self,
        adapter: &'a Adapter,
        spec: &'a SidecarSpec,
    ) -> BoxFuture<'a, Result<PathBuf, FetchError>> {
        Box::pin(adapter.ensure_sandbox_bundle(spec))
    }

    /// A fresh loopback port each launch: its callers learn the URL from
    /// the desktop.
    fn listener<'a>(
        &'a self,
        adapter: &'a Adapter,
        spec: &'a SidecarSpec,
    ) -> BoxFuture<'a, Result<RuntimeListener, String>> {
        Box::pin(adapter.sandbox_listener(spec))
    }

    fn launch<'a>(
        &'a self,
        adapter: &'a Adapter,
        spec: &'a SidecarSpec,
        exe: &'a Path,
        cmd: &'a mut std::process::Command,
    ) -> BoxFuture<'a, Result<LaunchGuard, String>> {
        Box::pin(async move {
            let token = Secret::generate()?;
            let data_dir = adapter.sandbox_data_dir(&spec.name)?;
            for (key, value) in sandbox_bundle::launch_env(spec, exe, token.expose(), &data_dir) {
                cmd.env(key, value);
            }
            let mut guard = LaunchGuard::default();
            guard.secret = Some(token);
            Ok(guard)
        })
    }

    /// The launch becomes the current one (discover, prewarm, watch) and
    /// ends with its process.
    fn on_spawned(
        &self,
        adapter: &Adapter,
        spec: &SidecarSpec,
        url: &str,
        exe: &Path,
        guard: &mut LaunchGuard,
    ) {
        let Some(token) = guard.secret.take() else {
            return;
        };
        let seq = adapter.sandbox_spawned(spec, url, token, exe);
        let adapter = adapter.clone();
        guard.on_end(move |tail| adapter.sandbox_exited(seq, tail));
    }
}

/// A previewer bundle: never a process, served by the previewer scheme.
struct Previewer;

impl KindRuntime for Previewer {
    fn sync(&self, adapter: &Adapter, catalog: &ComponentsResponse) {
        adapter.retain_previewers(catalog);
    }

    fn sweep(&self, dirs: &SweepDirs, live: &Live, _sign_ins: &SignIns) {
        if let Some(dir) = &dirs.previewers {
            crate::internal_ext::previewer_cache::forget_bundles_except(dir, live.previewers());
        }
    }
}

/// An MCP tool: never a supervised process.
struct Mcp;

impl KindRuntime for Mcp {
    fn sync(&self, adapter: &Adapter, catalog: &ComponentsResponse) {
        adapter.sync_mcp_tools(catalog);
    }

    fn sweep(&self, dirs: &SweepDirs, live: &Live, sign_ins: &SignIns) {
        if let Some(member) = &dirs.member {
            let mcp = member.join(MCP_SETTINGS_DIR);
            forget_unlisted(&mcp, &live.mcp().settings);
            sign_ins.retain(&mcp, &live.mcp().sign_ins);
        }
    }
}

struct Inert;

impl KindRuntime for Inert {}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::gateway_client::GatewayClient;

    /// A plugin's host API grant is revoked however its launch ends: a
    /// failed or superseded spawn (the guard is dropped) or the process exit.
    #[tokio::test]
    async fn host_grant_revoked_on_every_exit_path() {
        let host = PluginHost::new(GatewayClient::default());
        let chat = vec!["chat".to_string()];

        let mut cmd = std::process::Command::new("true");
        let mut guard = LaunchGuard::default();
        grant_host(&host, "bot", &chat, &mut cmd, &mut guard)
            .await
            .expect("grant");
        assert_eq!(host.grant_count(), 1);
        drop(guard); // the spawn failed, or a restart superseded it
        assert_eq!(host.grant_count(), 0, "a launch that never ran");

        let mut guard = LaunchGuard::default();
        grant_host(&host, "bot", &chat, &mut cmd, &mut guard)
            .await
            .expect("grant");
        assert_eq!(host.grant_count(), 1);
        guard.exited("bye");
        assert_eq!(host.grant_count(), 0, "a launch whose process exited");
    }

    #[test]
    fn a_launch_ends_once_with_the_last_line() {
        let ended = Arc::new(parking_lot::Mutex::new(Vec::new()));
        let mut guard = LaunchGuard::default();
        let seen = Arc::clone(&ended);
        guard.on_end(move |tail| seen.lock().push(tail.to_string()));
        guard.exited("vm stopped");
        assert_eq!(*ended.lock(), vec!["vm stopped".to_string()]);
    }

    #[test]
    fn every_known_kind_has_a_runtime() {
        for kind in KINDS {
            let _ = runtime(kind);
        }
        assert_eq!(KINDS.len(), 5);
    }
}
