//! The core daemons of this computer, shared by every account
//! (docs/prd/2026-10-22-multi-account.md §2.4).
//!
//! A core is a daemon on the package's fixed `localUrl` that every bridge
//! speaks the Core Protocol to; two processes of one core cannot share that
//! address, and a person's CLI logins and state are the machine's. So a core
//! runs **once per computer**, keyed by its name and the digest of the build,
//! and every account whose catalog pins that digest holds it:
//!
//! - the first holder starts it, the last one's release stops it; a holder
//!   that leaves (an account removed) never stops it for the others, and so
//!   never cuts their turns;
//! - an account whose catalog pins ANOTHER digest of a running core is told
//!   so (`Hold::Differs`, "the core version differs") and waits; it is never
//!   served the other build in silence. A holder that is alone may replace
//!   the build (its own update);
//! - the process is supervised here, not by any account's adapter: its restart
//!   backoff, a listener the app did not start (reused in place when it
//!   speaks the Core Protocol), an orphan of an earlier session (replaced).
//!
//! Plugins and the sandbox are not shared: an account's own adapter runs
//! them on ports the desktop picks.
//!
//! The host also knows every account's running turns (`running_turns`): a core
//! update restarts what every account's turns run on, so the update gate waits
//! for all of them (update_gate).
use std::collections::{BTreeMap, HashMap};
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

use parking_lot::Mutex;
use tauri::async_runtime::JoinHandle;
use tokio::sync::watch;

use crate::child_process::{self, Child, ChildEvent, RestartPolicy, Stopping, STOP_GRACE};
use crate::dirs::Dirs;
use crate::internal_ext::kind_runtime::runtime;
use crate::internal_ext::loopback::LoopbackHttp;
use crate::internal_ext::pin::Sha256Pin;
use crate::internal_ext::sidecar_model::SidecarSpec;
use crate::internal_ext::sidecar_orphan::{
    listener_port, listening_pid, owned_orphan, plan_occupied_listener, process_alive, process_exe,
    read_spawn_record, remove_spawn_record, terminate_orphan, write_spawn_record, ListenerPlan,
    OccupiedListener, Orphan, SpawnRecord, CAN_TERMINATE,
};
use crate::internal_ext::sidecar_util::{
    cache_stem, probe_core_protocol_listener, runtime_listener_for,
};

/// The directory (under the spawn records) of the cores' records: a core is
/// the machine's, not an account's.
const RECORDS_DIR: &str = "cores";

/// Who holds a core: an account's adapter.
pub(crate) type HolderId = u64;

/// An account's running turns (its bridge's), read when asked.
pub(crate) type TurnsOf = Arc<dyn Fn() -> u32 + Send + Sync>;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Phase {
    Starting,
    Running,
    /// Not running now (it exited, its listener is taken, it could not start);
    /// the message says why and a restart is coming.
    Waiting,
}

/// What a holder sees of a core.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct RunView {
    pub(super) phase: Phase,
    pub(super) message: String,
    /// Where it answers while it runs.
    pub(super) url: Option<String>,
    pub(super) pid: Option<u32>,
}

/// What asking to hold a core comes to.
#[derive(Debug, PartialEq, Eq)]
pub(super) enum Hold {
    /// The caller holds it (and it runs, or is starting).
    Joined,
    /// Another build of this core runs for other accounts: the caller waits.
    Differs { running: String },
}

struct Run {
    spec: SidecarSpec,
    pin: Sha256Pin,
    exe: PathBuf,
    /// Each holder, with the supervisor generation of the sidecar that holds
    /// (a release of an older generation never drops a newer hold).
    holders: BTreeMap<HolderId, u64>,
    generation: u64,
    view: RunView,
    child: Option<Child>,
    task: Option<JoinHandle<()>>,
}

#[derive(Default)]
struct Inner {
    runs: HashMap<String, Run>,
    turns: HashMap<HolderId, TurnsOf>,
}

pub(crate) struct CoreHost {
    inner: Mutex<Inner>,
    dirs: Result<Dirs, String>,
    stopping: Stopping,
    next_holder: AtomicU64,
    /// Every run gets a generation of its own across the whole host: a task
    /// that outlived its run can never match a run created after it.
    next_generation: AtomicU64,
    changed: watch::Sender<u64>,
}

