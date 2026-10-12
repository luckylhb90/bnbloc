use std::collections::HashMap;
use std::sync::Arc;

use parking_lot::Mutex;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

use super::*;

/// One request the mock server saw.
#[derive(Clone, Debug)]
struct Seen {
    method: String,
    target: String,
    headers: HashMap<String, String>,
    body: String,
}

impl Seen {
    fn path(&self) -> &str {
        self.target.split('?').next().unwrap_or_default()
    }
    fn form(&self) -> HashMap<String, String> {
        Url::parse(&format!("http://x/?{}", self.body))
            .unwrap()
            .query_pairs()
            .map(|(k, v)| (k.into_owned(), v.into_owned()))
            .collect()
    }
}

type Reply = (u16, Vec<(String, String)>, String);
type Handler = Arc<dyn Fn(&Seen, &str) -> Reply + Send + Sync>;

/// A tiny HTTP/1.1 server (one request per connection) standing in for the
/// MCP server, its authorization server and the browser's redirects.
struct Mock {
    base: String,
    seen: Arc<Mutex<Vec<Seen>>>,
}

impl Mock {
    async fn start(handler: Handler) -> Mock {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://127.0.0.1:{}", listener.local_addr().unwrap().port());
        let seen = Arc::new(Mutex::new(Vec::new()));
        let (b, s) = (base.clone(), Arc::clone(&seen));
        tokio::spawn(async move {
            loop {
                let Ok((mut sock, _)) = listener.accept().await else {
                    return;
                };
                let (handler, b, s) = (Arc::clone(&handler), b.clone(), Arc::clone(&s));
                tokio::spawn(async move {
                    let mut buf = Vec::new();
                    let mut chunk = [0u8; 4096];
                    let (head_end, len) = loop {
                        let n = sock.read(&mut chunk).await.unwrap_or(0);
                        if n == 0 {
                            return;
                        }
                        buf.extend_from_slice(&chunk[..n]);
                        if let Some(at) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
                            let head = String::from_utf8_lossy(&buf[..at]).to_string();
                            let len = head
                                .lines()
                                .find_map(|l| {
                                    let (k, v) = l.split_once(':')?;
                                    k.eq_ignore_ascii_case("content-length")
                                        .then(|| v.trim().parse::<usize>().ok())
                                        .flatten()
                                })
                                .unwrap_or(0);
                            break (at + 4, len);
                        }
                    };
                    while buf.len() < head_end + len {
                        let n = sock.read(&mut chunk).await.unwrap_or(0);
                        if n == 0 {
                            break;
                        }
                        buf.extend_from_slice(&chunk[..n]);
                    }
                    let head = String::from_utf8_lossy(&buf[..head_end]).to_string();
                    let mut lines = head.lines();
                    let mut first = lines.next().unwrap_or_default().split(' ');
                    let method = first.next().unwrap_or_default().to_string();
                    let target = first.next().unwrap_or_default().to_string();
                    let headers = lines
                        .filter_map(|l| l.split_once(':'))
                        .map(|(k, v)| (k.trim().to_ascii_lowercase(), v.trim().to_string()))
                        .collect();
                    let body = String::from_utf8_lossy(&buf[head_end..]).to_string();
                    let req = Seen {
                        method,
                        target,
                        headers,
                        body,
                    };
                    s.lock().push(req.clone());
                    let (status, hdrs, body) = handler(&req, &b);
                    let mut resp = format!(
                        "HTTP/1.1 {status} X\r\nContent-Length: {}\r\nConnection: close\r\n",
                        body.len()
                    );
                    for (k, v) in hdrs {
                        resp.push_str(&format!("{k}: {v}\r\n"));
                    }
                    resp.push_str("\r\n");
                    resp.push_str(&body);
                    let _ = sock.write_all(resp.as_bytes()).await;
                    let _ = sock.shutdown().await;
                });
            }
        });
        Mock { base, seen }
    }

    fn paths(&self) -> Vec<String> {
        self.seen
            .lock()
            .iter()
            .map(|s| s.path().to_string())
            .collect()
    }

    fn find(&self, path: &str) -> Option<Seen> {
        self.seen.lock().iter().find(|s| s.path() == path).cloned()
    }
}

fn json(status: u16, v: serde_json::Value) -> Reply {
    (
        status,
        vec![("Content-Type".into(), "application/json".into())],
        v.to_string(),
    )
}

