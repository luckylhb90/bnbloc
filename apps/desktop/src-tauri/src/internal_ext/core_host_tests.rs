//! The core host runs one process per core and build for every account that
//! holds it; these run real (shell) processes as the cores.
use std::time::Duration;

use super::*;
use crate::internal_ext::sidecar_model::{ComponentArtifact, ComponentKind};
use crate::internal_ext::sidecar_orphan::process_alive;

fn pin(seed: &str) -> Sha256Pin {
    Sha256Pin::of(seed.as_bytes())
}

/// A core spec whose process is a shell that sleeps, on a free loopback port.
fn spec(version: &str, build: &str, script: &str) -> SidecarSpec {
    let port = crate::internal_ext::loopback::bind_ephemeral()
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    SidecarSpec {
        kind: ComponentKind::Core,
        name: "acme".into(),
        version: version.into(),
        artifact: Some(ComponentArtifact {
            url: "https://example.invalid/acme".into(),
            sha256: pin(build),
        }),
        manifest: None,
        local_url: format!("http://127.0.0.1:{port}"),
        listen_env: String::new(),
        supervised_env: String::new(),
        args: vec!["-c".into(), script.into()],
        plugin: Default::default(),
        sandbox: Default::default(),
    }
}

fn host(tag: &str) -> (Arc<CoreHost>, PathBuf) {
    let root = std::env::temp_dir().join(format!("bnbloc-core-host-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    (CoreHost::new(Ok(Dirs::under(&root))), root)
}

const SH: &str = "/bin/sh";
const SLEEP: &str = "sleep 300";

async fn until(what: &str, mut done: impl FnMut() -> bool) {
    for _ in 0..400 {
        if done() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    panic!("timed out waiting for {what}");
}

async fn running(host: &CoreHost, holder: HolderId, id: &str) -> RunView {
    until("the core to run", || {
        host.view(holder, id)
            .is_some_and(|v| v.phase == Phase::Running)
    })
    .await;
    host.view(holder, id).unwrap()
}

/// Two accounts that pin the same build share one process; the second one
/// asking does not start another.
#[cfg(unix)]
#[tokio::test]
async fn accounts_pinning_one_build_share_one_process() {
    let (host, root) = host("share");
    let (maya, jack) = (host.register(), host.register());
    let s = spec("1.0.0", "build-1", SLEEP);
    let id = s.id();
    assert_eq!(host.hold(maya, 1, &s, SH.into()), Hold::Joined);
    let first = running(&host, maya, &id).await;
    assert_eq!(host.hold(jack, 1, &s, SH.into()), Hold::Joined);
    let second = host.view(jack, &id).unwrap();
    assert_eq!(first.pid, second.pid, "one process for both");
    assert_eq!(first.url, Some(s.local_url.clone()));
    // A holder sees only what it holds.
    assert!(host.view(host.register(), &id).is_none());
    host.release(maya, &id, None);
    host.release(jack, &id, None);
    let _ = std::fs::remove_dir_all(root);
}

/// A holder that leaves never stops the core for the others; the last one's
/// release does, and a stale generation's release changes nothing.
#[cfg(unix)]
#[tokio::test]
async fn the_last_holder_stops_the_core_and_no_other_does() {
    let (host, root) = host("release");
    let (maya, jack) = (host.register(), host.register());
    let s = spec("1.0.0", "build-1", SLEEP);
    let id = s.id();
    host.hold(maya, 1, &s, SH.into());
    let pid = running(&host, maya, &id).await.pid.unwrap();
    host.hold(jack, 1, &s, SH.into());

    // Maya's sidecar was replaced (generation 2 holds): the old release is
    // a no-op, and when she goes the core stays for Jack.
    host.hold(maya, 2, &s, SH.into());
    host.release(maya, &id, Some(1));
    assert!(
        host.view(maya, &id).is_some(),
        "an older generation's release is ignored"
    );
    host.release(maya, &id, Some(2));
    assert!(host.view(maya, &id).is_none());
    assert!(
        process_alive(pid),
        "the core runs on for the account that holds it"
    );
    assert_eq!(host.view(jack, &id).unwrap().pid, Some(pid));

    host.release(jack, &id, Some(1));
    until("the core to stop", || !process_alive(pid)).await;
    assert!(host.view(jack, &id).is_none());
    let _ = std::fs::remove_dir_all(root);
}

/// A core of another build is never served in silence: the second account is
/// told it differs, and gets in once the first is gone. A holder that is alone
/// may change the build (its own update).
#[cfg(unix)]
#[tokio::test]
async fn another_build_differs_unless_the_holder_is_alone() {
    let (host, root) = host("differs");
    let (maya, jack) = (host.register(), host.register());
    let old = spec("1.0.0", "build-1", SLEEP);
    let new = SidecarSpec {
        version: "2.0.0".into(),
        artifact: Some(ComponentArtifact {
            url: "https://example.invalid/acme".into(),
            sha256: pin("build-2"),
        }),
        local_url: old.local_url.clone(),
        ..old.clone()
    };
    let id = old.id();
    assert_eq!(host.hold(maya, 1, &old, SH.into()), Hold::Joined);
    let pid = running(&host, maya, &id).await.pid.unwrap();

    // Jack's catalog pins the newer build while Maya's account runs the old.
    assert_eq!(
        host.hold(jack, 1, &new, SH.into()),
        Hold::Differs {
            running: "1.0.0".into()
        }
    );
    assert!(
        host.view(jack, &id).is_none(),
        "a differing account holds nothing"
    );
    assert_eq!(
        host.view(maya, &id).unwrap().pid,
        Some(pid),
        "Maya's core is untouched"
    );

    // Maya alone updates to the new build: the old one stops, the new runs.
    host.release(maya, &id, None);
    until("the old build to stop", || !process_alive(pid)).await;
    assert_eq!(host.hold(jack, 1, &new, SH.into()), Hold::Joined);
    let newer = running(&host, jack, &id).await.pid.unwrap();
    assert_ne!(newer, pid);
    let alone = spec("3.0.0", "build-3", SLEEP);
    let alone = SidecarSpec {
        local_url: old.local_url.clone(),
        ..alone
    };
    assert_eq!(
        host.hold(jack, 2, &alone, SH.into()),
        Hold::Joined,
        "the only holder updates it"
    );
    until("the replaced build to stop", || !process_alive(newer)).await;
    let replaced = running(&host, jack, &id).await;
    assert_ne!(replaced.pid, Some(newer));
    host.release(jack, &id, None);
    let _ = std::fs::remove_dir_all(root);
}

/// A core that exits is run again, and its holders see it was gone.
#[cfg(unix)]
#[tokio::test]
async fn a_core_that_exits_is_restarted_for_its_holders() {
    let (host, root) = host("restart");
    let maya = host.register();
    let s = spec("1.0.0", "build-1", "echo going down; exit 3");
    let id = s.id();
    host.hold(maya, 1, &s, SH.into());
    until("the exit to be seen", || {
        host.view(maya, &id)
            .is_some_and(|v| v.phase == Phase::Waiting && v.message.contains("going down"))
    })
    .await;
    let message = host.view(maya, &id).unwrap().message;
    assert!(message.contains("restarting soon"), "{message}");
    host.release(maya, &id, None);
    let _ = std::fs::remove_dir_all(root);
}

/// Every account's running turns are counted: a core update restarts what
/// they all run on, so the update gate waits for all of them; a holder that
/// leaves counts none.
#[test]
fn the_update_gate_counts_every_accounts_turns() {
    let (host, _) = host("turns");
    let (maya, jack) = (host.register(), host.register());
    host.set_turns(maya, Arc::new(|| 2));
    host.set_turns(jack, Arc::new(|| 1));
    assert_eq!(host.running_turns(), 3);
    host.unregister(maya);
    assert_eq!(host.running_turns(), 1);
    host.unregister(jack);
    assert_eq!(host.running_turns(), 0);
}

/// A joint stop (app exit, an update install) takes every core's process and
/// leaves nothing held; a holder that comes back holds it anew.
#[cfg(unix)]
#[tokio::test]
async fn a_joint_stop_takes_every_core_process() {
    let (host, root) = host("detach");
    let maya = host.register();
    let s = spec("1.0.0", "build-1", SLEEP);
    let id = s.id();
    host.hold(maya, 1, &s, SH.into());
    let pid = running(&host, maya, &id).await.pid.unwrap();
    let children = host.detach_all();
    assert_eq!(children.len(), 1);
    assert!(
        host.view(maya, &id).is_none(),
        "nothing is held after a joint stop"
    );
    child_process::terminate(children, Duration::from_secs(2)).await;
    until("the process to be gone", || !process_alive(pid)).await;
    assert_eq!(host.hold(maya, 2, &s, SH.into()), Hold::Joined, "held anew");
    let again = running(&host, maya, &id).await.pid.unwrap();
    assert_ne!(again, pid);
    host.release(maya, &id, None);
    let _ = std::fs::remove_dir_all(root);
}