impl CoreHost {
    pub(crate) fn new(dirs: Result<Dirs, String>) -> Arc<Self> {
        Arc::new(Self {
            inner: Mutex::new(Inner::default()),
            dirs,
            stopping: Stopping::default(),
            next_holder: AtomicU64::new(1),
            next_generation: AtomicU64::new(1),
            changed: watch::channel(0).0,
        })
    }

    /// A new holder (an account's adapter).
    pub(crate) fn register(&self) -> HolderId {
        self.next_holder.fetch_add(1, Ordering::SeqCst)
    }

    /// How the holder's running turns are read (the update gate sums them).
    pub(crate) fn set_turns(&self, holder: HolderId, turns: TurnsOf) {
        self.inner.lock().turns.insert(holder, turns);
    }

    /// The holder is gone: it holds no core and counts no turns.
    pub(crate) fn unregister(&self, holder: HolderId) {
        let ids: Vec<String> = {
            let mut inner = self.inner.lock();
            inner.turns.remove(&holder);
            inner
                .runs
                .iter()
                .filter(|(_, run)| run.holders.contains_key(&holder))
                .map(|(id, _)| id.clone())
                .collect()
        };
        for id in ids {
            self.release(holder, &id, None);
        }
    }

    /// The builds the running cores run from, as `(cache stem, sha256)`: the
    /// binary cache keeps them whatever a catalog pins.
    pub(crate) fn held_builds(&self) -> Vec<(String, String)> {
        self.inner
            .lock()
            .runs
            .iter()
            .map(|(id, run)| (cache_stem(id), run.pin.as_str().to_string()))
            .collect()
    }

    /// The agent turns every account's bridge drives now.
    pub(crate) fn running_turns(&self) -> u32 {
        // Read outside the lock: a reader looks at files and the bridge.
        let readers: Vec<TurnsOf> = self.inner.lock().turns.values().cloned().collect();
        readers.iter().map(|turns| turns()).sum()
    }

    /// Something changed: every follower looks again.
    pub(crate) fn nudge(&self) {
        self.changed.send_modify(|n| *n += 1);
    }

    pub(super) fn subscribe(&self) -> watch::Receiver<u64> {
        self.changed.subscribe()
    }

    /// Hold the core `spec` names, started from `exe`, for `holder` as its
    /// sidecar of supervisor generation `generation`.
    pub(super) fn hold(
        self: &Arc<Self>,
        holder: HolderId,
        generation: u64,
        spec: &SidecarSpec,
        exe: PathBuf,
    ) -> Hold {
        let Some(pin) = spec.pin().cloned() else {
            return Hold::Differs {
                running: "no artifact".into(),
            };
        };
        let id = spec.id();
        let mut stale = None;
        let outcome = {
            let mut inner = self.inner.lock();
            match inner.runs.get_mut(&id) {
                Some(run) if same_launch(&run.spec, spec) => {
                    let held = run.holders.entry(holder).or_insert(generation);
                    *held = (*held).max(generation);
                    Hold::Joined
                }
                Some(run) if run.holders.keys().all(|h| *h == holder) => {
                    // The only holder changes the build or how it launches:
                    // its own update.
                    stale = Some((run.child.take(), run.task.take()));
                    run.generation = self.next_generation.fetch_add(1, Ordering::SeqCst);
                    run.spec = spec.clone();
                    run.pin = pin;
                    run.exe = exe;
                    run.holders = BTreeMap::from([(holder, generation)]);
                    run.view = starting(spec);
                    self.start_task(&id, run);
                    Hold::Joined
                }
                Some(run) => Hold::Differs {
                    running: run.spec.version.clone(),
                },
                None => {
                    let mut run = Run {
                        spec: spec.clone(),
                        pin,
                        exe,
                        holders: BTreeMap::from([(holder, generation)]),
                        generation: self.next_generation.fetch_add(1, Ordering::SeqCst),
                        view: starting(spec),
                        child: None,
                        task: None,
                    };
                    self.start_task(&id, &mut run);
                    inner.runs.insert(id.clone(), run);
                    Hold::Joined
                }
            }
        };
        if let Some((child, task)) = stale {
            if let Some(task) = task {
                task.abort();
            }
            if let Some(child) = child {
                self.forget_spawn(&id, child.pid());
                self.stopping.stop(&id, vec![child]);
            }
        }
        self.nudge();
        outcome
    }