fn not_found() -> Reply {
    (404, vec![], String::new())
}

/// How the mock authorization server behaves.
#[derive(Clone)]
struct Behavior {
    challenge_header: bool,
    pkce: bool,
    registration: bool,
    iss_supported: bool,
    /// The `iss` the authorization response carries (None: omitted;
    /// Some("") : the real issuer).
    iss: Option<String>,
    state_override: Option<String>,
    rotate_refresh: bool,
}

impl Default for Behavior {
    fn default() -> Self {
        Behavior {
            challenge_header: true,
            pkce: true,
            registration: true,
            iss_supported: true,
            iss: Some(String::new()),
            state_override: None,
            rotate_refresh: true,
        }
    }
}

/// The tool's MCP server at /mcp with its authorization server at /tenant1
/// on one origin. The authorization endpoint answers the browser with the
/// redirect back to the loopback callback.
fn handler(b: Behavior, challenge: Arc<Mutex<String>>) -> Handler {
    Arc::new(move |req: &Seen, base: &str| {
        let issuer = format!("{base}/tenant1");
        match (req.method.as_str(), req.path()) {
            ("POST", "/mcp") => {
                let mut h = vec![];
                if b.challenge_header {
                    h.push((
                        "WWW-Authenticate".into(),
                        format!(r#"Bearer resource_metadata="{base}/.well-known/oauth-protected-resource/mcp", scope="files:read""#),
                    ));
                }
                (401, h, String::new())
            }
            ("GET", "/.well-known/oauth-protected-resource/mcp") if b.challenge_header => json(
                200,
                serde_json::json!({"resource": format!("{base}/mcp"), "authorization_servers": [issuer], "scopes_supported": ["files:read", "files:write"]}),
            ),
            ("GET", "/.well-known/oauth-protected-resource") => json(
                200,
                serde_json::json!({"resource": format!("{base}/mcp"), "authorization_servers": [issuer], "scopes_supported": ["files:read", "files:write"]}),
            ),
            // RFC 8414 path insertion is missing: OIDC path insertion answers.
            ("GET", "/.well-known/openid-configuration/tenant1") => {
                let mut m = serde_json::json!({
                    "issuer": issuer,
                    "authorization_endpoint": format!("{base}/tenant1/authorize"),
                    "token_endpoint": format!("{base}/tenant1/token"),
                    "scopes_supported": ["files:read", "offline_access"],
                    "authorization_response_iss_parameter_supported": b.iss_supported,
                });
                if b.pkce {
                    m["code_challenge_methods_supported"] = serde_json::json!(["S256"]);
                }
                if b.registration {
                    m["registration_endpoint"] =
                        serde_json::json!(format!("{base}/tenant1/register"));
                }
                json(200, m)
            }
            ("POST", "/tenant1/register") => {
                json(201, serde_json::json!({"client_id": "dyn-client"}))
            }
            ("GET", "/tenant1/authorize") => {
                let q: HashMap<String, String> = Url::parse(&format!("http://x{}", req.target))
                    .unwrap()
                    .query_pairs()
                    .map(|(k, v)| (k.into_owned(), v.into_owned()))
                    .collect();
                *challenge.lock() = q.get("code_challenge").cloned().unwrap_or_default();
                let mut back = Url::parse(&q["redirect_uri"]).unwrap();
                {
                    let mut p = back.query_pairs_mut();
                    p.append_pair("code", "the-code");
                    p.append_pair("state", b.state_override.as_deref().unwrap_or(&q["state"]));
                    match &b.iss {
                        Some(i) if i.is_empty() => {
                            p.append_pair("iss", &issuer);
                        }
                        Some(i) => {
                            p.append_pair("iss", i);
                        }
                        None => {}
                    }
                }
                (
                    302,
                    vec![("Location".into(), back.to_string())],
                    String::new(),
                )
            }
            ("POST", "/tenant1/token") => {
                let f = req.form();
                match f.get("grant_type").map(String::as_str) {
                    Some("authorization_code") => {
                        let ok = pkce_challenge(
                            f.get("code_verifier")
                                .map(String::as_str)
                                .unwrap_or_default(),
                        ) == *challenge.lock()
                            && f.get("code").map(String::as_str) == Some("the-code")
                            && f.get("resource").map(String::as_str)
                                == Some(format!("{base}/mcp").as_str());
                        if !ok {
                            return json(400, serde_json::json!({"error": "invalid_grant"}));
                        }
                        json(
                            200,
                            serde_json::json!({"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "rt-1", "scope": "files:read"}),
                        )
                    }
                    Some("refresh_token") => match f.get("refresh_token").map(String::as_str) {
                        Some("rt-1")
                            if f.get("resource").map(String::as_str)
                                == Some(format!("{base}/mcp").as_str()) =>
                        {
                            let mut v = serde_json::json!({"access_token": "at-2", "token_type": "bearer", "expires_in": 60});
                            if b.rotate_refresh {
                                v["refresh_token"] = serde_json::json!("rt-2");
                            }
                            json(200, v)
                        }
                        _ => json(400, serde_json::json!({"error": "invalid_grant"})),
                    },
                    _ => json(400, serde_json::json!({"error": "unsupported_grant_type"})),
                }
            }
            _ => not_found(),
        }
    })
}

/// A browser that follows the authorization redirect to the loopback
/// callback.
fn browser() -> impl FnOnce(String) -> Result<(), String> {
    |url: String| {
        tokio::spawn(async move {
            let http = oauth_client().unwrap();
            let resp = http.get(&url).send().await.unwrap();
            let location = resp.headers()["location"].to_str().unwrap().to_string();
            let _ = http.get(&location).send().await;
        });
        Ok(())
    }
}

async fn run(b: Behavior, spec: impl FnOnce(&str) -> OAuthSpec) -> (Mock, Result<SignIn, String>) {
    let challenge = Arc::new(Mutex::new(String::new()));
    let mock = Mock::start(handler(b, challenge)).await;
    let spec = spec(&mock.base);
    let result = sign_in(
        &oauth_client().unwrap(),
        "drive",
        &spec,
        browser(),
        Duration::from_secs(10),
    )
    .await;
    (mock, result)
}

fn plain(base: &str) -> OAuthSpec {
    OAuthSpec {
        url: format!("{base}/mcp"),
        client_id: None,
        scopes: vec![],
    }
}

#[tokio::test]
async fn signs_in_through_discovery_registration_pkce_and_resource() {
    let (mock, result) = run(Behavior::default(), plain).await;
    let s = result.expect("sign-in");
    assert_eq!(s.access_token, "at-1");
    assert_eq!(s.refresh_token, "rt-1");
    assert_eq!(s.client_id, "dyn-client");
    assert_eq!(s.issuer, format!("{}/tenant1", mock.base));
    assert_eq!(s.resource, format!("{}/mcp", mock.base));
    assert!(s.expires_at_ms > now_ms() + 3_000_000);
    // Discovery order: the probe, the challenged metadata, RFC 8414 path
    // insertion (missing), OIDC path insertion.
    let paths = mock.paths();
    assert_eq!(
        &paths[..4],
        &[
            "/mcp",
            "/.well-known/oauth-protected-resource/mcp",
            "/.well-known/oauth-authorization-server/tenant1",
            "/.well-known/openid-configuration/tenant1",
        ]
    );
    let reg: serde_json::Value =
        serde_json::from_str(&mock.find("/tenant1/register").unwrap().body).unwrap();
    assert!(reg["redirect_uris"][0]
        .as_str()
        .unwrap()
        .starts_with("http://127.0.0.1:"));
    assert_eq!(reg["token_endpoint_auth_method"], "none");
    let auth = mock.find("/tenant1/authorize").unwrap();
    let q: HashMap<String, String> = Url::parse(&format!("http://x{}", auth.target))
        .unwrap()
        .query_pairs()
        .map(|(k, v)| (k.into_owned(), v.into_owned()))
        .collect();
    assert_eq!(q["code_challenge_method"], "S256");
    assert_eq!(q["resource"], format!("{}/mcp", mock.base));
    // The challenge's scope wins; offline_access asks for a refresh token.
    assert_eq!(q["scope"], "files:read offline_access");
    assert_eq!(q["client_id"], "dyn-client");
    assert!(!q["state"].is_empty());
    let probe = mock.find("/mcp").unwrap();
    assert!(probe.headers["accept"].contains("text/event-stream"));
    assert!(!probe.headers.contains_key("authorization"));
    let token = mock.find("/tenant1/token").unwrap().form();
    assert_eq!(token["client_id"], "dyn-client");
    assert!(token.contains_key("code_verifier"));
}

#[tokio::test]
async fn falls_back_to_well_known_metadata_and_uses_the_package_client() {
    let b = Behavior {
        challenge_header: false,
        ..Behavior::default()
    };
    let (mock, result) = run(b, |base| OAuthSpec {
        url: format!("{base}/mcp"),
        client_id: Some("pre-registered".into()),
        scopes: vec!["files:write".into()],
    })
    .await;
    let s = result.expect("sign-in");
    assert_eq!(s.client_id, "pre-registered");
    let paths = mock.paths();
    assert_eq!(
        &paths[..3],
        &[
            "/mcp",
            "/.well-known/oauth-protected-resource/mcp",
            "/.well-known/oauth-protected-resource"
        ]
    );
    assert!(
        mock.find("/tenant1/register").is_none(),
        "a package client is not registered again"
    );
    let auth = mock.find("/tenant1/authorize").unwrap();
    assert!(
        auth.target.contains("scope=files%3Awrite+offline_access"),
        "{}",
        auth.target
    );
}

#[tokio::test]
async fn refuses_a_server_without_pkce() {
    let b = Behavior {
        pkce: false,
        ..Behavior::default()
    };
    let (mock, result) = run(b, plain).await;
    assert!(result.unwrap_err().contains("PKCE"));
    assert!(mock.find("/tenant1/authorize").is_none());
}

#[tokio::test]
async fn needs_a_client_when_registration_is_not_offered() {
    let b = Behavior {
        registration: false,
        ..Behavior::default()
    };
    let (_, result) = run(b, plain).await;
    assert!(result.unwrap_err().contains("needs a registered client"));
}

#[tokio::test]
async fn ignores_an_answer_from_another_issuer() {
    let b = Behavior {
        iss: Some("https://evil.example.com".into()),
        ..Behavior::default()
    };
    let (mock, result) = run(b, plain).await;
    assert!(result.unwrap_err().contains("another server"));
    assert!(
        mock.find("/tenant1/token").is_none(),
        "the code never reaches a token endpoint"
    );
    // A server advertising iss must send it.
    let b = Behavior {
        iss: None,
        ..Behavior::default()
    };
    assert!(run(b, plain).await.1.unwrap_err().contains("did not say"));
    // A server that does not advertise it may omit it.
    let b = Behavior {
        iss: None,
        iss_supported: false,
        ..Behavior::default()
    };
    assert!(run(b, plain).await.1.is_ok());
}

#[tokio::test]
async fn ignores_an_answer_with_another_state() {
    let b = Behavior {
        state_override: Some("forged".into()),
        ..Behavior::default()
    };
    let (mock, result) = run(b, plain).await;
    assert!(result.unwrap_err().contains("does not belong"));
    assert!(mock.find("/tenant1/token").is_none());
}

#[tokio::test]
async fn refresh_rotates_or_keeps_the_refresh_token() {
    let (mock, result) = run(Behavior::default(), plain).await;
    let s = result.unwrap();
    let http = oauth_client().unwrap();
    let next = refresh(&http, &s).await.expect("refresh");
    assert_eq!(
        (next.access_token.as_str(), next.refresh_token.as_str()),
        ("at-2", "rt-2")
    );
    assert!(next.expires_at_ms > now_ms());
    let form = mock
        .seen
        .lock()
        .iter()
        .rev()
        .find(|r| r.path() == "/tenant1/token")
        .unwrap()
        .form();
    assert_eq!(form["resource"], format!("{}/mcp", mock.base));
    // A rotated-away refresh token is refused: the sign-in must be redone.
    let mut stale = s.clone();
    stale.refresh_token = "rt-0".into();
    assert!(matches!(
        refresh(&http, &stale).await,
        Err(TokenFailure::Refused(_))
    ));

    let b = Behavior {
        rotate_refresh: false,
        ..Behavior::default()
    };
    let (_, result) = run(b, plain).await;
    let s = result.unwrap();
    let next = refresh(&http, &s).await.unwrap();
    assert_eq!(
        next.refresh_token, "rt-1",
        "kept when the server returns none"
    );
}

#[test]
fn sign_in_is_stored_owner_only_beside_the_tool_settings() {
    let dir = std::env::temp_dir().join(format!("bnbloc-mcp-oauth-test-{}", std::process::id()));
    let path = sign_in_path(&dir, "drive").unwrap();
    assert_eq!(path, dir.join("oauth").join("drive.json"));
    assert!(sign_in_path(&dir, "../x").is_err());
    let s = SignIn {
        access_token: "secret-at".into(),
        expires_at_ms: 42,
        refresh_token: "secret-rt".into(),
        issuer: "https://auth.example.com".into(),
        ..SignIn::default()
    };
    let store = SignIns::default();
    store.save(&dir, "drive", &s).unwrap();
    assert_eq!(load_sign_in(&path), Some(s.clone()));
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = std::fs::metadata(&path).unwrap().permissions().mode();
        assert_eq!(mode & 0o777, 0o600);
    }
    // The bridge reads these two names.
    let raw: serde_json::Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
    assert_eq!(raw["accessToken"], "secret-at");
    assert_eq!(raw["expiresAtMs"], 42);
    assert_eq!(s.issuer_host(), "auth.example.com");
    store.forget(&dir, "drive").unwrap();
    store.forget(&dir, "drive").unwrap();
    assert!(load_sign_in(&path).is_none());
    let _ = std::fs::remove_dir_all(&dir);
}

/// A token endpoint that answers one refresh only when the test releases
/// it: `arrived` fires once the request is in, `release` lets it answer.
async fn gated_token_endpoint() -> (
    String,
    tokio::sync::oneshot::Receiver<()>,
    tokio::sync::oneshot::Sender<()>,
) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let endpoint = format!("http://{}/token", listener.local_addr().unwrap());
    let (arrived_tx, arrived_rx) = tokio::sync::oneshot::channel();
    let (release_tx, release_rx) = tokio::sync::oneshot::channel::<()>();
    tokio::spawn(async move {
        let (mut sock, _) = listener.accept().await.unwrap();
        let mut buf = Vec::new();
        let mut chunk = [0u8; 4096];
        loop {
            let n = sock.read(&mut chunk).await.unwrap_or(0);
            buf.extend_from_slice(&chunk[..n]);
            let text = String::from_utf8_lossy(&buf);
            if n == 0 || text.contains("refresh_token=rt-1") {
                break;
            }
        }
        let _ = arrived_tx.send(());
        let _ = release_rx.await;
        let body = r#"{"access_token":"at-2","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-2"}"#;
        let resp = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        );
        let _ = sock.write_all(resp.as_bytes()).await;
        let _ = sock.shutdown().await;
    });
    (endpoint, arrived_rx, release_tx)
}

