//! Apply a component update only when no agent turn runs
//! (docs/prd/2026-10-13-live-turns.md): a turn has no fixed length, and
//! swapping a core or the sandbox restarts what the turn runs on (and the
//! bridge, which re-reads its cores at spawn), so an update that arrives while
//! a turn runs waits as "pending" and applies at the first idle moment. No
//! ceiling is added; the member may apply it at once from the Agents page.
//!
//! The bridge records its running turns per core in its state directory
//! (`turns.json`, written by the bridge's turns_running.go); a file left by an
//! earlier bridge (another pid) counts nothing.
use std::collections::HashMap;
use std::path::Path;
use std::time::{Duration, Instant};

use super::sidecar_model::ComponentKind;

/// The bridge's running-turns file, in its state directory.
pub(crate) const TURNS_FILE: &str = "turns.json";

/// How often a pending update re-checks for an idle moment.
pub(crate) const RECHECK_EVERY: Duration = Duration::from_secs(30);

/// How long "apply now" overrides the wait (the swap and the bridge's
/// stabilized restart both happen within it).
pub(crate) const APPLY_NOW_WINDOW: Duration = Duration::from_secs(120);

/// `turns.json` as the bridge writes it.
#[derive(Debug, Default, serde::Deserialize)]
pub(crate) struct RunningTurns {
    #[serde(default)]
    pub(crate) pid: u32,
    #[serde(default)]
    pub(crate) running: HashMap<String, u32>,
    /// The running turns on the member's own sandbox machine
    /// (sandbox::OWN_MACHINE_NAME).
    #[serde(default, rename = "ownMachine")]
    pub(crate) own_machine: u32,
}

/// Read `turns.json` from the bridge's state directory; None when absent or
/// unreadable (a bridge that never ran a turn, or one that predates it).
pub(crate) fn read_running_turns(state_dir: &Path) -> Option<RunningTurns> {
    let raw = std::fs::read(state_dir.join(TURNS_FILE)).ok()?;
    serde_json::from_slice(&raw).ok()
}

/// How many turns the bridge running as `bridge_pid` drives now: the file's
/// count when it is that bridge's, else none (no bridge, or a file an
/// earlier bridge left behind).
pub(crate) fn running_turns(file: Option<&RunningTurns>, bridge_pid: Option<u32>) -> u32 {
    match (file, bridge_pid) {
        (Some(file), Some(pid)) if file.pid == pid => file.running.values().sum(),
        _ => 0,
    }
}

/// How many turns the bridge running as `bridge_pid` drives on the own
/// sandbox machine now (as `running_turns`).
pub(crate) fn own_machine_turns(file: Option<&RunningTurns>, bridge_pid: Option<u32>) -> u32 {
    match (file, bridge_pid) {
        (Some(file), Some(pid)) if file.pid == pid => file.own_machine,
        _ => 0,
    }
}

/// What to do with an update now.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Gate {
    Apply,
    /// Wait: this many turns run.
    Wait(u32),
}

/// An update of a sidecar of `kind` applies now unless turns run. A core or
/// the sandbox is what turns run on (and their swap restarts the bridge); a
/// plugin or previewer is neither.
pub(crate) fn gate(kind: ComponentKind, running: u32, apply_now: bool) -> Gate {
    let affects_turns = matches!(kind, ComponentKind::Core | ComponentKind::Sandbox);
    if affects_turns && running > 0 && !apply_now {
        Gate::Wait(running)
    } else {
        Gate::Apply
    }
}

/// A stabilized bridge restart (its cores or sandbox changed) waits while
/// turns run, like the update that caused it; a member or tenant switch
/// (`member_key` changed) never waits: the bridge must not keep acting for
/// the previous member.
pub(crate) fn bridge_restart_waits(
    current: &HashMap<String, String>,
    desired: &HashMap<String, String>,
    member_key: &str,
    running: u32,
    apply_now: bool,
) -> bool {
    running > 0 && !apply_now && current.get(member_key) == desired.get(member_key)
}

