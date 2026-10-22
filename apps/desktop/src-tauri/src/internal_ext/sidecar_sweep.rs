//! Forget what left the member catalog, on every reconcile.
//!
//! Uninstalling a component (or flipping it to off, or dropping an opt-in)
//! removes what this desktop keeps for it: cached binaries, a sandbox's
//! bundles and data dir, a plugin's or MCP tool's member settings and its
//! launch file, an MCP tool's sign-in. Each successful reconcile sweeps the disk against
//! the live catalog it just fetched, never against what this app session saw
//! before, so a component uninstalled while the app was closed is forgotten
//! as well. Each kind sweeps what it keeps (`KindRuntime::sweep`); the
//! binary cache, shared by every binary kind, is swept here once against all
//! of them. MCP binaries and core manifests follow the catalog where they
//! are written (the MCP pass, the manifest writer).
//!
//! A machine-wide cache is shared by every signed-in account, so it is judged
//! against what ALL of their catalogs still list (`LiveRegistry::union`), never
//! against whichever catalog reconciled last: one account's reconcile must not
//! delete a binary another account runs.
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
use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};

use parking_lot::Mutex;

use crate::internal_ext::kind_runtime::{runtime, KINDS};
use crate::internal_ext::mcp_tools::{installed_mcp_tools, mcp_keep, McpKeep, SignIns};
use crate::internal_ext::pin::Sha256Pin;
use crate::internal_ext::previewer_cache::installed_previewers;
use crate::internal_ext::sidecar_model::{
    desktop_components, resolve_component_specs, ComponentKind, ComponentsResponse,
};
use crate::internal_ext::sidecar_util::{cache_file_is_live, cache_stem};

/// What the live catalog still has, per kind of thing this desktop keeps.
#[derive(Clone, Default)]
pub(super) struct Live {
    /// The binary builds the catalog pins: `(cache stem, sha256)` of every
    /// desktop sidecar's artifact for this platform. Two accounts may pin two
    /// builds of one component, and both are kept.
    binaries: HashSet<(String, String)>,
    /// The sandbox bundle directories the catalog pins: `(cache stem, directory
    /// name)`.
    bundles: HashSet<(String, String)>,
    /// The desktop components (cores, plugins, sandboxes) by kind.
    names: HashMap<ComponentKind, HashSet<String>>,
    mcp: McpKeep,
    previewers: HashSet<Sha256Pin>,
}

impl Live {
    pub(super) fn of(catalog: &ComponentsResponse, platform: &str) -> Self {
        let mut live = Live::default();
        for c in desktop_components(catalog) {
            live.names.entry(c.kind).or_default().insert(c.name.clone());
        }
        for spec in resolve_component_specs(catalog, platform).specs {
            if let Some(pin) = spec.pin() {
                live.binaries
                    .insert((cache_stem(&spec.id()), pin.as_str().to_string()));
            }
            // A bundle cache holds one directory per component NAME.
            if let Some(dir) = runtime(spec.kind).bundle_dir(&spec) {
                live.bundles.insert((cache_stem(&spec.name), dir));
            }
        }
        live.mcp = mcp_keep(&installed_mcp_tools(catalog, platform));
        live.previewers = installed_previewers(catalog)
            .into_values()
            .map(|pin| pin.sha256)
            .collect();
        live
    }

    /// What every one of `lives` still has: the live set of the machine-wide
    /// caches when several accounts' catalogs are in force at once.
    pub(super) fn union<'a>(lives: impl IntoIterator<Item = &'a Live>) -> Live {
        let lives: Vec<&Live> = lives.into_iter().collect();
        let mut out = Live::default();
        for live in &lives {
            out.binaries.extend(live.binaries.iter().cloned());
            out.bundles.extend(live.bundles.iter().cloned());
            for (kind, names) in &live.names {
                out.names
                    .entry(*kind)
                    .or_default()
                    .extend(names.iter().cloned());
            }
            out.previewers.extend(live.previewers.iter().cloned());
        }
        out.mcp = McpKeep::union(lives.iter().map(|live| &live.mcp));
        out
    }

    /// What `self` lists and, besides, the builds in `running` (as `(stem,
    /// sha256)`): a core that runs keeps the build it runs from, whatever a
    /// catalog pins by now, or its restart would find no binary.
    pub(super) fn with_running(
        mut self,
        running: impl IntoIterator<Item = (String, String)>,
    ) -> Self {
        self.binaries.extend(running);
        self
    }

    /// The live desktop components of `kind`.
    pub(super) fn names(&self, kind: ComponentKind) -> &HashSet<String> {
        static NONE: std::sync::OnceLock<HashSet<String>> = std::sync::OnceLock::new();
        self.names
            .get(&kind)
            .unwrap_or_else(|| NONE.get_or_init(HashSet::new))
    }

    /// The sandbox bundle directories of component `stem` the catalog pins.
    pub(super) fn bundle_dirs(&self, stem: &str) -> HashSet<String> {
        self.bundles
            .iter()
            .filter(|(s, _)| s == stem)
            .map(|(_, dir)| dir.clone())
            .collect()
    }

    /// The installed MCP tools, and those whose sign-ins the catalog still
    /// asks for.
    pub(super) fn mcp(&self) -> &McpKeep {
        &self.mcp
    }

    /// The pins of the installed previewer bundles.
    pub(super) fn previewers(&self) -> &HashSet<Sha256Pin> {
        &self.previewers
    }
}

