use std::path::Path;
use std::time::Instant;

use crate::child_process::{self, ChildEvent, RestartPolicy, STOP_GRACE};
use crate::internal_ext::core_host::{Hold, Phase};
use crate::internal_ext::kind_runtime::runtime;
use crate::internal_ext::loopback::LoopbackHttp;
use crate::internal_ext::pin::Sha256Pin;
use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::SidecarSpec;
use crate::internal_ext::sidecar_orphan::{
    owned_orphan, process_alive, process_exe, read_spawn_record, remove_spawn_record,
    terminate_orphan, write_spawn_record, SpawnRecord, CAN_TERMINATE,
};
use crate::internal_ext::Adapter;

/// How often a sidecar that follows the core host looks again when nothing
/// nudged it.
const SHARED_RECHECK: std::time::Duration = std::time::Duration::from_secs(5);

/// Consecutive checksum mismatches tolerated (a truncated or proxied
/// transfer) before the artifact is refused for this spec.
const MAX_VERIFY_FAILURES: u32 = 3;

impl Adapter {
    pub(super) fn start_known(&self, manager_generation: u64) {
        let specs = {
            let mut inner = self.inner.lock();
            inner
                .sidecars
                .values_mut()
                .filter_map(|sidecar| {
                    if sidecar.child.is_some() {
                        return None;
                    }
                    let spec = sidecar.spec.clone()?;
                    spec.artifact.is_some().then_some(spec)
                })
                .collect::<Vec<_>>()
        };
        // A sidecar its kind holds back (a plugin missing required local
        // settings) is not launched; its status says why.
        let specs = specs
            .into_iter()
            .filter_map(|spec| {
                let id = spec.id();
                if let Some(blocker) = runtime(spec.kind).blocker(self, &spec) {
                    self.set_status(&id, "waiting", blocker, None);
                    return None;
                }
                let mut inner = self.inner.lock();
                let sidecar = inner.sidecars.get_mut(&id)?;
                if sidecar.child.is_some() {
                    return None;
                }
                let sidecar_generation = claim_supervisor(sidecar)?;
                Some((spec, sidecar_generation))
            })
            .collect::<Vec<_>>();
        for (spec, sidecar_generation) in specs {
            let sup = self.clone();
            tauri::async_runtime::spawn(async move {
                sup.supervise(spec, manager_generation, sidecar_generation)
                    .await;
            });
        }
    }

    async fn supervise(&self, spec: SidecarSpec, manager_generation: u64, sidecar_generation: u64) {
        // Any exit restarts a sidecar (RestartPolicy::SIDECAR).
        let mut backoff = RestartPolicy::SIDECAR.backoff();
        let mut verify_failures = 0;
        let id = spec.id();
        loop {
            if !self.current(&id, manager_generation, sidecar_generation) {
                return;
            }
            let started = Instant::now();
            let prepared = runtime(spec.kind).prepare(self, &spec).await;
            match prepared {
                Ok(path) if runtime(spec.kind).shared() => {
                    verify_failures = 0;
                    self.follow_shared(&spec, path, manager_generation, sidecar_generation)
                        .await;
                }
                Ok(path) => {
                    verify_failures = 0;
                    // Never overlap the run this one replaces (a data dir).
                    self.stopping.stopped(&id).await;
                    if !self.current(&id, manager_generation, sidecar_generation) {
                        return;
                    }
                    self.run_binary(&spec, &path, manager_generation, sidecar_generation)
                        .await;
                }
                Err(FetchError::Refused(e)) => {
                    self.refuse(&id, sidecar_generation, e);
                    return;
                }
                Err(FetchError::Mismatch(e)) => {
                    verify_failures += 1;
                    if verify_failures >= MAX_VERIFY_FAILURES {
                        self.refuse(&id, sidecar_generation, e);
                        return;
                    }
                    self.waiting_to_install(&spec, &e);
                }
                Err(FetchError::Retry(e)) => self.waiting_to_install(&spec, &e),
            }
            if !self.current(&id, manager_generation, sidecar_generation) {
                return;
            }
            tokio::time::sleep(backoff.next(started.elapsed())).await;
        }
    }

    fn waiting_to_install(&self, spec: &SidecarSpec, error: &str) {
        eprintln!(
            "[sidecar-manager:{}] no runnable binary: {error}",
            spec.name
        );
        self.set_status(
            &spec.id(),
            "waiting",
            format!("Waiting to install {}: {error}", spec.name),
            None,
        );
    }

