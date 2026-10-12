//! Forget what left the member catalog, on every reconcile.
//!
//! Uninstalling a component (or flipping it to off, or dropping an opt-in)
//! removes what this desktop keeps for it: cached binaries, a sandbox's
//! bundles and data dir, a plugin's local settings, an MCP tool's member
//! credentials and sign-in. Each successful reconcile sweeps the disk against
//! the live catalog it just fetched, never against what this app session saw
//! before, so a component uninstalled while the app was closed is forgotten
//! as well. Previewer bundles, MCP binaries and core manifests follow the
//! catalog the same way in their own modules.
//!
//! State is only ever judged against a catalog of its own scope. The
//! catalog belongs to one member acting in one tenant (a super-admin's
//! tenant switch hands over a whole other catalog), so what that member
//! keeps (settings, credentials, sign-ins, sandbox data) lives in their
//! member directory for that tenant (member_dir) and only that directory is
//! swept, the one of the session the catalog was fetched with. Another
//! member's or another tenant's state waits for its own catalog. Only
//! rebuildable machine-wide caches (sidecar binaries, sandbox bundles) are
//! swept against whichever catalog is current: re-downloading them is
//! harmless.
use std::collections::HashSet;
use std::path::{Path, PathBuf};

use crate::internal_ext::mcp_tools::{installed_mcp_tools, mcp_keep, SignIns};
use crate::internal_ext::member_dir::{MCP_SETTINGS_DIR, PLUGIN_SETTINGS_DIR, SANDBOX_DATA_DIR};
use crate::internal_ext::plugin_settings::forget_unlisted;
use crate::internal_ext::sidecar_model::{desktop_components, ComponentKind, ComponentsResponse};
use crate::internal_ext::sidecar_status::component_id;
use crate::internal_ext::sidecar_util::{cache_stem, parse_cache_file};

/// What the live catalog still has, per kind of thing this desktop keeps.
#[derive(Debug, Default)]
pub(super) struct Live {
    /// Cache stems of the desktop components (binary cache names).
    binaries: HashSet<String>,
    /// Cache stems of the sandbox components (bundle and data dir names).
    sandboxes: HashSet<String>,
    plugins: HashSet<String>,
    mcp_settings: HashSet<String>,
    mcp_sign_ins: HashSet<String>,
}

impl Live {
    pub(super) fn of(catalog: &ComponentsResponse, platform: &str) -> Self {
        let mut live = Live::default();
        for c in desktop_components(catalog) {
            live.binaries
                .insert(cache_stem(&component_id(c.kind, &c.name)));
            match c.kind {
                ComponentKind::Sandbox => {
                    live.sandboxes.insert(cache_stem(&c.name));
                }
                ComponentKind::Plugin => {
                    live.plugins.insert(c.name.clone());
                }
                _ => {}
            }
        }
        let keep = mcp_keep(&installed_mcp_tools(catalog, platform));
        live.mcp_settings = keep.settings;
        live.mcp_sign_ins = keep.sign_ins;
        live
    }
}

/// Where the swept things live; None where the dir is unknown (no member
/// scope in the session, no app dir).
#[derive(Debug, Default)]
pub(super) struct SweepDirs {
    /// Machine-wide, rebuildable: the sidecar binary cache
    /// (`<app cache>/cores`).
    pub(super) binaries: Option<PathBuf>,
    /// Machine-wide, rebuildable: the sandbox bundle cache
    /// (`<app cache>/sandbox`, one directory per sandbox component).
    pub(super) sandbox_bundles: Option<PathBuf>,
    /// The member directory (member_dir) of the SAME session the catalog
    /// was fetched with: its plugin settings, MCP settings and sign-ins,
    /// and sandbox data.
    pub(super) member: Option<PathBuf>,
}

