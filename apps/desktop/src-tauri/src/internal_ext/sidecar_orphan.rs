//! Occupied sidecar listeners: decide whether the process already bound to a
//! sidecar's fixed `localUrl` is reused, replaced, or waited on.
//!
//! A sidecar this app spawned in an earlier session can outlive it (the app
//! crashed or was force-quit, and that build of the sidecar ignored stdin
//! EOF). Such an orphan is unsupervised and may run an outdated artifact, so
//! reusing it forever would keep a newer published package from ever running.
//! The manager replaces it — but only with evidence this app spawned it, and
//! `owned_orphan` is the one rule for that evidence (fixed-port sidecars and
//! the sandbox alike). It takes BOTH:
//!
//! - a spawn record (`<app cache>/sidecar-pids/<id>.json`) written at spawn,
//!   removed when that child exits, naming the pid, the artifact sha256, and
//!   the app process that spawned it (another app process than this one);
//!   and
//! - the process's executable lives under this component's private cache
//!   root.
//!
//! A pid the OS reused after a crash or reboot fails the second check; a
//! child of this session (running, or replaced and still stopping) fails the
//! first. A process without that evidence is never signalled: a compatible
//! Core Protocol listener is reused as before, anything else waits.
//!
//! Versions are not compared: the package version is publisher-chosen and the
//! daemon's discover `version` is its own semver — different version spaces.
//! The artifact sha256 is the comparable identity.
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use crate::internal_ext::pin::Sha256Pin;
use crate::internal_ext::sidecar_util::{cache_stem, listen_addr, CoreListener};

/// What the app recorded when it spawned a sidecar child.
#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct SpawnRecord {
    pub(super) pid: u32,
    pub(super) sha256: Sha256Pin,
    /// The app process that spawned it: a record from the running app is not
    /// an orphan from an earlier session.
    pub(super) app_pid: u32,
}

/// A process this app spawned in an earlier session.
#[derive(Debug, PartialEq, Eq)]
pub(super) struct Orphan {
    pub(super) pid: u32,
    /// The artifact it runs (from its spawn record).
    pub(super) sha256: Sha256Pin,
}

/// What the manager does about an occupied fixed listener.
#[derive(Debug, PartialEq, Eq)]
pub(super) enum ListenerPlan {
    /// A compatible listener the app does not own: drive it in place.
    Reuse,
    /// Stop this orphan, then start the current artifact.
    Replace { pid: u32, message: String },
    /// Leave it alone; surface why the sidecar is not running.
    Wait(String),
}

/// The facts about one occupied listener, gathered by the runtime.
pub(super) struct OccupiedListener<'a> {
    pub(super) name: &'a str,
    pub(super) spec_version: &'a str,
    pub(super) spec_sha256: &'a Sha256Pin,
    pub(super) url: &'a str,
    /// The bind error that found the port taken.
    pub(super) bind_error: &'a str,
    pub(super) orphan: Option<Orphan>,
    /// None when the sidecar does not speak Core Protocol (never probed).
    pub(super) probe: Option<Result<Option<CoreListener>, String>>,
    pub(super) can_terminate: bool,
}

pub(super) fn plan_occupied_listener(facts: OccupiedListener<'_>) -> ListenerPlan {
    let name = facts.name;
    let discovered = match &facts.probe {
        Some(Ok(Some(listener))) if !listener.version.is_empty() => {
            format!("{name} {}", listener.version)
        }
        _ => name.to_string(),
    };
    if let Some(orphan) = facts.orphan {
        if !facts.can_terminate {
            return ListenerPlan::Wait(format!(
                "{discovered} from an earlier session is still running on {}; quit it to start {name} {}",
                facts.url, facts.spec_version
            ));
        }
        let reason = if &orphan.sha256 == facts.spec_sha256 {
            "no longer supervised"
        } else {
            "an outdated build"
        };
        return ListenerPlan::Replace {
            pid: orphan.pid,
            message: format!(
                "Replacing {discovered} left running from an earlier session on {} ({reason}) with {name} {}",
                facts.url, facts.spec_version
            ),
        };
    }
    match facts.probe {
        Some(Ok(Some(_))) => ListenerPlan::Reuse,
        Some(Err(probe_error)) => ListenerPlan::Wait(format!(
            "Waiting to verify {name} listener after {}: {probe_error}",
            facts.bind_error
        )),
        Some(Ok(None)) | None => {
            ListenerPlan::Wait(format!("Waiting to start {name}: {}", facts.bind_error))
        }
    }
}

