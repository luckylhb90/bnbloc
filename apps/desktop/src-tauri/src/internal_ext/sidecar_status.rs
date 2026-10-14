use crate::internal_ext::sidecar_model::{
    AdapterStatus, ComponentKind, ManagedSidecar, PendingUpdate, SidecarSpec,
};
use crate::internal_ext::Adapter;

impl Adapter {
    pub fn status(&self) -> Vec<AdapterStatus> {
        // Read once, outside the lock: what a waiting update waits for.
        let running = self.running_turns();
        let mut statuses: Vec<AdapterStatus> = self
            .inner
            .lock()
            .sidecars
            .values()
            .map(|sidecar| status_for(sidecar, running))
            .collect();
        statuses.sort_by(|a, b| {
            a.name
                .cmp(&b.name)
                .then_with(|| kind_rank(a.kind).cmp(&kind_rank(b.kind)))
        });
        statuses
    }

    pub(super) async fn write_manifests(&self, specs: &[SidecarSpec]) -> Result<(), String> {
        let Some(dir) = self.manifest_dir_path() else {
            return Ok(());
        };
        let specs = specs.to_vec();
        tauri::async_runtime::spawn_blocking(move || write_manifest_files(&dir, &specs))
            .await
            .map_err(|e| format!("write core manifests: {e}"))?
    }

    pub(super) fn set_initial_status(
        &self,
        kind: ComponentKind,
        name: &str,
        state: &str,
        message: String,
    ) {
        let mut inner = self.inner.lock();
        let status = AdapterStatus::new(kind, name, state, message, None, None);
        inner
            .sidecars
            .entry(component_id(kind, name))
            .or_insert_with(|| ManagedSidecar::pending(status.clone()))
            .status = status.clone();
    }

    pub(super) fn set_status(&self, name: &str, state: &str, message: String, pid: Option<u32>) {
        let mut inner = self.inner.lock();
        if let Some(sidecar) = inner.sidecars.get_mut(name) {
            sidecar.status.state = state.into();
            sidecar.status.message = message;
            sidecar.status.pid = pid;
        }
    }

    pub(super) fn manifest_dir(&self) -> Option<String> {
        self.manifest_dir_path()
            .map(|dir| dir.to_string_lossy().into_owned())
    }

    fn manifest_dir_path(&self) -> Option<std::path::PathBuf> {
        self.dirs().ok().map(crate::dirs::Dirs::manifests)
    }
}

/// Reconcile the manifest dir to exactly the current manifest cores. The
/// bridge may read this dir at any moment (its start), so the dir is never
/// emptied and rewritten: each manifest is replaced atomically
/// (files::atomic_write), unchanged ones are left alone, and only manifests
/// of cores no longer installed are removed.
fn write_manifest_files(dir: &std::path::Path, specs: &[SidecarSpec]) -> Result<(), String> {
    std::fs::create_dir_all(dir).map_err(|e| format!("create core manifests dir: {e}"))?;
    let mut wanted = std::collections::HashSet::new();
    for spec in specs {
        if spec.kind != ComponentKind::Core {
            continue;
        }
        let Some(manifest) = &spec.manifest else {
            continue;
        };
        let file = format!("{}.json", spec.name);
        let path = dir.join(&file);
        wanted.insert(file);
        let raw = manifest_file_bytes(manifest)
            .map_err(|e| format!("encode {} manifest: {e}", spec.name))?;
        if std::fs::read(&path).is_ok_and(|current| current == raw) {
            continue;
        }
        crate::files::atomic_write(&path, &raw)?;
    }
    for entry in std::fs::read_dir(dir).map_err(|e| format!("read core manifests dir: {e}"))? {
        let path = entry
            .map_err(|e| format!("read core manifests entry: {e}"))?
            .path();
        let Some(name) = path.file_name().and_then(|n| n.to_str()) else {
            continue;
        };
        if path.extension().and_then(|ext| ext.to_str()) == Some("json") && !wanted.contains(name) {
            std::fs::remove_file(&path).map_err(|e| format!("remove stale manifest: {e}"))?;
        }
    }
    Ok(())
}

/// The exact bytes written to `<dir>/<name>.json` for a manifest core.
fn manifest_file_bytes(manifest: &serde_json::Value) -> Result<Vec<u8>, serde_json::Error> {
    serde_json::to_vec_pretty(manifest)
}

