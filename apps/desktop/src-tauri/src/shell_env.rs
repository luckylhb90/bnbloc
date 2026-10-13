//! The member's login-shell environment, for every child the desktop spawns.
//!
//! A GUI app started from Finder, the Dock or a desktop launcher never runs
//! the member's login shell, so what `~/.zshrc`/`~/.zprofile` export (API
//! keys, version-manager PATH entries) reaches no agent CLI
//! (docs/bugs/2026-10-13-desktop-agents-miss-shell-env.md). This module is
//! the one owner of that environment:
//!
//! - It is resolved once per app launch (the first child spawn waits for it)
//!   and again on "Reload shell environment" ([`reload`]): the member's
//!   `$SHELL` runs as an interactive login shell (`-ilc`) and prints its
//!   environment NUL-separated between a per-run random marker. stdin is
//!   `/dev/null`, the shell leads its own session (no controlling terminal to
//!   take over), and a run past [`RESOLVE_TIMEOUT`] is killed with its whole
//!   process group. Output outside the markers (rc banners) is ignored.
//! - A failed or timed-out run is logged without any value and the children
//!   get the app's own environment.
//! - Never imported: `BNBLOC_*` (the desktop's own handoff to its children)
//!   and shell-session names. PATH is merged: the shell's entries first, then
//!   the app's entries not already there.
//! - [`ShellEnv::apply`] (called only by `child_process::spawn`) sets it
//!   beneath a command's explicit env: what the desktop sets for a child
//!   always wins, and a child's own config applies later inside the child.
//!   Nothing the app sends over the network reads it.
//! - Logs carry the count of imported names (the names only in debug builds),
//!   never a value; `Debug` prints names only.
//! - Windows: a no-op (GUI apps there inherit the user's variables).
use std::collections::{BTreeMap, HashSet};
use std::ffi::{OsStr, OsString};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Arc;
use std::time::Duration;

/// How long the login shell may take before it is killed and the children
/// get the app's own environment.
pub(crate) const RESOLVE_TIMEOUT: Duration = Duration::from_secs(4);

/// Shell-session names that describe the resolving shell, not the member's
/// environment: a child would read them as its own.
const SESSION_NAMES: [&str; 5] = ["PWD", "OLDPWD", "SHLVL", "_", "TERM_SESSION_ID"];
const SESSION_PREFIX: &str = "SHELL_SESSION_";
/// The desktop's own namespace: never taken from the shell.
const OWN_PREFIX: &str = "BNBLOC_";

/// The imported variables. Constructed only here (resolution) and in tests;
/// `Debug` shows names, never values.
#[derive(Default, Clone, PartialEq, Eq)]
pub(crate) struct ShellEnv {
    vars: BTreeMap<String, String>,
}

impl std::fmt::Debug for ShellEnv {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ShellEnv")
            .field("names", &self.vars.keys().collect::<Vec<_>>())
            .finish()
    }
}

impl ShellEnv {
    /// The shell's `NAME=value` entries as children get them: session and
    /// `BNBLOC_*` names dropped, PATH merged with the app's `app_path`.
    fn from_entries(entries: Vec<(String, String)>, app_path: Option<&OsStr>) -> Self {
        let mut vars: BTreeMap<String, String> = entries
            .into_iter()
            .filter(|(name, _)| imported(name))
            .collect();
        if let Some(path) = vars.get_mut("PATH") {
            *path = merge_path(path, app_path);
        }
        Self { vars }
    }

    #[cfg(test)]
    pub(crate) fn for_test(entries: &[(&str, &str)], app_path: Option<&str>) -> Self {
        Self::from_entries(
            entries
                .iter()
                .map(|(n, v)| (n.to_string(), v.to_string()))
                .collect(),
            app_path.map(OsStr::new),
        )
    }

    pub(crate) fn len(&self) -> usize {
        self.vars.len()
    }

    #[cfg(test)]
    pub(crate) fn get(&self, name: &str) -> Option<&str> {
        self.vars.get(name).map(String::as_str)
    }

