//! The one supervised-child primitive shared by the bridge supervisor
//! (`bridge.rs`) and the component sidecar manager (`internal_ext`).
//!
//! A spawned child gets the member's login-shell environment
//! (`shell_env::ShellEnv`) beneath the env its caller sets, and piped
//! stdio: its stdout/stderr arrive as
//! [`ChildEvent::Line`]s and its exit as one final [`ChildEvent::Exited`]. The
//! shell keeps the child's stdin open for the child's whole lifetime; a
//! supervised child (bridge: `BNBLOC_BRIDGE_SUPERVISED`, sidecars: the
//! package's `supervisedEnv`) shuts down gracefully on stdin EOF. That is the
//! cross-platform graceful-stop signal, and it also fires when the shell dies
//! by SIGKILL, so children never outlive it.
//!
//! Stopping is graceful-then-forced: close stdin (and SIGTERM the child's
//! process group on Unix), wait up to a grace period, then force-kill the
//! whole tree. On Unix each child leads its own process group, so the stop
//! reaches anything the child spawned too. On Windows each child runs in its
//! own kill-on-close Job Object (`windows_job`): the force-kill terminates the
//! job, and the app exiting or crashing closes the job handle, which takes the
//! child's whole tree down with it.
#[cfg(windows)]
mod windows_job;

use std::future::Future;
use std::io::{BufRead, BufReader, Read};
use std::process::{ChildStdin, Command, Stdio};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use futures_util::future::{BoxFuture, FutureExt, Shared};
use shared_child::SharedChild;
use tokio::sync::mpsc;

use crate::shell_env::ShellEnv;

/// Grace period between the graceful stop signal and a force-kill.
pub(crate) const STOP_GRACE: Duration = Duration::from_secs(5);

/// Output lines buffered per child. A child that writes faster than its
/// supervisor reads loses lines (counted, then reported in one line), never
/// memory: the pumps never block the child.
const LINE_BUFFER: usize = 1024;

/// How long the exit event waits for the output pumps to drain. Bounded so a
/// grandchild that inherited (and keeps open) the stdout pipe cannot hold the
/// exit event — and with it the crash-restart path — hostage.
const OUTPUT_DRAIN: Duration = Duration::from_millis(500);

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum ChildEvent {
    /// One non-empty stdout/stderr line (trailing newline stripped).
    Line(String),
    /// The child exited; `None` when it was terminated by a signal.
    Exited(Option<i32>),
}

/// A running child: a shared wait/kill handle plus the stdin pipe the shell
/// holds open until it wants the child to stop.
pub(crate) struct Child {
    shared: Arc<SharedChild>,
    stdin: Option<ChildStdin>,
    /// The child's kill-on-close job: dropping it ends the child's tree.
    #[cfg(windows)]
    job: windows_job::Job,
}

/// Spawn `cmd` with piped stdio and the member's shell environment beneath
/// its explicit env (`shell_env`: what `cmd` sets wins). Returns the event
/// stream and the handle.
pub(crate) fn spawn(
    mut cmd: Command,
    env: &ShellEnv,
) -> std::io::Result<(mpsc::Receiver<ChildEvent>, Child)> {
    env.apply(&mut cmd);
    cmd.stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    #[cfg(unix)]
    let shared = {
        use std::os::unix::process::CommandExt;
        // Lead a fresh process group so stop/kill reaches the whole tree.
        cmd.process_group(0);
        Arc::new(SharedChild::spawn(&mut cmd)?)
    };
    #[cfg(windows)]
    let (shared, job) = {
        let (child, job) = windows_job::spawn_in_job(&mut cmd)?;
        (Arc::new(SharedChild::new(child)?), job)
    };
    let stdin = shared.take_stdin();
    let (tx, rx) = mpsc::channel(LINE_BUFFER);
    let dropped = Arc::new(AtomicUsize::new(0));
    let mut pumps = Vec::new();
    if let Some(out) = shared.take_stdout() {
        pumps.push(pump_lines(out, tx.clone(), Arc::clone(&dropped)));
    }
    if let Some(err) = shared.take_stderr() {
        pumps.push(pump_lines(err, tx.clone(), Arc::clone(&dropped)));
    }
    let waiter = shared.clone();
    std::thread::spawn(move || {
        let code = waiter.wait().ok().and_then(|status| status.code());
        let deadline = Instant::now() + OUTPUT_DRAIN;
        for pump in &pumps {
            while !pump.is_finished() && Instant::now() < deadline {
                std::thread::sleep(Duration::from_millis(10));
            }
        }
        // A loss no later line reported, then the exit: never dropped, they
        // wait for room (the supervisor reads until the exit; a dropped
        // receiver ends the wait).
        let lost = dropped.swap(0, Ordering::SeqCst);
        if lost > 0 {
            let _ = tx.blocking_send(dropped_note(lost));
        }
        let _ = tx.blocking_send(ChildEvent::Exited(code));
    });
    Ok((
        rx,
        Child {
            shared,
            stdin,
            #[cfg(windows)]
            job,
        },
    ))
}