    /// Park this spec as refused: status failed, supervisor stays claimed (so
    /// no reconcile restarts it) until a different spec replaces it.
    fn refuse(&self, id: &str, sidecar_generation: u64, error: String) {
        eprintln!("[sidecar-manager:{id}] refusing artifact: {error}");
        let mut inner = self.inner.lock();
        if let Some(sidecar) = inner.sidecars.get_mut(id) {
            if sidecar.generation == sidecar_generation {
                sidecar.status.state = "failed".into();
                sidecar.status.message = format!("Refused {id} artifact: {error}");
                sidecar.status.pid = None;
                sidecar.refused = Some(error);
            }
        }
    }

    /// A shared sidecar (one process for every account, run by the core host)
    /// is held for this account: it follows what the host says of it until
    /// this sidecar is replaced, removed or stopped, then lets go. Another
    /// build of it running for other accounts is told as such, and this
    /// account waits for it.
    async fn follow_shared(
        &self,
        spec: &SidecarSpec,
        path: std::path::PathBuf,
        manager_generation: u64,
        sidecar_generation: u64,
    ) {
        let id = spec.id();
        let mut changes = self.cores.subscribe();
        if !self.current(&id, manager_generation, sidecar_generation) {
            return;
        }
        let hold = self.cores.hold(self.holder, sidecar_generation, spec, path);
        if let Hold::Differs { running } = hold {
            self.set_status(
                &id,
                "waiting",
                format!(
                    "{name} {running} is running on this computer for another account; {name} {version} starts when that one stops",
                    name = spec.name,
                    version = spec.version
                ),
                None,
            );
            self.lost_shared_endpoint(&id, sidecar_generation);
            // Look again when anything changes, and at least this often.
            let _ = tokio::time::timeout(SHARED_RECHECK, changes.changed()).await;
            return;
        }
        loop {
            if !self.current(&id, manager_generation, sidecar_generation) {
                self.cores
                    .release(self.holder, &id, Some(sidecar_generation));
                self.lost_shared_endpoint(&id, sidecar_generation);
                return;
            }
            let Some(view) = self.cores.view(self.holder, &id) else {
                // The host let go of it (it was stopped for an update or an
                // exit): hold it again from the top.
                self.lost_shared_endpoint(&id, sidecar_generation);
                return;
            };
            self.show_shared(&id, sidecar_generation, spec, &view);
            let _ = tokio::time::timeout(SHARED_RECHECK, changes.changed()).await;
        }
    }

    /// What the host says of a shared sidecar is what this account shows.
    fn show_shared(
        &self,
        id: &str,
        sidecar_generation: u64,
        spec: &SidecarSpec,
        view: &crate::internal_ext::core_host::RunView,
    ) {
        let mut inner = self.inner.lock();
        if let Some(sidecar) = inner
            .sidecars
            .get_mut(id)
            .filter(|s| s.generation == sidecar_generation)
        {
            sidecar.runtime_url = view.url.clone();
            sidecar.status.state = match view.phase {
                Phase::Starting => "starting",
                Phase::Running => "running",
                Phase::Waiting => "waiting",
            }
            .into();
            sidecar.status.message = view.message.clone();
            sidecar.status.pid = view.pid;
            sidecar.status.version = Some(spec.version.clone());
        }
    }

    /// A shared sidecar this account no longer holds answers nowhere for it.
    fn lost_shared_endpoint(&self, id: &str, sidecar_generation: u64) {
        let mut inner = self.inner.lock();
        if let Some(sidecar) = inner
            .sidecars
            .get_mut(id)
            .filter(|s| s.generation == sidecar_generation)
        {
            sidecar.runtime_url = None;
            sidecar.status.pid = None;
        }
    }

    /// Run the verified binary once and return when it exits (a sidecar of
    /// this account's own: on a port the desktop picks).
    async fn run_binary(
        &self,
        spec: &SidecarSpec,
        path: &Path,
        manager_generation: u64,
        sidecar_generation: u64,
    ) {
        let id = spec.id();
        let listener = runtime(spec.kind).listener(self, spec).await;
        match listener {
            Ok(listener) => {
                if let Err(error) = self
                    .spawn_once(
                        spec,
                        &listener.url,
                        listener.listener,
                        manager_generation,
                        sidecar_generation,
                        path,
                    )
                    .await
                {
                    eprintln!("[sidecar-manager:{}] spawn error: {error}", spec.name);
                    self.set_status(
                        &id,
                        "waiting",
                        format!("Waiting to start {}: {error}", spec.name),
                        None,
                    );
                }
            }
            Err(error) => self.set_status(
                &id,
                "waiting",
                format!("Waiting to start {}: {error}", spec.name),
                None,
            ),
        }
    }