    /// Set every imported variable `cmd` does not set (or remove) itself:
    /// the command's explicit env always wins.
    pub(crate) fn apply(&self, cmd: &mut Command) {
        let explicit: HashSet<OsString> = cmd.get_envs().map(|(k, _)| k.to_os_string()).collect();
        for (name, value) in &self.vars {
            if !explicit.contains(OsStr::new(name)) {
                cmd.env(name, value);
            }
        }
    }
}

/// Whether a shell variable is handed to children.
fn imported(name: &str) -> bool {
    !name.is_empty()
        && !name.starts_with(OWN_PREFIX)
        && !name.starts_with(SESSION_PREFIX)
        && !SESSION_NAMES.contains(&name)
}

/// The shell's PATH entries first, then the app's entries not already in it;
/// empty and repeated entries dropped.
fn merge_path(shell: &str, app: Option<&OsStr>) -> String {
    let mut out: Vec<PathBuf> = Vec::new();
    let app = app.map(std::env::split_paths).into_iter().flatten();
    for entry in std::env::split_paths(shell).chain(app) {
        if !entry.as_os_str().is_empty() && !out.contains(&entry) {
            out.push(entry);
        }
    }
    std::env::join_paths(out)
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_else(|_| shell.to_string())
}

/// The `NAME=value` entries between the first two `marker`s of `output`
/// (NUL-separated, as `env -0` prints them). None when the closing marker
/// has not arrived.
#[cfg_attr(not(unix), allow(dead_code))]
fn parse(output: &[u8], marker: &[u8]) -> Option<Vec<(String, String)>> {
    let start = find(output, marker)? + marker.len();
    let len = find(&output[start..], marker)?;
    let body = &output[start..start + len];
    Some(
        body.split(|b| *b == 0)
            .filter_map(|entry| {
                let entry = std::str::from_utf8(entry).ok()?;
                let (name, value) = entry.split_once('=')?;
                Some((name.to_string(), value.to_string()))
            })
            .filter(|(name, _)| !name.is_empty())
            .collect(),
    )
}

#[cfg_attr(not(unix), allow(dead_code))]
fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack.windows(needle.len()).position(|w| w == needle)
}

/// Why a resolution imported nothing. Carries no output of the shell.
#[derive(Debug, Clone, PartialEq, Eq)]
#[cfg_attr(not(unix), allow(dead_code))]
pub(crate) enum Failure {
    Spawn(String),
    TimedOut(Duration),
    /// The shell ended without printing the closing marker.
    Exited(Option<i32>),
}

impl std::fmt::Display for Failure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Spawn(e) => write!(f, "could not start: {e}"),
            Self::TimedOut(t) => write!(f, "timed out after {}s", t.as_secs_f32()),
            Self::Exited(Some(code)) => write!(f, "exited {code} without an environment"),
            Self::Exited(None) => write!(f, "was killed without an environment"),
        }
    }
}

/// One resolution: what children get and how it went.
pub(crate) struct Resolution {
    pub(crate) env: ShellEnv,
    pub(crate) shell: PathBuf,
    pub(crate) outcome: Result<Duration, Failure>,
}

/// What a resolution logs: the shell, the count, the names only when
/// `with_names` (debug builds), never a value.
pub(crate) fn log_lines(res: &Resolution, with_names: bool) -> Vec<String> {
    let shell = res.shell.display();
    match &res.outcome {
        Ok(took) => {
            let mut lines = vec![format!(
                "imported {} variables from {shell} in {} ms",
                res.env.len(),
                took.as_millis()
            )];
            if with_names {
                let names: Vec<&str> = res.env.vars.keys().map(String::as_str).collect();
                lines.push(format!("names: {}", names.join(" ")));
            }
            lines
        }
        Err(failure) => vec![format!(
            "{shell} {failure}; children get the app's own environment"
        )],
    }
}

