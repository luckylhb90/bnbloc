//! Desktop supervision of the sandbox component (`bnbloc.sandbox.package/v1`)
//! and its one machine. Contract: docs/prd/2026-10-09-agent-vm-sandbox.md,
//! "Phase 1 contracts".
//!
//! The generic sidecar manager downloads and runs the sandbox sidecar like
//! any component (sidecar_runtime), with three sandbox-only launch inputs
//! wired here: its files laid out in one per-version bundle
//! (sandbox_bundle), a fresh loopback port, and a fresh bearer + private data
//! dir. Once the sidecar runs, this module:
//!
//! 1. polls Sandbox Protocol discover until it answers (else kills it and
//!    the supervise loop restarts it with backoff);
//! 2. prewarms the ONE machine the desktop owns (`bnbloc`): pull the
//!    package's guest image, create (or start) the machine with the shared
//!    workspaces root; mode `off` deletes it instead;
//! 3. watches the machine and re-prewarms it when it is gone;
//! 4. hands the bridge `BNBLOC_SANDBOX_*` (memory/env only) while the
//!    sandbox is ready; the bridge-stabilizing watcher restarts the bridge
//!    when that handoff appears, disappears or the mode changes.
//!
//! At most one sandbox component is active per desktop (sidecar_model).
use std::path::PathBuf;
use std::time::{Duration, Instant};

use tauri::Manager;

use crate::child_process::{self, STOP_GRACE};
use crate::internal_ext::sandbox_api::{
    create_machine_request, Discovery, MachineView, SandboxApi,
};
use crate::internal_ext::sandbox_bundle;
use crate::internal_ext::sidecar_model::{ComponentKind, GuestProfile, SidecarSpec};
use crate::internal_ext::sidecar_util::{cache_stem, home_env};
use crate::internal_ext::Adapter;
use crate::secret::Secret;
use crate::settings::SandboxMode;

/// The machine the desktop owns (`BNBLOC_SANDBOX_MACHINE`).
pub(super) const MACHINE_NAME: &str = "bnbloc";
/// The share carrying the workspaces root into the machine.
const WORKSPACES_SHARE: &str = "workspaces";
/// The one host directory shared into the machine, under the member's home
/// (`BNBLOC_SANDBOX_WORKSPACES_ROOT`). Nothing else of the home is shared.
const WORKSPACES_DIR: &str = "bnbloc-workspaces";
/// The bridge env names (Phase 1 contracts).
pub(super) const ENV_URL: &str = "BNBLOC_SANDBOX_URL";
pub(super) const ENV_TOKEN: &str = "BNBLOC_SANDBOX_TOKEN";
pub(super) const ENV_VMEXEC: &str = "BNBLOC_SANDBOX_VMEXEC";
pub(super) const ENV_MODE: &str = "BNBLOC_SANDBOX_MODE";
pub(super) const ENV_MACHINE: &str = "BNBLOC_SANDBOX_MACHINE";
pub(super) const ENV_WORKSPACES_ROOT: &str = "BNBLOC_SANDBOX_WORKSPACES_ROOT";
/// The bridge-stabilizing watcher's key for the sandbox handoff (a slash
/// never appears in a core name, so it cannot collide with one).
pub(super) const BRIDGE_KEY: &str = "sandbox/handoff";

/// How long a fresh sidecar may take to answer discover.
const READY_TIMEOUT: Duration = Duration::from_secs(60);
const READY_POLL: Duration = Duration::from_millis(250);
/// Machine watch cadence while the sidecar runs.
const WATCH_INTERVAL: Duration = Duration::from_secs(15);
/// Minimum spacing of automatic re-prewarms after a failure.
const REPREWARM_BACKOFF: Duration = Duration::from_secs(60);

/// Where a launch is in its bring-up.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) enum Phase {
    /// Spawned; waiting for discover.
    Starting,
    /// Preparing the guest image.
    PullingImage,
    /// Creating / starting the machine.
    StartingMachine,
    /// Machine running.
    Ready,
    /// Mode off: the sidecar runs, no machine.
    Off,
    /// The package declares no guest image: nothing to prewarm.
    NoGuest,
    Error(String),
}