    /// Stop the process of `spec` an earlier app session left running (its
    /// spawn record names it, that session's app is gone, and its executable
    /// is under `root`): it would hold the sidecar's data dir. Best-effort.
    pub(super) async fn replace_recorded_orphan(
        &self,
        spec: &SidecarSpec,
        root: std::path::PathBuf,
        what: &'static str,
    ) {
        let Some(record_dir) = self.spawn_record_dir() else {
            return;
        };
        let id = spec.id();
        let orphan = {
            let id = id.clone();
            tauri::async_runtime::spawn_blocking(move || {
                let record = read_spawn_record(&record_dir, &id)?;
                owned_orphan(
                    record.pid,
                    Some(&record),
                    process_exe(record.pid).as_deref(),
                    &root,
                    std::process::id(),
                    process_alive,
                )
            })
            .await
            .ok()
            .flatten()
        };
        let Some(pid) = orphan.map(|o| o.pid) else {
            return;
        };
        if !CAN_TERMINATE {
            return;
        }
        eprintln!(
            "[{what}] stopping {} left running by an earlier session (pid {pid})",
            spec.name
        );
        let stopped =
            tauri::async_runtime::spawn_blocking(move || terminate_orphan(pid, STOP_GRACE))
                .await
                .unwrap_or_else(|e| Err(format!("stop task: {e}")));
        match stopped {
            Ok(()) => self.forget_spawn(&id, pid),
            Err(e) => eprintln!("[{what}] could not stop pid {pid}: {e}"),
        }
    }

    /// The spawn records of this account's own sidecars: each account has a
    /// directory of its own, so two accounts' plugins never share a record
    /// (the cores' are the core host's).
    pub(super) fn spawn_record_dir(&self) -> Option<std::path::PathBuf> {
        self.dirs()
            .ok()
            .map(|dirs| dirs.spawn_records().join(&self.scope))
    }

    fn record_spawn(&self, id: &str, pid: u32, sha256: Option<&Sha256Pin>) {
        let (Some(dir), Some(sha256)) = (self.spawn_record_dir(), sha256) else {
            return;
        };
        let record = SpawnRecord {
            pid,
            sha256: sha256.clone(),
            app_pid: std::process::id(),
        };
        if let Err(error) = write_spawn_record(&dir, id, &record) {
            eprintln!("[sidecar-manager:{id}] {error}");
        }
    }

    /// Drop `id`'s spawn record once child `pid` is gone (exit or clean stop).
    pub(super) fn forget_spawn(&self, id: &str, pid: u32) {
        if let Some(dir) = self.spawn_record_dir() {
            remove_spawn_record(&dir, id, pid);
        }
    }

    async fn spawn_once(
        &self,
        spec: &SidecarSpec,
        runtime_url: &str,
        mut listener: Option<std::net::TcpListener>,
        manager_generation: u64,
        sidecar_generation: u64,
        path: &Path,
    ) -> Result<(), String> {
        let mut cmd = std::process::Command::new(path);
        let id = spec.id();
        if !spec.supervised_env.trim().is_empty() {
            cmd.env(&spec.supervised_env, "1");
        }
        if !spec.listen_env.trim().is_empty() {
            cmd.env(
                &spec.listen_env,
                LoopbackHttp::parse(runtime_url)?.authority(),
            );
        }
        cmd.args(&spec.args);
        let kind = runtime(spec.kind);
        // Released however this launch ends: a failed spawn, a stale one,
        // the exit (kind_runtime::LaunchGuard).
        let mut guard = kind.launch(self, spec, path, &mut cmd).await?;
        let shell_env = crate::shell_env::current().await;
        drop(listener.take());
        let (mut rx, child) =
            child_process::spawn(cmd, &shell_env).map_err(|e| format!("spawn sidecar: {e}"))?;
        let pid = child.pid();
        {
            let mut inner = self.inner.lock();
            let manager_current = inner.generation == manager_generation;
            let stale = match inner.sidecars.get_mut(&id) {
                Some(sidecar) if manager_current && sidecar.generation == sidecar_generation => {
                    sidecar.status.state = "running".into();
                    sidecar.runtime_url = Some(runtime_url.to_string());
                    sidecar.status.message = format!("{} is running at {runtime_url}", spec.name);
                    sidecar.status.pid = Some(pid);
                    sidecar.status.version = Some(spec.version.clone());
                    sidecar.child = Some(child);
                    None
                }
                _ => Some(child),
            };
            drop(inner);
            if let Some(child) = stale {
                self.stopping.stop(&id, vec![child]);
                return Ok(());
            }
        }
        self.record_spawn(&id, pid, spec.pin());
        kind.on_spawned(self, spec, runtime_url, path, &mut guard);
        let mut tail = String::new();
        let mut exit_code = None;
        while let Some(event) = rx.recv().await {
            match event {
                ChildEvent::Line(line) => {
                    let trimmed = line.trim();
                    tail = trimmed.to_string();
                    eprintln!("[sidecar-manager:{}] {trimmed}", spec.name);
                }
                ChildEvent::Exited(code) => {
                    exit_code = code;
                    break;
                }
            }
        }
        self.forget_spawn(&id, pid);
        guard.exited(&tail);
        self.clear_child(
            &id,
            manager_generation,
            sidecar_generation,
            exit_code,
            &tail,
        );
        Ok(())
    }

