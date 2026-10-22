//! Bridge supervision: spawn the embedded `bnbloc-bridge` sidecar, track its
//! process state, tail its logs, and stop/restart it across POSIX and Windows.
//!
//! The shell owns *process lifecycle only* — it adds no HTTP/business surface to
//! the bridge, so the connector stays a connector (not a second business core).
//! Lifecycle is derived from the child process:
//!
//!   starting -> (settled alive) running -> stopped (clean exit)
//!                                        \-> failed  (non-zero / signal / spawn error)
//!
//! A crash while the user still wants it running triggers a bounded-backoff
//! restart. Stopping is explicit (user or app quit), graceful (stdin EOF +
//! SIGTERM, then a force-kill after a grace period), and suppresses restart.
//!
//! Liveness is the process itself: an exit is observed and restarted. There
//! is deliberately no log-silence watchdog — a healthy idle bridge logs
//! nothing, so silence is not a wedge signal (it killed idle bridges every few
//! minutes and long turns mid-way). The bridge's server-side liveness is its
//! gateway heartbeat; a desktop-side wedge check needs a real bridge-emitted
//! heartbeat signal, not log volume.
use std::collections::VecDeque;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::Mutex;
use serde::Serialize;
use tauri::async_runtime::JoinHandle;

use crate::child_process::{self, Child, ChildEvent, RestartPolicy, Stopping};
use crate::internal_ext::sidecar_util::core_env_var;
use crate::internal_ext::Adapter;
use crate::settings::Settings;

const MAX_LOG_LINES: usize = 200;
const SETTLE: Duration = Duration::from_millis(1200);
/// Log lines reach the web at most this often (one status per interval,
/// however fast the bridge logs).
const LOG_EMIT_INTERVAL: Duration = Duration::from_millis(250);
/// The bridge's key in its `Stopping`.
const STOP_KEY: &str = "bridge";
/// Settle window before a refresh-token handoff restarts the bridge, so the
/// sign-in burst (adapter handoff + owner/tenant `set_settings` restart)
/// collapses into at most one restart.
const REFRESH_RESTART_SETTLE: Duration = Duration::from_secs(2);
/// The bridge env carrying the member's refresh token: the bridge renews its
/// own gateway session from it (SSO-only gateways have no direct login).
const REFRESH_TOKEN_ENV: &str = "BNBLOC_GATEWAY_REFRESH_TOKEN";

