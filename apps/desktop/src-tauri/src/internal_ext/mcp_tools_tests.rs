use std::collections::HashSet;
use std::io::{BufRead, BufReader, Write};

use super::*;
use crate::internal_ext::sidecar_model::GatewayAccess;

fn catalog(json: serde_json::Value) -> ComponentsResponse {
    serde_json::from_value(json).expect("catalog decodes")
}

fn sample_catalog() -> ComponentsResponse {
    catalog(serde_json::json!({"components": [
        {"kind": "mcp", "name": "codeword", "version": "1.0.0", "runtime": "stdio", "rollout": "auto", "installed": true,
         "artifacts": {"darwin-arm64": {"url": "/v1/artifacts/sha256/AA", "sha256": "AA00000000000000000000000000000000000000000000000000000000000000"}, "linux-amd64": {"url": "/v1/artifacts/sha256/bb", "sha256": "bb00000000000000000000000000000000000000000000000000000000000000"}},
         "mcp": {"transport": "stdio", "credentials": "member",
                 "tools": [{"name": "get_codeword", "description": "the codeword"}],
                 "env": [{"name": "CODEWORD_SALT", "description": "salt", "secret": true, "required": true}, {"name": "CODEWORD_LANG"}]}},
        {"kind": "mcp", "name": "search", "version": "2.0.0", "runtime": "endpoint", "rollout": "auto", "installed": true,
         "mcp": {"transport": "http", "credentials": "org", "tools": [{"name": "search"}], "env": [{"name": "TOOLS_TOKEN", "secret": true, "required": true}]}},
        {"kind": "mcp", "name": "optin", "version": "1.0.0", "runtime": "stdio", "rollout": "optin", "installed": false},
        {"kind": "mcp", "name": "off", "version": "1.0.0", "runtime": "stdio", "rollout": "off", "installed": true,
         "mcp": {"transport": "stdio", "tools": [{"name": "x"}]}},
        {"kind": "core", "name": "acme", "version": "1.0.0", "runtime": "manifest", "rollout": "auto", "installed": true}
    ]}))
}

#[test]
fn installed_tools_follow_the_catalog_gate() {
    let tools = installed_mcp_tools(&sample_catalog(), "darwin-arm64");
    let names: Vec<&str> = tools.iter().map(|t| t.name.as_str()).collect();
    assert_eq!(names, vec!["codeword", "search"]);
    let cw = &tools[0];
    let binary = cw.binary.as_ref().expect("binary");
    assert_eq!(binary.url, "/v1/artifacts/sha256/AA");
    assert_eq!(
        binary.sha256.as_str(),
        "aa00000000000000000000000000000000000000000000000000000000000000",
        "pins are normalized"
    );
    assert_eq!(cw.tools[0].name, "get_codeword");
    assert_eq!(cw.tools[0].description.as_deref(), Some("the codeword"));
    assert_eq!(cw.fields.len(), 2);
    assert!(cw.fields[0].secret && cw.fields[0].required);
    assert_eq!(cw.fields[0].help.as_deref(), Some("salt"));
    // Org credentials take no member settings; http tools have no binary.
    let search = &tools[1];
    assert!(search.fields.is_empty() && search.binary.is_none());
    // No binary for an unpublished platform.
    let other = installed_mcp_tools(&sample_catalog(), "windows-amd64");
    assert!(other[0].binary.is_none());
}

#[test]
fn an_unknown_mcp_block_never_fails_the_catalog() {
    let c = catalog(serde_json::json!({"components": [
        {"kind": "mcp", "name": "odd", "version": "1.0.0", "runtime": "stdio", "rollout": "auto", "installed": true,
         "mcp": {"surprise": true}}
    ]}));
    let tools = installed_mcp_tools(&c, "darwin-arm64");
    assert_eq!(tools.len(), 1);
    assert!(tools[0].fields.is_empty());
}

#[test]
fn member_settings_views_never_return_secrets() {
    let tools = installed_mcp_tools(&sample_catalog(), "darwin-arm64");
    let schema = &tools[0].fields;
    let mut update = std::collections::BTreeMap::new();
    update.insert("CODEWORD_SALT".to_string(), "s3cr3t".to_string());
    let values = merge(schema, &Values::new(), update).expect("valid");
    assert!(missing_required(schema, &values).is_empty());
    let json = serde_json::to_string(&views(schema, &values)).unwrap();
    assert!(!json.contains("s3cr3t"), "{json}");
}