/// One running launch of the active sandbox sidecar.
pub(super) struct Launch {
    pub(super) seq: u64,
    pub(super) id: String,
    pub(super) name: String,
    pub(super) api: SandboxApi,
    pub(super) vmexec: Option<PathBuf>,
    pub(super) guest: Option<GuestProfile>,
    pub(super) spawned_at: Instant,
    pub(super) discovery: Option<Discovery>,
    pub(super) sidecar_ready_ms: Option<u64>,
    pub(super) phase: Phase,
    pub(super) machine: Option<MachineView>,
    pub(super) prewarm_ms: Option<u64>,
    /// The bridge may drive this sandbox: set when a prewarm succeeds,
    /// cleared when the machine is lost, the mode turns off, or the launch
    /// ends. A deliberate VM restart keeps it (same URL, token and machine).
    pub(super) handoff: bool,
    /// A lifecycle operation (prewarm / restart / delete) is in flight.
    pub(super) busy: bool,
    pub(super) last_prewarm_attempt: Option<Instant>,
}

#[derive(Default)]
pub(super) struct SandboxState {
    seq: u64,
    pub(super) launch: Option<Launch>,
    pub(super) last_error: Option<String>,
}

/// What the bridge gets while the sandbox is ready (Phase 1 contracts).
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct BridgeHandoff {
    pub(crate) seq: u64,
    pub(crate) url: String,
    pub(crate) token: Secret,
    pub(crate) vmexec: PathBuf,
    pub(crate) machine: String,
    pub(crate) workspaces_root: PathBuf,
}

/// The bridge env for `mode` and the current handoff: the mode always; the
/// rest only while ready and not off.
pub(crate) fn bridge_env(
    mode: SandboxMode,
    handoff: Option<&BridgeHandoff>,
) -> Vec<(String, String)> {
    let mut env = vec![(ENV_MODE.to_string(), mode.as_str().to_string())];
    if let Some(h) = handoff.filter(|_| mode != SandboxMode::Off) {
        env.push((ENV_URL.into(), h.url.clone()));
        env.push((ENV_TOKEN.into(), h.token.expose().to_string()));
        env.push((ENV_VMEXEC.into(), h.vmexec.to_string_lossy().into_owned()));
        env.push((ENV_MACHINE.into(), h.machine.clone()));
        env.push((
            ENV_WORKSPACES_ROOT.into(),
            h.workspaces_root.to_string_lossy().into_owned(),
        ));
    }
    env
}

/// The bridge watcher's identity of the handoff: changes whenever the env
/// the bridge needs changes (never contains the token; a new launch has a new
/// seq and therefore a new token).
pub(super) fn bridge_fingerprint(
    mode: SandboxMode,
    handoff: Option<&BridgeHandoff>,
) -> Option<String> {
    let h = handoff.filter(|_| mode != SandboxMode::Off)?;
    Some(format!(
        "{}|{}|{}|{}|{}",
        mode.as_str(),
        h.seq,
        h.url,
        h.machine,
        h.vmexec.display()
    ))
}

/// The status the Settings → Agent sandbox section renders.
#[derive(Clone, Debug, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxStatus {
    pub installed: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    /// notInstalled | installing | starting | ready | off | error
    pub state: &'static str,
    pub message: String,
    pub mode: SandboxMode,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub isolation: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub machine: Option<MachineView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_error: Option<String>,
    pub timings: SandboxTimings,
    /// Commands of the selected runs go to the VM right now.
    pub handed_to_bridge: bool,
}

#[derive(Clone, Debug, Default, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxTimings {
    /// Sidecar spawn → discover answered.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sidecar_ready_ms: Option<u64>,
    /// Prewarm (image prepare + create/start), client-measured.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub prewarm_ms: Option<u64>,
}

impl Adapter {
    // ---------------------------------------------------------------- launch

    /// The private data dir of sandbox `name` (its VM disks), created
    /// owner-only in the signed-in member's own directory (member_dir:
    /// member and tenant), so another member or tenant never uses it and
    /// the uninstall sweep judges it only against its own scope's catalog.
    /// The runtime derives Unix socket paths from it; macOS caps them at 104
    /// bytes and the sidecar shortens them through a /tmp link.
    fn sandbox_data_dir(&self, name: &str) -> Result<PathBuf, String> {
        let dir = self
            .member_dir()?
            .join(crate::internal_ext::member_dir::SANDBOX_DATA_DIR)
            .join(cache_stem(name));
        create_private_dir(&dir)?;
        Ok(dir)
    }

