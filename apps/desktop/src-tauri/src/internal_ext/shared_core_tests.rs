//! Two accounts' adapters on one computer: a core runs once for every account
//! whose catalog pins it, an account that leaves never cuts the others' core,
//! another build is told as such, and a plugin runs per account on a port the
//! desktop picks (docs/prd/2026-10-22-multi-account.md §2.4). Real (shell)
//! processes are the cores.
#![cfg(unix)]
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use parking_lot::Mutex;

use super::sidecar_model::{ComponentArtifact, ComponentKind, SidecarSpec};
use super::sidecar_orphan::process_alive;
use super::sidecar_util::cache_file_name;
use super::{Adapter, AdapterStatus, BridgeControl, Shared};
use crate::bridge::{Status, Supervisor};
use crate::dirs::Dirs;
use crate::internal_ext::pin::Sha256Pin;
use crate::settings::Settings;

const GW: &str = "https://gw.example.com";

struct FakeBridge;

impl BridgeControl for FakeBridge {
    fn settings(&self) -> Settings {
        Settings::default()
    }
    fn status(&self) -> Status {
        Supervisor::stopped_status()
    }
    fn start(&self, _: Settings) -> Result<(), String> {
        Ok(())
    }
}

/// A "binary" the machine runs: a shell script that sleeps, cached where the
/// adapter looks for a verified build of component `id`.
fn cached_build(dirs: &Dirs, id: &str, body: &str) -> Sha256Pin {
    let script = format!("#!/bin/sh\n{body}\n");
    let pin = Sha256Pin::of(script.as_bytes());
    let path = dirs.binaries().join(cache_file_name(id, pin.as_str()));
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(&path, script).unwrap();
    super::sidecar_util::set_executable(&path).unwrap();
    pin
}

fn spec(
    kind: ComponentKind,
    name: &str,
    version: &str,
    pin: &Sha256Pin,
    listen: &str,
) -> SidecarSpec {
    let port = super::loopback::bind_ephemeral()
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    SidecarSpec {
        kind,
        name: name.into(),
        version: version.into(),
        artifact: Some(ComponentArtifact {
            url: "/v1/artifacts/x".into(),
            sha256: pin.clone(),
        }),
        manifest: None,
        local_url: format!("http://127.0.0.1:{port}"),
        listen_env: listen.into(),
        supervised_env: String::new(),
        args: Vec::new(),
        plugin: Default::default(),
        sandbox: Default::default(),
    }
}

fn adapter(dirs: &Dirs, shared: &Shared, scope: &str) -> Adapter {
    let adapter = Adapter::new(
        Ok(dirs.clone()),
        GW,
        shared.clone(),
        scope,
        true,
        Arc::new(FakeBridge),
    );
    adapter.update_gateway(
        GW,
        super::member_dir::session_token(serde_json::json!({"sub": scope, "tenant": "org-a"})),
    );
    adapter
}

/// Make `spec` this adapter's only component and start supervising it.
fn run(adapter: &Adapter, spec: &SidecarSpec) {
    adapter.start();
    adapter.apply_specs(
        vec![spec.clone()],
        std::collections::HashSet::from([spec.id()]),
    );
}

fn status_of(adapter: &Adapter, name: &str) -> AdapterStatus {
    adapter
        .status()
        .into_iter()
        .find(|s| s.name == name)
        .unwrap_or_else(|| panic!("no status for {name}"))
}

