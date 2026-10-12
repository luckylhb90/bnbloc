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
//! Only the signed-in member's own directory is swept: another member's
//! credentials wait for that member's catalog.
use std::collections::HashSet;
use std::path::{Path, PathBuf};

use crate::internal_ext::mcp_tools::{installed_mcp_tools, mcp_keep, SignIns};
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
/// signed in yet, no app dir).
#[derive(Debug, Default)]
pub(super) struct SweepDirs {
    /// The sidecar binary cache (`<app cache>/cores`).
    pub(super) binaries: Option<PathBuf>,
    /// The sandbox bundle and data bases (`<app cache>/sandbox`,
    /// `<app data>/sandbox`): one directory per sandbox component.
    pub(super) sandboxes: Vec<PathBuf>,
    /// The member's plugin settings dir.
    pub(super) plugin_settings: Option<PathBuf>,
    /// The member's MCP settings dir (sign-ins under `oauth/`).
    pub(super) mcp_settings: Option<PathBuf>,
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
    for base in &dirs.sandboxes {
        for (name, path) in entries(base) {
            if !live.sandboxes.contains(&name) {
                remove(&path);
            }
        }
    }
    if let Some(dir) = &dirs.plugin_settings {
        forget_unlisted(dir, &live.plugins);
    }
    if let Some(dir) = &dirs.mcp_settings {
        forget_unlisted(dir, &live.mcp_settings);
        sign_ins.retain(dir, &live.mcp_sign_ins);
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
    use crate::internal_ext::sidecar_util::cache_file_name;

    const SHA: &str = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";

    fn catalog(json: serde_json::Value) -> ComponentsResponse {
        serde_json::from_value(json).expect("catalog")
    }

    fn write(path: &Path) {
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, b"{}").unwrap();
    }

    /// Components uninstalled while the app was closed are forgotten by the
    /// first reconcile of a fresh app session: nothing about them is in
    /// memory, the sweep works from the disk and the live catalog alone.
    #[test]
    fn uninstalled_while_closed_is_forgotten() {
        let root = std::env::temp_dir().join(format!("bnbloc-sweep-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let dirs = SweepDirs {
            binaries: Some(root.join("cache/cores")),
            sandboxes: vec![root.join("cache/sandbox"), root.join("data/sandbox")],
            plugin_settings: Some(root.join("member/plugin-settings")),
            mcp_settings: Some(root.join("member/mcp-settings")),
        };
        let cores = dirs.binaries.clone().unwrap();
        let plugins = dirs.plugin_settings.clone().unwrap();
        let mcp = dirs.mcp_settings.clone().unwrap();

        // Left on disk by an earlier session: x (plugin), y (MCP tool with
        // member settings and a sign-in), z (core), s (sandbox) were
        // uninstalled; keep/kept/vm are still installed.
        let gone = [
            plugins.join("x.json"),
            plugins.join(".x.json.tmp"),
            mcp.join("y.json"),
            mcp.join("oauth/y.json"),
            cores.join(cache_file_name("z", SHA)),
            cores.join(format!(".{}.part", cache_file_name("plugin/x", SHA))),
            root.join("cache/sandbox/s/0.1.0-abc/s"),
            root.join("data/sandbox/s/disk.img"),
        ];
        let stays = [
            plugins.join("keep.json"),
            mcp.join("kept.json"),
            mcp.join("oauth/drive.json"),
            cores.join(cache_file_name("acme", SHA)),
            cores.join(cache_file_name("plugin/keep", SHA)),
            cores.join("notes.txt"),
            root.join("cache/sandbox/vm/0.1.0-abc/vm"),
            root.join("data/sandbox/vm/disk.img"),
        ];
        for path in gone.iter().chain(&stays) {
            write(path);
        }

        let live = Live::of(
            &catalog(serde_json::json!({"components": [
                {"kind": "core", "name": "acme", "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true},
                {"kind": "plugin", "name": "keep", "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true},
                {"kind": "sandbox", "name": "vm", "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true},
                // Opt-in dropped: forgotten like an uninstall.
                {"kind": "plugin", "name": "x", "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "optin", "installed": false},
                {"kind": "mcp", "name": "kept", "version": "1", "runtime": "stdio",
                 "rollout": "auto", "installed": true,
                 "mcp": {"transport": "stdio", "credentials": "member",
                         "env": [{"name": "TOKEN", "secret": true, "required": true}]}},
                {"kind": "mcp", "name": "drive", "version": "1", "runtime": "endpoint",
                 "rollout": "auto", "installed": true,
                 "endpoint": {"baseUrl": "https://tools.example.com/mcp"},
                 "mcp": {"transport": "http", "http": {"auth": {"scheme": "oauth"}}}}
            ]})),
            "darwin-arm64",
        );
        sweep(&dirs, &live, &SignIns::default());

        for path in &gone {
            assert!(!path.exists(), "{} survived the sweep", path.display());
        }
        assert!(!root.join("cache/sandbox/s").exists());
        assert!(!root.join("data/sandbox/s").exists());
        for path in &stays {
            assert!(path.exists(), "{} was swept", path.display());
        }

        // An empty catalog forgets every component's files.
        sweep(&dirs, &Live::default(), &SignIns::default());
        for path in &stays[..5] {
            assert!(!path.exists(), "{} survived", path.display());
        }
        assert!(cores.join("notes.txt").exists(), "not a cache file");
        std::fs::remove_dir_all(&root).unwrap();
    }
}
