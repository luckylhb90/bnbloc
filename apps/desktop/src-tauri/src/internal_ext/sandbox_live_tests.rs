//! Live check of the desktop sandbox path against a REAL sandbox sidecar
//! (ignored by default; needs the package and its platform files):
//!
//! ```sh
//! SANDBOX_LIVE_PACKAGE=/path/sandbox-package.json \
//! SANDBOX_LIVE_FILES="darwin-arm64=/path/sidecar,darwin-arm64/<file>=/path/<file>,..." \
//! SANDBOX_LIVE_IMAGE=alpine:3.20 \
//! cargo test --manifest-path apps/desktop/src-tauri/Cargo.toml sandbox_live -- --ignored --nocapture
//! ```
//!
//! It runs the same code the manager runs: the member-catalog entry →
//! spec, bundle download/verify/layout from a local artifact server, the
//! sandbox launch env with a fresh bearer, discover, then the prewarm
//! request (pull + create with the workspaces share), a VM restart, and
//! delete.
use std::collections::HashMap;
use std::path::PathBuf;
use std::time::{Duration, Instant};

use sha2::{Digest, Sha256};

use crate::internal_ext::loopback::{self, LoopbackHttp};
use crate::internal_ext::sandbox_api::{create_machine_request, SandboxApi};
use crate::internal_ext::sandbox_bundle;
use crate::internal_ext::sidecar_model::{
    resolve_component_specs, ComponentsResponse, GatewayAccess, GuestProfile,
};
use crate::internal_ext::sidecar_util::{current_platform, hex_lower};
use crate::secret::Secret;

fn serve_files(files: HashMap<String, Vec<u8>>) -> String {
    use std::io::{BufRead, BufReader, Write};
    let server = loopback::bind_ephemeral().expect("bind");
    let base = format!("http://{}", server.local_addr().expect("addr"));
    std::thread::spawn(move || {
        for stream in server.incoming() {
            let Ok(mut stream) = stream else { continue };
            let mut reader = BufReader::new(stream.try_clone().expect("clone"));
            let mut line = String::new();
            let _ = reader.read_line(&mut line);
            let target = line.split_whitespace().nth(1).unwrap_or("");
            let path = target.split('?').next().unwrap_or("").to_string();
            loop {
                let mut header = String::new();
                if reader.read_line(&mut header).is_err() || header.trim().is_empty() {
                    break;
                }
            }
            let key = path
                .trim_start_matches("/v1/artifacts/")
                .replace("%2F", "/");
            match files.get(&key) {
                Some(body) => {
                    let _ = write!(
                        stream,
                        "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                        body.len()
                    );
                    let _ = stream.write_all(body);
                }
                None => {
                    let _ = write!(
                        stream,
                        "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"
                    );
                }
            }
        }
    });
    base
}