fn temp_dir(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("bnbloc-mcp-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[test]
fn index_is_replaced_atomically_and_garbage_is_collected() {
    let dir = temp_dir("gc");
    let pin = |c: &str| Sha256Pin::parse(&c.repeat(64)).unwrap();
    let keep = tool_file(&dir, "codeword", &pin("a"));
    let old = tool_file(&dir, "codeword", &pin("b"));
    let removed = tool_file(&dir, "gone", &pin("c"));
    for p in [&keep, &old, &removed] {
        std::fs::write(p, b"bin").unwrap();
    }
    std::fs::write(dir.join(".partial.part"), b"x").unwrap();
    assert_eq!(
        keep.file_name().unwrap().to_string_lossy(),
        format!(
            "mcp@codeword-{}{}",
            "a".repeat(64),
            crate::internal_ext::sidecar_util::exe_suffix()
        )
    );
    let entries = vec![IndexEntry {
        name: "codeword".into(),
        sha256: pin("a"),
        path: keep.to_string_lossy().into_owned(),
    }];
    write_index(&dir, &entries).unwrap();
    collect_garbage(&dir, &HashSet::from([keep.clone()]));
    let mut left: Vec<String> = std::fs::read_dir(&dir)
        .unwrap()
        .flatten()
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    left.sort();
    assert_eq!(
        left,
        vec![
            INDEX_FILE.to_string(),
            keep.file_name().unwrap().to_string_lossy().into_owned()
        ]
    );
    let index: serde_json::Value =
        serde_json::from_slice(&std::fs::read(dir.join(INDEX_FILE)).unwrap()).unwrap();
    assert_eq!(index["tools"][0]["name"], "codeword");
    assert_eq!(index["tools"][0]["path"], keep.to_string_lossy().as_ref());
    let _ = std::fs::remove_dir_all(&dir);
}

/// A one-route HTTP server answering every request with `body`; counts hits.
fn serve(body: &'static [u8]) -> (String, std::sync::Arc<std::sync::atomic::AtomicUsize>) {
    let server = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
    let base = format!("http://127.0.0.1:{}", server.local_addr().unwrap().port());
    let hits = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let count = std::sync::Arc::clone(&hits);
    std::thread::spawn(move || {
        for stream in server.incoming() {
            let Ok(mut stream) = stream else { continue };
            let mut reader = BufReader::new(stream.try_clone().expect("clone"));
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() || line.trim().is_empty() {
                    break;
                }
            }
            count.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            let mut out = format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                body.len()
            )
            .into_bytes();
            out.extend_from_slice(body);
            let _ = stream.write_all(&out);
        }
    });
    (base, hits)
}

/// The shared verified download materializes a stdio tool's binary once
/// (executable, under the tools dir), keeps it while the pin holds, and
/// refuses bytes that do not match the pin.
#[tokio::test(flavor = "multi_thread")]
async fn binaries_are_downloaded_verified_and_cached() {
    const BIN: &[u8] = b"#!/bin/sh\necho codeword\n";
    let (base, hits) = serve(BIN);
    let gateway = GatewayAccess {
        base_url: base,
        token: "member-session".into(),
    };
    let client = reqwest::Client::new();
    let dir = temp_dir("fetch");
    let sha = Sha256Pin::of(BIN);
    let dest = tool_file(&dir, "codeword", &sha);
    let url = format!("/v1/artifacts/sha256/{sha}");
    let first = ensure_verified(&client, &gateway, &url, &sha, &dest, true, |_| {})
        .await
        .expect("download");
    assert!(first, "the first pass downloads");
    assert_eq!(std::fs::read(&dest).unwrap(), BIN);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_ne!(
            std::fs::metadata(&dest).unwrap().permissions().mode() & 0o111,
            0
        );
    }
    let again = ensure_verified(&client, &gateway, &url, &sha, &dest, true, |_| {})
        .await
        .expect("cached");
    assert!(!again, "a verified binary is kept");
    assert_eq!(hits.load(std::sync::atomic::Ordering::SeqCst), 1);
    let zero = Sha256Pin::parse(&"0".repeat(64)).unwrap();
    let wrong = tool_file(&dir, "codeword", &zero);
    let refused = ensure_verified(&client, &gateway, &url, &zero, &wrong, true, |_| {}).await;
    assert!(matches!(refused, Err(FetchError::Mismatch(_))));
    assert!(!wrong.exists(), "mismatched bytes never land");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn an_oauth_tool_carries_its_sign_in_spec() {
    let c = catalog(serde_json::json!({"components": [
        {"kind": "mcp", "name": "drive", "version": "1.0.0", "runtime": "endpoint", "rollout": "auto", "installed": true,
         "endpoint": {"baseUrl": "https://tools.example.com/mcp"},
         "mcp": {"transport": "http", "tools": [{"name": "list_files"}],
                 "http": {"auth": {"scheme": "oauth", "clientId": "bnbloc", "scopes": ["files:read"]}}}},
        {"kind": "mcp", "name": "search", "version": "2.0.0", "runtime": "endpoint", "rollout": "auto", "installed": true,
         "endpoint": {"baseUrl": "https://tools.example.com/search"},
         "mcp": {"transport": "http", "credentials": "member", "tools": [{"name": "search"}],
                 "env": [{"name": "TOOLS_TOKEN", "secret": true, "required": true}],
                 "http": {"auth": {"scheme": "bearer", "env": "TOOLS_TOKEN"}}}}
    ]}));
    let tools = installed_mcp_tools(&c, "darwin-arm64");
    let drive = &tools[0];
    assert_eq!(
        drive.oauth,
        Some(OAuthSpec {
            url: "https://tools.example.com/mcp".into(),
            client_id: Some("bnbloc".into()),
            scopes: vec!["files:read".into()],
        })
    );
    // The sweep keeps the oauth tool's sign-in; a bearer tool keeps member
    // settings, not a sign-in.
    let keep = mcp_keep(&tools);
    assert!(keep.sign_ins.contains("drive") && !keep.settings.contains("drive"));
    assert!(keep.settings.contains("search") && !keep.sign_ins.contains("search"));
}