    /// The holder lets go of the core `id` (its sidecar is gone, replaced or
    /// stopped): the last one's release stops it. `generation` limits it to
    /// the hold of that supervisor generation (None: whichever the holder has).
    pub(super) fn release(&self, holder: HolderId, id: &str, generation: Option<u64>) {
        let stopped = {
            let mut inner = self.inner.lock();
            let Some(run) = inner.runs.get_mut(id) else {
                return;
            };
            match (run.holders.get(&holder), generation) {
                (None, _) => return,
                (Some(held), Some(generation)) if *held != generation => return,
                _ => {}
            }
            run.holders.remove(&holder);
            if !run.holders.is_empty() {
                None
            } else {
                inner.runs.remove(id)
            }
        };
        if let Some(mut run) = stopped {
            if let Some(task) = run.task.take() {
                task.abort();
            }
            if let Some(child) = run.child.take() {
                self.forget_spawn(id, child.pid());
                self.stopping.stop(id, vec![child]);
            }
        }
        self.nudge();
    }

    /// What `holder` sees of the core `id`: None when it does not hold it.
    pub(super) fn view(&self, holder: HolderId, id: &str) -> Option<RunView> {
        let inner = self.inner.lock();
        let run = inner.runs.get(id)?;
        run.holders.contains_key(&holder).then(|| run.view.clone())
    }

    /// Stop supervising every core and hand back their children, for one
    /// joint stop (app exit, an update install). Holders find their cores
    /// gone and hold them again when they resume.
    pub(crate) fn detach_all(&self) -> Vec<Child> {
        let runs: Vec<(String, Run)> = self.inner.lock().runs.drain().collect();
        let mut children = Vec::new();
        for (id, mut run) in runs {
            if let Some(task) = run.task.take() {
                task.abort();
            }
            if let Some(child) = run.child.take() {
                // A clean stop: not an orphan for the next session.
                self.forget_spawn(&id, child.pid());
                children.push(child);
            }
        }
        self.nudge();
        children
    }

    fn start_task(self: &Arc<Self>, id: &str, run: &mut Run) {
        let host = Arc::clone(self);
        let (id, generation) = (id.to_string(), run.generation);
        let (spec, exe) = (run.spec.clone(), run.exe.clone());
        run.task = Some(tauri::async_runtime::spawn(async move {
            host.supervise(id, generation, spec, exe).await;
        }));
    }

    fn current(&self, id: &str, generation: u64) -> bool {
        self.inner
            .lock()
            .runs
            .get(id)
            .is_some_and(|run| run.generation == generation)
    }

    /// Change what the core shows its holders (only while `generation` is the
    /// run's).
    fn set_view(&self, id: &str, generation: u64, edit: impl FnOnce(&mut Run)) {
        {
            let mut inner = self.inner.lock();
            match inner.runs.get_mut(id) {
                Some(run) if run.generation == generation => edit(run),
                _ => return,
            }
        }
        self.nudge();
    }

    fn waiting(&self, id: &str, generation: u64, message: String) {
        self.set_view(id, generation, |run| {
            run.view = RunView {
                phase: Phase::Waiting,
                message,
                url: None,
                pid: None,
            };
        });
    }

    /// The supervise loop of one core: run it, and run it again after any
    /// exit (RestartPolicy::SIDECAR) until it is released or replaced.
    async fn supervise(
        self: Arc<Self>,
        id: String,
        generation: u64,
        spec: SidecarSpec,
        exe: PathBuf,
    ) {
        let mut backoff = RestartPolicy::SIDECAR.backoff();
        loop {
            // Never overlap the run this one replaces (a fixed port).
            self.stopping.stopped(&id).await;
            if !self.current(&id, generation) {
                return;
            }
            let started = Instant::now();
            self.run_binary(&id, generation, &spec, &exe).await;
            if !self.current(&id, generation) {
                return;
            }
            tokio::time::sleep(backoff.next(started.elapsed())).await;
        }
    }