/// The live set of every signed-in account's catalog, by scope: what a
/// machine-wide sweep judges against (the union), so no account's reconcile
/// deletes what another account still lists. A scope is set when its
/// catalog is fetched and removed when its account signs out or changes scope.
///
/// An account that has not fetched its catalog yet (it just came up, or its
/// gateway is slow or down) lists nothing in the registry, so a machine-wide
/// sweep waits until every account that is live has (`settled`): the first to
/// reconcile must not delete what the others have cached.
#[derive(Default)]
pub(crate) struct LiveRegistry {
    by_scope: Mutex<HashMap<String, Live>>,
    /// The adapters that are live and have not registered a catalog yet.
    unsettled: Mutex<HashSet<u64>>,
}

impl LiveRegistry {
    pub(super) fn set(&self, scope: &str, live: Live) {
        self.by_scope.lock().insert(scope.to_string(), live);
    }

    /// An adapter (by holder id) came up: it has no catalog registered yet.
    pub(super) fn expect(&self, holder: u64) {
        self.unsettled.lock().insert(holder);
    }

    /// The adapter registered its first catalog.
    pub(super) fn arrived(&self, holder: u64) {
        self.unsettled.lock().remove(&holder);
    }

    /// The adapter is gone: nothing waits for it.
    pub(super) fn leave(&self, holder: u64) {
        self.unsettled.lock().remove(&holder);
    }

    /// Whether every live account's catalog is registered: only then is the
    /// union what the machine still needs.
    pub(super) fn settled(&self) -> bool {
        self.unsettled.lock().is_empty()
    }

    pub(super) fn remove(&self, scope: &str) {
        self.by_scope.lock().remove(scope);
    }

    /// How many scopes are registered.
    #[cfg(test)]
    pub(super) fn len(&self) -> usize {
        self.by_scope.lock().len()
    }

