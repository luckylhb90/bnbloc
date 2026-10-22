use std::collections::HashSet;
use std::io::{BufRead, BufReader, Write};

use super::*;
use crate::internal_ext::component_settings::merge_settings;
use crate::internal_ext::sidecar_model::GatewayAccess;

fn catalog(json: serde_json::Value) -> ComponentsResponse {
    serde_json::from_value(json).expect("catalog decodes")
}

fn sample_catalog() -> ComponentsResponse {
    catalog(serde_json::json!({"components": [
        {"kind": "mcp", "name": "codeword", "version": "1.0.0", "runtime": "stdio", "rollout": "auto", "installed": true,
         "artifacts": {"darwin-arm64": {"url": "/v1/artifacts/sha256/AA", "sha256": "AA00000000000000000000000000000000000000000000000000000000000000"}, "linux-amd64": {"url": "/v1/artifacts/sha256/bb", "sha256": "bb00000000000000000000000000000000000000000000000000000000000000"}},
         "settings": [{"key": "salt", "label": "Salt", "help": "salt", "type": "secret", "scope": "member", "required": true, "deliver": {"env": "CODEWORD_SALT"}},
                      {"key": "lang", "label": "Language", "type": "choice", "options": ["en", "fr"], "scope": "org", "default": "en"}],
         "settingsSet": ["lang"],
         "mcp": {"transport": "stdio",
                 "tools": [{"name": "get_codeword", "description": "the codeword"}]}},
        {"kind": "mcp", "name": "search", "version": "2.0.0", "runtime": "endpoint", "rollout": "auto", "installed": true,
         "settings": [{"key": "token", "label": "Token", "type": "secret", "scope": "org", "required": true}],
         "settingsMissing": ["token"],
         "mcp": {"transport": "http", "tools": [{"name": "search"}], "http": {"auth": {"scheme": "bearer", "setting": "token"}}}},
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
    assert_eq!(cw.settings.len(), 2);
    assert!(cw.settings[0].kind == "secret" && cw.settings[0].required);
    assert_eq!(
        cw.settings[0].deliver.as_ref().unwrap().env,
        "CODEWORD_SALT"
    );
    assert_eq!(cw.settings[1].options, vec!["en", "fr"]);
    assert_eq!(cw.settings_set, vec!["lang"]);
    // An http tool has no binary; the catalog says what it misses.
    let search = &tools[1];
    assert!(search.binary.is_none());
    assert_eq!(search.settings_missing, vec!["token"]);
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
    assert!(tools[0].settings.is_empty());
    // An odd settings entry is defaulted, never a failed catalog.
    let c = catalog(serde_json::json!({"components": [
        {"kind": "mcp", "name": "odd", "version": "1.0.0", "runtime": "stdio", "rollout": "auto", "installed": true,
         "settings": [{"surprise": true}], "mcp": {"transport": "stdio"}}
    ]}));
    assert_eq!(installed_mcp_tools(&c, "darwin-arm64")[0].settings.len(), 1);
}

#[test]
fn settings_views_never_return_secrets_or_org_values() {
    let tools = installed_mcp_tools(&sample_catalog(), "darwin-arm64");
    let cw = &tools[0];
    let update: Values = [("salt".to_string(), "s3cr3t".to_string())].into();
    let (values, changed) =
        merge_settings(&cw.settings, "member", &Values::new(), &update, &[]).expect("valid");
    assert_eq!(changed, vec!["salt"]);
    assert!(missing(&cw.settings, &values, &cw.settings_missing).is_empty());
    let view = fields(&cw.settings, &values, &cw.settings_set);
    assert!(view[0].set && view[0].value.is_none());
    assert!(
        view[1].set && view[1].value.is_none(),
        "an org setting: set, never its value"
    );
    let json = serde_json::to_string(&view).unwrap();
    assert!(!json.contains("s3cr3t"), "{json}");
    // An org value is never the member's to set.
    let org: Values = [("lang".to_string(), "fr".to_string())].into();
    assert!(merge_settings(&cw.settings, "member", &Values::new(), &org, &[]).is_err());
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
    // The index of a member is its own directory's (not the shared store's);
    // the binaries it lists stay in the store, which keeps what any catalog
    // keeps and nothing else.
    let member = temp_dir("gc-member");
    write_index(&member, &entries).unwrap();
    let kept_name = keep.file_name().unwrap().to_string_lossy().into_owned();
    collect_garbage(&dir, &HashSet::from([kept_name.clone()]));
    let mut left: Vec<String> = std::fs::read_dir(&dir)
        .unwrap()
        .flatten()
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    left.sort();
    assert_eq!(left, vec![kept_name]);
    let index: serde_json::Value =
        serde_json::from_slice(&std::fs::read(member.join(INDEX_FILE)).unwrap()).unwrap();
    assert_eq!(index["tools"][0]["name"], "codeword");
    assert_eq!(index["tools"][0]["path"], keep.to_string_lossy().as_ref());
    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&member);
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
    let dir = temp_dir("fetch");
    let sha = Sha256Pin::of(BIN);
    let dest = tool_file(&dir, "codeword", &sha);
    let url = format!("/v1/artifacts/sha256/{sha}");
    let first = ensure_verified(&gateway, &url, &sha, &dest, true, |_| {})
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
    let again = ensure_verified(&gateway, &url, &sha, &dest, true, |_| {})
        .await
        .expect("cached");
    assert!(!again, "a verified binary is kept");
    assert_eq!(hits.load(std::sync::atomic::Ordering::SeqCst), 1);
    let zero = Sha256Pin::parse(&"0".repeat(64)).unwrap();
    let wrong = tool_file(&dir, "codeword", &zero);
    let refused = ensure_verified(&gateway, &url, &zero, &wrong, true, |_| {}).await;
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
         "settings": [{"key": "token", "label": "Token", "type": "secret", "scope": "member", "required": true}],
         "mcp": {"transport": "http", "tools": [{"name": "search"}],
                 "http": {"auth": {"scheme": "bearer", "setting": "token"}}}}
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
    // The sweep keeps every installed tool's settings and the oauth tool's
    // sign-in; a bearer tool keeps no sign-in.
    let keep = mcp_keep(&tools);
    assert!(keep.sign_ins.contains("drive") && keep.tools.contains("drive"));
    assert!(keep.tools.contains("search") && !keep.sign_ins.contains("search"));
}

/// Catalog refreshes during a slow pass never queue passes: one runs, at
/// most one (the newest) waits.
#[test]
fn materialization_passes_coalesce() {
    let passes = Coalesced::default();
    assert!(passes.request(1), "the first request starts the runner");
    assert_eq!(passes.next(), Some(1));
    // While the runner works, three catalogs arrive: only the newest waits.
    assert!(!passes.request(2));
    assert!(!passes.request(3));
    assert!(!passes.request(4));
    assert_eq!(passes.next(), Some(4));
    assert_eq!(passes.next(), None);
    assert!(!passes.finish(), "nothing waits: the runner stops");
    assert!(passes.request(5), "the next request starts a runner again");
    assert_eq!(passes.next(), Some(5));
    assert!(
        !passes.request(6),
        "a request between the last next and finish"
    );
    assert!(passes.finish(), "keeps the runner going");
    assert_eq!(passes.next(), Some(6));
}