/// The externalBin base name. Tauri bundles the per-triple binary next to the
/// shell executable under this plain name (`.exe` on Windows); the
/// `binaries/` prefix and triple suffix exist only at build time.
const SIDECAR: &str = "bnbloc-bridge";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum State {
    /// No process; never started or fully stopped.
    Stopped,
    /// Spawned, within the settle window.
    Starting,
    /// Alive past the settle window.
    Running,
    /// Exited non-zero or by signal, or failed to spawn, while not
    /// intentionally stopped.
    Failed,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Status {
    pub state: State,
    pub pid: Option<u32>,
    pub last_exit_code: Option<i32>,
    pub logs_tail: Vec<String>,
}

/// Inner mutable supervision state, shared behind a mutex.
struct Inner {
    state: State,
    pid: Option<u32>,
    last_exit_code: Option<i32>,
    logs: VecDeque<String>,
    /// The running child, if any, for stop/restart.
    child: Option<Child>,
    /// True when the user/app wants the bridge running; drives crash-restart.
    desired_up: bool,
    /// Generation counter: increments on every stop so stale watchers/backoff
    /// loops from a previous run no-op instead of resurrecting a dead process.
    generation: u64,
    /// Handle to the running supervise task, aborted on stop.
    task: Option<JoinHandle<()>>,
    /// Settings of the last start, reused by a refresh-token restart.
    settings: Option<Settings>,
    /// The member's newest refresh token, handed over by the web session.
    /// Memory only: never persisted to settings.json.
    refresh_token: Option<String>,
    /// Whether the current run was spawned with a refresh token.
    run_has_refresh: bool,
    /// A refresh-token restart is scheduled (debounce).
    refresh_restart_pending: bool,
    /// A status emit for new log lines is scheduled (throttle).
    log_emit_pending: bool,
}

impl Inner {
    fn push_log(&mut self, line: String) {
        if self.logs.len() >= MAX_LOG_LINES {
            self.logs.pop_front();
        }
        self.logs.push_back(line);
    }
}

/// Where a supervisor reports its status (the web shows it): the account
/// runtime sends it to the windows that show the account.
pub(crate) type StatusEmit = Arc<dyn Fn(&Status) + Send + Sync>;

/// The supervisor of one account's bridge: cheaply cloneable handle to the
/// shared state.
#[derive(Clone)]
pub struct Supervisor {
    emit: StatusEmit,
    /// The adapter of the same account, whose managed cores, MCP tools and
    /// sandbox the bridge's env names (`attach`; none until attached and
    /// after `release`).
    adapter: Arc<Mutex<Option<Adapter>>>,
    inner: Arc<Mutex<Inner>>,
    /// Stops of earlier runs still in progress: a start waits for them, so
    /// two bridges never overlap.
    stopping: Stopping,
}

impl Supervisor {
    pub fn new(emit: StatusEmit) -> Self {
        Self {
            emit,
            adapter: Arc::new(Mutex::new(None)),
            inner: Arc::new(Mutex::new(Inner {
                state: State::Stopped,
                pid: None,
                last_exit_code: None,
                logs: VecDeque::with_capacity(MAX_LOG_LINES),
                child: None,
                desired_up: false,
                generation: 0,
                task: None,
                settings: None,
                refresh_token: None,
                run_has_refresh: false,
                refresh_restart_pending: false,
                log_emit_pending: false,
            })),
            stopping: Stopping::default(),
        }
    }

    /// The tenant the current run of the bridge was started for (None: it
    /// never started).
    pub(crate) fn running_tenant(&self) -> Option<String> {
        self.inner
            .lock()
            .settings
            .as_ref()
            .map(|s| s.tenant.clone())
    }

    /// Tie the bridge's env to this account's adapter.
    pub(crate) fn attach(&self, adapter: Adapter) {
        *self.adapter.lock() = Some(adapter);
    }

    /// Let go of the adapter (the account is leaving): the two reference each
    /// other, and this ends the loop.
    pub(crate) fn release(&self) {
        self.adapter.lock().take();
    }

    /// The status of a bridge that does not exist (no supervisor yet).
    pub fn stopped_status() -> Status {
        Status {
            state: State::Stopped,
            pid: None,
            last_exit_code: None,
            logs_tail: Vec::new(),
        }
    }

    /// Snapshot the current status for the IPC surface.
    pub fn status(&self) -> Status {
        let g = self.inner.lock();
        Status {
            state: g.state,
            pid: g.pid,
            last_exit_code: g.last_exit_code,
            logs_tail: g.logs.iter().cloned().collect(),
        }
    }

    /// Start (or restart) the bridge with the given settings. Marks the bridge
    /// as desired-up and launches a fresh supervise task; every spawn first
    /// waits for the stops of earlier runs (`Stopping`), so two bridges never
    /// overlap, however many starts arrive together.
    pub fn start(&self, settings: Settings) -> Result<(), String> {
        if !settings.is_runnable() {
            return Err("settings incomplete: gatewayUrl and owner are required".to_string());
        }
        // The previous run's graceful stop starts now.
        if let Some(child) = self.detach() {
            self.stopping.stop(STOP_KEY, vec![child]);
        }
        let generation = {
            let mut g = self.inner.lock();
            g.desired_up = true;
            g.state = State::Starting;
            g.generation += 1;
            g.settings = Some(settings.clone());
            // The new run spawns with the current token (spawn_once re-reads
            // it); record that now so a pending handoff restart sees it.
            g.run_has_refresh = g.refresh_token.is_some();
            g.generation
        };

        let sup = self.clone();
        let task = tauri::async_runtime::spawn(async move {
            sup.supervise(settings, generation).await;
        });
        self.inner.lock().task = Some(task);
        self.emit_status();
        Ok(())
    }

    /// Hand the member's refresh token (None = signed out) to the bridge.
    /// Every spawn carries the newest one; a running bridge is restarted only
    /// when it has NO refresh token yet (first handoff after app start, or a
    /// respawn while signed out) — it renews and rotates its own token, so a
    /// web-side rotation never restarts it. Signing out only stops handing a
    /// token to future spawns.
    pub fn set_refresh_token(&self, token: Option<String>) {
        let token = token
            .map(|t| t.trim().to_string())
            .filter(|t| !t.is_empty());
        let schedule = {
            let mut g = self.inner.lock();
            g.refresh_token = token;
            let need =
                needs_refresh_restart(g.desired_up, g.run_has_refresh, g.refresh_token.is_some());
            let schedule = need && !g.refresh_restart_pending;
            if schedule {
                g.refresh_restart_pending = true;
            }
            schedule
        };
        if !schedule {
            return;
        }
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            tokio::time::sleep(REFRESH_RESTART_SETTLE).await;
            let settings = {
                let mut g = sup.inner.lock();
                g.refresh_restart_pending = false;
                // Re-check after the settle: a start in between (owner/tenant
                // change) already spawned with the token.
                if needs_refresh_restart(g.desired_up, g.run_has_refresh, g.refresh_token.is_some())
                {
                    g.settings.clone()
                } else {
                    None
                }
            };
            if let Some(settings) = settings {
                sup.log_line(
                    "[supervisor] restarting bridge to hand it the session refresh token".into(),
                );
                if let Err(e) = sup.start(settings) {
                    sup.log_line(format!("[supervisor] refresh-token restart failed: {e}"));
                }
            }
        });
    }

    /// Stop the bridge and suppress crash-restart. Idempotent; the graceful
    /// stop completes in the background.
    pub fn stop(&self) {
        if let Some(child) = self.detach() {
            self.stopping.stop(STOP_KEY, vec![child]);
        }
        self.emit_status();
    }

    /// Mark the bridge stopped, cancel its supervise task, and hand back the
    /// running child (if any) for the caller to terminate — app exit and the
    /// updater collect every child and stop them together.
    pub fn detach(&self) -> Option<Child> {
        let (child, task) = {
            let mut g = self.inner.lock();
            g.desired_up = false;
            g.generation += 1; // invalidate any in-flight supervise/backoff loop
            g.state = State::Stopped;
            g.pid = None;
            (g.child.take(), g.task.take())
        };
        if let Some(task) = task {
            task.abort();
        }
        child
    }

    /// The supervise loop: spawn, mark running after settle, then restart on
    /// exit per `RestartPolicy::BRIDGE` until desired-up is cleared. Each
    /// iteration is guarded by the generation so a stale loop exits after
    /// stop()/restart.
    async fn supervise(&self, settings: Settings, generation: u64) {
        let policy = RestartPolicy::BRIDGE;
        let mut backoff = policy.backoff();
        loop {
            // Never overlap a run that is still stopping.
            self.stopping.stopped(STOP_KEY).await;
            if !self.generation_current(generation) {
                return;
            }
            let started = Instant::now();
            let outcome = self.spawn_once(&settings, generation).await;
            let ran_for = started.elapsed();
            match outcome {
                Ok(exit) if !policy.restarts_after(exit) => return,
                Ok(_) => {}
                Err(e) => {
                    self.log_line(format!("[supervisor] spawn error: {e}"));
                    let mut g = self.inner.lock();
                    if g.generation == generation {
                        g.state = State::Failed;
                    }
                    drop(g);
                    self.emit_status();
                }
            }
            let cont = {
                let g = self.inner.lock();
                g.desired_up && g.generation == generation
            };
            if !cont {
                return;
            }
            let delay = backoff.next(ran_for);
            self.log_line(format!(
                "[supervisor] bridge exited; restarting in {}s",
                delay.as_secs().max(1)
            ));
            tokio::time::sleep(delay).await;
        }
    }

    /// Spawn one bridge process and pump its events until it exits. Returns
    /// its exit code (None: killed by a signal, or superseded before it ran).
    async fn spawn_once(
        &self,
        settings: &Settings,
        generation: u64,
    ) -> Result<Option<i32>, String> {
        let mut cmd = std::process::Command::new(sidecar_path()?);
        let refresh_token = {
            let mut g = self.inner.lock();
            if g.generation == generation {
                g.run_has_refresh = g.refresh_token.is_some();
            }
            g.refresh_token.clone()
        };
        let adapter = self.adapter.lock().clone();
        cmd.envs(env_for(
            settings,
            refresh_token.as_deref(),
            adapter.as_ref(),
        ));
        let shell_env = crate::shell_env::current().await;
        let (mut rx, child) =
            child_process::spawn(cmd, &shell_env).map_err(|e| format!("spawn sidecar: {e}"))?;
        let pid = child.pid();
        {
            let mut g = self.inner.lock();
            if g.generation != generation {
                // A stop/restart raced ahead; stop this child and bail (the
                // next run waits for this stop too).
                drop(g);
                self.stopping.stop(STOP_KEY, vec![child]);
                return Ok(None);
            }
            g.child = Some(child);
            g.pid = Some(pid);
            g.state = State::Starting;
            g.last_exit_code = None;
        }
        self.emit_status();

        // Mark running after a short settle if it is still alive.
        let settle_sup = self.clone();
        tauri::async_runtime::spawn(async move {
            tokio::time::sleep(SETTLE).await;
            let mut g = settle_sup.inner.lock();
            if g.generation == generation && g.state == State::Starting && g.child.is_some() {
                g.state = State::Running;
                drop(g);
                settle_sup.emit_status();
            }
        });

        let mut exit_code = None;
        while let Some(event) = rx.recv().await {
            match event {
                ChildEvent::Line(line) => self.log_line(line),
                ChildEvent::Exited(code) => {
                    exit_code = code;
                    break;
                }
            }
        }
        let clean = exit_code == Some(0);
        let mut g = self.inner.lock();
        if g.generation == generation {
            g.child = None;
            g.pid = None;
            g.last_exit_code = exit_code;
            g.state = if g.desired_up && !clean {
                State::Failed
            } else {
                State::Stopped
            };
        }
        drop(g);
        self.emit_status();
        Ok(exit_code)
    }

    /// Add a shell line to the bridge log the Agents view shows.
    pub fn note(&self, line: String) {
        self.log_line(line);
    }

    fn generation_current(&self, generation: u64) -> bool {
        self.inner.lock().generation == generation
    }

    fn log_line(&self, line: String) {
        // Mirror to stderr so `bnbloc-desktop` surfaces bridge output and
        // supervisor errors in the shell's own log stream, not only in the
        // webview status feed.
        eprintln!("[bridge-supervisor] {line}");
        // The web gets the tail at most every LOG_EMIT_INTERVAL: a chatty
        // bridge costs one status per interval, not one per line.
        let schedule = {
            let mut g = self.inner.lock();
            g.push_log(line);
            !std::mem::replace(&mut g.log_emit_pending, true)
        };
        if schedule {
            let sup = self.clone();
            tauri::async_runtime::spawn(async move {
                tokio::time::sleep(LOG_EMIT_INTERVAL).await;
                sup.inner.lock().log_emit_pending = false;
                sup.emit_status();
            });
        }
    }

    fn emit_status(&self) {
        (self.emit)(&self.status());
    }
}

