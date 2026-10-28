use super::*;
use crate::internal_ext::sidecar_model::PluginLaunch;
use std::collections::HashSet;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

fn spec(name: &str, caps: &[&str]) -> SidecarSpec {
    SidecarSpec {
        kind: ComponentKind::Plugin,
        name: name.into(),
        version: "1.0.0".into(),
        artifact: None,
        manifest: None,
        local_url: "http://127.0.0.1:7192".into(),
        listen_env: String::new(),
        supervised_env: String::new(),
        args: Vec::new(),
        plugin: PluginLaunch {
            capabilities: caps.iter().map(|c| c.to_string()).collect(),
            token_env: "SK_TOKEN".into(),
            data_dir_env: "SK_DATA_DIR".into(),
            mounts_env: "SK_MOUNTS_FILE".into(),
            mount_root_env: "SK_MOUNT_ROOT".into(),
            ..PluginLaunch::default()
        },
        sandbox: Default::default(),
    }
}

fn temp(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("bnbloc-skills-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[test]
fn a_packed_skill_is_its_hash_a_line_feed_and_the_archive() {
    let framed = frame_packed_skill("abc123", &[0x1f, 0x8b, 0x0a, 0x00]);
    assert_eq!(&framed[..7], b"abc123\n");
    assert_eq!(
        &framed[7..],
        &[0x1f, 0x8b, 0x0a, 0x00],
        "the archive may hold line feeds"
    );
}

#[test]
fn capabilities_earn_launch_inputs_and_nothing_else() {
    let root = temp("env");
    let (member, mirror) = (root.join("member"), root.join("workspaces/.mounts"));

    let none = job_env(&spec("hooks-only", &[]), &member, Some(&mirror)).unwrap();
    assert!(
        none.env.iter().all(|(k, _)| k == "SK_DATA_DIR"),
        "no capability, no mounts or token: {none:?}"
    );
    assert!(none.token.is_none());

    let job = job_env(
        &spec("local-skills", &[CAP_MOUNTS, CAP_PROVIDER]),
        &member,
        Some(&mirror),
    )
    .unwrap();
    let get = |key: &str| {
        job.env
            .iter()
            .find(|(k, _)| k == key)
            .map(|(_, v)| v.clone())
    };
    assert_eq!(
        get("SK_MOUNTS_FILE"),
        Some(member.join("bridge/mounts/local-skills/mounts.json"))
    );
    assert_eq!(get("SK_MOUNT_ROOT"), Some(mirror.join("local-skills")));
    assert_eq!(
        get("SK_DATA_DIR"),
        Some(member.join("plugin-data/local-skills"))
    );
    let (env, token) = job.token.expect("a provider gets a bearer");
    assert_eq!(env, "SK_TOKEN");
    assert!(token.expose().len() >= 32);
    assert!(
        member.join("bridge/mounts/local-skills").is_dir() && mirror.join("local-skills").is_dir()
    );
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = std::fs::metadata(member.join("bridge/mounts/local-skills"))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o077, 0, "the descriptor folder is owner-only");
    }

    let only_provider = job_env(&spec("p", &[CAP_PROVIDER]), &member, None).unwrap();
    assert!(
        only_provider.env.iter().all(|(k, _)| k == "SK_DATA_DIR"),
        "a provider alone gets no mounts"
    );
    let err = job_env(&spec("m", &[CAP_MOUNTS]), &member, None).unwrap_err();
    assert!(
        err.contains("home folder"),
        "mounts need the shared folder: {err}"
    );
    let _ = std::fs::remove_dir_all(root);
}