    /// Download and verify every file of sandbox `spec` into its bundle
    /// directory; returns the sidecar executable.
    pub(super) async fn ensure_sandbox_bundle(
        &self,
        spec: &SidecarSpec,
    ) -> Result<PathBuf, crate::internal_ext::sidecar_fetch::FetchError> {
        use crate::internal_ext::sidecar_fetch::FetchError;
        let id = spec.id();
        let base = self.dirs().map_err(FetchError::Retry)?.sandbox_bundles();
        let plan = sandbox_bundle::bundle_plan(&base, spec).map_err(FetchError::Refused)?;
        let gateway = self.gateway.session();
        let started = Instant::now();
        let downloaded = sandbox_bundle::prepare_bundle(&gateway, &plan, |file, progress| {
            let (state, message) = progress.status(&format!("{} {file}", spec.name));
            self.set_status(&id, state, message, None);
        })
        .await?;
        self.set_status(
            &id,
            "ready",
            if downloaded {
                format!(
                    "{} downloaded and verified in {:.1}s",
                    spec.name,
                    started.elapsed().as_secs_f64()
                )
            } else {
                format!("{} is already downloaded", spec.name)
            },
            None,
        );
        let (root, keep) = (plan.root.clone(), plan.dir.clone());
        tauri::async_runtime::spawn_blocking(move || {
            sandbox_bundle::remove_stale_bundles(&root, Some(&keep))
        });
        Ok(plan.exe())
    }

    /// A fresh loopback listener for the sandbox sidecar (handed over in its
    /// listenEnv), after stopping a sidecar of this component an earlier app
    /// session left running (it would hold the data dir and its VMs).
    pub(super) async fn sandbox_listener(
        &self,
        spec: &SidecarSpec,
    ) -> Result<crate::internal_ext::sidecar_util::RuntimeListener, String> {
        use crate::internal_ext::loopback;
        self.replace_sandbox_orphan(spec).await;
        let listener =
            loopback::bind_ephemeral().map_err(|e| format!("reserve sandbox port: {e}"))?;
        let addr = listener
            .local_addr()
            .map_err(|e| format!("reserve sandbox port: {e}"))?;
        Ok(crate::internal_ext::sidecar_util::RuntimeListener {
            url: loopback::LoopbackHttp::of_addr(addr),
            listener: Some(listener),
        })
    }

    async fn replace_sandbox_orphan(&self, spec: &SidecarSpec) {
        let (Some(record_dir), Ok(base)) = (
            self.spawn_record_dir(),
            self.dirs().map(crate::dirs::Dirs::sandbox_bundles),
        ) else {
            return;
        };
        let id = spec.id();
        let root = base.join(cache_stem(&spec.name));
        let orphan = tauri::async_runtime::spawn_blocking(move || {
            use crate::internal_ext::sidecar_orphan::{
                owned_orphan, process_exe, read_spawn_record,
            };
            let record = read_spawn_record(&record_dir, &id)?;
            owned_orphan(
                record.pid,
                Some(&record),
                process_exe(record.pid).as_deref(),
                &root,
                std::process::id(),
            )
        })
        .await
        .ok()
        .flatten();
        let Some(pid) = orphan.map(|o| o.pid) else {
            return;
        };
        if !crate::internal_ext::sidecar_orphan::CAN_TERMINATE {
            return;
        }
        eprintln!(
            "[sandbox] stopping {} left running by an earlier session (pid {pid})",
            spec.name
        );
        let stopped = tauri::async_runtime::spawn_blocking(move || {
            crate::internal_ext::sidecar_orphan::terminate_orphan(pid, STOP_GRACE)
        })
        .await
        .unwrap_or_else(|e| Err(format!("stop task: {e}")));
        match stopped {
            Ok(()) => self.forget_spawn(&spec.id(), pid),
            Err(e) => eprintln!("[sandbox] could not stop pid {pid}: {e}"),
        }
    }

    /// The sandbox-only launch env (token, data dir, file paths); returns
    /// the launch's bearer. None for other kinds.
    pub(super) fn sandbox_launch_env(
        &self,
        spec: &SidecarSpec,
        exe: &std::path::Path,
        cmd: &mut std::process::Command,
    ) -> Result<Option<Secret>, String> {
        if spec.kind != ComponentKind::Sandbox {
            return Ok(None);
        }
        let token = Secret::generate()?;
        let data_dir = self.sandbox_data_dir(&spec.name)?;
        for (key, value) in sandbox_bundle::launch_env(spec, exe, token.expose(), &data_dir) {
            cmd.env(key, value);
        }
        Ok(Some(token))
    }