    /// Run the verified binary once (or reuse/replace whatever already
    /// listens on its fixed port) and return when it exits.
    async fn run_binary(
        &self,
        id: &str,
        generation: u64,
        spec: &SidecarSpec,
        exe: &std::path::Path,
    ) {
        match runtime_listener_for(&spec.local_url) {
            Ok(listener) => {
                if let Err(error) = self
                    .spawn_once(id, generation, spec, &listener.url, listener.listener, exe)
                    .await
                {
                    eprintln!("[core-host:{}] spawn error: {error}", spec.name);
                    self.waiting(
                        id,
                        generation,
                        format!("Waiting to start {}: {error}", spec.name),
                    );
                }
            }
            Err(error) if error.contains("already in use") => {
                self.handle_occupied(id, generation, spec, &error).await
            }
            Err(error) => self.waiting(
                id,
                generation,
                format!("Waiting to start {}: {error}", spec.name),
            ),
        }
    }

    /// The fixed port is taken: replace an orphan this app spawned in an
    /// earlier session, reuse a compatible foreign Core Protocol listener, or
    /// wait (sidecar_orphan). A replaced orphan's port frees up for the
    /// supervise loop's next attempt.
    async fn handle_occupied(
        &self,
        id: &str,
        generation: u64,
        spec: &SidecarSpec,
        bind_error: &str,
    ) {
        let Some(pin) = spec.pin() else {
            return;
        };
        let orphan = self.find_orphan(id, spec).await;
        let probe = if runtime(spec.kind).reuses_listener(spec) {
            Some(probe_core_protocol_listener(&spec.local_url, &spec.name).await)
        } else {
            None
        };
        let plan = plan_occupied_listener(OccupiedListener {
            name: &spec.name,
            spec_version: &spec.version,
            spec_sha256: pin,
            url: &spec.local_url,
            bind_error,
            orphan,
            probe,
            can_terminate: CAN_TERMINATE,
        });
        match plan {
            ListenerPlan::Reuse => {
                self.set_view(id, generation, |run| {
                    run.view = RunView {
                        phase: Phase::Running,
                        message: format!(
                            "{} is already responding at {}",
                            spec.name, spec.local_url
                        ),
                        url: Some(spec.local_url.clone()),
                        pid: None,
                    };
                });
                eprintln!(
                    "[core-host:{}] reusing existing Core Protocol listener",
                    spec.name
                );
            }
            ListenerPlan::Wait(message) => self.waiting(id, generation, message),
            ListenerPlan::Replace { pid, message } => {
                eprintln!("[core-host:{}] {message} (pid {pid})", spec.name);
                self.waiting(id, generation, message);
                let stopped =
                    tauri::async_runtime::spawn_blocking(move || terminate_orphan(pid, STOP_GRACE))
                        .await
                        .unwrap_or_else(|e| Err(format!("stop task: {e}")));
                match stopped {
                    Ok(()) => self.forget_spawn(id, pid),
                    Err(error) => {
                        eprintln!(
                            "[core-host:{}] could not stop pid {pid}: {error}",
                            spec.name
                        );
                        self.waiting(
                            id,
                            generation,
                            format!(
                                "{} from an earlier session is still running on {} (pid {pid}: {error}); quit it to start {} {}",
                                spec.name, spec.local_url, spec.name, spec.version
                            ),
                        );
                    }
                }
            }
        }
    }

    /// The orphan from an earlier session listening on `spec`'s port, if this
    /// app spawned it. Best-effort: lsof/ps run off the async runtime.
    async fn find_orphan(&self, id: &str, spec: &SidecarSpec) -> Option<Orphan> {
        let port = listener_port(&spec.local_url)?;
        let cache_dir = self.dirs.as_ref().ok()?.binaries();
        let record_dir = self.record_dir()?;
        let id = id.to_string();
        tauri::async_runtime::spawn_blocking(move || {
            let pid = listening_pid(port)?;
            owned_orphan(
                pid,
                read_spawn_record(&record_dir, &id).as_ref(),
                process_exe(pid).as_deref(),
                &cache_dir,
                std::process::id(),
                process_alive,
            )
        })
        .await
        .ok()
        .flatten()
    }