fn dropped_note(n: usize) -> ChildEvent {
    ChildEvent::Line(format!("[{n} output lines dropped]"))
}

/// Forward `reader`'s lines without ever blocking the child: a line that
/// finds the buffer full is counted in `dropped` and reported by the next
/// line that fits (or by the exit).
fn pump_lines<R: Read + Send + 'static>(
    reader: R,
    tx: mpsc::Sender<ChildEvent>,
    dropped: Arc<AtomicUsize>,
) -> std::thread::JoinHandle<()> {
    std::thread::spawn(move || {
        let mut reader = BufReader::new(reader);
        let mut buf = Vec::new();
        loop {
            buf.clear();
            match reader.read_until(b'\n', &mut buf) {
                Ok(0) | Err(_) => return,
                Ok(_) => {
                    let line = String::from_utf8_lossy(&buf);
                    let line = line.trim_end_matches(['\r', '\n']);
                    if line.trim().is_empty() {
                        continue;
                    }
                    let lost = dropped.swap(0, Ordering::SeqCst);
                    if lost > 0 {
                        match tx.try_send(dropped_note(lost)) {
                            Ok(()) => {}
                            Err(mpsc::error::TrySendError::Full(_)) => {
                                dropped.fetch_add(lost + 1, Ordering::SeqCst);
                                continue;
                            }
                            Err(mpsc::error::TrySendError::Closed(_)) => return,
                        }
                    }
                    match tx.try_send(ChildEvent::Line(line.to_string())) {
                        Ok(()) => {}
                        Err(mpsc::error::TrySendError::Full(_)) => {
                            dropped.fetch_add(1, Ordering::SeqCst);
                        }
                        Err(mpsc::error::TrySendError::Closed(_)) => return,
                    }
                }
            }
        }
    })
}

impl Child {
    pub(crate) fn pid(&self) -> u32 {
        self.shared.id()
    }

    fn exited(&self) -> bool {
        !matches!(self.shared.try_wait(), Ok(None))
    }

    /// The graceful stop signal: close stdin (a supervised child exits on
    /// EOF) and, on Unix, SIGTERM the child's process group. Windows has no
    /// console-less equivalent of SIGTERM: stdin EOF is the whole signal there.
    fn request_stop(&mut self) {
        drop(self.stdin.take());
        #[cfg(unix)]
        self.signal_group(libc::SIGTERM);
    }

    fn force_kill(&self) {
        #[cfg(unix)]
        self.signal_group(libc::SIGKILL);
        #[cfg(windows)]
        self.job.terminate();
        let _ = self.shared.kill();
    }

    /// Signal the child's process group, but only while the group leader is
    /// still unreaped: its pid (== pgid) cannot have been recycled then.
    #[cfg(unix)]
    fn signal_group(&self, signal: libc::c_int) {
        if self.exited() {
            return;
        }
        let Ok(pgid) = libc::pid_t::try_from(self.pid()) else {
            return;
        };
        // SAFETY: kill(2) with a negative pid signals that process group; it
        // touches no memory owned by this process.
        unsafe {
            libc::kill(-pgid, signal);
        }
    }
}

/// Stop every child gracefully, in parallel, within one shared `grace`
/// deadline; force-kill the ones still running when it passes. Blocking.
pub(crate) fn terminate_all(mut children: Vec<Child>, grace: Duration) {
    if children.is_empty() {
        return;
    }
    for child in &mut children {
        child.request_stop();
    }
    let deadline = Instant::now() + grace;
    while Instant::now() < deadline && children.iter().any(|child| !child.exited()) {
        std::thread::sleep(Duration::from_millis(25));
    }
    for child in &children {
        if !child.exited() {
            child.force_kill();
        }
    }
}

/// [`terminate_all`] on a blocking thread, started immediately. The stop runs
/// to completion even if nobody awaits the handle (or the awaiting task is
/// aborted); await it to know every child is gone.
fn terminate_detached(
    children: Vec<Child>,
    grace: Duration,
) -> tauri::async_runtime::JoinHandle<()> {
    tauri::async_runtime::spawn_blocking(move || terminate_all(children, grace))
}