#[tokio::test(flavor = "multi_thread")]
#[ignore = "needs a real sandbox sidecar package (see module doc)"]
async fn sandbox_live_bundle_launch_discover_prewarm_restart() {
    let package_path = std::env::var("SANDBOX_LIVE_PACKAGE").expect("SANDBOX_LIVE_PACKAGE");
    let files_spec = std::env::var("SANDBOX_LIVE_FILES").expect("SANDBOX_LIVE_FILES");
    let image = std::env::var("SANDBOX_LIVE_IMAGE").unwrap_or_else(|_| "alpine:3.20".into());
    let platform = current_platform();

    // Artifacts: key → bytes, served under /v1/artifacts/<key>.
    let mut bodies = HashMap::new();
    for pair in files_spec.split(',') {
        let (key, path) = pair.split_once('=').expect("key=path");
        bodies.insert(key.to_string(), std::fs::read(path).expect("read artifact"));
    }
    let mut package: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&package_path).expect("read package")).expect("json");
    let base_url = serve_files(bodies.clone());
    let mut artifacts = serde_json::Map::new();
    for (key, body) in &bodies {
        artifacts.insert(
            key.clone(),
            serde_json::json!({
                "url": format!("{base_url}/v1/artifacts/{key}"),
                "sha256": hex_lower(&Sha256::digest(body)),
            }),
        );
    }
    package["artifacts"] = serde_json::Value::Object(artifacts);
    package["guest"] = serde_json::json!({"image": image, "cpus": 1, "memoryMiB": 512});
    package["kind"] = "sandbox".into();
    package["requiresDesktop"] = true.into();
    package["rollout"] = "auto".into();
    package["installed"] = true.into();
    let catalog: ComponentsResponse =
        serde_json::from_value(serde_json::json!({"components": [package]})).expect("catalog");
    let resolved = resolve_component_specs(&catalog, &platform);
    assert!(
        resolved.failures.is_empty(),
        "{:?}",
        resolved
            .failures
            .iter()
            .map(|f| &f.message)
            .collect::<Vec<_>>()
    );
    let spec = resolved.specs.into_iter().next().expect("sandbox spec");

    // Bundle.
    let root = std::env::temp_dir().join(format!("bnbloc-sbx-live-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    let plan = sandbox_bundle::bundle_plan(&root.join("cache"), &spec).expect("plan");
    let gateway = GatewayAccess {
        base_url: base_url.clone(),
        token: "session".into(),
    };
    let t = Instant::now();
    sandbox_bundle::prepare_bundle(&gateway, &plan, |_, _| {})
        .await
        .expect("bundle");
    eprintln!(
        "bundle laid out in {:?}: {}",
        t.elapsed(),
        plan.dir.display()
    );

    // Launch, like spawn_once + sandbox_launch_env.
    let port = loopback::bind_ephemeral().unwrap().local_addr().unwrap();
    let url = format!("http://{port}");
    let token = Secret::generate().unwrap();
    let data_dir = root.join("data");
    std::fs::create_dir_all(&data_dir).unwrap();
    let mut cmd = std::process::Command::new(plan.exe());
    cmd.env(&spec.supervised_env, "1");
    cmd.env(
        &spec.listen_env,
        LoopbackHttp::parse(&url).unwrap().authority(),
    );
    for (k, v) in sandbox_bundle::launch_env(&spec, &plan.exe(), token.expose(), &data_dir) {
        cmd.env(k, v);
    }
    cmd.stdin(std::process::Stdio::piped());
    let mut child = cmd.spawn().expect("spawn sidecar");

    let api = SandboxApi {
        url: url.clone(),
        token: token.clone(),
    };
    let t = Instant::now();
    let discovery = loop {
        match api.discover().await {
            Ok(d) => break d,
            Err(e) if t.elapsed() > Duration::from_secs(60) => panic!("discover: {e}"),
            Err(_) => tokio::time::sleep(Duration::from_millis(250)).await,
        }
    };
    eprintln!("discover in {:?}: {discovery:?}", t.elapsed());

    let workspaces = root.join("bnbloc-workspaces");
    std::fs::create_dir_all(&workspaces).unwrap();
    let t = Instant::now();
    api.pull_image(&image).await.expect("pull");
    let guest = GuestProfile {
        image: image.clone(),
        cpus: 1,
        memory_mib: 512,
    };
    let machine = api
        .create_machine(create_machine_request(
            "bnbloc",
            &guest,
            &workspaces,
            "workspaces",
            &discovery,
            "me",
            &[],
        ))
        .await
        .expect("create machine");
    eprintln!("prewarm in {:?}: {machine:?}", t.elapsed());
    assert_eq!(machine.state, "running");
    assert_eq!(
        api.machine("bnbloc")
            .await
            .unwrap()
            .map(|m| m.state)
            .as_deref(),
        Some("running")
    );

    let t = Instant::now();
    api.lifecycle("bnbloc", "stop").await.expect("stop");
    let restarted = api.lifecycle("bnbloc", "start").await.expect("start");
    eprintln!("restart in {:?}: {restarted:?}", t.elapsed());
    assert_eq!(restarted.state, "running");

    api.delete_machine("bnbloc").await.expect("delete");
    assert!(api.machine("bnbloc").await.unwrap().is_none());
    api.delete_machine("bnbloc")
        .await
        .expect("delete is idempotent");

    // Wrong bearer is refused.
    let wrong = SandboxApi {
        url,
        token: Secret::generate().unwrap(),
    };
    assert_eq!(wrong.discover().await.unwrap_err().code, "unauthorized");

    // Supervised: closing stdin stops it.
    drop(child.stdin.take());
    let t = Instant::now();
    loop {
        if child.try_wait().unwrap().is_some() {
            break;
        }
        assert!(
            t.elapsed() < Duration::from_secs(20),
            "sidecar ignored stdin EOF"
        );
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let _ = std::fs::remove_dir_all(&root);
    let _: PathBuf = plan.exe();
}