    /// Record a spawned sandbox sidecar as the current launch and start its
    /// bring-up (discover → prewarm → watch). Returns the launch seq.
    pub(super) fn sandbox_spawned(
        &self,
        spec: &SidecarSpec,
        url: &str,
        token: Secret,
        exe: &std::path::Path,
    ) -> u64 {
        let seq = {
            let mut state = self.sandbox.lock();
            state.seq += 1;
            state.launch = Some(Launch {
                seq: state.seq,
                id: spec.id(),
                name: spec.name.clone(),
                api: SandboxApi {
                    url: url.to_string(),
                    token,
                },
                vmexec: sandbox_bundle::vmexec_path(spec, exe),
                guest: spec.sandbox.guest.clone(),
                spawned_at: Instant::now(),
                discovery: None,
                sidecar_ready_ms: None,
                phase: Phase::Starting,
                machine: None,
                prewarm_ms: None,
                handoff: false,
                busy: false,
                last_prewarm_attempt: None,
            });
            state.seq
        };
        let sup = self.clone();
        tauri::async_runtime::spawn(async move { sup.sandbox_bring_up(seq).await });
        seq
    }

    /// The sidecar of launch `seq` exited: its machines went with it.
    pub(super) fn sandbox_exited(&self, seq: u64, detail: &str) {
        let mut state = self.sandbox.lock();
        if state.launch.as_ref().is_some_and(|l| l.seq == seq) {
            state.launch = None;
            if !detail.trim().is_empty() {
                state.last_error = Some(format!("The sandbox runtime exited: {}", detail.trim()));
            }
        }
    }

    fn sandbox_api(&self, seq: u64) -> Option<SandboxApi> {
        let state = self.sandbox.lock();
        state
            .launch
            .as_ref()
            .filter(|l| l.seq == seq)
            .map(|l| l.api.clone())
    }

    fn with_launch<R>(&self, seq: u64, f: impl FnOnce(&mut Launch) -> R) -> Option<R> {
        let mut state = self.sandbox.lock();
        state.launch.as_mut().filter(|l| l.seq == seq).map(f)
    }

    fn sandbox_error(&self, seq: u64, message: String) {
        eprintln!("[sandbox] {message}");
        let mut state = self.sandbox.lock();
        if let Some(launch) = state.launch.as_mut().filter(|l| l.seq == seq) {
            launch.phase = Phase::Error(message.clone());
            launch.handoff = false;
        }
        state.last_error = Some(message);
    }

    pub(super) fn sandbox_mode(&self) -> SandboxMode {
        self.app
            .try_state::<crate::AppState>()
            .map(|state| state.settings.lock().sandbox_mode)
            .unwrap_or_default()
    }

    async fn sandbox_bring_up(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        let deadline = Instant::now() + READY_TIMEOUT;
        let discovery = loop {
            if self.sandbox_api(seq).is_none() {
                return;
            }
            match api.discover().await {
                Ok(discovery) => break discovery,
                Err(e) if e.code == "incompatible" => {
                    self.sandbox_error(seq, e.message);
                    return;
                }
                Err(e) if Instant::now() >= deadline => {
                    self.sandbox_error(
                        seq,
                        format!(
                            "The sandbox runtime did not become ready in {}s ({e}); restarting it",
                            READY_TIMEOUT.as_secs()
                        ),
                    );
                    self.kill_sandbox_child(seq);
                    return;
                }
                Err(_) => tokio::time::sleep(READY_POLL).await,
            }
        };
        let ok = self.with_launch(seq, |launch| {
            launch.sidecar_ready_ms = Some(launch.spawned_at.elapsed().as_millis() as u64);
            launch.discovery = Some(discovery);
        });
        if ok.is_none() {
            return;
        }
        self.sandbox.lock().last_error = None;
        self.sandbox_reconcile(seq).await;
        loop {
            tokio::time::sleep(WATCH_INTERVAL).await;
            if self.sandbox_api(seq).is_none() {
                return;
            }
            self.sandbox_watch(seq).await;
        }
    }

