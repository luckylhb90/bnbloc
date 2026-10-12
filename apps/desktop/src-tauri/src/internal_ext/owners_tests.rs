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
            let code = text
                .split("#[cfg(test)]\nmod tests {")
                .next()
                .unwrap_or("")
                .to_string();
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
    only_in("\"plugin-settings\"", "internal_ext/member_dir.rs");
    only_in("\"mcp-settings\"", "internal_ext/member_dir.rs");
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
    // The sandbox API authenticates to its own sidecar with the launch's
    // bearer, never the member session.
    only_in_any(
        ".bearer_auth(",
        &[
            "internal_ext/gateway_client.rs",
            "internal_ext/sandbox_api.rs",
        ],
    );
}

#[test]
fn sign_in_files_are_reached_only_through_sign_ins() {
    // Every read, write and delete of a sign-in file goes through SignIns
    // (mcp_oauth.rs): nothing else resolves a sign-in path.
    only_in("sign_in_path(", "internal_ext/mcp_oauth.rs");
    only_in("OAUTH_DIR", "internal_ext/mcp_oauth.rs");
}