async fn until(what: &str, mut done: impl FnMut() -> bool) {
    for _ in 0..400 {
        if done() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    panic!("timed out waiting for {what}");
}

fn root(tag: &str) -> Dirs {
    let root =
        std::env::temp_dir().join(format!("bnbloc-shared-core-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    Dirs::under(&root)
}

fn cleanup(dirs: &Dirs) {
    if let Some(root) = dirs.config().parent() {
        let _ = std::fs::remove_dir_all(root);
    }
}

#[tokio::test]
async fn a_core_runs_once_for_every_account_and_outlives_one_leaving() {
    let dirs = root("once");
    let shared = Shared::new(Ok(dirs.clone()));
    let pin = cached_build(&dirs, "acme", "sleep 300");
    let core = spec(ComponentKind::Core, "acme", "1.0.0", &pin, "");
    let (maya, jack) = (
        adapter(&dirs, &shared, "maya"),
        adapter(&dirs, &shared, "jack"),
    );
    run(&maya, &core);
    run(&jack, &core);

    until("both accounts to see it run", || {
        status_of(&maya, "acme").state == "running" && status_of(&jack, "acme").state == "running"
    })
    .await;
    let (m, j) = (status_of(&maya, "acme"), status_of(&jack, "acme"));
    assert_eq!(m.pid, j.pid, "one process for both accounts");
    assert_eq!(m.local_url, j.local_url);
    let pid = m.pid.expect("a pid");

    // Maya's account leaves: Jack's core keeps running, untouched.
    let children = maya.detach_all();
    assert!(
        children.is_empty(),
        "a shared core is nobody's child but the host's"
    );
    maya.leave();
    tokio::time::sleep(Duration::from_millis(600)).await;
    assert!(
        process_alive(pid),
        "an account leaving stopped the core of the others"
    );
    assert_eq!(status_of(&jack, "acme").pid, Some(pid));
    assert_eq!(status_of(&jack, "acme").state, "running");

    // Jack leaves too: the last holder's release stops it.
    jack.detach_all();
    jack.leave();
    until("the core to stop with its last holder", || {
        !process_alive(pid)
    })
    .await;
    cleanup(&dirs);
}

#[tokio::test]
async fn another_build_of_a_running_core_is_told_not_served_in_silence() {
    let dirs = root("differs");
    let shared = Shared::new(Ok(dirs.clone()));
    let old = cached_build(&dirs, "acme", "sleep 300");
    let new = cached_build(&dirs, "acme", "sleep 301");
    let (maya, jack) = (
        adapter(&dirs, &shared, "maya"),
        adapter(&dirs, &shared, "jack"),
    );
    let on_old = spec(ComponentKind::Core, "acme", "1.0.0", &old, "");
    let on_new = SidecarSpec {
        local_url: on_old.local_url.clone(),
        ..spec(ComponentKind::Core, "acme", "2.0.0", &new, "")
    };
    run(&maya, &on_old);
    until("Maya's core", || {
        status_of(&maya, "acme").state == "running"
    })
    .await;
    let pid = status_of(&maya, "acme").pid.unwrap();

    run(&jack, &on_new);
    until("Jack to be told", || {
        status_of(&jack, "acme")
            .message
            .contains("for another account")
    })
    .await;
    let told = status_of(&jack, "acme");
    assert_eq!(told.state, "waiting");
    assert!(
        told.message.contains("1.0.0") && told.message.contains("2.0.0"),
        "{}",
        told.message
    );
    assert!(
        told.local_url.as_deref() != status_of(&maya, "acme").local_url.as_deref()
            || told.pid.is_none()
    );
    assert_eq!(
        status_of(&maya, "acme").pid,
        Some(pid),
        "Maya's core is untouched"
    );

    // Maya's account leaves; Jack's build then takes the address.
    maya.detach_all();
    maya.leave();
    until("Jack's build to run", || {
        let s = status_of(&jack, "acme");
        s.state == "running" && s.pid.is_some() && s.pid != Some(pid)
    })
    .await;
    jack.detach_all();
    jack.leave();
    cleanup(&dirs);
}

/// A plugin is not shared: each account's own copy runs on a port the desktop
/// picks and tells it in its `listenEnv`; one that declares no `listenEnv`
/// cannot, and says so.
#[tokio::test]
async fn a_plugin_runs_per_account_on_a_port_the_desktop_picks() {
    let dirs = root("plugin");
    let shared = Shared::new(Ok(dirs.clone()));
    // The plugin prints where it was told to listen, then runs.
    let pin = cached_build(
        &dirs,
        "plugin/bot",
        "echo \"listening $BOT_LISTEN\"; sleep 300",
    );
    let bot = spec(ComponentKind::Plugin, "bot", "1.0.0", &pin, "BOT_LISTEN");
    let (maya, jack) = (
        adapter(&dirs, &shared, "maya"),
        adapter(&dirs, &shared, "jack"),
    );
    run(&maya, &bot);
    run(&jack, &bot);
    until("both plugins to run", || {
        status_of(&maya, "bot").state == "running" && status_of(&jack, "bot").state == "running"
    })
    .await;
    let (m, j) = (status_of(&maya, "bot"), status_of(&jack, "bot"));
    assert_ne!(m.pid, j.pid, "one instance per account");
    assert_ne!(m.local_url, j.local_url, "each on a port of its own");
    for url in [&m.local_url, &j.local_url] {
        assert_ne!(
            url.as_deref(),
            Some(bot.local_url.as_str()),
            "never the package's fixed port"
        );
    }
    for a in [&maya, &jack] {
        a.detach_all();
        a.leave();
    }

    // No listenEnv: it could only listen where the package hard-codes.
    let silent = spec(ComponentKind::Plugin, "silent", "1.0.0", &pin, "");
    let nobody = adapter(&dirs, &shared, "nobody");
    run(&nobody, &silent);
    until("the refusal", || {
        status_of(&nobody, "silent")
            .message
            .contains("sidecar.listenEnv")
    })
    .await;
    assert_ne!(status_of(&nobody, "silent").state, "running");
    nobody.detach_all();
    nobody.leave();
    cleanup(&dirs);
}

/// Each account keeps its spawn records of its own sidecars apart.
#[test]
fn spawn_records_are_per_account() {
    let dirs = root("records");
    let shared = Shared::new(Ok(dirs.clone()));
    let (maya, jack) = (
        adapter(&dirs, &shared, "maya-dir"),
        adapter(&dirs, &shared, "jack-dir"),
    );
    let (a, b) = (
        maya.spawn_record_dir().unwrap(),
        jack.spawn_record_dir().unwrap(),
    );
    assert_ne!(a, b);
    assert!(a.starts_with(dirs.spawn_records()) && a.ends_with(Path::new("maya-dir")));
    let _ = Mutex::new(0);
    cleanup(&dirs);
}

/// A gateway on loopback that answers every request with `catalog`.
fn catalog_gateway(catalog: serde_json::Value) -> String {
    use std::io::{Read, Write};
    let server = super::loopback::bind_ephemeral().unwrap();
    let base = super::loopback::LoopbackHttp::of_addr(server.local_addr().unwrap());
    let body = catalog.to_string();
    std::thread::spawn(move || {
        for stream in server.incoming() {
            let Ok(mut stream) = stream else { return };
            let mut buf = [0u8; 4096];
            let _ = stream.read(&mut buf);
            let _ = write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
        }
    });
    base
}

/// An account that does not serve agents (a test account, which only looks)
/// runs no component and follows its catalog for what its windows show only;
/// one that serves resolves the same catalog into components.
#[tokio::test]
async fn an_account_that_does_not_serve_runs_no_component() {
    let dirs = root("view-only");
    let catalog = serde_json::json!({"components": [
        {"kind": "plugin", "name": "bot", "version": "1", "runtime": "sidecar",
         "requiresDesktop": true, "rollout": "auto", "installed": true,
         "artifacts": {super::sidecar_util::current_platform(): {"url": "/v1/artifacts/p", "sha256": "a".repeat(64)}},
         "sidecar": {"localUrl": "http://127.0.0.1:7191", "listenEnv": "L"}}
    ]});
    let gateway = catalog_gateway(catalog);
    let token =
        super::member_dir::session_token(serde_json::json!({"sub": "qa", "tenant": "org-a"}));
    for (serves, listed) in [(false, false), (true, true)] {
        let shared = Shared::new(Ok(dirs.clone()));
        let adapter = Adapter::new(
            Ok(dirs.clone()),
            &gateway,
            shared,
            "qa",
            serves,
            Arc::new(FakeBridge),
        );
        adapter.update_gateway(&gateway, token.clone());
        adapter.reconcile_bounded().await.expect("reconcile");
        let names: Vec<String> = adapter.status().into_iter().map(|s| s.name).collect();
        assert_eq!(
            names.contains(&"bot".to_string()),
            listed,
            "serves={serves}: {names:?}"
        );
        adapter.detach_all();
        adapter.leave();
    }
    cleanup(&dirs);
}
