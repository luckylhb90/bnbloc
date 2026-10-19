//! Architecture tests: the desktop rules with one owner (AGENTS.md, "One
//! rule, one owner") are not written a second time elsewhere in the shell.
use std::path::{Path, PathBuf};

/// Every non-test source of the crate: `(path relative to src, text)` with
/// `#[cfg(test)]` code cut off and `*_tests.rs` files left out.
fn sources() -> Vec<(String, String)> {
    fn walk(dir: &Path, out: &mut Vec<PathBuf>) {
        for entry in std::fs::read_dir(dir).expect("read src").flatten() {
            let path = entry.path();
            if path.is_dir() {
                walk(&path, out);
            } else if path.extension().is_some_and(|e| e == "rs") {
                out.push(path);
            }
        }
    }
    let src = Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
    let mut files = Vec::new();
    walk(&src, &mut files);
    files.sort();
    files
        .into_iter()
        .filter(|p| !p.to_string_lossy().ends_with("_tests.rs"))
        .map(|p| {
            let text = std::fs::read_to_string(&p).expect("read source");
            // Test modules come last; the first one ends the code.
            let end = [
                "#[cfg(test)]\nmod tests {",
                "#[cfg(all(test, unix))]\nmod ",
                "#[cfg(all(test, windows))]\nmod ",
            ]
            .iter()
            .filter_map(|marker| text.find(marker))
            .min()
            .unwrap_or(text.len());
            let code = text[..end].to_string();
            let rel = p
                .strip_prefix(&src)
                .unwrap()
                .to_string_lossy()
                .replace('\\', "/");
            (rel, code)
        })
        .collect()
}

/// `needle` appears in non-test code only in `owner`.
fn only_in(needle: &str, owner: &str) {
    only_in_any(needle, &[owner]);
}

/// `needle` appears in non-test code only in `owners`.
fn only_in_any(needle: &str, owners: &[&str]) {
    let offenders: Vec<String> = sources()
        .into_iter()
        .filter(|(rel, code)| !owners.contains(&rel.as_str()) && code.contains(needle))
        .map(|(rel, _)| rel)
        .collect();
    assert!(
        offenders.is_empty(),
        "{needle:?} belongs to {owners:?} alone; also in {offenders:?}"
    );
}

#[test]
fn the_scan_sees_the_shell_sources() {
    let sources = sources();
    let code = |rel: &str| {
        sources
            .iter()
            .find(|(r, _)| r == rel)
            .map(|(_, c)| c.as_str())
            .unwrap_or_default()
    };
    assert!(code("internal_ext.rs").contains("fn extend_core_env"));
    assert!(code("bridge.rs").contains("fn env_for"));
    assert!(!code("internal_ext/sidecar_orphan.rs").contains("fn orphan_requires_exe_evidence"));
}

#[test]
fn core_env_vars_are_named_only_by_core_env_var() {
    only_in("\"BNBLOC_CORE_{", "internal_ext/sidecar_util.rs");
}

#[test]
fn member_secret_dirs_are_named_only_by_member_dir() {
    for name in [
        "\"component-settings\"",
        "\"mcp-sign-ins\"",
        "\"plugin-settings\"",
        "\"mcp-settings\"",
    ] {
        only_in(name, "internal_ext/member_dir.rs");
    }
}

#[test]
fn orphan_evidence_is_judged_only_by_owned_orphan() {
    only_in(".app_pid", "internal_ext/sidecar_orphan.rs");
}

/// The shell's state files have one writer (files::atomic_write, owner-only,
/// unique temp, synced) and one reader that never resets an unreadable file;
/// the only other file writes are the verified download (sidecar_cache) and
/// moving a verified sandbox bundle into place (sandbox_bundle).
#[test]
fn secret_files_use_one_writer() {
    for needle in ["fs::write(", "OpenOptions", "write_owner_only"] {
        only_in(needle, "files.rs");
    }
    only_in("File::create(", "internal_ext/sidecar_cache.rs");
    let renames: Vec<String> = sources()
        .into_iter()
        .filter(|(_, code)| code.contains("fs::rename("))
        .map(|(rel, _)| rel)
        .collect();
    assert_eq!(
        renames,
        vec![
            "files.rs".to_string(),
            "internal_ext/sandbox_bundle.rs".to_string(),
            "internal_ext/sidecar_cache.rs".to_string(),
        ],
        "a rename outside the writers"
    );
}

/// App directories are resolved once (dirs.rs); every path below them is
/// named there.
#[test]
fn app_dirs_are_resolved_only_by_dirs() {
    for needle in ["app_cache_dir", "app_config_dir", "app_data_dir"] {
        only_in(needle, "dirs.rs");
    }
}

/// Random values and secret comparisons have one owner (secret.rs).
#[test]
fn secrets_are_generated_and_compared_only_by_secret() {
    only_in("getrandom::", "secret.rs");
    only_in("fold(0u8", "secret.rs");
}