/// Delete everything in `dirs` the live catalog no longer has. Blocking,
/// best-effort: a failure is logged and retried by the next reconcile.
pub(super) fn sweep(dirs: &SweepDirs, live: &Live, sign_ins: &SignIns) {
    if let Some(dir) = &dirs.binaries {
        for (name, path) in entries(dir) {
            // Only cache-shaped names are ours to judge.
            if parse_cache_file(&name).is_some_and(|(stem, _)| !live.binaries.contains(stem)) {
                remove(&path);
            }
        }
    }
    if let Some(base) = &dirs.sandbox_bundles {
        forget_sandboxes(base, live);
    }
    let Some(member) = &dirs.member else {
        return;
    };
    forget_sandboxes(&member.join(SANDBOX_DATA_DIR), live);
    forget_unlisted(&member.join(PLUGIN_SETTINGS_DIR), &live.plugins);
    let mcp = member.join(MCP_SETTINGS_DIR);
    forget_unlisted(&mcp, &live.mcp_settings);
    sign_ins.retain(&mcp, &live.mcp_sign_ins);
}

/// `base` holds one directory per sandbox component.
fn forget_sandboxes(base: &Path, live: &Live) {
    for (name, path) in entries(base) {
        if !live.sandboxes.contains(&name) {
            remove(&path);
        }
    }
}

fn entries(dir: &Path) -> Vec<(String, PathBuf)> {
    let Ok(read) = std::fs::read_dir(dir) else {
        return Vec::new();
    };
    read.flatten()
        .filter_map(|e| Some((e.file_name().into_string().ok()?, e.path())))
        .collect()
}