    fn record_dir(&self) -> Option<PathBuf> {
        self.dirs
            .as_ref()
            .ok()
            .map(|dirs| dirs.spawn_records().join(RECORDS_DIR))
    }

    fn record_spawn(&self, id: &str, pid: u32, pin: &Sha256Pin) {
        let Some(dir) = self.record_dir() else { return };
        let record = SpawnRecord {
            pid,
            sha256: pin.clone(),
            app_pid: std::process::id(),
        };
        if let Err(error) = write_spawn_record(&dir, id, &record) {
            eprintln!("[core-host:{id}] {error}");
        }
    }

    /// Drop `id`'s spawn record once child `pid` is gone (exit or clean stop).
    fn forget_spawn(&self, id: &str, pid: u32) {
        if let Some(dir) = self.record_dir() {
            remove_spawn_record(&dir, id, pid);
        }
    }

    async fn spawn_once(
        &self,
        id: &str,
        generation: u64,
        spec: &SidecarSpec,
        runtime_url: &str,
        mut listener: Option<std::net::TcpListener>,
        exe: &std::path::Path,
    ) -> Result<(), String> {
        let mut cmd = std::process::Command::new(exe);
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
        let shell_env = crate::shell_env::current().await;
        drop(listener.take());
        let (mut rx, child) =
            child_process::spawn(cmd, &shell_env).map_err(|e| format!("spawn sidecar: {e}"))?;
        let pid = child.pid();
        let pin = spec.pin().cloned();
        let stale = {
            let mut inner = self.inner.lock();
            match inner.runs.get_mut(id) {
                Some(run) if run.generation == generation => {
                    run.view = RunView {
                        phase: Phase::Running,
                        message: format!("{} is running at {runtime_url}", spec.name),
                        url: Some(runtime_url.to_string()),
                        pid: Some(pid),
                    };
                    run.child = Some(child);
                    None
                }
                _ => Some(child),
            }
        };
        if let Some(child) = stale {
            self.stopping.stop(id, vec![child]);
            return Ok(());
        }
        self.nudge();
        if let Some(pin) = &pin {
            self.record_spawn(id, pid, pin);
        }
        let mut tail = String::new();
        let mut exit_code = None;
        while let Some(event) = rx.recv().await {
            match event {
                ChildEvent::Line(line) => {
                    let trimmed = line.trim();
                    tail = trimmed.to_string();
                    eprintln!("[core-host:{}] {trimmed}", spec.name);
                }
                ChildEvent::Exited(code) => {
                    exit_code = code;
                    break;
                }
            }
        }
        self.forget_spawn(id, pid);
        let detail = if tail.trim().is_empty() {
            exit_code
                .map(|code| format!("exit {code}"))
                .unwrap_or_else(|| "no exit code".to_string())
        } else {
            tail.trim().to_string()
        };
        self.set_view(id, generation, |run| {
            run.child = None;
            run.view = RunView {
                phase: Phase::Waiting,
                message: format!("{} exited ({detail}); restarting soon", spec.name),
                url: None,
                pid: None,
            };
        });
        Ok(())
    }
}

/// Whether two specs launch the same process: the same build, address and
/// way to start it.
fn same_launch(a: &SidecarSpec, b: &SidecarSpec) -> bool {
    a.pin() == b.pin()
        && a.local_url == b.local_url
        && a.listen_env == b.listen_env
        && a.supervised_env == b.supervised_env
        && a.args == b.args
}

fn starting(spec: &SidecarSpec) -> RunView {
    RunView {
        phase: Phase::Starting,
        message: format!("Starting {}", spec.name),
        url: None,
        pid: None,
    }
}

#[cfg(test)]
#[path = "core_host_tests.rs"]
mod core_host_tests;