/// The bundled bridge binary: next to the shell executable (one level up when
/// running from a test harness's `deps/` directory).
fn sidecar_path() -> Result<PathBuf, String> {
    let exe = tauri::utils::platform::current_exe().map_err(|e| format!("resolve sidecar: {e}"))?;
    let dir = exe
        .parent()
        .ok_or("resolve sidecar: executable has no parent")?;
    let dir = if dir.ends_with("deps") {
        dir.parent().unwrap_or(dir)
    } else {
        dir
    };
    Ok(dir.join(format!("{SIDECAR}{}", std::env::consts::EXE_SUFFIX)))
}

/// Whether a running bridge must be restarted to receive the refresh token:
/// only when one is known and the current run was spawned without one.
fn needs_refresh_restart(desired_up: bool, run_has_refresh: bool, have_token: bool) -> bool {
    desired_up && have_token && !run_has_refresh
}

/// Build the bridge's environment from settings plus the in-memory session
/// refresh token. Empty optional values are omitted so the bridge falls back
/// to its own defaults.
fn env_for(
    s: &Settings,
    refresh_token: Option<&str>,
    adapter: Option<&Adapter>,
) -> std::collections::HashMap<String, String> {
    let mut env = std::collections::HashMap::new();
    // Mark the child as supervised: it shuts down gracefully on stdin EOF —
    // the shell's stop signal, which also fires if this shell dies (SIGKILL,
    // crash), so the bridge never outlives it.
    env.insert("BNBLOC_BRIDGE_SUPERVISED".into(), "1".into());
    let gateway = crate::settings::normalize_gateway_url(&s.gateway_url);
    env.insert("BNBLOC_GATEWAY_BASE_URL".into(), gateway.clone());
    // Live file sync: the public edge serves the relay under /relay/* on the
    // gateway's own origin, and the bridge appends /relay/stream/<key> itself,
    // so the relay base is the gateway's bare origin.
    if let Some(relay) = relay_base_url(&gateway) {
        env.insert("BNBLOC_RELAY_BASE_URL".into(), relay);
    }
    env.insert("BNBLOC_BRIDGE_OWNER".into(), s.owner.clone());
    if !s.tenant.trim().is_empty() {
        env.insert("BNBLOC_BRIDGE_TENANT".into(), s.tenant.clone());
    }
    // Cores → BNBLOC_CORES (comma-joined names) plus per-core endpoint
    // overrides BNBLOC_CORE_<KEY>_URL / _TOKEN (env_key: the rule the
    // bridge's coreset.EnvKey reads). Empty url/token are omitted so the
    // bridge's per-core auto-discovery fills them.
    let names: Vec<String> = s
        .cores
        .iter()
        .map(|c| c.name.trim())
        .filter(|n| !n.is_empty())
        .map(str::to_string)
        .collect();
    let mut names = names;
    for c in &s.cores {
        let name = c.name.trim();
        if name.is_empty() {
            continue;
        }
        if let Some(url) = c.url.as_deref().map(str::trim).filter(|v| !v.is_empty()) {
            env.insert(core_env_var(name, "URL"), url.to_string());
        }
        if let Some(token) = c.token.as_deref().map(str::trim).filter(|v| !v.is_empty()) {
            env.insert(core_env_var(name, "TOKEN"), token.to_string());
        }
    }
    // Desktop builds wire managed sidecar cores after user settings so
    // the verified package contract remains the source of truth for localUrl.
    names = crate::internal_ext::extend_core_env(adapter, &mut env, names, s);
    if !names.is_empty() {
        env.insert("BNBLOC_CORES".into(), names.join(","));
    }
    // Providers the user turned off, as comma-joined "core/provider".
    if !s.disabled_providers.is_empty() {
        env.insert(
            "BNBLOC_BRIDGE_DISABLED_PROVIDERS".into(),
            s.disabled_providers.join(","),
        );
    }
    if !s.sync_secret.trim().is_empty() {
        env.insert("BNBLOC_SYNC_SECRET".into(), s.sync_secret.clone());
    }
    // A static gateway bearer for enterprise-IdP (remote) gateways that reject
    // the bridge's dev login. Empty keeps the default owner-login path.
    if !s.gateway_token.trim().is_empty() {
        env.insert("BNBLOC_GATEWAY_TOKEN".into(), s.gateway_token.clone());
    }
    // The signed-in member's refresh token (memory only, from the web
    // session): the bridge mints and renews its own session from it.
    if let Some(token) = refresh_token.map(str::trim).filter(|t| !t.is_empty()) {
        env.insert(REFRESH_TOKEN_ENV.into(), token.to_string());
    }
    // Single-host mode: one embedded bridge drives every member's agent from the
    // one local core (local multi-user testing). Enabled by the persisted
    // setting, or by the BNBLOC_BRIDGE_SERVE_ALL_OWNERS env as a launch-time
    // escape hatch. Dev gateways only: activity honors serve-all claims only
    // with BNBLOC_ACTIVITY_ALLOW_SERVE_ALL=1 server-side and refuses them in
    // production, where each member runs their own bridge.
    if s.serve_all_owners || std::env::var("BNBLOC_BRIDGE_SERVE_ALL_OWNERS").is_ok() {
        env.insert("BNBLOC_BRIDGE_SERVE_ALL_OWNERS".into(), "1".into());
    }
    // The local sandbox (BNBLOC_SANDBOX_*): the mode its settings say
    // always, the sidecar URL/bearer/machine only while it is ready. Memory
    // only — the bearer is never written to settings or logged.
    crate::internal_ext::sandbox::extend_bridge_env(adapter, &mut env, s);
    env
}

