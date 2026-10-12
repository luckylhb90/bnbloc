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
    let offenders: Vec<String> = sources()
        .into_iter()
        .filter(|(rel, code)| rel != owner && code.contains(needle))
        .map(|(rel, _)| rel)
        .collect();
    assert!(
        offenders.is_empty(),
        "{needle:?} belongs to {owner} alone; also in {offenders:?}"
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

#[test]
fn sign_in_files_are_reached_only_through_sign_ins() {
    // Every read, write and delete of a sign-in file goes through SignIns
    // (mcp_oauth.rs): nothing else resolves a sign-in path.
    only_in("sign_in_path(", "internal_ext/mcp_oauth.rs");
    only_in("OAUTH_DIR", "internal_ext/mcp_oauth.rs");
}