/// Whether "apply now" is in effect at `now`.
pub(crate) fn apply_now_active(until: Option<Instant>, now: Instant) -> bool {
    until.is_some_and(|until| now < until)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn turns(pid: u32, running: &[(&str, u32)]) -> RunningTurns {
        RunningTurns {
            pid,
            running: running.iter().map(|(k, v)| (k.to_string(), *v)).collect(),
            own_machine: 1,
        }
    }

    #[test]
    fn own_machine_turns_count_only_for_the_running_bridge() {
        let file = turns(41, &[("acp", 2)]);
        assert_eq!(own_machine_turns(Some(&file), Some(41)), 1);
        assert_eq!(own_machine_turns(Some(&file), Some(42)), 0);
        let old: RunningTurns = serde_json::from_str(r#"{"pid":41,"running":{"acp":1}}"#).unwrap();
        assert_eq!(
            own_machine_turns(Some(&old), Some(41)),
            0,
            "a bridge without the field"
        );
    }

    #[test]
    fn only_the_running_bridges_file_counts() {
        let file = turns(41, &[("acp", 2), ("nimbus", 1)]);
        assert_eq!(running_turns(Some(&file), Some(41)), 3);
        assert_eq!(
            running_turns(Some(&file), Some(42)),
            0,
            "an earlier bridge's file"
        );
        assert_eq!(running_turns(Some(&file), None), 0, "no bridge");
        assert_eq!(running_turns(None, Some(41)), 0, "no file");
    }

    #[test]
    fn core_and_sandbox_updates_wait_for_running_turns() {
        assert_eq!(gate(ComponentKind::Core, 2, false), Gate::Wait(2));
        assert_eq!(gate(ComponentKind::Sandbox, 1, false), Gate::Wait(1));
        assert_eq!(gate(ComponentKind::Core, 0, false), Gate::Apply, "idle");
        assert_eq!(gate(ComponentKind::Core, 3, true), Gate::Apply, "apply now");
        assert_eq!(
            gate(ComponentKind::Plugin, 3, false),
            Gate::Apply,
            "not what turns run on"
        );
    }

    #[test]
    fn a_bridge_restart_waits_unless_the_member_changed() {
        let key = "member";
        let current = HashMap::from([
            (key.to_string(), "/a".to_string()),
            ("acp".into(), "u1".into()),
        ]);
        let core_changed = HashMap::from([
            (key.to_string(), "/a".to_string()),
            ("acp".into(), "u2".into()),
        ]);
        let member_changed = HashMap::from([
            (key.to_string(), "/b".to_string()),
            ("acp".into(), "u1".into()),
        ]);
        assert!(bridge_restart_waits(&current, &core_changed, key, 1, false));
        assert!(
            !bridge_restart_waits(&current, &core_changed, key, 0, false),
            "idle"
        );
        assert!(
            !bridge_restart_waits(&current, &core_changed, key, 1, true),
            "apply now"
        );
        assert!(
            !bridge_restart_waits(&current, &member_changed, key, 1, false),
            "member switch"
        );
    }

    #[test]
    fn reads_the_bridges_file() {
        let dir = std::env::temp_dir().join(format!("bnbloc-turns-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(
            dir.join(TURNS_FILE),
            br#"{"pid":7,"running":{"acp":2},"updatedAtMs":1}"#,
        )
        .unwrap();
        let file = read_running_turns(&dir).expect("parsed");
        assert_eq!(running_turns(Some(&file), Some(7)), 2);
        std::fs::remove_dir_all(&dir).unwrap();
        assert!(read_running_turns(&dir).is_none());
    }

    #[test]
    fn apply_now_is_a_window() {
        let now = Instant::now();
        assert!(apply_now_active(Some(now + Duration::from_secs(5)), now));
        assert!(!apply_now_active(Some(now), now + Duration::from_secs(1)));
        assert!(!apply_now_active(None, now));
    }
}