/// The member's login shell: `$SHELL` when it is an absolute path, else the
/// platform's default.
fn member_shell(shell_var: Option<OsString>) -> PathBuf {
    shell_var
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .unwrap_or_else(|| {
            PathBuf::from(if cfg!(target_os = "macos") {
                "/bin/zsh"
            } else {
                "/bin/sh"
            })
        })
}

/// Run `shell` as an interactive login shell and import its environment.
/// Blocking, bounded by `timeout`. Imports nothing on Windows.
pub(crate) fn resolve(shell: &Path, timeout: Duration) -> Resolution {
    let started = std::time::Instant::now();
    #[cfg(unix)]
    let outcome = unix::run(shell, timeout);
    #[cfg(not(unix))]
    let outcome: Result<Vec<(String, String)>, Failure> = {
        let _ = timeout;
        Ok(Vec::new())
    };
    let (env, outcome) = match outcome {
        Ok(entries) => (
            ShellEnv::from_entries(entries, std::env::var_os("PATH").as_deref()),
            Ok(started.elapsed()),
        ),
        Err(failure) => (ShellEnv::default(), Err(failure)),
    };
    Resolution {
        env,
        shell: shell.to_path_buf(),
        outcome,
    }
}

#[cfg(unix)]
mod unix {
    use std::io::Read;
    use std::os::unix::process::CommandExt;
    use std::path::Path;
    use std::process::{Command, Stdio};
    use std::sync::mpsc;
    use std::time::{Duration, Instant};

    use super::{parse, Failure};

    /// The per-run marker, printed in two halves so the command line itself
    /// (echoed by a shell's xtrace or an rc's history hook) never matches.
    fn marker() -> Result<(String, String), Failure> {
        let raw = crate::secret::random_bytes(12).map_err(Failure::Spawn)?;
        let hex: String = raw.iter().map(|b| format!("{b:02x}")).collect();
        let (a, b) = hex.split_at(hex.len() / 2);
        Ok((format!("__shell_env_{a}"), format!("{b}__")))
    }

    pub(super) fn run(shell: &Path, timeout: Duration) -> Result<Vec<(String, String)>, Failure> {
        let (a, b) = marker()?;
        let full = format!("{a}{b}");
        let print = format!("printf '%s%s' '{a}' '{b}'");
        let mut cmd = Command::new(shell);
        cmd.arg("-ilc")
            .arg(format!("{print}; /usr/bin/env -0; {print}"))
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::null());
        // SAFETY: setsid(2) is async-signal-safe and touches no memory of
        // this process. The shell then leads its own session and process
        // group: no controlling terminal for an interactive shell to grab,
        // and one group to kill on a timeout.
        unsafe {
            cmd.pre_exec(|| {
                if libc::setsid() == -1 {
                    return Err(std::io::Error::last_os_error());
                }
                Ok(())
            });
        }
        let mut child = cmd.spawn().map_err(|e| Failure::Spawn(e.to_string()))?;
        let mut stdout = child.stdout.take().expect("piped stdout");
        // Chunks as they arrive: a process the rc started may keep the pipe
        // open after the shell exits, so the closing marker ends the read,
        // not EOF.
        let (tx, rx) = mpsc::channel::<Vec<u8>>();
        std::thread::spawn(move || {
            let mut buf = [0u8; 16 * 1024];
            while let Ok(n) = stdout.read(&mut buf) {
                if n == 0 || tx.send(buf[..n].to_vec()).is_err() {
                    return;
                }
            }
        });
        let deadline = Instant::now() + timeout;
        let mut output = Vec::new();
        let pid = child.id();
        let kill_group = move || {
            if let Ok(pgid) = libc::pid_t::try_from(pid) {
                // SAFETY: kill(2) on the group the unreaped shell leads.
                unsafe {
                    libc::kill(-pgid, libc::SIGKILL);
                }
            }
        };
        loop {
            let left = deadline.saturating_duration_since(Instant::now());
            match rx.recv_timeout(left) {
                Ok(chunk) => {
                    output.extend_from_slice(&chunk);
                    if let Some(entries) = parse(&output, full.as_bytes()) {
                        // The shell exits by itself; reap it off this thread.
                        std::thread::spawn(move || {
                            let _ = child.wait();
                        });
                        return Ok(entries);
                    }
                }
                Err(mpsc::RecvTimeoutError::Timeout) => {
                    kill_group();
                    let _ = child.wait();
                    return Err(Failure::TimedOut(timeout));
                }
                Err(mpsc::RecvTimeoutError::Disconnected) => {
                    kill_group();
                    let code = child.wait().ok().and_then(|s| s.code());
                    return Err(Failure::Exited(code));
                }
            }
        }
    }
}