/// The one orphan rule: `pid` is a process this app spawned in an EARLIER
/// session only when its spawn `record` names that pid and another app
/// process, AND its executable (`exe`, as the OS reports it) lives under the
/// component's private `root`. Either piece alone is not evidence: a record
/// survives a crash while the OS reuses the pid, and an executable path says
/// nothing about which session started it. Blocking (canonicalizes paths).
pub(super) fn owned_orphan(
    pid: u32,
    record: Option<&SpawnRecord>,
    exe: Option<&str>,
    root: &Path,
    app_pid: u32,
) -> Option<Orphan> {
    let record = record.filter(|r| r.pid == pid && r.app_pid != app_pid)?;
    let exe = PathBuf::from(exe?.trim().trim_end_matches(" (deleted)"));
    let parent = exe.parent()?;
    let parent = std::fs::canonicalize(parent).unwrap_or_else(|_| parent.to_path_buf());
    let root = std::fs::canonicalize(root).unwrap_or_else(|_| root.to_path_buf());
    (root.is_absolute() && parent.starts_with(&root)).then(|| Orphan {
        pid,
        sha256: record.sha256.clone(),
    })
}

fn record_path(dir: &Path, id: &str) -> PathBuf {
    dir.join(format!("{}.json", cache_stem(id)))
}

pub(super) fn write_spawn_record(dir: &Path, id: &str, record: &SpawnRecord) -> Result<(), String> {
    let raw = serde_json::to_vec(record).map_err(|e| format!("encode spawn record: {e}"))?;
    crate::files::atomic_write(&record_path(dir, id), &raw)
}

pub(super) fn read_spawn_record(dir: &Path, id: &str) -> Option<SpawnRecord> {
    crate::files::read_json(&record_path(dir, id))
        .ok()
        .flatten()
}

/// Remove `id`'s record only if it still names `pid`: a newer child of the
/// same component may already have replaced it.
pub(super) fn remove_spawn_record(dir: &Path, id: &str, pid: u32) {
    if read_spawn_record(dir, id).is_some_and(|record| record.pid == pid) {
        let _ = std::fs::remove_file(record_path(dir, id));
    }
}

/// The TCP port of a sidecar `localUrl`.
pub(super) fn listener_port(local_url: &str) -> Option<u16> {
    listen_addr(local_url)
        .ok()?
        .rsplit_once(':')?
        .1
        .parse()
        .ok()
}

/// Whether this platform can find and stop a listener's process.
pub(super) const CAN_TERMINATE: bool = cfg!(unix);

/// The pid listening on loopback `port`, best-effort (`lsof`; None when it is
/// unavailable or nothing is found).
#[cfg(unix)]
pub(super) fn listening_pid(port: u16) -> Option<u32> {
    // A GUI app's PATH is sparse; lsof lives in sbin on macOS.
    for lsof in ["/usr/sbin/lsof", "/usr/bin/lsof", "lsof"] {
        let Ok(out) = std::process::Command::new(lsof)
            .args(["-nP", "-t", &format!("-iTCP:{port}"), "-sTCP:LISTEN"])
            .output()
        else {
            continue;
        };
        return String::from_utf8_lossy(&out.stdout)
            .lines()
            .find_map(|line| line.trim().parse().ok());
    }
    None
}

#[cfg(not(unix))]
pub(super) fn listening_pid(_port: u16) -> Option<u32> {
    None
}

/// The executable path of `pid`, best-effort.
#[cfg(target_os = "linux")]
pub(super) fn process_exe(pid: u32) -> Option<String> {
    std::fs::read_link(format!("/proc/{pid}/exe"))
        .ok()
        .map(|path| path.to_string_lossy().into_owned())
}

#[cfg(all(unix, not(target_os = "linux")))]
pub(super) fn process_exe(pid: u32) -> Option<String> {
    let out = std::process::Command::new("/bin/ps")
        .args(["-o", "comm=", "-p", &pid.to_string()])
        .output()
        .ok()?;
    let exe = String::from_utf8_lossy(&out.stdout).trim().to_string();
    (!exe.is_empty()).then_some(exe)
}