    fn current(&self, name: &str, manager_generation: u64, sidecar_generation: u64) -> bool {
        let inner = self.inner.lock();
        inner.desired_up
            && inner.generation == manager_generation
            && inner
                .sidecars
                .get(name)
                .map(|sidecar| sidecar.generation == sidecar_generation)
                .unwrap_or(false)
    }

    fn clear_child(
        &self,
        name: &str,
        manager_generation: u64,
        sidecar_generation: u64,
        exit_code: Option<i32>,
        tail: &str,
    ) {
        let mut inner = self.inner.lock();
        if inner.generation != manager_generation {
            return;
        }
        if let Some(sidecar) = inner.sidecars.get_mut(name) {
            if sidecar.generation == sidecar_generation {
                sidecar.child = None;
                sidecar.runtime_url = None;
                sidecar.status.state = "waiting".into();
                let detail = if tail.trim().is_empty() {
                    exit_code
                        .map(|code| format!("exit {code}"))
                        .unwrap_or_else(|| "no exit code".to_string())
                } else {
                    tail.trim().to_string()
                };
                sidecar.status.message = format!("{name} exited ({detail}); restarting soon");
                sidecar.status.pid = None;
            }
        }
    }
}

fn claim_supervisor(
    sidecar: &mut crate::internal_ext::sidecar_model::ManagedSidecar,
) -> Option<u64> {
    if sidecar.supervisor_active || sidecar.refused.is_some() {
        return None;
    }
    sidecar.generation += 1;
    sidecar.supervisor_active = true;
    Some(sidecar.generation)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::sidecar_model::{AdapterStatus, ManagedSidecar};

    #[test]
    fn repeated_start_claims_only_one_supervisor_generation() {
        let status = AdapterStatus::new(
            crate::internal_ext::sidecar_model::ComponentKind::Core,
            "samplecore",
            "ready",
            "Ready".into(),
            None,
            None,
        );
        let mut sidecar = ManagedSidecar::pending(status);
        let first_generation =
            claim_supervisor(&mut sidecar).expect("first start claims supervisor");

        assert!(
            claim_supervisor(&mut sidecar).is_none(),
            "reconcile cannot start a duplicate supervisor"
        );
        sidecar.supervisor_active = false;
        let restarted_generation =
            claim_supervisor(&mut sidecar).expect("restart claims released supervisor");
        assert!(restarted_generation > first_generation);
    }

    #[test]
    fn refused_spec_is_never_reclaimed_until_replaced() {
        let status = AdapterStatus::new(
            crate::internal_ext::sidecar_model::ComponentKind::Core,
            "samplecore",
            "failed",
            "Refused".into(),
            None,
            None,
        );
        let mut sidecar = ManagedSidecar::pending(status);
        sidecar.refused = Some("sha256 mismatch".into());
        assert!(
            claim_supervisor(&mut sidecar).is_none(),
            "a refused artifact must not be re-downloaded by a reconcile/start"
        );
        sidecar.refused = None; // a new spec from the catalog clears it
        assert!(claim_supervisor(&mut sidecar).is_some());
    }
}