#[test]
fn an_uninstalled_plugin_loses_its_descriptor_data_and_copies() {
    let root = temp("sweep");
    let (member, mirror) = (root.join("member"), root.join("mirror"));
    for name in ["kept", "gone"] {
        for dir in [
            mounts_dir(&member).join(name),
            member.join(PLUGIN_DATA_DIR).join(name),
            mirror.join(name),
        ] {
            std::fs::create_dir_all(&dir).unwrap();
            std::fs::write(dir.join("f"), b"x").unwrap();
        }
    }
    std::fs::create_dir_all(member.join("bridge/other")).unwrap();
    sweep_dirs(&member, Some(&mirror), &HashSet::from(["kept".to_string()]));
    for dir in [
        mounts_dir(&member).join("kept"),
        member.join(PLUGIN_DATA_DIR).join("kept"),
        mirror.join("kept"),
    ] {
        assert!(dir.is_dir(), "{} stays", dir.display());
    }
    for dir in [
        mounts_dir(&member).join("gone"),
        member.join(PLUGIN_DATA_DIR).join("gone"),
        mirror.join("gone"),
    ] {
        assert!(!dir.exists(), "{} goes", dir.display());
    }
    assert!(
        member.join("bridge/other").is_dir(),
        "the rest of the bridge's state is not ours to judge"
    );
    let _ = std::fs::remove_dir_all(root);
}

#[test]
fn the_registry_keeps_one_launch_per_plugin() {
    let mut providers = Providers::default();
    let first = providers.spawned("skills", "http://127.0.0.1:1", Secret::from_test("a"));
    let second = providers.spawned("skills", "http://127.0.0.1:2", Secret::from_test("b"));
    assert_eq!(providers.names(), vec!["skills".to_string()]);
    providers.exited(first);
    assert_eq!(
        providers.find("skills").map(|p| p.url),
        Some("http://127.0.0.1:2".to_string()),
        "the old launch's exit does not drop the new one"
    );
    providers.exited(second);
    assert!(providers.find("skills").is_none());
}

/// A one-shot loopback server answering every request with `status`, `headers`
/// and `body`, and sending back the request head it read.
async fn serve(
    status: &str,
    headers: &str,
    body: Vec<u8>,
) -> (String, tokio::task::JoinHandle<String>) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let head = format!(
        "HTTP/1.1 {status}\r\n{headers}Content-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    let task = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let mut buf = vec![0u8; 8192];
        let n = socket.read(&mut buf).await.unwrap();
        socket.write_all(head.as_bytes()).await.unwrap();
        socket.write_all(&body).await.unwrap();
        let _ = socket.shutdown().await;
        String::from_utf8_lossy(&buf[..n]).into_owned()
    });
    (url, task)
}

fn launch(url: &str) -> ProviderLaunch {
    ProviderLaunch {
        seq: 1,
        plugin: "skills".into(),
        url: url.into(),
        token: Secret::from_test("tok"),
    }
}

#[tokio::test]
async fn a_call_carries_the_bearer_and_returns_the_body_and_headers() {
    let (url, request) = serve("200 OK", "X-Skill-Hash: h1\r\n", b"archive".to_vec()).await;
    let (headers, body) = call(
        &launch(&url),
        reqwest::Method::POST,
        "/v1/pack?name=a",
        None,
        1024,
    )
    .await
    .unwrap();
    assert_eq!(body, b"archive");
    assert_eq!(headers.get("x-skill-hash").unwrap(), "h1");
    let head = request.await.unwrap();
    assert!(head.starts_with("POST /v1/pack?name=a "), "{head}");
    assert!(
        head.to_lowercase().contains("authorization: bearer tok"),
        "{head}"
    );
}