/// The member's shell environment, resolved now. Blocking.
fn resolve_member_shell() -> Resolution {
    resolve(&member_shell(std::env::var_os("SHELL")), RESOLVE_TIMEOUT)
}

static CURRENT: parking_lot::RwLock<Option<Arc<ShellEnv>>> = parking_lot::RwLock::new(None);
static FIRST: tokio::sync::OnceCell<()> = tokio::sync::OnceCell::const_new();

/// What a reload tells the member: how many names children now get, and
/// why none when the shell failed (never a name or a value).
#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct Reloaded {
    pub(crate) imported: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(crate) error: Option<String>,
}

async fn resolve_and_install() -> Reloaded {
    let res = tauri::async_runtime::spawn_blocking(resolve_member_shell)
        .await
        .unwrap_or_else(|e| Resolution {
            env: ShellEnv::default(),
            shell: PathBuf::new(),
            outcome: Err(Failure::Spawn(e.to_string())),
        });
    if cfg!(unix) {
        for line in log_lines(&res, cfg!(debug_assertions)) {
            eprintln!("[shell-env] {line}");
        }
    }
    let reloaded = Reloaded {
        imported: res.env.len(),
        error: res
            .outcome
            .err()
            .map(|failure| format!("{} {failure}", res.shell.display())),
    };
    *CURRENT.write() = Some(Arc::new(res.env));
    reloaded
}

/// The environment every child gets: resolved on the first call of the
/// app's life (concurrent callers wait for that one run), then the latest
/// [`reload`].
pub(crate) async fn current() -> Arc<ShellEnv> {
    FIRST
        .get_or_init(|| async {
            resolve_and_install().await;
        })
        .await;
    CURRENT.read().clone().unwrap_or_default()
}

/// Resolve again (the member exported something new); children spawned
/// from now on get the result.
pub(crate) async fn reload() -> Reloaded {
    current().await;
    resolve_and_install().await
}

#[cfg(test)]
mod tests {
    use super::*;

    const M: &[u8] = b"__shell_env_ab12cd__";

    fn out(parts: &[&[u8]]) -> Vec<u8> {
        parts.concat()
    }

    #[test]
    fn parses_nul_separated_entries_between_markers_and_ignores_noise() {
        let output = out(&[
            b"Welcome to the machine\nlast login: today\n",
            M,
            b"EXAMPLE_API_KEY=k-1\0MULTI=line one\nline two\0EQ=a=b\0EMPTY=\0junk-without-equals\0",
            M,
            b"\nbye from .zlogout\n",
        ]);
        let entries = parse(&output, M).expect("markers found");
        assert_eq!(
            entries,
            vec![
                ("EXAMPLE_API_KEY".into(), "k-1".into()),
                ("MULTI".into(), "line one\nline two".into()),
                ("EQ".into(), "a=b".into()),
                ("EMPTY".into(), String::new()),
            ]
        );
    }

    #[test]
    fn no_closing_marker_is_no_environment() {
        assert_eq!(parse(b"banner only", M), None);
        assert_eq!(parse(&out(&[M, b"A=1\0"]), M), None, "still arriving");
    }