    /// Stop launch `seq`'s child (it never became ready): the supervise loop
    /// sees the exit and restarts it with backoff.
    fn kill_sandbox_child(&self, seq: u64) {
        let Some(id) = self.with_launch(seq, |launch| launch.id.clone()) else {
            return;
        };
        let child = self
            .inner
            .lock()
            .sidecars
            .get_mut(&id)
            .and_then(|sidecar| sidecar.child.take());
        if let Some(child) = child {
            child_process::terminate_detached(vec![child], STOP_GRACE);
        }
    }

    /// Bring the machine to what the mode wants: off → no machine; otherwise
    /// a running prewarmed machine. Serialized per launch by `busy`.
    async fn sandbox_reconcile(&self, seq: u64) {
        let claimed = self.with_launch(seq, |launch| {
            if launch.busy || launch.discovery.is_none() {
                return false;
            }
            launch.busy = true;
            true
        });
        if claimed != Some(true) {
            return;
        }
        if self.sandbox_mode() == SandboxMode::Off {
            self.sandbox_turn_off(seq).await;
        } else {
            self.sandbox_prewarm(seq).await;
        }
        self.with_launch(seq, |launch| launch.busy = false);
    }

    async fn sandbox_turn_off(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        let result = api.delete_machine(MACHINE_NAME).await;
        self.with_launch(seq, |launch| {
            launch.handoff = false;
            launch.machine = None;
            launch.phase = Phase::Off;
        });
        if let Err(e) = result {
            self.sandbox_error(seq, format!("Could not stop the sandbox VM: {e}"));
        }
    }

    async fn sandbox_prewarm(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        let Some((guest, discovery)) = self.with_launch(seq, |launch| {
            launch.last_prewarm_attempt = Some(Instant::now());
            (
                launch.guest.clone(),
                launch.discovery.clone().unwrap_or_default(),
            )
        }) else {
            return;
        };
        let Some(guest) = guest else {
            self.with_launch(seq, |launch| {
                launch.phase = Phase::NoGuest;
                launch.handoff = false;
            });
            return;
        };
        let started = Instant::now();
        let result = async {
            let root = workspaces_root().ok_or("no home directory for the sandbox workspaces")?;
            create_private_dir(&root)?;
            // A machine that survived (a reused launch) is reused.
            let existing = api.machine(MACHINE_NAME).await.map_err(|e| e.to_string())?;
            let machine = match existing {
                Some(m) if m.state == "running" => m,
                Some(m) if m.state == "paused" => {
                    self.with_launch(seq, |l| l.phase = Phase::StartingMachine);
                    api.lifecycle(MACHINE_NAME, "resume")
                        .await
                        .map_err(|e| e.to_string())?
                }
                Some(m) if m.state == "stopped" => {
                    self.with_launch(seq, |l| l.phase = Phase::StartingMachine);
                    api.lifecycle(MACHINE_NAME, "start")
                        .await
                        .map_err(|e| e.to_string())?
                }
                existing => {
                    if existing.is_some() {
                        // failed / creating leftovers: start clean.
                        api.delete_machine(MACHINE_NAME)
                            .await
                            .map_err(|e| e.to_string())?;
                    }
                    self.with_launch(seq, |l| l.phase = Phase::PullingImage);
                    api.pull_image(&guest.image).await.map_err(|e| {
                        format!("Could not prepare the VM image {}: {e}", guest.image)
                    })?;
                    self.with_launch(seq, |l| l.phase = Phase::StartingMachine);
                    let owner = self
                        .app
                        .try_state::<crate::AppState>()
                        .map(|s| s.settings.lock().owner.clone())
                        .unwrap_or_default();
                    let req = create_machine_request(
                        MACHINE_NAME,
                        &guest,
                        &root,
                        WORKSPACES_SHARE,
                        &discovery,
                        &owner,
                    );
                    api.create_machine(req)
                        .await
                        .map_err(|e| format!("Could not start the VM: {e}"))?
                }
            };
            Ok::<MachineView, String>(machine)
        }
        .await;
        match result {
            Ok(machine) if machine.state == "running" => {
                let elapsed = started.elapsed().as_millis() as u64;
                eprintln!(
                    "[sandbox] VM {MACHINE_NAME} running (prewarm {elapsed} ms, boot {} ms)",
                    machine.boot_ms.unwrap_or_default()
                );
                self.with_launch(seq, |launch| {
                    launch.machine = Some(machine);
                    launch.phase = Phase::Ready;
                    launch.prewarm_ms = Some(elapsed);
                    launch.handoff = true;
                });
                self.sandbox.lock().last_error = None;
            }
            Ok(machine) => {
                let state = machine.state.clone();
                let error = machine.error.clone();
                self.with_launch(seq, |launch| launch.machine = Some(machine));
                self.sandbox_error(
                    seq,
                    format!(
                        "The VM is {state}{}",
                        error.map(|e| format!(": {e}")).unwrap_or_default()
                    ),
                );
            }
            Err(e) => self.sandbox_error(seq, e),
        }
    }