    /// What any registered scope still lists.
    pub(super) fn union(&self) -> Live {
        Live::union(self.by_scope.lock().values())
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
    /// Machine-wide, rebuildable: the previewer bundle archives.
    pub(super) previewers: Option<PathBuf>,
    /// The member directory (member_dir) of the SAME session the catalog
    /// was fetched with: its component settings, MCP sign-ins and sandbox
    /// data.
    pub(super) member: Option<PathBuf>,
    /// The account's folder of component copies the sandbox mounts
    /// (`<workspaces root>/.mounts/<account>`).
    pub(super) mirror: Option<PathBuf>,
}

/// Delete everything in `dirs` the live catalogs no longer have: the shared
/// binary cache here, then each kind's own things (`KindRuntime::sweep`).
/// `own` is the catalog of the scope whose member directory is swept (its
/// settings, sign-ins, sandbox data); `machine` is what ALL scopes list
/// (`LiveRegistry::union`), which the machine-wide caches are judged by.
/// Blocking, best-effort: a failure is logged and retried by the next
/// reconcile.
pub(super) fn sweep(dirs: &SweepDirs, own: &Live, machine: &Live, sign_ins: &SignIns) {
    if let Some(dir) = &dirs.binaries {
        for (name, path) in entries(dir) {
            // Only cache-shaped names are ours to judge, and each is judged
            // by the build it is: a component every account has stopped
            // pinning that build of is forgotten, whichever accounts remain.
            if cache_file_is_live(&name, &machine.binaries) == Some(false) {
                remove(&path);
            }
        }
    }
    for kind in KINDS {
        runtime(kind).sweep(dirs, own, machine, sign_ins);
    }
}

/// Remove every entry of `base` whose name is not in `keep` (`base` holds
/// one entry per component).
pub(super) fn forget_entries_except(base: &Path, keep: &HashSet<String>) {
    for (name, path) in entries(base) {
        if !keep.contains(&name) {
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

pub(super) fn remove(path: &Path) {
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
            previewers: Some(root.join("cache/previewers")),
            member: member_dir(&root.join("config"), GW, "maya", tenant),
            // Never the real home's.
            mirror: None,
        }
    }

    /// The bundle pin of previewer `<p>view`.
    fn view_pin(p: &str) -> String {
        Sha256Pin::of(p.as_bytes()).as_str().to_string()
    }

    /// A tenant's catalog: core `<p>core`, plugin `<p>bot`, sandbox `<p>vm`,
    /// MCP tools `<p>kept` (member settings) and `<p>drive` (sign-in),
    /// previewer `<p>view`.
    fn tenant_catalog(p: &str) -> Live {
        Live::of(
            &catalog(serde_json::json!({"components": [
                {"kind": "previewer", "name": format!("{p}view"), "version": "1.0.0", "runtime": "bundle",
                 "rollout": "auto", "installed": true,
                 "previewer": {"bundleUrl": "/v1/artifacts/v", "bundleSha256": view_pin(p)}},
                {"kind": "core", "name": format!("{p}core"), "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true,
                 "artifacts": {"darwin-arm64": {"url": "/v1/artifacts/c", "sha256": SHA}},
                 "sidecar": {"localUrl": "http://127.0.0.1:7190"}},
                {"kind": "plugin", "name": format!("{p}bot"), "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true,
                 "artifacts": {"darwin-arm64": {"url": "/v1/artifacts/p", "sha256": SHA}},
                 "sidecar": {"localUrl": "http://127.0.0.1:7191", "listenEnv": "L"}},
                {"kind": "sandbox", "name": format!("{p}vm"), "version": "1", "runtime": "sidecar",
                 "requiresDesktop": true, "rollout": "auto", "installed": true,
                 "artifacts": {"darwin-arm64": {"url": "/v1/artifacts/s", "sha256": SHA}},
                 "sidecar": {"localUrl": "http://127.0.0.1:7192", "tokenEnv": "T"}},
                {"kind": "mcp", "name": format!("{p}kept"), "version": "1", "runtime": "stdio",
                 "rollout": "auto", "installed": true,
                 "settings": [{"key": "token", "label": "Token", "type": "secret", "scope": "member", "required": true}],
                 "mcp": {"transport": "stdio"}},
                {"kind": "mcp", "name": format!("{p}drive"), "version": "1", "runtime": "endpoint",
                 "rollout": "auto", "installed": true,
                 "endpoint": {"baseUrl": "https://tools.example.com/mcp"},
                 "mcp": {"transport": "http", "http": {"auth": {"scheme": "oauth"}}}}
            ]})),
            "darwin-arm64",
        )
    }

    /// The bundle directory of tenant `p`'s sandbox `<p>vm`.
    fn bundle_dir(p: &str) -> String {
        tenant_catalog(p)
            .bundle_dirs(&format!("{p}vm"))
            .into_iter()
            .next()
            .expect("the sandbox pins a bundle")
    }

    /// What a tenant's catalog leaves in the member directory.
    fn member_state(member: &Path, p: &str) -> Vec<PathBuf> {
        vec![
            member.join(format!("component-settings/plugin@{p}bot.json")),
            member.join(format!("component-settings/launch/plugin@{p}bot.json")),
            member.join(format!("component-settings/mcp@{p}kept.json")),
            member.join(format!("mcp-sign-ins/{p}drive.json")),
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
            member.join("component-settings/plugin@x.json"),
            member.join("component-settings/.plugin@x.json.0123456789abcdef.tmp"),
            member.join("component-settings/plugin@x.json.corrupt-1700000000000"),
            member.join("component-settings/launch/plugin@x.json"),
            member.join("component-settings/mcp@y.json"),
            member.join("mcp-sign-ins/y.json"),
            member.join("sandbox/s/disk.img"),
            cores.join(cache_file_name("z", SHA)),
            root.join(format!("cache/previewers/{}.tar", view_pin("gone"))),
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
            root.join("cache/sandbox/avm")
                .join(bundle_dir("a"))
                .join("avm"),
            root.join(format!("cache/previewers/{}.tar", view_pin("a"))),
        ]);
        for path in gone.iter().chain(&stays) {
            write(path);
        }
        write(&cores.join("notes.txt"));

        let live = tenant_catalog("a");
        sweep(&dirs, &live, &live, &SignIns::default());

        for path in &gone {
            assert!(!path.exists(), "{} survived the sweep", path.display());
        }
        assert!(!member.join("sandbox/s").exists());
        assert!(!root.join("cache/sandbox/s").exists());
        for path in &stays {
            assert!(path.exists(), "{} was swept", path.display());
        }

        // An empty catalog of the same scope forgets every component's files.
        sweep(
            &dirs,
            &Live::default(),
            &Live::default(),
            &SignIns::default(),
        );
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
        let live_a = tenant_catalog("a");
        sweep(&in_a, &live_a, &live_a, &SignIns::default());
        let live_b = tenant_catalog("b");
        sweep(&in_b, &live_b, &live_b, &SignIns::default());
        for path in a_state.iter().chain(&b_state) {
            assert!(path.exists(), "{} lost on a tenant switch", path.display());
        }
        // A rebuildable cache of A's core may go; it is re-downloaded.
        assert!(!a_binary.exists());

        // Back to tenant A: everything of A is still there.
        let live_a = tenant_catalog("a");
        sweep(&in_a, &live_a, &live_a, &SignIns::default());
        for path in a_state.iter().chain(&b_state) {
            assert!(path.exists(), "{} lost switching back", path.display());
        }
        std::fs::remove_dir_all(&root).unwrap();
    }

    /// Two accounts' catalogs are in force at once: a reconcile of one never
    /// deletes a machine-wide binary, bundle or previewer the other still
    /// lists (the registry's union judges them), while each account's own
    /// member state is judged by its own catalog only. When an account's scope
    /// leaves the registry, what only it listed goes at the next sweep.
    #[test]
    fn two_accounts_share_the_machine_caches_by_union() {
        let root = std::env::temp_dir().join(format!("bnbloc-sweep-union-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let (in_a, in_b) = (dirs(&root, "t-a"), dirs(&root, "t-b"));
        let cores = in_a.binaries.clone().unwrap();
        let a_binary = cores.join(cache_file_name("acore", SHA));
        let b_binary = cores.join(cache_file_name("bcore", SHA));
        let a_bundle = root
            .join("cache/sandbox/avm")
            .join(bundle_dir("a"))
            .join("avm");
        let a_view = root.join(format!("cache/previewers/{}.tar", view_pin("a")));
        let shared = [&a_binary, &b_binary, &a_bundle, &a_view];
        for path in shared {
            write(path);
        }
        let a_state = member_state(&in_a.member.clone().unwrap(), "a");
        for path in &a_state {
            write(path);
        }

        let registry = LiveRegistry::default();
        registry.set("a", tenant_catalog("a"));
        registry.set("b", tenant_catalog("b"));

        // B reconciles: A's machine-wide files stay (A's catalog is in
        // force), and so does all of A's member state (never B's to judge).
        let live_b = tenant_catalog("b");
        sweep(&in_b, &live_b, &registry.union(), &SignIns::default());
        for path in shared.iter().copied().chain(&a_state) {
            assert!(
                path.exists(),
                "{} was swept by another account",
                path.display()
            );
        }

        // A signs out: its catalog leaves the registry, and B's next reconcile
        // forgets what only A listed. A's member state is still A's.
        registry.remove("a");
        sweep(&in_b, &live_b, &registry.union(), &SignIns::default());
        assert!(b_binary.exists(), "B's own core stays");
        for path in [&a_binary, &a_bundle, &a_view] {
            assert!(
                !path.exists(),
                "{} outlived its only account",
                path.display()
            );
        }
        for path in &a_state {
            assert!(path.exists(), "A's member state is not B's to sweep");
        }
        std::fs::remove_dir_all(&root).unwrap();
    }

    /// The registry's union is what any scope lists, and a scope that is set
    /// again replaces its own entry.
    #[test]
    fn the_registry_unions_what_every_scope_lists() {
        let registry = LiveRegistry::default();
        assert!(registry.union().names(ComponentKind::Core).is_empty());
        registry.set("a", tenant_catalog("a"));
        registry.set("b", tenant_catalog("b"));
        let both = registry.union();
        assert!(both.names(ComponentKind::Core).contains("acore"));
        assert!(both.names(ComponentKind::Core).contains("bcore"));
        assert!(both.mcp().tools.contains("akept") && both.mcp().tools.contains("bkept"));
        assert_eq!(
            both.mcp().files.len(),
            0,
            "no stdio binary for this platform in the fixture"
        );
        registry.set("a", tenant_catalog("c"));
        let after = registry.union();
        assert!(!after.names(ComponentKind::Core).contains("acore"));
        assert!(after.names(ComponentKind::Core).contains("ccore"));
        registry.remove("a");
        registry.remove("b");
        assert!(registry.union().names(ComponentKind::Core).is_empty());
    }

    /// Two accounts may pin two builds of one core (a staged rollout): both
    /// builds stay while a catalog pins them, one account's reconcile never
    /// deletes the build the other runs, and a build nobody pins goes.
    #[test]
    fn two_accounts_may_pin_two_builds_of_one_core() {
        const SHA_B: &str = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
        let pinning = |sha: &str| {
            Live::of(
                &catalog(serde_json::json!({"components": [
                    {"kind": "core", "name": "acme", "version": "1", "runtime": "sidecar",
                     "requiresDesktop": true, "rollout": "auto", "installed": true,
                     "artifacts": {"darwin-arm64": {"url": "/v1/artifacts/c", "sha256": sha}},
                     "sidecar": {"localUrl": "http://127.0.0.1:7190"}}
                ]})),
                "darwin-arm64",
            )
        };
        let root = std::env::temp_dir().join(format!("bnbloc-sweep-builds-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let dirs = dirs(&root, "t-a");
        let cores = dirs.binaries.clone().unwrap();
        let (build_a, build_b, build_old) = (
            cores.join(cache_file_name("acme", SHA)),
            cores.join(cache_file_name("acme", SHA_B)),
            cores.join(cache_file_name("acme", &"c".repeat(64))),
        );
        for path in [&build_a, &build_b, &build_old] {
            write(path);
        }
        let registry = LiveRegistry::default();
        registry.set("a", pinning(SHA));
        registry.set("b", pinning(SHA_B));
        // Either account's reconcile: both pinned builds stay, the unpinned goes.
        sweep(&dirs, &pinning(SHA), &registry.union(), &SignIns::default());
        assert!(
            build_a.exists() && build_b.exists(),
            "a pinned build was swept"
        );
        assert!(!build_old.exists(), "a build nobody pins stayed");
        // B leaves: the build only B pinned goes at the next sweep.
        registry.remove("b");
        sweep(&dirs, &pinning(SHA), &registry.union(), &SignIns::default());
        assert!(build_a.exists());
        assert!(!build_b.exists());
        std::fs::remove_dir_all(&root).unwrap();
    }

    /// The first account to reconcile must not sweep what an account that has
    /// not fetched its catalog yet keeps cached: machine-wide sweeps wait until
    /// every live account has registered one.
    #[test]
    fn machine_wide_sweeps_wait_for_every_live_account() {
        let registry = LiveRegistry::default();
        assert!(registry.settled(), "nobody is waited for");
        registry.expect(1);
        registry.expect(2);
        registry.arrived(1);
        assert!(!registry.settled(), "account 2 has not fetched its catalog");
        registry.arrived(2);
        assert!(registry.settled());
        registry.expect(3);
        assert!(!registry.settled());
        registry.leave(3);
        assert!(registry.settled(), "an account that left is not waited for");
    }

    /// A core that runs keeps the build it runs from, whatever a catalog pins
    /// by now: its restart finds the binary.
    #[test]
    fn a_running_cores_build_survives_a_catalog_that_pins_another() {
        const SHA_B: &str = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
        let root =
            std::env::temp_dir().join(format!("bnbloc-sweep-running-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let dirs = dirs(&root, "t-a");
        let cores = dirs.binaries.clone().unwrap();
        let (running, pinned) = (
            cores.join(cache_file_name("acore", SHA_B)),
            cores.join(cache_file_name("acore", SHA)),
        );
        write(&running);
        write(&pinned);
        let live = tenant_catalog("a"); // pins SHA
        sweep(
            &dirs,
            &live,
            &live
                .clone()
                .with_running([("acore".to_string(), SHA_B.to_string())]),
            &SignIns::default(),
        );
        assert!(
            running.exists() && pinned.exists(),
            "the running build and the pinned one stay"
        );
        sweep(&dirs, &live, &live, &SignIns::default());
        assert!(
            !running.exists(),
            "once it no longer runs, the unpinned build goes"
        );
        std::fs::remove_dir_all(&root).unwrap();
    }
}