    #[test]
    fn session_and_own_names_are_never_imported() {
        let env = ShellEnv::for_test(
            &[
                ("EXAMPLE_API_KEY", "k"),
                ("BNBLOC_SANDBOX_TOKEN", "x"),
                ("BNBLOC_GATEWAY_TOKEN", "x"),
                ("PWD", "/x"),
                ("OLDPWD", "/y"),
                ("SHLVL", "2"),
                ("_", "/usr/bin/env"),
                ("TERM_SESSION_ID", "w0t0p0"),
                ("SHELL_SESSION_DIR", "/z"),
                ("SHELL_SESSION_FILE", "/z/f"),
                ("HOME", "/Users/maya"),
            ],
            None,
        );
        let names: Vec<&str> = env.vars.keys().map(String::as_str).collect();
        assert_eq!(names, vec!["EXAMPLE_API_KEY", "HOME"]);
    }

    #[test]
    fn path_is_the_shells_first_then_the_apps_missing_entries() {
        let env = ShellEnv::for_test(
            &[(
                "PATH",
                "/Users/maya/.local/share/mise/shims:/opt/homebrew/bin:/usr/bin::/usr/bin",
            )],
            Some("/usr/bin:/bin:/opt/homebrew/bin:/app/only"),
        );
        assert_eq!(
            env.get("PATH"),
            Some("/Users/maya/.local/share/mise/shims:/opt/homebrew/bin:/usr/bin:/bin:/app/only")
        );
        // No shell PATH: the app's own is inherited untouched.
        assert_eq!(
            ShellEnv::for_test(&[("A", "1")], Some("/bin")).get("PATH"),
            None
        );
    }

    #[test]
    fn explicit_child_env_wins_over_the_shell() {
        let env = ShellEnv::for_test(
            &[
                ("EXAMPLE_API_KEY", "from-shell"),
                ("PORT", "1"),
                ("LANG", "en"),
            ],
            None,
        );
        let mut cmd = Command::new("true");
        cmd.env("PORT", "50123");
        cmd.env_remove("LANG");
        env.apply(&mut cmd);
        let set: BTreeMap<String, Option<String>> = cmd
            .get_envs()
            .map(|(k, v)| {
                (
                    k.to_string_lossy().into_owned(),
                    v.map(|v| v.to_string_lossy().into_owned()),
                )
            })
            .collect();
        assert_eq!(set["PORT"].as_deref(), Some("50123"));
        assert_eq!(set["LANG"], None, "an explicit removal stays removed");
        assert_eq!(set["EXAMPLE_API_KEY"].as_deref(), Some("from-shell"));
    }

    #[test]
    fn debug_shows_names_only() {
        let env = ShellEnv::for_test(&[("EXAMPLE_API_KEY", "sk-very-secret")], None);
        let shown = format!("{env:?}");
        assert!(shown.contains("EXAMPLE_API_KEY"), "{shown}");
        assert!(!shown.contains("sk-very-secret"), "{shown}");
    }

    #[test]
    fn member_shell_falls_back_when_unset_or_relative() {
        let default = if cfg!(target_os = "macos") {
            "/bin/zsh"
        } else {
            "/bin/sh"
        };
        assert_eq!(member_shell(None), PathBuf::from(default));
        assert_eq!(member_shell(Some("zsh".into())), PathBuf::from(default));
        assert_eq!(
            member_shell(Some("/usr/local/bin/fish".into())),
            PathBuf::from("/usr/local/bin/fish")
        );
    }

    #[cfg(unix)]
    mod live {
        use super::*;
        use std::os::unix::fs::PermissionsExt;