    /// Periodic check: refresh the machine state; re-prewarm a lost machine
    /// (spaced by REPREWARM_BACKOFF).
    async fn sandbox_watch(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        if self.with_launch(seq, |l| l.busy || l.discovery.is_none()) != Some(false) {
            return;
        }
        let mode = self.sandbox_mode();
        let observed = api.machine(MACHINE_NAME).await;
        let want_machine = mode != SandboxMode::Off;
        let retry_due = self
            .with_launch(seq, |launch| {
                launch
                    .last_prewarm_attempt
                    .map_or(true, |at| at.elapsed() >= REPREWARM_BACKOFF)
            })
            .unwrap_or(false);
        match observed {
            Ok(Some(machine)) if machine.state == "running" => {
                self.with_launch(seq, |launch| {
                    launch.machine = Some(machine);
                    if launch.phase != Phase::Ready && want_machine {
                        launch.phase = Phase::Ready;
                        launch.handoff = true;
                    }
                });
                if !want_machine {
                    self.sandbox_reconcile(seq).await;
                }
            }
            Ok(observed) => {
                let gone = observed.is_none();
                self.with_launch(seq, |launch| {
                    launch.machine = observed;
                    if want_machine && launch.phase == Phase::Ready {
                        launch.phase = Phase::Error(if gone {
                            "The VM is gone; starting it again".into()
                        } else {
                            "The VM stopped; starting it again".into()
                        });
                        launch.handoff = false;
                    }
                });
                if want_machine && retry_due {
                    self.sandbox_reconcile(seq).await;
                }
            }
            Err(e) => self.sandbox_error(seq, format!("Could not reach the sandbox runtime: {e}")),
        }
    }

    /// The member changed the mode: reconcile the machine in the background.
    pub(super) fn sandbox_mode_changed(&self) {
        let Some(seq) = self.sandbox.lock().launch.as_ref().map(|l| l.seq) else {
            return;
        };
        let sup = self.clone();
        tauri::async_runtime::spawn(async move { sup.sandbox_reconcile(seq).await });
    }

    /// Restart the VM (Settings → "Restart VM"): stop + start the same
    /// machine (spaces are re-established by the runtime), recreating it when
    /// that fails. The bridge keeps its env (same URL, token and machine).
    pub(super) async fn sandbox_restart_vm(&self) -> Result<(), String> {
        if self.sandbox_mode() == SandboxMode::Off {
            return Err("The agent sandbox is off".into());
        }
        let seq = self
            .sandbox
            .lock()
            .launch
            .as_ref()
            .filter(|l| l.discovery.is_some())
            .map(|l| l.seq)
            .ok_or("The sandbox runtime is not running")?;
        let claimed = self.with_launch(seq, |launch| {
            if launch.busy {
                return false;
            }
            launch.busy = true;
            launch.phase = Phase::StartingMachine;
            true
        });
        if claimed != Some(true) {
            return Err("The VM is busy; try again in a moment".into());
        }
        let api = self
            .sandbox_api(seq)
            .ok_or("The sandbox runtime is not running")?;
        let started = Instant::now();
        let restarted = async {
            api.lifecycle(MACHINE_NAME, "stop").await?;
            api.lifecycle(MACHINE_NAME, "start").await
        }
        .await;
        match restarted {
            Ok(machine) if machine.state == "running" => {
                let elapsed = started.elapsed().as_millis() as u64;
                self.with_launch(seq, |launch| {
                    launch.machine = Some(machine);
                    launch.phase = Phase::Ready;
                    launch.prewarm_ms = Some(elapsed);
                    launch.handoff = true;
                    launch.busy = false;
                });
                self.sandbox.lock().last_error = None;
                Ok(())
            }
            other => {
                if let Err(e) = &other {
                    eprintln!("[sandbox] restart failed ({e}); recreating the VM");
                }
                if let Err(e) = api.delete_machine(MACHINE_NAME).await {
                    eprintln!("[sandbox] could not delete the VM before recreating: {e}");
                }
                self.sandbox_prewarm(seq).await;
                self.with_launch(seq, |launch| launch.busy = false);
                match self.with_launch(seq, |l| l.phase.clone()) {
                    Some(Phase::Ready) => Ok(()),
                    Some(Phase::Error(e)) => Err(e),
                    _ => Err("The VM did not start".into()),
                }
            }
        }
    }