/// What counts as this machine is decided once (loopback.rs): no other
/// source names a loopback host.
#[test]
fn loopback_hosts_are_named_only_by_loopback() {
    for needle in ["\"127.0.0.1", "\"localhost\"", "[::1]", "is_loopback()"] {
        only_in(needle, "internal_ext/loopback.rs");
    }
}

/// Calls as the member go through GatewayAccess::send, which reads every
/// answer once (gateway_client::check): no call site attaches the session
/// or maps a gateway status itself.
#[test]
fn gateway_calls_go_through_the_gateway_client() {
    // The sandbox API and a skills provider authenticate to their own
    // sidecar with the launch's bearer, never the member session.
    only_in_any(
        ".bearer_auth(",
        &[
            "internal_ext/gateway_client.rs",
            "internal_ext/sandbox_api.rs",
            "internal_ext/skills_provider.rs",
        ],
    );
}

/// Every child the desktop runs (the bridge, every sidecar, and through them
/// every agent CLI and MCP stdio tool) is spawned by child_process::spawn,
/// which sets the member's login-shell environment beneath the child's own
/// (shell_env). The only other processes are the resolving shell itself and
/// the orphan check's system tools (lsof, ps).
#[test]
fn every_child_gets_the_shell_env() {
    only_in_any(
        "Command::new(",
        &[
            "bridge.rs",
            "internal_ext/sidecar_runtime.rs",
            "internal_ext/sidecar_orphan.rs",
            "shell_env.rs",
        ],
    );
    only_in_any(
        "child_process::spawn(",
        &["bridge.rs", "internal_ext/sidecar_runtime.rs"],
    );
    only_in_any(
        ".spawn()",
        &["child_process/windows_job.rs", "shell_env.rs"],
    );
    only_in_any(".output()", &["internal_ext/sidecar_orphan.rs"]);
    only_in("SharedChild::", "child_process.rs");
    // The env a spawn gets is the one resolution's (shell_env::current),
    // never one a caller builds; it is applied only by spawn.
    only_in("ShellEnv::default()", "shell_env.rs");
    only_in(".apply(&mut cmd)", "child_process.rs");
    let sources = sources();
    for spawner in ["bridge.rs", "internal_ext/sidecar_runtime.rs"] {
        let code = &sources.iter().find(|(rel, _)| rel == spawner).unwrap().1;
        assert!(
            code.contains("shell_env::current().await"),
            "{spawner} spawns without the shell environment"
        );
    }
}

/// Every stop of a supervised child goes through child_process::Stopping,
/// so a relaunch waits for the run it replaces.
#[test]
fn stops_go_through_stopping() {
    only_in("terminate_detached(", "child_process.rs");
}

/// Restart backoff comes from the one RestartPolicy.
#[test]
fn restarts_follow_one_policy() {
    only_in_any(
        "Backoff::new(",
        &[
            "child_process.rs",
            // Not process restarts: the catalog poll retry and the member
            // stream reconnect.
            "internal_ext.rs",
            "internal_ext/plugin_events.rs",
        ],
    );
}

/// settings.json has one writer (AppState::update_settings).
#[test]
fn settings_have_one_writer() {
    only_in("settings::save(", "lib.rs");
}

/// The generic machinery (reconcile, supervision, the sweep) asks the kind's
/// runtime (kind_runtime::runtime) and never branches on a kind itself.
#[test]
fn the_generic_machinery_never_branches_on_a_kind() {
    let generic = [
        "internal_ext.rs",
        "internal_ext/sidecar_runtime.rs",
        "internal_ext/sidecar_fetch.rs",
        "internal_ext/sidecar_sweep.rs",
    ];
    for (rel, code) in sources() {
        if generic.contains(&rel.as_str()) {
            assert!(
                !code.contains("ComponentKind::"),
                "{rel} branches on a component kind; extend KindRuntime instead"
            );
        }
    }
}

/// A plugin's host grant is revoked only by the launch's guard, which ends
/// on every exit path.
#[test]
fn host_grants_are_revoked_only_by_the_launch_guard() {
    only_in(".revoke(", "internal_ext/kind_runtime.rs");
}

#[test]
fn sign_in_files_are_reached_only_through_sign_ins() {
    // Every read, write and delete of a sign-in file goes through SignIns
    // (mcp_oauth.rs): nothing else resolves a sign-in path.
    only_in("sign_in_path(", "internal_ext/mcp_oauth.rs");
}

/// The setting value rules and the member store have one owner
/// (component_settings.rs, pinned to core/pkgmeta): no other module merges,
/// checks or resolves a setting, or names the settings file variable.
#[test]
fn component_settings_have_one_owner() {
    for needle in [
        "fn merge_settings",
        "fn check_setting_value",
        "fn effective_settings",
        "fn unset_settings",
        "BNBLOC_COMPONENT_SETTINGS_FILE",
        "BNBLOC_COMPONENT_SETTINGS_DIR",
        "/settings/resolved",
    ] {
        only_in(needle, "internal_ext/component_settings.rs");
    }
}