        /// A fake login shell: a script that prints noise, exports a secret
        /// and then runs the `-c` command the resolver hands it.
        fn fake_shell(dir: &Path, body: &str) -> PathBuf {
            let path = dir.join("fake-shell");
            std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).expect("write");
            std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).expect("chmod");
            path
        }

        fn temp_dir(tag: &str) -> PathBuf {
            let dir = std::env::temp_dir().join(format!(
                "bnbloc-shell-env-{tag}-{}",
                crate::secret::Secret::generate().unwrap().expose()
            ));
            std::fs::create_dir_all(&dir).expect("mkdir");
            dir
        }

        const SECRET: &str = "sk-live-0123456789";
        // Spelled through constants: the fixture rc file is test data, not
        // a credential assignment.
        const KEY_VAR: &str = "EXAMPLE_API_KEY";
        const BNBLOC_VAR: &str = "BNBLOC_GATEWAY_TOKEN";

        fn exporting_shell(dir: &Path) -> PathBuf {
            // "$1" is -ilc, "$2" the command.
            fake_shell(
                dir,
                &format!(
                    "echo 'motd: welcome'\n\
                     export {KEY_VAR}='{SECRET}'\n\
                     export {BNBLOC_VAR}=from-rc\n\
                     export PATH=\"/rc/bin:$PATH\"\n\
                     [ \"$1\" = -ilc ] || exit 9\n\
                     eval \"$2\"\n\
                     echo 'after the command'"
                ),
            )
        }

        #[test]
        fn a_login_shell_environment_is_imported() {
            let dir = temp_dir("ok");
            let res = resolve(&exporting_shell(&dir), Duration::from_secs(10));
            assert!(res.outcome.is_ok(), "{:?}", res.outcome.as_ref().err());
            assert_eq!(res.env.get("EXAMPLE_API_KEY"), Some(SECRET));
            assert_eq!(res.env.get("BNBLOC_GATEWAY_TOKEN"), None);
            assert_eq!(res.env.get("SHLVL"), None);
            assert!(res.env.get("PATH").unwrap().starts_with("/rc/bin"));
            let _ = std::fs::remove_dir_all(dir);
        }

        #[test]
        fn a_hanging_shell_times_out_and_imports_nothing() {
            let dir = temp_dir("hang");
            // An rc that never finishes (and a grandchild holding the pipe).
            let shell = fake_shell(&dir, "sleep 30 &\nwhile :; do sleep 1; done");
            let started = std::time::Instant::now();
            let res = resolve(&shell, Duration::from_millis(400));
            assert!(started.elapsed() < Duration::from_secs(5));
            assert_eq!(
                res.outcome,
                Err(Failure::TimedOut(Duration::from_millis(400)))
            );
            assert_eq!(res.env, ShellEnv::default());
            let _ = std::fs::remove_dir_all(dir);
        }

        #[test]
        fn a_failing_shell_imports_nothing() {
            let dir = temp_dir("fail");
            let res = resolve(
                &fake_shell(&dir, "echo oops; exit 3"),
                Duration::from_secs(10),
            );
            assert_eq!(res.outcome, Err(Failure::Exited(Some(3))));
            assert_eq!(res.env, ShellEnv::default());
            let missing = resolve(&dir.join("no-such-shell"), Duration::from_secs(1));
            assert!(matches!(missing.outcome, Err(Failure::Spawn(_))));
            let _ = std::fs::remove_dir_all(dir);
        }

        /// What the resolver logs: the count (names only when asked), never
        /// a value, on success and failure alike.
        #[test]
        fn no_value_is_ever_logged() {
            let dir = temp_dir("log");
            let ok = resolve(&exporting_shell(&dir), Duration::from_secs(10));
            let printed = fake_shell(&dir, &format!("echo '{SECRET}'; exit 1"));
            let failed = resolve(&printed, Duration::from_secs(10));
            for res in [&ok, &failed] {
                for with_names in [false, true] {
                    let log = log_lines(res, with_names).join("\n");
                    assert!(!log.contains(SECRET), "a value in the log: {log}");
                    assert!(!log.contains("/rc/bin"), "a value in the log: {log}");
                }
            }
            let quiet = log_lines(&ok, false).join("\n");
            assert!(quiet.contains("imported"), "{quiet}");
            assert!(
                !quiet.contains("EXAMPLE_API_KEY"),
                "names only in debug: {quiet}"
            );
            assert!(log_lines(&ok, true).join("\n").contains("EXAMPLE_API_KEY"));
            let _ = std::fs::remove_dir_all(dir);
        }
    }
}