    // ----------------------------------------------------- bridge + status

    /// The handoff while the sandbox is ready.
    pub(crate) fn sandbox_handoff(&self) -> Option<BridgeHandoff> {
        let state = self.sandbox.lock();
        let launch = state.launch.as_ref()?;
        if !launch.handoff {
            return None;
        }
        Some(BridgeHandoff {
            seq: launch.seq,
            url: launch.api.url.clone(),
            token: launch.api.token.clone(),
            vmexec: launch.vmexec.clone()?,
            machine: MACHINE_NAME.to_string(),
            workspaces_root: workspaces_root()?,
        })
    }

    pub fn sandbox_status(&self) -> SandboxStatus {
        let mode = self.sandbox_mode();
        let component = self
            .inner
            .lock()
            .sidecars
            .values()
            .find(|s| {
                s.spec
                    .as_ref()
                    .is_some_and(|spec| spec.kind == ComponentKind::Sandbox)
            })
            .map(|s| (s.spec.clone(), s.status.clone(), s.child.is_some()));
        let handoff = self.sandbox_handoff().is_some();
        let state = self.sandbox.lock();
        let mut status = SandboxStatus {
            installed: false,
            name: None,
            version: None,
            state: "notInstalled",
            message: "No sandbox component is installed".into(),
            mode,
            isolation: None,
            machine: None,
            last_error: state.last_error.clone(),
            timings: SandboxTimings::default(),
            handed_to_bridge: handoff && mode != SandboxMode::Off,
        };
        let Some((Some(spec), sidecar_status, running)) = component else {
            return status;
        };
        status.installed = true;
        status.name = Some(spec.name.clone());
        status.version = Some(spec.version.clone());
        let Some(launch) = state.launch.as_ref().filter(|l| l.id == spec.id()) else {
            let (state_name, message) = match sidecar_status.state.as_str() {
                "failed" => ("error", sidecar_status.message),
                "downloading" | "verifying" | "resolving" => ("installing", sidecar_status.message),
                _ if running => ("starting", "Starting the sandbox runtime".into()),
                _ => ("starting", sidecar_status.message),
            };
            status.state = state_name;
            status.message = message;
            return status;
        };
        status.isolation = launch.discovery.as_ref().map(|d| d.isolation.clone());
        status.machine = launch.machine.clone();
        status.timings = SandboxTimings {
            sidecar_ready_ms: launch.sidecar_ready_ms,
            prewarm_ms: launch.prewarm_ms,
        };
        let (state_name, message) = match &launch.phase {
            Phase::Starting => ("starting", "Starting the sandbox runtime".to_string()),
            Phase::PullingImage => ("starting", "Preparing the VM image".to_string()),
            Phase::StartingMachine => ("starting", "Starting the VM".to_string()),
            Phase::Ready if launch.vmexec.is_none() => (
                "error",
                format!(
                    "The VM is up, but {} ships no {} helper, so agents cannot use it",
                    launch.name,
                    sandbox_bundle::VMEXEC_FILE
                ),
            ),
            Phase::Ready => ("ready", "VM up".to_string()),
            Phase::Off => ("off", "The agent sandbox is off".to_string()),
            Phase::NoGuest => (
                "error",
                format!(
                    "{} runs, but no guest image declared: no VM is prewarmed",
                    launch.name
                ),
            ),
            Phase::Error(e) => ("error", e.clone()),
        };
        status.state = if mode == SandboxMode::Off && state_name == "ready" {
            "off"
        } else {
            state_name
        };
        status.message = message;
        status
    }
}

/// `<home>/bnbloc-workspaces`: the one host directory the machine shares.
pub(crate) fn workspaces_root() -> Option<PathBuf> {
    home_env().map(|(_, home)| PathBuf::from(home).join(WORKSPACES_DIR))
}

fn create_private_dir(dir: &std::path::Path) -> Result<(), String> {
    std::fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))
            .map_err(|e| format!("restrict {}: {e}", dir.display()))?;
    }
    Ok(())
}