/// [`terminate_all`] off the async runtime, resolving once every child is
/// gone (the updater's joint stop before an install).
pub(crate) async fn terminate(children: Vec<Child>, grace: Duration) {
    let _ = terminate_detached(children, grace).await;
}

type PendingStop = Shared<BoxFuture<'static, ()>>;

/// The stops still in progress, by what they stop (the bridge, a sidecar
/// id), so a relaunch never overlaps the run it replaces. Every stop of a
/// supervised child goes through `stop`; a launch first awaits `stopped`
/// for its key. A pending stop stays listed until it finishes, so a
/// relaunch whose task is aborted (a second start right after the first)
/// loses nothing: the next one still waits for it.
#[derive(Clone)]
pub(crate) struct Stopping {
    grace: Duration,
    pending: Arc<parking_lot::Mutex<Vec<(String, PendingStop)>>>,
}

impl Default for Stopping {
    fn default() -> Self {
        Self::with_grace(STOP_GRACE)
    }
}

impl Stopping {
    pub(crate) fn with_grace(grace: Duration) -> Self {
        Self {
            grace,
            pending: Arc::default(),
        }
    }

    /// Stop `children` of `key` gracefully in the background.
    pub(crate) fn stop(&self, key: &str, children: Vec<Child>) {
        if children.is_empty() {
            return;
        }
        let handle = terminate_detached(children, self.grace);
        let done = async move {
            let _ = handle.await;
        }
        .boxed()
        .shared();
        let mut pending = self.pending.lock();
        pending.retain(|(_, stop)| stop.peek().is_none());
        pending.push((key.to_string(), done));
    }

    /// Resolves once every stop of `key` started so far has finished.
    pub(crate) fn stopped(&self, key: &str) -> impl Future<Output = ()> + Send + 'static {
        let waits: Vec<PendingStop> = self
            .pending
            .lock()
            .iter()
            .filter(|(k, _)| k == key)
            .map(|(_, stop)| stop.clone())
            .collect();
        async move {
            for stop in waits {
                stop.await;
            }
        }
    }
}

/// How a supervised child is restarted after it exits: the crash backoff,
/// and whether a clean exit ends supervision. The two supervised kinds
/// diverge on purpose: the bridge exits 0 only when it decides to stop, so a
/// clean exit is final; a component sidecar runs for as long as it is
/// installed, so any exit restarts it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RestartPolicy {
    min: Duration,
    max: Duration,
    /// A run that stayed up this long resets the backoff.
    stable_after: Duration,
    clean_exit_is_final: bool,
}

impl RestartPolicy {
    pub(crate) const BRIDGE: Self = Self {
        min: Duration::from_secs(1),
        max: Duration::from_secs(30),
        stable_after: Duration::from_secs(60),
        clean_exit_is_final: true,
    };
    pub(crate) const SIDECAR: Self = Self {
        clean_exit_is_final: false,
        ..Self::BRIDGE
    };

    pub(crate) fn backoff(&self) -> Backoff {
        Backoff::new(self.min, self.max, self.stable_after)
    }

    /// Whether a run that ended with `exit` is started again.
    pub(crate) fn restarts_after(&self, exit: Option<i32>) -> bool {
        !(self.clean_exit_is_final && exit == Some(0))
    }
}

/// Crash-restart backoff shared by the bridge supervisor and the sidecar
/// manager. The delay doubles on every quick failure and resets only after a
/// run that stayed up for `stable_after` — a process that crashes a few
/// seconds after every start must not be respawned at the minimum delay
/// forever.
#[derive(Debug, Clone)]
pub(crate) struct Backoff {
    current: Duration,
    min: Duration,
    max: Duration,
    stable_after: Duration,
}

impl Backoff {
    pub(crate) fn new(min: Duration, max: Duration, stable_after: Duration) -> Self {
        Self {
            current: min,
            min,
            max,
            stable_after,
        }
    }

    /// The delay before the next attempt, given how long the last attempt ran
    /// (`Duration::ZERO` for an attempt that never started).
    pub(crate) fn next(&mut self, ran_for: Duration) -> Duration {
        if ran_for >= self.stable_after {
            self.current = self.min;
        }
        let delay = self.current;
        self.current = (self.current * 2).min(self.max);
        delay
    }

    pub(crate) fn reset(&mut self) {
        self.current = self.min;
    }
}

#[cfg(all(test, windows))]
mod windows_tests {
    use super::*;

    fn spawn(cmd: Command) -> std::io::Result<(mpsc::Receiver<ChildEvent>, Child)> {
        super::spawn(cmd, &ShellEnv::default())
    }