#[cfg(not(unix))]
pub(super) fn process_exe(_pid: u32) -> Option<String> {
    None
}

/// Stop an orphan: SIGTERM (its process group when it leads one — the app
/// spawns every sidecar as a group leader, so its helpers go too), then
/// SIGKILL after `grace`. Blocking.
#[cfg(unix)]
pub(super) fn terminate_orphan(pid: u32, grace: Duration) -> Result<(), String> {
    let pid = libc::pid_t::try_from(pid).map_err(|_| format!("invalid pid {pid}"))?;
    // SAFETY: getpgid/kill take plain integers and touch no memory of ours.
    let alive = || unsafe { libc::kill(pid, 0) } == 0;
    let target = if unsafe { libc::getpgid(pid) } == pid {
        -pid
    } else {
        pid
    };
    let signal = |sig| unsafe { libc::kill(target, sig) };
    if signal(libc::SIGTERM) != 0 && alive() {
        return Err(format!(
            "signal pid {pid}: {}",
            std::io::Error::last_os_error()
        ));
    }
    let deadline = Instant::now() + grace;
    while alive() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    if alive() {
        signal(libc::SIGKILL);
        std::thread::sleep(Duration::from_millis(200));
    }
    if alive() {
        return Err(format!("pid {pid} did not exit"));
    }
    Ok(())
}