/// Content identity of a manifest core as the bridge reads it from disk: the
/// sha256 of the written file bytes. The bridge-stabilizing watcher compares
/// it, so a manifest whose content changes under the same core name (new ops,
/// changed discovery) restarts the bridge like an install/uninstall does —
/// the bridge reads manifests only at spawn.
pub(super) fn manifest_fingerprint(manifest: &serde_json::Value) -> String {
    use sha2::{Digest, Sha256};
    let bytes = manifest_file_bytes(manifest).unwrap_or_default();
    format!(
        "manifest:sha256:{}",
        crate::internal_ext::sidecar_util::hex_lower(&Sha256::digest(&bytes))
    )
}

pub(super) fn component_id(kind: ComponentKind, name: &str) -> String {
    match kind {
        ComponentKind::Core => name.to_string(),
        ComponentKind::Plugin => format!("plugin/{name}"),
        ComponentKind::Sandbox => format!("sandbox/{name}"),
        ComponentKind::Previewer => format!("previewer/{name}"),
        ComponentKind::Mcp => format!("mcp/{name}"),
        ComponentKind::Unknown => format!("unknown/{name}"),
    }
}

fn kind_rank(kind: ComponentKind) -> u8 {
    match kind {
        ComponentKind::Core => 0,
        ComponentKind::Plugin => 1,
        ComponentKind::Sandbox => 2,
        ComponentKind::Previewer => 3,
        ComponentKind::Mcp => 4,
        ComponentKind::Unknown => 5,
    }
}

#[cfg(test)]
pub(super) fn status_for_test(sidecar: &ManagedSidecar, running: u32) -> AdapterStatus {
    status_for(sidecar, running)
}

/// A sidecar's status as the web reads it: its endpoint, and the update
/// that waits for the bridge's `running` turns (update_gate) while it keeps
/// running.
fn status_for(sidecar: &ManagedSidecar, running: u32) -> AdapterStatus {
    let mut status = sidecar.status.clone();
    status.local_url = sidecar
        .runtime_url
        .clone()
        .or_else(|| sidecar.spec.as_ref().map(|spec| spec.local_url.clone()));
    status.pending_update = sidecar.pending.as_ref().map(|spec| PendingUpdate {
        version: spec.version.clone(),
        waiting_for: running,
    });
    status
}

#[cfg(test)]
mod tests {
    use super::*;

    fn manifest_spec(name: &str) -> SidecarSpec {
        SidecarSpec {
            kind: ComponentKind::Core,
            name: name.to_string(),
            version: "1.0.0".to_string(),
            artifact: None,
            manifest: Some(serde_json::json!({"name": name})),
            local_url: String::new(),
            listen_env: String::new(),
            supervised_env: String::new(),
            args: Vec::new(),
            plugin: Default::default(),
            sandbox: Default::default(),
        }
    }

    #[test]
    fn write_manifest_files_removes_stale_uninstalled_manifest() {
        let dir = std::env::temp_dir().join(format!("bnbloc-manifest-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("Given a manifest cache directory");
        std::fs::write(dir.join("old.json"), b"{}").expect("Given a stale manifest file");

        write_manifest_files(&dir, &[manifest_spec("current")])
            .expect("When manifest files are reconciled");

        assert!(!dir.join("old.json").exists());
        assert!(dir.join("current.json").exists());
        std::fs::remove_dir_all(&dir).expect("cleanup manifest test dir");
    }

    #[test]
    fn write_manifest_files_keeps_unchanged_manifests_in_place() {
        let dir =
            std::env::temp_dir().join(format!("bnbloc-manifest-stable-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let specs = [manifest_spec("stable"), manifest_spec("other")];
        write_manifest_files(&dir, &specs).expect("first write");
        let before = std::fs::metadata(dir.join("stable.json"))
            .and_then(|m| m.modified())
            .expect("mtime");
        std::thread::sleep(std::time::Duration::from_millis(20));

        // A refresh with the same catalog does not touch the files (the
        // bridge can never observe a missing manifest), and leaves no temps.
        write_manifest_files(&dir, &specs).expect("second write");
        let after = std::fs::metadata(dir.join("stable.json"))
            .and_then(|m| m.modified())
            .expect("mtime");
        assert_eq!(before, after);
        let names: Vec<_> = std::fs::read_dir(&dir)
            .expect("list")
            .map(|e| e.expect("entry").file_name().into_string().expect("utf8"))
            .collect();
        assert!(names.iter().all(|n| !n.ends_with(".tmp")), "{names:?}");
        assert_eq!(names.len(), 2);
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }
}