fn remove(path: &Path) {
    let removed = if path.is_dir() {
        std::fs::remove_dir_all(path)
    } else {
        std::fs::remove_file(path)
    };
    match removed {
        Ok(()) => eprintln!("[sidecar-manager] forgot uninstalled {}", path.display()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => eprintln!("[sidecar-manager] could not remove {}: {e}", path.display()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::member_dir::member_dir;
    use crate::internal_ext::sidecar_util::cache_file_name;

    const SHA: &str = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";

    fn catalog(json: serde_json::Value) -> ComponentsResponse {
        serde_json::from_value(json).expect("catalog")
    }

    fn write(path: &Path) {
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, b"{}").unwrap();
    }

    const GW: &str = "https://gw.example.com";

    /// The dirs of one session: machine-wide caches under `root`, member
    /// state in the member directory of `maya` acting in `tenant`.
    fn dirs(root: &Path, tenant: &str) -> SweepDirs {
        SweepDirs {
            binaries: Some(root.join("cache/cores")),
            sandbox_bundles: Some(root.join("cache/sandbox")),
            member: member_dir(&root.join("config"), GW, "maya", tenant),
        }
    }

    /// A tenant's catalog: core `<p>core`, plugin `<p>bot`, sandbox `<p>vm`,
    /// MCP tools `<p>kept` (member settings) and `<p>drive` (sign-in).
    fn tenant_catalog(p: &str) -> Live {
        Live::of(
            &catalog(serde_json::json!({"components": [
                {"kind": "core", "name": format!("{p}core"), "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true},
                {"kind": "plugin", "name": format!("{p}bot"), "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true},
                {"kind": "sandbox", "name": format!("{p}vm"), "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true},
                {"kind": "mcp", "name": format!("{p}kept"), "version": "1", "runtime": "stdio",
                 "rollout": "auto", "installed": true,
                 "mcp": {"transport": "stdio", "credentials": "member",
                         "env": [{"name": "TOKEN", "secret": true, "required": true}]}},
                {"kind": "mcp", "name": format!("{p}drive"), "version": "1", "runtime": "endpoint",
                 "rollout": "auto", "installed": true,
                 "endpoint": {"baseUrl": "https://tools.example.com/mcp"},
                 "mcp": {"transport": "http", "http": {"auth": {"scheme": "oauth"}}}}
            ]})),
            "darwin-arm64",
        )
    }

    /// What a tenant's catalog leaves in the member directory.
    fn member_state(member: &Path, p: &str) -> Vec<PathBuf> {
        vec![
            member.join(format!("plugin-settings/{p}bot.json")),
            member.join(format!("mcp-settings/{p}kept.json")),
            member.join(format!("mcp-settings/oauth/{p}drive.json")),
            member.join(format!("sandbox/{p}vm/disk.img")),
        ]
    }

    /// Components uninstalled while the app was closed are forgotten by the
    /// first reconcile of a fresh app session: nothing about them is in
    /// memory, the sweep works from the disk and the live catalog alone.
    #[test]
    fn uninstalled_while_closed_is_forgotten() {
        let root = std::env::temp_dir().join(format!("bnbloc-sweep-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let dirs = dirs(&root, "t-a");
        let cores = dirs.binaries.clone().unwrap();
        let member = dirs.member.clone().unwrap();

        // Left on disk by an earlier session: x (plugin), y (MCP tool with
        // member settings and a sign-in), z (core), s (sandbox) were
        // uninstalled; the a* components are still installed.
        let gone = [
            member.join("plugin-settings/x.json"),
            member.join("plugin-settings/.x.json.0123456789abcdef.tmp"),
            member.join("plugin-settings/x.json.corrupt-1700000000000"),
            member.join("mcp-settings/y.json"),
            member.join("mcp-settings/oauth/y.json"),
            member.join("sandbox/s/disk.img"),
            cores.join(cache_file_name("z", SHA)),
            cores.join(format!(
                ".{}.0123456789abcdef.part",
                cache_file_name("plugin/x", SHA)
            )),
            root.join("cache/sandbox/s/0.1.0-abc/s"),
        ];
        let mut stays = member_state(&member, "a");
        stays.extend([
            cores.join(cache_file_name("acore", SHA)),
            cores.join(cache_file_name("plugin/abot", SHA)),
            root.join("cache/sandbox/avm/0.1.0-abc/avm"),
        ]);
        for path in gone.iter().chain(&stays) {
            write(path);
        }
        write(&cores.join("notes.txt"));

        sweep(&dirs, &tenant_catalog("a"), &SignIns::default());

        for path in &gone {
            assert!(!path.exists(), "{} survived the sweep", path.display());
        }
        assert!(!member.join("sandbox/s").exists());
        assert!(!root.join("cache/sandbox/s").exists());
        for path in &stays {
            assert!(path.exists(), "{} was swept", path.display());
        }

        // An empty catalog of the same scope forgets every component's files.
        sweep(&dirs, &Live::default(), &SignIns::default());
        for path in &stays {
            assert!(!path.exists(), "{} survived", path.display());
        }
        assert!(cores.join("notes.txt").exists(), "not a cache file");
        std::fs::remove_dir_all(&root).unwrap();
    }

    /// A tenant switch hands the desktop another tenant's catalog. Its
    /// reconcile judges only that tenant's member directory and the
    /// rebuildable caches: tenant A's settings, sign-ins and sandbox data
    /// survive, and switching back finds them all.
    #[test]
    fn a_tenant_switch_never_sweeps_the_other_tenants_state() {
        let root = std::env::temp_dir().join(format!("bnbloc-sweep-tenant-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let (in_a, in_b) = (dirs(&root, "t-a"), dirs(&root, "t-b"));
        let (member_a, member_b) = (in_a.member.clone().unwrap(), in_b.member.clone().unwrap());
        assert_ne!(member_a, member_b);
        let a_state = member_state(&member_a, "a");
        let b_state = member_state(&member_b, "b");
        for path in a_state.iter().chain(&b_state) {
            write(path);
        }
        let cores = in_a.binaries.clone().unwrap();
        let a_binary = cores.join(cache_file_name("acore", SHA));
        write(&a_binary);

        // Signed in to tenant A, then switched to tenant B: B's reconcile.
        sweep(&in_a, &tenant_catalog("a"), &SignIns::default());
        sweep(&in_b, &tenant_catalog("b"), &SignIns::default());
        for path in a_state.iter().chain(&b_state) {
            assert!(path.exists(), "{} lost on a tenant switch", path.display());
        }
        // A rebuildable cache of A's core may go; it is re-downloaded.
        assert!(!a_binary.exists());

        // Back to tenant A: everything of A is still there.
        sweep(&in_a, &tenant_catalog("a"), &SignIns::default());
        for path in a_state.iter().chain(&b_state) {
            assert!(path.exists(), "{} lost switching back", path.display());
        }
        std::fs::remove_dir_all(&root).unwrap();
    }
}