    fn wait_for(mut done: impl FnMut() -> bool, within: Duration) -> bool {
        let deadline = Instant::now() + within;
        while Instant::now() < deadline {
            if done() {
                return true;
            }
            std::thread::sleep(Duration::from_millis(25));
        }
        done()
    }

    #[test]
    fn force_kill_takes_down_grandchildren_through_the_job() {
        // cmd.exe spawns ping as a grandchild; neither reads stdin, so the
        // graceful stop is ignored and the job termination must end both.
        let mut cmd = Command::new("cmd.exe");
        cmd.args(["/C", "ping -n 60 127.0.0.1 >NUL"]);
        let (_rx, mut child) = spawn(cmd).expect("spawn cmd");
        assert!(
            wait_for(
                || child.job.active_processes().unwrap_or(0) >= 2,
                Duration::from_secs(10)
            ),
            "cmd and its ping grandchild run inside the job"
        );
        child.request_stop();
        child.force_kill();
        assert!(
            wait_for(
                || child.job.active_processes().ok() == Some(0),
                Duration::from_secs(5)
            ),
            "the job is empty after a force kill"
        );
        assert!(wait_for(|| child.exited(), Duration::from_secs(5)));
    }

    #[test]
    fn graceful_stop_closes_stdin_before_force_kill() {
        // `sort` reads stdin until EOF, then exits: the graceful path.
        let (_rx, child) = spawn(Command::new("sort.exe")).expect("spawn sort");
        let started = Instant::now();
        terminate_all(vec![child], Duration::from_secs(10));
        assert!(started.elapsed() < Duration::from_secs(5));
    }
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;

    fn spawn(cmd: Command) -> std::io::Result<(mpsc::Receiver<ChildEvent>, Child)> {
        super::spawn(cmd, &ShellEnv::default())
    }

    fn sh(script: &str) -> Command {
        let mut cmd = Command::new("/bin/sh");
        cmd.args(["-c", script]);
        cmd
    }

    /// A child sees the shell's variables, and what its spawner sets for it
    /// wins over them.
    #[tokio::test]
    async fn children_get_the_shell_env_beneath_their_explicit_env() {
        let env = ShellEnv::for_test(
            &[
                ("EXAMPLE_API_KEY", "from-shell"),
                ("EXAMPLE_PORT", "from-shell"),
                ("BNBLOC_BRIDGE_OWNER", "from-shell"),
            ],
            None,
        );
        let mut cmd = sh("echo \"$EXAMPLE_API_KEY|$EXAMPLE_PORT|$BNBLOC_BRIDGE_OWNER\"");
        cmd.env("EXAMPLE_PORT", "explicit");
        cmd.env("BNBLOC_BRIDGE_OWNER", "maya");
        let (rx, _child) = super::spawn(cmd, &env).expect("spawn");
        let events = collect(rx).await;
        assert_eq!(
            events.first(),
            Some(&ChildEvent::Line("from-shell|explicit|maya".into())),
            "{events:?}"
        );
    }

    async fn collect(mut rx: mpsc::Receiver<ChildEvent>) -> Vec<ChildEvent> {
        let mut events = Vec::new();
        while let Some(event) = rx.recv().await {
            let done = matches!(event, ChildEvent::Exited(_));
            events.push(event);
            if done {
                break;
            }
        }
        events
    }

    #[tokio::test]
    async fn output_lines_precede_the_exit_event() {
        let (rx, _child) = spawn(sh("echo one; echo; echo two 1>&2; exit 3")).expect("spawn");
        let events = collect(rx).await;
        let lines: Vec<_> = events
            .iter()
            .filter_map(|e| match e {
                ChildEvent::Line(l) => Some(l.as_str()),
                _ => None,
            })
            .collect();
        assert!(
            lines.contains(&"one") && lines.contains(&"two"),
            "{events:?}"
        );
        assert_eq!(events.last(), Some(&ChildEvent::Exited(Some(3))));
    }

    #[test]
    fn graceful_stop_closes_stdin_before_force_kill() {
        // `cat` exits on stdin EOF: the graceful path, well inside the grace.
        let (_rx, child) = spawn(Command::new("cat")).expect("spawn cat");
        let started = Instant::now();
        terminate_all(vec![child], Duration::from_secs(10));
        assert!(started.elapsed() < Duration::from_secs(5));
    }