fn due_sign_in(token_endpoint: &str) -> SignIn {
    SignIn {
        access_token: "at-1".into(),
        // Inside the refresh window.
        expires_at_ms: now_ms() + 1_000,
        refresh_token: "rt-1".into(),
        issuer: "http://127.0.0.1".into(),
        token_endpoint: token_endpoint.into(),
        resource: "http://127.0.0.1/mcp".into(),
        client_id: "c".into(),
        token_auth: "none".into(),
        ..SignIn::default()
    }
}

/// A sign-out, or a fresh sign-in, while a refresh's token request is in
/// flight wins: the refresh never re-creates the deleted file or overwrites
/// the new grant with the old one.
#[tokio::test]
async fn sign_out_and_refresh_do_not_race() {
    let dir = std::env::temp_dir().join(format!("bnbloc-mcp-race-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    let path = sign_in_path(&dir, "drive").unwrap();
    let http = oauth_client().unwrap();
    let store = SignIns::default();

    // Sign-out during the refresh: stays signed out.
    let (endpoint, arrived, release) = gated_token_endpoint().await;
    store.save(&dir, "drive", &due_sign_in(&endpoint)).unwrap();
    let (outcome, ()) = tokio::join!(refresh_stored(&http, &store, &dir, "drive"), async {
        arrived.await.unwrap();
        store.forget(&dir, "drive").unwrap();
        release.send(()).unwrap();
    });
    assert_eq!(outcome, RefreshOutcome::Superseded);
    assert!(
        !path.exists(),
        "a sign-out during a refresh must stay signed out"
    );

    // A fresh sign-in during the refresh: kept, not overwritten.
    let (endpoint, arrived, release) = gated_token_endpoint().await;
    store.save(&dir, "drive", &due_sign_in(&endpoint)).unwrap();
    let fresh = SignIn {
        access_token: "at-fresh".into(),
        refresh_token: "rt-fresh".into(),
        expires_at_ms: now_ms() + 3_600_000,
        ..due_sign_in(&endpoint)
    };
    let (outcome, ()) = tokio::join!(refresh_stored(&http, &store, &dir, "drive"), async {
        arrived.await.unwrap();
        store.save(&dir, "drive", &fresh).unwrap();
        release.send(()).unwrap();
    });
    assert_eq!(outcome, RefreshOutcome::Superseded);
    assert_eq!(load_sign_in(&path), Some(fresh));

    // Undisturbed, the refresh commits over the sign-in it started from.
    let (endpoint, arrived, release) = gated_token_endpoint().await;
    store.save(&dir, "drive", &due_sign_in(&endpoint)).unwrap();
    let (outcome, ()) = tokio::join!(refresh_stored(&http, &store, &dir, "drive"), async {
        arrived.await.unwrap();
        release.send(()).unwrap();
    });
    assert_eq!(outcome, RefreshOutcome::Refreshed);
    let stored = load_sign_in(&path).expect("refreshed sign-in");
    assert_eq!(
        (stored.access_token.as_str(), stored.refresh_token.as_str()),
        ("at-2", "rt-2")
    );
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn parses_bearer_challenges() {
    let c = parse_challenge(
        r#"Bearer error="invalid_token", resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource", scope="files:read files:write""#,
    );
    assert_eq!(
        c.resource_metadata.as_deref(),
        Some("https://mcp.example.com/.well-known/oauth-protected-resource")
    );
    assert_eq!(c.scope.as_deref(), Some("files:read files:write"));
    let c = parse_challenge(r#"Basic realm="x", Bearer scope=files:read"#);
    assert_eq!(c.scope.as_deref(), Some("files:read"));
    assert_eq!(parse_challenge("Basic realm=\"x\""), Challenge::default());
}

#[test]
fn well_known_urls_follow_the_specification_order() {
    assert_eq!(
        auth_server_metadata_urls("https://auth.example.com/tenant1").unwrap(),
        vec![
            "https://auth.example.com/.well-known/oauth-authorization-server/tenant1",
            "https://auth.example.com/.well-known/openid-configuration/tenant1",
            "https://auth.example.com/tenant1/.well-known/openid-configuration",
        ]
    );
    assert_eq!(
        auth_server_metadata_urls("https://auth.example.com").unwrap(),
        vec![
            "https://auth.example.com/.well-known/oauth-authorization-server",
            "https://auth.example.com/.well-known/openid-configuration",
        ]
    );
    assert_eq!(
        resource_metadata_urls("https://mcp.example.com/public/mcp").unwrap(),
        vec![
            "https://mcp.example.com/.well-known/oauth-protected-resource/public/mcp",
            "https://mcp.example.com/.well-known/oauth-protected-resource",
        ]
    );
    assert_eq!(
        canonical_resource("HTTPS://MCP.Example.com/mcp/#x").unwrap(),
        "https://mcp.example.com/mcp"
    );
    assert_eq!(
        canonical_resource("https://mcp.example.com/").unwrap(),
        "https://mcp.example.com"
    );
}

#[test]
fn pkce_and_encoding() {
    // RFC 7636 appendix B.
    assert_eq!(
        pkce_challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
        "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
    );
    assert_eq!(b64url(b"f"), "Zg");
    assert_eq!(b64url(b"fo"), "Zm8");
    assert_eq!(b64url(b"foo"), "Zm9v");
    assert_eq!(random_token(32).unwrap().len(), 43);
}

#[test]
fn refresh_is_due_shortly_before_expiry() {
    let now = 1_000_000_000;
    let mut s = SignIn {
        access_token: "a".into(),
        refresh_token: "r".into(),
        expires_at_ms: now + REFRESH_AHEAD_MS + 1,
        ..SignIn::default()
    };
    assert!(s.valid_at(now) && !s.needs_refresh(now));
    s.expires_at_ms = now + 1000;
    assert!(s.needs_refresh(now));
    s.expires_at_ms = now - 1;
    assert!(!s.valid_at(now));
    s.refresh_token.clear();
    assert!(!s.needs_refresh(now));
}
