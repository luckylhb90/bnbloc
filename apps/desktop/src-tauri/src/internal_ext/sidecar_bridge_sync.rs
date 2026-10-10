use std::collections::HashMap;
use std::time::Duration;

use tauri::Manager;

use crate::internal_ext::sidecar_model::{ComponentKind, ManagedSidecar};
use crate::internal_ext::sidecar_status::manifest_fingerprint;
use crate::internal_ext::Adapter;

// The bridge resolves its core list (coreset) exactly once, at process start:
// a core that is not listening at that moment is skipped for the whole bridge
// lifetime — "skipping protocol core samplecore: connection refused" is forever.
// Managed sidecars start asynchronously (artifact download, spawn, crash
// respawn possibly on a fresh loopback port), so whichever side wins the
// start-up race, the bridge can end up running without cores that are in fact
// up. This watcher closes the gap: once the set of running sidecar URLs has
// been stable across two consecutive checks, restart the bridge so coreset
// re-discovers the live set. Restarts happen only when the URL set actually
// differs from what the running bridge was started with, so a quiet system
// never restarts and a flapping sidecar must settle first. Manifest cores are
// part of the same set, keyed by their manifest CONTENT: the bridge reads the
// manifest dir only at spawn, so a changed manifest (new ops) under an
// unchanged core name needs the same stabilized restart as an install.
const CHECK_INTERVAL: Duration = Duration::from_secs(2);

impl Adapter {
    pub(super) async fn stabilize_bridge(&self, generation: u64) {
        let mut previous: Option<HashMap<String, String>> = None;
        loop {
            tokio::time::sleep(CHECK_INTERVAL).await;
            if !self.manager_current(generation) {
                return;
            }
            let desired = self.desired_bridge_cores();
            let converged = {
                let inner = self.inner.lock();
                inner.bridge_cores == desired
            };
            if converged {
                previous = None;
                continue;
            }
            if previous.as_ref() == Some(&desired) {
                self.restart_bridge(desired);
                previous = None;
            } else {
                previous = Some(desired);
            }
        }
    }

    fn desired_bridge_cores(&self) -> HashMap<String, String> {
        let mut set = bridge_core_set(&self.inner.lock().sidecars);
        // The sandbox handoff (BNBLOC_SANDBOX_*) is bridge env read at spawn
        // too: it appearing, going away, or the mode changing restarts the
        // bridge through the same stabilized path.
        let handoff = self.sandbox_handoff();
        if let Some(fingerprint) =
            super::sandbox::bridge_fingerprint(self.sandbox_mode(), handoff.as_ref())
        {
            set.insert(super::sandbox::BRIDGE_KEY.to_string(), fingerprint);
        }
        set
    }

    fn restart_bridge(&self, desired: HashMap<String, String>) {
        let Some(state) = self.app.try_state::<crate::AppState>() else {
            return;
        };
        let settings = state.settings.lock().clone();
        let up = matches!(
            state.supervisor.status().state,
            crate::bridge::State::Starting | crate::bridge::State::Running
        );
        // Never override an explicitly stopped bridge: the user's stop wins.
        // Record the set so the watcher doesn't keep retrying; the next
        // explicit start rebuilds its env from the live sidecar state anyway.
        if up && settings.is_runnable() {
            let _ = state.supervisor.start(settings);
        }
        self.inner.lock().bridge_cores = desired;
    }
}

/// The core set the bridge env should carry: binary core sidecars with a live
/// child, keyed by their actual runtime URL (a respawn may have moved a daemon
/// to a different loopback port), plus manifest cores keyed by the content
/// fingerprint of the manifest file the bridge will read — installing,
/// removing, or changing one alters the bridge's cores, so each must trigger
/// the same stabilizing restart.
fn bridge_core_set(sidecars: &HashMap<String, ManagedSidecar>) -> HashMap<String, String> {
    sidecars
        .iter()
        .filter_map(|(name, sidecar)| {
            let spec = sidecar.spec.as_ref()?;
            if spec.kind != ComponentKind::Core {
                return None;
            }
            if !spec.binary {
                let content = spec.manifest.as_ref().map(manifest_fingerprint);
                return Some((name.clone(), content.unwrap_or_default()));
            }
            sidecar.child.as_ref()?;
            let url = sidecar.runtime_url.clone()?;
            Some((name.clone(), url))
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::sidecar_model::{AdapterStatus, SidecarSpec};

    fn manifest_core(name: &str, manifest: serde_json::Value) -> ManagedSidecar {
        let spec = SidecarSpec {
            kind: ComponentKind::Core,
            name: name.to_string(),
            version: "1.0.0".to_string(),
            sha256: String::new(),
            url: String::new(),
            manifest: Some(manifest),
            local_url: String::new(),
            listen_env: String::new(),
            supervised_env: String::new(),
            args: Vec::new(),
            binary: false,
            plugin: Default::default(),
            sandbox: Default::default(),
        };
        let mut sidecar = ManagedSidecar::pending(AdapterStatus::ready(&spec));
        sidecar.spec = Some(spec);
        sidecar
    }

    #[test]
    fn manifest_content_change_under_the_same_name_changes_the_bridge_core_set() {
        let v1 = serde_json::json!({"name": "acme", "ops": {"listSessions": {}}});
        let v2 = serde_json::json!({"name": "acme", "ops": {"listSessions": {}, "readSessionFile": {}}});
        let before = bridge_core_set(&HashMap::from([(
            "acme".to_string(),
            manifest_core("acme", v1.clone()),
        )]));
        let same = bridge_core_set(&HashMap::from([(
            "acme".to_string(),
            manifest_core("acme", v1),
        )]));
        let after = bridge_core_set(&HashMap::from([(
            "acme".to_string(),
            manifest_core("acme", v2),
        )]));

        assert_eq!(
            before, same,
            "an unchanged manifest never restarts the bridge"
        );
        assert_eq!(
            before.keys().collect::<Vec<_>>(),
            after.keys().collect::<Vec<_>>()
        );
        assert_ne!(
            before, after,
            "new manifest content under the same name must restart the bridge"
        );
        assert!(before["acme"].starts_with("manifest:sha256:"));
    }

    #[test]
    fn plugins_and_binary_cores_without_a_live_child_are_not_bridge_cores() {
        let mut plugin = manifest_core("hook", serde_json::json!({}));
        if let Some(spec) = plugin.spec.as_mut() {
            spec.kind = ComponentKind::Plugin;
        }
        let mut idle_binary = manifest_core("samplecore", serde_json::json!({}));
        if let Some(spec) = idle_binary.spec.as_mut() {
            spec.binary = true;
            spec.manifest = None;
        }
        idle_binary.runtime_url = Some("http://127.0.0.1:7190".to_string());
        let set = bridge_core_set(&HashMap::from([
            ("plugin/hook".to_string(), plugin),
            ("samplecore".to_string(), idle_binary),
        ]));
        assert!(set.is_empty(), "{set:?}");
    }

    #[test]
    fn check_interval_is_short_enough_to_converge_quickly() {
        assert!(CHECK_INTERVAL.as_secs() <= 2);
    }
}