#[tokio::test]
async fn a_failure_is_the_providers_message_and_an_oversize_answer_is_refused() {
    let (url, _) = serve(
        "422 Unprocessable Entity",
        "",
        br#"{"error":"no skill named \"x\""}"#.to_vec(),
    )
    .await;
    let err = call(&launch(&url), reqwest::Method::GET, "/v1/state", None, 1024)
        .await
        .unwrap_err();
    assert_eq!(err, r#"no skill named "x""#);

    let (url, _) = serve("200 OK", "", vec![b'x'; 2048]).await;
    let err = call(&launch(&url), reqwest::Method::GET, "/v1/state", None, 1024)
        .await
        .unwrap_err();
    assert!(err.contains("more than 1024 bytes"), "{err}");

    let err = call(
        &launch("http://example.com:80"),
        reqwest::Method::GET,
        "/v1/state",
        None,
        1024,
    )
    .await
    .unwrap_err();
    assert!(
        err.contains("loopback"),
        "a provider is only ever called on this machine: {err}"
    );
}

#[test]
fn a_panel_earns_a_bearer_and_a_data_dir_and_nothing_else() {
    let root = temp("panel");
    let member = root.join("member");
    let mut s = spec("panelled", &[]);
    s.plugin.panel = true;
    let job = job_env(&s, &member, None).unwrap();
    assert!(
        job.env.iter().all(|(k, _)| k == "SK_DATA_DIR"),
        "a panel gets no mounts: {job:?}"
    );
    let (env, token) = job.token.expect("a panel plugin gets a bearer");
    assert_eq!(env, "SK_TOKEN");
    assert!(token.expose().len() >= 32);
    let _ = std::fs::remove_dir_all(root);
}

#[test]
fn a_panel_may_only_call_paths_under_v1_with_the_methods_it_is_given() {
    for (method, path) in [
        ("GET", "/v1/state"),
        ("POST", "/v1/agents/gemini/install"),
        ("PUT", "/v1/settings"),
        ("DELETE", "/v1/things/1"),
        ("GET", "/v1/pack?name=a%20b&x=1"),
    ] {
        assert!(panel_request(method, path).is_ok(), "{method} {path}");
    }
    for (method, path) in [
        ("PATCH", "/v1/state"),
        ("GET", "/state"),
        ("GET", "/v1"),
        ("GET", "/v1/"),
        ("GET", "/v1/../admin"),
        ("GET", "/v1/a/../b"),
        ("GET", "/v1/./a"),
        ("GET", "/v1//a"),
        ("GET", "/v1/%2e%2e/a"),
        ("GET", "/v1/a%2fb"),
        ("GET", "/v1/a\\b"),
        ("GET", "/v1/a#frag"),
        ("GET", "/v1/a b"),
        ("GET", "/v1/a\nb"),
        ("GET", "/v1/state?next=../x"),
        ("GET", "http://example.com/v1/state"),
        ("GET", "//example.com/v1/state"),
    ] {
        assert!(panel_request(method, path).is_err(), "{method} {path}");
    }
    let long = format!("/v1/{}", "a".repeat(MAX_PANEL_PATH));
    assert!(panel_request("GET", &long).is_err());
}

#[tokio::test]
async fn a_panel_call_keeps_the_status_of_a_failure_for_the_page() {
    let (url, _) = serve(
        "409 Conflict",
        "Content-Type: application/json\r\n",
        br#"{"error":"another operation is running"}"#.to_vec(),
    )
    .await;
    let (status, _, body) = call_raw(
        &launch(&url),
        reqwest::Method::POST,
        "/v1/agents/x/install",
        None,
        1024,
    )
    .await
    .unwrap();
    assert_eq!(status.as_u16(), 409);
    assert!(String::from_utf8_lossy(&body).contains("another operation"));
}

/// The folder the desktop gives a plugin for its mounts is directly under the folder it
/// tells the bridge to read them from (`ENV_MOUNTS_ROOT`, `mirror_root`): the bridge once
/// derived its own, without the account, and dropped every component's mount in the real
/// app. One path, one owner.
#[test]
fn the_folder_a_plugin_writes_its_mounts_in_is_directly_under_the_one_the_bridge_is_told() {
    let root = sandbox::workspaces_root().expect("a home folder");
    let mounts_root = mirror_root("acct1234").expect("a mirror root");
    assert_eq!(mounts_root, root.join(".mounts").join("acct1234"));

    let tmp = temp("mounts-root");
    let member = tmp.join("member");
    let job = job_env(&spec("agent-manager", &[CAP_MOUNTS]), &member, Some(&mounts_root_in(&tmp))).unwrap();
    let handed: Vec<PathBuf> = job
        .env
        .iter()
        .filter(|(k, _)| k == "SK_MOUNT_ROOT")
        .map(|(_, v)| v.clone())
        .collect();
    assert_eq!(handed, vec![mounts_root_in(&tmp).join("agent-manager")]);
    assert_eq!(
        handed[0].parent().unwrap(),
        mounts_root_in(&tmp),
        "a plugin's folder sits directly under the mounts root the bridge is told"
    );
}

fn mounts_root_in(tmp: &Path) -> PathBuf {
    tmp.join("workspaces/.mounts/acct1234")
}