/// Extend the bridge env with the sandbox handoff (bridge::env_for).
pub(crate) fn extend_bridge_env(
    app: Option<&tauri::AppHandle>,
    env: &mut std::collections::HashMap<String, String>,
    mode: SandboxMode,
) {
    let handoff = app
        .and_then(|app| app.try_state::<Adapter>())
        .and_then(|adapter| adapter.sandbox_handoff());
    env.extend(bridge_env(mode, handoff.as_ref()));
}

// ------------------------------------------------------------------ commands

#[tauri::command]
pub(crate) fn sandbox_status(adapter: tauri::State<'_, Adapter>) -> SandboxStatus {
    adapter.sandbox_status()
}

#[tauri::command]
pub(crate) fn sandbox_mode_get(state: tauri::State<'_, crate::AppState>) -> SandboxMode {
    state.settings.lock().sandbox_mode
}

/// Persist the member's mode and reconcile the VM; the bridge-stabilizing
/// watcher restarts the bridge when its sandbox env changes.
#[tauri::command]
pub(crate) fn sandbox_mode_set(
    state: tauri::State<'_, crate::AppState>,
    adapter: tauri::State<'_, Adapter>,
    mode: SandboxMode,
) -> Result<SandboxStatus, String> {
    let mut settings = state.settings.lock().clone();
    settings.sandbox_mode = mode;
    state.save_settings(settings)?;
    adapter.sandbox_mode_changed();
    Ok(adapter.sandbox_status())
}

#[tauri::command]
pub(crate) async fn sandbox_restart_vm(
    adapter: tauri::State<'_, Adapter>,
) -> Result<SandboxStatus, String> {
    adapter.sandbox_restart_vm().await?;
    Ok(adapter.sandbox_status())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn handoff() -> BridgeHandoff {
        BridgeHandoff {
            seq: 3,
            url: "http://127.0.0.1:50123".into(),
            token: Secret::from_test("tok-secret-value"),
            vmexec: PathBuf::from("/cache/sandbox/vmrt/0.1.0-abc/bnbloc-vmexec"),
            machine: MACHINE_NAME.into(),
            workspaces_root: PathBuf::from("/Users/me/bnbloc-workspaces"),
        }
    }

    #[test]
    fn bridge_env_carries_the_mode_always_and_the_rest_only_while_ready() {
        let h = handoff();
        let env: std::collections::HashMap<_, _> = bridge_env(SandboxMode::Borrowed, Some(&h))
            .into_iter()
            .collect();
        assert_eq!(env[ENV_MODE], "borrowed");
        assert_eq!(env[ENV_URL], "http://127.0.0.1:50123");
        assert_eq!(env[ENV_TOKEN], "tok-secret-value");
        assert_eq!(
            env[ENV_VMEXEC],
            "/cache/sandbox/vmrt/0.1.0-abc/bnbloc-vmexec"
        );
        assert_eq!(env[ENV_MACHINE], "bnbloc");
        assert_eq!(env[ENV_WORKSPACES_ROOT], "/Users/me/bnbloc-workspaces");

        assert_eq!(
            bridge_env(SandboxMode::All, None),
            vec![(ENV_MODE.to_string(), "all".to_string())]
        );
        assert_eq!(
            bridge_env(SandboxMode::Off, Some(&h)),
            vec![(ENV_MODE.to_string(), "off".to_string())],
            "off never hands the sandbox over"
        );
    }

    #[test]
    fn the_token_never_appears_in_the_fingerprint_or_debug_output() {
        let h = handoff();
        let fp = bridge_fingerprint(SandboxMode::Borrowed, Some(&h)).expect("ready");
        assert!(!fp.contains("tok-secret-value"), "{fp}");
        assert!(!format!("{h:?}").contains("tok-secret-value"));
        // The fingerprint changes with everything the bridge env carries.
        assert_ne!(
            Some(fp.clone()),
            bridge_fingerprint(SandboxMode::All, Some(&h))
        );
        let mut relaunched = h.clone();
        relaunched.seq += 1;
        assert_ne!(
            Some(fp),
            bridge_fingerprint(SandboxMode::Borrowed, Some(&relaunched))
        );
        assert_eq!(bridge_fingerprint(SandboxMode::Off, Some(&h)), None);
        assert_eq!(bridge_fingerprint(SandboxMode::Borrowed, None), None);
    }
}