#[cfg(not(unix))]
pub(super) fn terminate_orphan(pid: u32, _grace: Duration) -> Result<(), String> {
    Err(format!("cannot stop pid {pid} on this platform"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::sidecar_util::cache_file_name;

    const SHA_OLD: &str = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    const SHA_NEW: &str = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";

    fn pin(sha: &str) -> Sha256Pin {
        Sha256Pin::parse(sha).unwrap()
    }
    const APP: u32 = 100;
    const URL: &str = "http://127.0.0.1:7100";

    fn facts(
        orphan: Option<Orphan>,
        probe: Option<Result<Option<CoreListener>, String>>,
        can_terminate: bool,
    ) -> OccupiedListener<'static> {
        OccupiedListener {
            name: "samplecore",
            spec_version: "2.0.0",
            spec_sha256: Box::leak(Box::new(pin(SHA_NEW))),
            url: URL,
            bind_error: "already in use",
            orphan,
            probe,
            can_terminate,
        }
    }

    fn compatible(version: &str) -> Option<Result<Option<CoreListener>, String>> {
        Some(Ok(Some(CoreListener {
            version: version.into(),
        })))
    }

    #[test]
    fn own_orphan_with_an_outdated_artifact_is_replaced() {
        let orphan = Orphan {
            pid: 42,
            sha256: pin(SHA_OLD),
        };
        match plan_occupied_listener(facts(Some(orphan), compatible("1.0.0"), true)) {
            ListenerPlan::Replace { pid, message } => {
                assert_eq!(pid, 42);
                assert!(
                    message.contains("samplecore 1.0.0") && message.contains("outdated"),
                    "{message}"
                );
            }
            other => panic!("expected replace, got {other:?}"),
        }
    }

    #[test]
    fn own_orphan_of_the_current_artifact_is_replaced_to_regain_supervision() {
        let orphan = Orphan {
            pid: 42,
            sha256: pin(SHA_NEW),
        };
        assert!(matches!(
            plan_occupied_listener(facts(Some(orphan), compatible("1.0.0"), true)),
            ListenerPlan::Replace { pid: 42, message } if message.contains("no longer supervised")
        ));
    }

    #[test]
    fn own_orphan_that_cannot_be_stopped_waits_with_guidance() {
        let orphan = Orphan {
            pid: 42,
            sha256: pin(SHA_OLD),
        };
        assert_eq!(
            plan_occupied_listener(facts(Some(orphan), compatible("1.0.0"), false)),
            ListenerPlan::Wait(format!(
                "samplecore 1.0.0 from an earlier session is still running on {URL}; quit it to start samplecore 2.0.0"
            ))
        );
    }

    #[test]
    fn foreign_listener_is_never_replaced() {
        assert_eq!(
            plan_occupied_listener(facts(None, compatible("9.9.9"), true)),
            ListenerPlan::Reuse
        );
        assert!(matches!(
            plan_occupied_listener(facts(None, Some(Ok(None)), true)),
            ListenerPlan::Wait(m) if m.starts_with("Waiting to start samplecore")
        ));
        assert!(matches!(
            plan_occupied_listener(facts(None, Some(Err("timeout".into())), true)),
            ListenerPlan::Wait(m) if m.contains("verify") && m.contains("timeout")
        ));
        // A non-Core-Protocol sidecar is never probed and never reused.
        assert!(matches!(
            plan_occupied_listener(facts(None, None, true)),
            ListenerPlan::Wait(_)
        ));
    }

    /// The orphan rule needs both pieces of evidence: a spawn record from an
    /// earlier app process naming the pid, and the pid's executable under the
    /// component's cache root.
    #[test]
    fn orphan_requires_exe_evidence() {
        let root = std::env::temp_dir().join(format!("bnbloc-orphan-root-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).expect("root");
        let exe = root.join(cache_file_name("samplecore", SHA_OLD));
        let exe = exe.to_string_lossy().into_owned();
        let record = SpawnRecord {
            pid: 42,
            sha256: pin(SHA_OLD),
            app_pid: 7,
        };

        // Both: an orphan of an earlier session, running the recorded build.
        assert_eq!(
            owned_orphan(42, Some(&record), Some(&exe), &root, APP),
            Some(Orphan {
                pid: 42,
                sha256: pin(SHA_OLD)
            })
        );
        let deleted = format!("{exe} (deleted)");
        assert!(owned_orphan(42, Some(&record), Some(&deleted), &root, APP).is_some());

        // A matching record but an executable outside the cache root: the OS
        // reused the pid for something else after a crash or reboot.
        for foreign in [
            "/usr/bin/python3".to_string(),
            format!("/usr/local/bin/{}", cache_file_name("samplecore", SHA_OLD)),
            "samplecore".to_string(),
        ] {
            assert_eq!(
                owned_orphan(42, Some(&record), Some(&foreign), &root, APP),
                None,
                "{foreign}"
            );
        }
        assert_eq!(owned_orphan(42, Some(&record), None, &root, APP), None);

        // Our executable but no record of an earlier session: never ours to
        // kill (a child of this session being replaced has no record left,
        // and a running one is recorded by this app).
        assert_eq!(owned_orphan(42, None, Some(&exe), &root, APP), None);
        let ours = SpawnRecord {
            app_pid: APP,
            ..record.clone()
        };
        assert_eq!(owned_orphan(42, Some(&ours), Some(&exe), &root, APP), None);
        // The record names another pid than the one in question.
        assert_eq!(
            owned_orphan(43, Some(&record), Some(&exe), &root, APP),
            None
        );
        // An empty root never contains anything.
        assert_eq!(
            owned_orphan(42, Some(&record), Some(&exe), Path::new(""), APP),
            None
        );
        std::fs::remove_dir_all(&root).expect("cleanup");
    }

    #[test]
    fn spawn_records_round_trip_and_are_removed_only_for_their_pid() {
        let dir = std::env::temp_dir().join(format!("bnbloc-spawn-record-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let record = SpawnRecord {
            pid: 42,
            sha256: pin(SHA_NEW),
            app_pid: APP,
        };
        write_spawn_record(&dir, "plugin/samplecore", &record).expect("write");
        assert_eq!(read_spawn_record(&dir, "plugin/samplecore"), Some(record));
        remove_spawn_record(&dir, "plugin/samplecore", 43);
        assert!(
            read_spawn_record(&dir, "plugin/samplecore").is_some(),
            "a newer child's record stays"
        );
        remove_spawn_record(&dir, "plugin/samplecore", 42);
        assert_eq!(read_spawn_record(&dir, "plugin/samplecore"), None);
        std::fs::remove_dir_all(&dir).expect("cleanup");
    }

    #[test]
    fn listener_port_reads_the_local_url_port() {
        assert_eq!(listener_port("http://127.0.0.1:7100"), Some(7100));
        assert_eq!(listener_port("http://localhost:7100/base"), Some(7100));
        assert_eq!(listener_port("http://localhost"), None);
    }
}