/// The bridge's relay base for a gateway URL: its origin (scheme://host[:port])
/// with any path dropped. None for an empty or non-http(s) gateway.
fn relay_base_url(gateway_url: &str) -> Option<String> {
    crate::account::gateway_origin(gateway_url)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn env_for_emits_disabled_providers() {
        let s = Settings {
            disabled_providers: vec!["acme/cursor".into(), "orbit/x".into()],
            ..Default::default()
        };
        let env = env_for(&s, None, None);
        assert_eq!(
            env.get("BNBLOC_BRIDGE_DISABLED_PROVIDERS"),
            Some(&"acme/cursor,orbit/x".to_string())
        );

        // An empty disabled list omits the key entirely.
        let env = env_for(&Settings::default(), None, None);
        assert!(!env.contains_key("BNBLOC_BRIDGE_DISABLED_PROVIDERS"));
    }

    /// The env keys the shell writes are the ones the bridge reads: env_key
    /// and Go's coreset.EnvKey are pinned to one fixture, and every core
    /// override env_for emits goes through env_key.
    #[test]
    fn core_env_key_matches_go() {
        #[derive(serde::Deserialize)]
        struct Fixture {
            cases: Vec<Case>,
        }
        #[derive(serde::Deserialize)]
        struct Case {
            name: String,
            key: String,
        }
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../../internal/coreset/testdata/core_env_keys.json"
        ))
        .expect("fixture");
        assert!(!fixture.cases.is_empty());
        for case in fixture.cases {
            assert_eq!(
                crate::internal_ext::sidecar_util::env_key(&case.name),
                case.key,
                "env_key({:?})",
                case.name
            );
            let s = Settings {
                cores: vec![crate::settings::CoreConfig {
                    name: case.name.clone(),
                    kind: "local".into(),
                    url: Some("http://127.0.0.1:7000".into()),
                    token: Some("t".into()),
                }],
                ..Default::default()
            };
            let env = env_for(&s, None, None);
            assert_eq!(
                env.get(&format!("BNBLOC_CORE_{}_URL", case.key))
                    .map(String::as_str),
                Some("http://127.0.0.1:7000"),
                "{}: {env:?}",
                case.name
            );
            assert_eq!(
                env.get(&format!("BNBLOC_CORE_{}_TOKEN", case.key))
                    .map(String::as_str),
                Some("t")
            );
        }
    }

    #[test]
    fn env_for_carries_the_refresh_token_only_when_known() {
        let s = Settings::default();
        assert_eq!(
            env_for(&s, Some(" rt-1 "), None)
                .get(REFRESH_TOKEN_ENV)
                .map(String::as_str),
            Some("rt-1")
        );
        assert!(!env_for(&s, None, None).contains_key(REFRESH_TOKEN_ENV));
        assert!(!env_for(&s, Some("  "), None).contains_key(REFRESH_TOKEN_ENV));
    }

    #[test]
    fn refresh_restart_only_when_the_run_lacks_a_token() {
        // First handoff to a bridge spawned without one: restart.
        assert!(needs_refresh_restart(true, false, true));
        // A rotation while the run already has a token: the bridge renews
        // itself, no restart.
        assert!(!needs_refresh_restart(true, true, true));
        // Signed out (token cleared): never restart for it.
        assert!(!needs_refresh_restart(true, false, false));
        assert!(!needs_refresh_restart(true, true, false));
        // An explicitly stopped/unconfigured bridge is never started by it.
        assert!(!needs_refresh_restart(false, false, true));
    }

    #[test]
    fn relay_base_is_the_gateway_origin() {
        assert_eq!(
            relay_base_url("https://bnbloc.example.com/").as_deref(),
            Some("https://bnbloc.example.com")
        );
        // A gateway path prefix is dropped: /relay/* lives at the origin root.
        assert_eq!(
            relay_base_url(" http://localhost:7090/api ").as_deref(),
            Some("http://localhost:7090")
        );
        assert_eq!(relay_base_url(""), None);
        assert_eq!(relay_base_url("not a url"), None);
        assert_eq!(relay_base_url("file:///tmp/x"), None);

        let s = Settings {
            gateway_url: "https://gw.example:8443".into(),
            ..Default::default()
        };
        assert_eq!(
            env_for(&s, None, None)
                .get("BNBLOC_RELAY_BASE_URL")
                .map(String::as_str),
            Some("https://gw.example:8443")
        );
        assert!(
            !env_for(&Settings::default(), None, None).contains_key("BNBLOC_RELAY_BASE_URL"),
            "no gateway: no relay"
        );
    }

    #[test]
    fn env_for_says_the_sandbox_is_off_without_the_manager() {
        let env = env_for(&Settings::default(), None, None);
        assert_eq!(
            env.get("BNBLOC_SANDBOX_MODE").map(String::as_str),
            Some("off")
        );
        assert!(!env.contains_key("BNBLOC_SANDBOX_URL"));
        assert!(!env.contains_key("BNBLOC_SANDBOX_TOKEN"));
    }

    #[test]
    fn env_for_sets_no_core_specific_discovery_env() {
        // Core discovery is manifest data (a listener's defaultPath resolves on
        // the host), so the shell injects no per-core listener/rewrite env.
        let env = env_for(&Settings::default(), None, None);
        assert!(
            !env.keys()
                .any(|k| k.contains("LISTENER") || k.contains("HOST_REWRITE")),
            "unexpected core-specific discovery env: {:?}",
            env.keys().collect::<Vec<_>>()
        );
    }
}