    #[test]
    fn child_ignoring_stop_signals_is_force_killed_after_grace() {
        let (mut rx, child) = spawn(sh(
            "trap '' TERM; exec 0</dev/null; echo ready; while :; do sleep 1; done",
        ))
        .expect("spawn");
        // Stop only once the trap is installed.
        assert_eq!(rx.blocking_recv(), Some(ChildEvent::Line("ready".into())));
        let pid = child.pid();
        let started = Instant::now();
        terminate_all(vec![child], Duration::from_millis(300));
        assert!(started.elapsed() >= Duration::from_millis(300));
        // The group is gone: signal 0 to it fails (allow the reaper a moment).
        let deadline = Instant::now() + Duration::from_secs(3);
        let mut alive = true;
        while alive && Instant::now() < deadline {
            alive = unsafe { libc::kill(-(pid as libc::pid_t), 0) } == 0;
            std::thread::sleep(Duration::from_millis(20));
        }
        assert!(!alive, "process group {pid} still alive after force kill");
    }

    /// A child that floods its output while nobody reads loses lines, never
    /// its exit, and the loss is reported.
    #[tokio::test]
    async fn output_is_bounded_and_the_exit_always_arrives() {
        let (rx, _child) = spawn(sh(
            "i=0; while [ $i -lt 5000 ]; do echo line-$i; i=$((i+1)); done; exit 7",
        ))
        .expect("spawn");
        // Let the child finish while the buffer fills up.
        tokio::time::sleep(Duration::from_millis(1500)).await;
        let events = collect(rx).await;
        assert_eq!(events.last(), Some(&ChildEvent::Exited(Some(7))));
        assert!(events.len() <= LINE_BUFFER + 2, "{} events", events.len());
        assert!(
            events
                .iter()
                .any(|e| matches!(e, ChildEvent::Line(l) if l.ends_with("output lines dropped]"))),
            "the loss is reported"
        );
    }

    /// A relaunch waits for the stop of the run it replaces, even when the
    /// first relaunch's task was aborted, and never for another key's.
    #[tokio::test(flavor = "multi_thread")]
    async fn relaunch_waits_for_previous_stop() {
        let (mut rx, child) = spawn(sh(
            "trap '' TERM; exec 0</dev/null; echo ready; while :; do sleep 1; done",
        ))
        .expect("spawn");
        assert_eq!(rx.recv().await, Some(ChildEvent::Line("ready".into())));
        let pid = child.pid() as libc::pid_t;
        let alive = move || unsafe { libc::kill(-pid, 0) } == 0;
        let stopping = Stopping::with_grace(Duration::from_millis(400));
        stopping.stop("bridge", vec![child]);

        // The first relaunch is aborted while it waits (a second start).
        let aborted = tokio::spawn(stopping.stopped("bridge"));
        aborted.abort();
        // Another key never waits for this stop.
        let other = Instant::now();
        stopping.stopped("sidecar").await;
        assert!(other.elapsed() < Duration::from_millis(200));

        let started = Instant::now();
        stopping.stopped("bridge").await;
        assert!(
            started.elapsed() >= Duration::from_millis(300),
            "waited for the stop"
        );
        let deadline = Instant::now() + Duration::from_secs(2);
        while alive() && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(20));
        }
        assert!(!alive(), "the replaced run is gone before the relaunch");
        // Finished stops are not waited for again.
        let again = Instant::now();
        stopping.stopped("bridge").await;
        assert!(again.elapsed() < Duration::from_millis(100));
    }

    #[test]
    fn restart_policies_diverge_only_on_a_clean_exit() {
        for policy in [RestartPolicy::BRIDGE, RestartPolicy::SIDECAR] {
            assert!(policy.restarts_after(Some(1)));
            assert!(policy.restarts_after(None), "killed by a signal");
            let mut b = policy.backoff();
            assert_eq!(b.next(Duration::ZERO), Duration::from_secs(1));
        }
        assert!(!RestartPolicy::BRIDGE.restarts_after(Some(0)));
        assert!(RestartPolicy::SIDECAR.restarts_after(Some(0)));
    }

    #[test]
    fn backoff_grows_on_quick_failures_and_resets_after_a_stable_run() {
        let mut b = Backoff::new(
            Duration::from_secs(1),
            Duration::from_secs(30),
            Duration::from_secs(60),
        );
        // A crash-loop (each run lasts 2s, past any settle window) keeps growing.
        let delays: Vec<u64> = (0..7)
            .map(|_| b.next(Duration::from_secs(2)).as_secs())
            .collect();
        assert_eq!(delays, vec![1, 2, 4, 8, 16, 30, 30]);
        // A run that stayed up past the stability window starts over.
        assert_eq!(b.next(Duration::from_secs(120)).as_secs(), 1);
        assert_eq!(b.next(Duration::ZERO).as_secs(), 2);
        b.reset();
        assert_eq!(b.next(Duration::ZERO).as_secs(), 1);
    }
}
