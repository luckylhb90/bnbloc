//! Per-member OAuth sign-in for http MCP tools
//! (docs/prd/2026-10-12-experts-and-teams.md, "Runtime gap closure" item 7).
//!
//! An endpoint MCP component may declare `mcp.http.auth.scheme = "oauth"`.
//! The member then signs in from Settings → Tools and this module runs the
//! MCP authorization flow (specification 2026-07-28) against the tool's own
//! server; nothing about the authorization server is declared or baked in:
//!
//! 1. An unauthenticated MCP request; a 401's `WWW-Authenticate` names the
//!    protected resource metadata (RFC 9728) and maybe the scopes. Without
//!    it the well-known URIs are tried (path-inserted, then root).
//! 2. The metadata's `resource` must be this tool's URL; its first
//!    authorization server is asked for metadata (RFC 8414 path insertion,
//!    OIDC path insertion, OIDC path appending; root forms for an issuer
//!    without a path). The returned `issuer` must be the one asked for, and
//!    `code_challenge_methods_supported` must list S256 (no PKCE, no
//!    sign-in).
//! 3. A client: the package's `clientId`, else dynamic registration where
//!    the server offers it, else a plain error naming what is missing.
//! 4. Authorization code with PKCE (S256), `state`, `resource` (RFC 8707)
//!    and a loopback redirect (127.0.0.1, random port, bounded wait) in the
//!    member's browser; the response's `iss` is checked (RFC 9207) before
//!    anything else in it is used.
//! 5. The code is exchanged (again with `resource`); the tokens are stored
//!    owner-only (0600) in `<mcp-settings>/oauth/<tool>.json`, next to the
//!    member's other tool credentials. The bridge reads the access token
//!    from there for the member's own drives only.
//!
//! Tokens are refreshed shortly before they expire (the refresh token
//! rotates when the server returns a new one); a refused refresh forgets
//! the sign-in. Removing the tool forgets it too. Every write and delete of
//! a sign-in file goes through `SignIns` (a per-tool lock; a refresh commits
//! only over the sign-in it started from, so a sign-out or a new sign-in
//! during a refresh wins). Token values are never logged and never returned
//! to the web view.
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use reqwest::header::{ACCEPT, CONTENT_TYPE, WWW_AUTHENTICATE};
use reqwest::{StatusCode, Url};
use sha2::{Digest, Sha256};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

use crate::internal_ext::plugin_settings::settings_path;

/// Sub-directory of the MCP settings dir holding sign-ins (the bridge
/// reads `<dir>/oauth/<tool>.json`; services/bridge/mcp_tools.go).
pub(super) const OAUTH_DIR: &str = "oauth";
/// How long the browser step may take before the sign-in gives up.
pub(super) const SIGN_IN_TIMEOUT: Duration = Duration::from_secs(300);
/// A token expiring within this window is refreshed (the catalog refresh
/// runs at least every minute).
pub(super) const REFRESH_AHEAD_MS: i64 = 5 * 60 * 1000;
/// Bound on every discovery, registration and token request.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(20);
/// Bound on one metadata or token response body.
const MAX_BODY_BYTES: usize = 256 << 10;
/// Bound on the browser's callback request head.
const MAX_CALLBACK_BYTES: usize = 16 << 10;
/// The name this desktop registers under (dynamic registration).
const CLIENT_NAME: &str = "bnbloc desktop";

/// What a tool's package declares about its sign-in.
#[derive(Clone, Debug, Default, PartialEq)]
pub(in crate::internal_ext) struct OAuthSpec {
    /// The MCP server URL (endpoint.baseUrl): the protected resource.
    pub(in crate::internal_ext) url: String,
    pub(in crate::internal_ext) client_id: Option<String>,
    pub(in crate::internal_ext) scopes: Vec<String>,
}

/// A stored sign-in. The bridge reads `accessToken` and `expiresAtMs`; the
/// rest lets this desktop refresh it.
#[derive(Clone, Debug, Default, PartialEq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct SignIn {
    pub(super) access_token: String,
    /// 0 when the server did not say.
    #[serde(default)]
    pub(super) expires_at_ms: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub(super) refresh_token: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub(super) scope: String,
    pub(super) issuer: String,
    pub(super) token_endpoint: String,
    pub(super) resource: String,
    pub(super) client_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub(super) client_secret: String,
    /// How the client authenticates at the token endpoint ("none",
    /// "client_secret_basic", "client_secret_post").
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub(super) token_auth: String,
    #[serde(default)]
    pub(super) signed_in_at_ms: i64,
}

impl SignIn {
    /// Whether the access token is usable now (no stated expiry counts as
    /// usable; the server answers 401 when it is not).
    pub(super) fn valid_at(&self, now_ms: i64) -> bool {
        !self.access_token.is_empty() && (self.expires_at_ms == 0 || self.expires_at_ms > now_ms)
    }

    /// Whether it should be refreshed now.
    pub(super) fn needs_refresh(&self, now_ms: i64) -> bool {
        !self.refresh_token.is_empty()
            && self.expires_at_ms > 0
            && self.expires_at_ms - now_ms < REFRESH_AHEAD_MS
    }

    /// The authorization server's host, for "Signed in with …".
    pub(super) fn issuer_host(&self) -> String {
        Url::parse(&self.issuer)
            .ok()
            .and_then(|u| u.host_str().map(str::to_string))
            .unwrap_or_default()
    }
}

/// The sign-in file of `tool` under the MCP settings dir.
pub(super) fn sign_in_path(settings_dir: &Path, tool: &str) -> Result<PathBuf, String> {
    settings_path(&settings_dir.join(OAUTH_DIR), tool)
}

pub(super) fn load_sign_in(path: &Path) -> Option<SignIn> {
    std::fs::read(path)
        .ok()
        .and_then(|raw| serde_json::from_slice::<SignIn>(&raw).ok())
        .filter(|s| !s.access_token.is_empty())
}

/// The member's stored sign-ins: the one owner of every write and delete of
/// a sign-in file. Each takes the tool's lock, so a sign-in, a sign-out, the
/// uninstall sweep and a refresh's commit never interleave. A refresh talks
/// to the sign-in server WITHOUT the lock and commits only over the exact
/// sign-in it started from (re-read and compared under the lock): a
/// sign-out or a fresh sign-in meanwhile wins.
#[derive(Default)]
pub(in crate::internal_ext) struct SignIns {
    locks: parking_lot::Mutex<HashMap<String, Arc<parking_lot::Mutex<()>>>>,
}

impl SignIns {
    fn lock(&self, tool: &str) -> Arc<parking_lot::Mutex<()>> {
        Arc::clone(self.locks.lock().entry(tool.to_string()).or_default())
    }

    /// Store `tool`'s sign-in owner-only (0600).
    pub(super) fn save(&self, dir: &Path, tool: &str, sign_in: &SignIn) -> Result<(), String> {
        let path = sign_in_path(dir, tool)?;
        let lock = self.lock(tool);
        let _held = lock.lock();
        write_sign_in(&path, sign_in)
    }

    /// Forget `tool`'s sign-in (sign-out).
    pub(super) fn forget(&self, dir: &Path, tool: &str) -> Result<(), String> {
        let path = sign_in_path(dir, tool)?;
        let lock = self.lock(tool);
        let _held = lock.lock();
        remove_sign_in(&path)
    }

    /// Replace `tool`'s sign-in with `next` (None: forget it) only while the
    /// stored one is still exactly `from`. Returns whether it did.
    pub(super) fn replace_if_current(
        &self,
        dir: &Path,
        tool: &str,
        from: &SignIn,
        next: Option<&SignIn>,
    ) -> Result<bool, String> {
        let path = sign_in_path(dir, tool)?;
        let lock = self.lock(tool);
        let _held = lock.lock();
        if load_sign_in(&path).as_ref() != Some(from) {
            return Ok(false);
        }
        match next {
            Some(next) => write_sign_in(&path, next)?,
            None => remove_sign_in(&path)?,
        }
        Ok(true)
    }
}

/// Owner-only (0600) through the shared credential writer. Only `SignIns`
/// calls it, under the tool's lock.
fn write_sign_in(path: &Path, sign_in: &SignIn) -> Result<(), String> {
    let raw = serde_json::to_vec(sign_in).map_err(|e| e.to_string())?;
    crate::settings::write_owner_only(path, &raw)
}

fn remove_sign_in(path: &Path) -> Result<(), String> {
    match std::fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(format!("forget sign-in: {e}")),
    }
}

/// What a refresh of one stored sign-in did.
#[derive(Debug, PartialEq)]
pub(super) enum RefreshOutcome {
    /// Nothing stored, or not due yet.
    NotDue,
    Refreshed,
    /// The server refused the grant: the sign-in was forgotten.
    Forgotten(String),
    /// The stored sign-in changed while the request ran (signed out, signed
    /// in again, refreshed elsewhere): the result was dropped.
    Superseded,
    /// The request failed; retried on the next catalog refresh.
    Failed(String),
}

/// Refresh `tool`'s stored sign-in when it is due. The token request runs
/// without the tool's lock; its result is committed only over the sign-in it
/// started from.
pub(super) async fn refresh_stored(
    http: &reqwest::Client,
    store: &SignIns,
    dir: &Path,
    tool: &str,
) -> RefreshOutcome {
    let Some(current) = sign_in_path(dir, tool).ok().and_then(|p| load_sign_in(&p)) else {
        return RefreshOutcome::NotDue;
    };
    if !current.needs_refresh(now_ms()) {
        return RefreshOutcome::NotDue;
    }
    let (next, outcome) = match refresh(http, &current).await {
        Ok(next) => (Some(next), RefreshOutcome::Refreshed),
        Err(TokenFailure::Refused(m)) => (None, RefreshOutcome::Forgotten(m)),
        Err(TokenFailure::Failed(m)) => return RefreshOutcome::Failed(m),
    };
    match store.replace_if_current(dir, tool, &current, next.as_ref()) {
        Ok(true) => outcome,
        Ok(false) => RefreshOutcome::Superseded,
        Err(e) => RefreshOutcome::Failed(e),
    }
}

pub(super) fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// The HTTP client of the flow: bounded, and never following redirects
/// (metadata, registration and token endpoints answer directly; a redirect
/// could carry the token request elsewhere).
pub(super) fn oauth_client() -> Result<reqwest::Client, String> {
    reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(REQUEST_TIMEOUT)
        .build()
        .map_err(|e| format!("build http client: {e}"))
}

// ---------------------------------------------------------------- encoding

/// base64url without padding (RFC 4648 §5), for PKCE and random values.
fn b64url(data: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let b = [
            chunk[0],
            *chunk.get(1).unwrap_or(&0),
            *chunk.get(2).unwrap_or(&0),
        ];
        let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
        let chars = chunk.len() + 1;
        for i in 0..chars {
            out.push(ALPHABET[((n >> (18 - 6 * i)) & 63) as usize] as char);
        }
    }
    out
}

fn random_token(bytes: usize) -> Result<String, String> {
    let mut raw = vec![0u8; bytes];
    getrandom::fill(&mut raw).map_err(|e| format!("random: {e}"))?;
    Ok(b64url(&raw))
}

/// The PKCE S256 challenge of a verifier.
pub(super) fn pkce_challenge(verifier: &str) -> String {
    b64url(&Sha256::digest(verifier.as_bytes()))
}

// ---------------------------------------------------------------- discovery

/// What a 401 `WWW-Authenticate: Bearer …` says.
#[derive(Debug, Default, PartialEq)]
pub(super) struct Challenge {
    pub(super) resource_metadata: Option<String>,
    pub(super) scope: Option<String>,
}

/// Parse the Bearer challenge's `resource_metadata` and `scope` (quoted or
/// token values; other schemes and parameters are ignored).
pub(super) fn parse_challenge(header: &str) -> Challenge {
    let mut out = Challenge::default();
    let Some(at) = header.to_ascii_lowercase().find("bearer") else {
        return out;
    };
    let mut rest = &header[at + "bearer".len()..];
    loop {
        rest = rest.trim_start_matches([' ', ',', '\t']);
        let Some(eq) = rest.find('=') else { break };
        let key = rest[..eq].trim().to_ascii_lowercase();
        if key.contains(' ') {
            // The next auth scheme starts here.
            break;
        }
        rest = &rest[eq + 1..];
        let value;
        if let Some(stripped) = rest.strip_prefix('"') {
            let mut v = String::new();
            let mut chars = stripped.char_indices();
            let mut end = stripped.len();
            while let Some((i, c)) = chars.next() {
                match c {
                    '\\' => {
                        if let Some((_, n)) = chars.next() {
                            v.push(n);
                        }
                    }
                    '"' => {
                        end = i + 1;
                        break;
                    }
                    _ => v.push(c),
                }
            }
            value = v;
            rest = &stripped[end.min(stripped.len())..];
        } else {
            let end = rest.find(',').unwrap_or(rest.len());
            value = rest[..end].trim().to_string();
            rest = &rest[end..];
        }
        match key.as_str() {
            "resource_metadata" => out.resource_metadata = Some(value),
            "scope" if !value.trim().is_empty() => out.scope = Some(value),
            _ => {}
        }
    }
    out
}

/// The canonical resource URI of an MCP server URL (RFC 8707): lower-case
/// scheme and host (the URL parser does that), no fragment, no trailing
/// slash.
pub(super) fn canonical_resource(raw: &str) -> Result<String, String> {
    let mut url = Url::parse(raw.trim()).map_err(|_| format!("invalid MCP server URL {raw:?}"))?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return Err(format!("invalid MCP server URL {raw:?}"));
    }
    url.set_fragment(None);
    let s = url.to_string();
    Ok(s.strip_suffix('/').unwrap_or(&s).to_string())
}

fn origin(url: &Url) -> String {
    let mut o = format!("{}://{}", url.scheme(), url.host_str().unwrap_or_default());
    if let Some(port) = url.port() {
        o.push_str(&format!(":{port}"));
    }
    o
}

/// Protected resource metadata URIs to try without a challenge: the
/// path-inserted form, then the root (RFC 9728 §3.1).
pub(super) fn resource_metadata_urls(resource: &str) -> Result<Vec<String>, String> {
    let url = Url::parse(resource).map_err(|e| format!("invalid MCP server URL: {e}"))?;
    let base = format!("{}/.well-known/oauth-protected-resource", origin(&url));
    let path = url.path().trim_end_matches('/');
    let mut out = Vec::new();
    if !path.is_empty() {
        out.push(format!("{base}{path}"));
    }
    out.push(base);
    Ok(out)
}

/// Authorization server metadata URIs in the specification's order.
pub(super) fn auth_server_metadata_urls(issuer: &str) -> Result<Vec<String>, String> {
    let url = Url::parse(issuer).map_err(|_| format!("invalid authorization server {issuer:?}"))?;
    let o = origin(&url);
    let path = url.path().trim_end_matches('/');
    Ok(if path.is_empty() {
        vec![
            format!("{o}/.well-known/oauth-authorization-server"),
            format!("{o}/.well-known/openid-configuration"),
        ]
    } else {
        vec![
            format!("{o}/.well-known/oauth-authorization-server{path}"),
            format!("{o}/.well-known/openid-configuration{path}"),
            format!("{o}{path}/.well-known/openid-configuration"),
        ]
    })
}

/// Authorization server endpoints must be https; a loopback host (a server
/// on this machine) may use http.
fn secure_endpoint(raw: &str, what: &str) -> Result<Url, String> {
    let url = Url::parse(raw).map_err(|_| format!("the sign-in server's {what} is not a URL"))?;
    let loopback = matches!(url.host_str(), Some("127.0.0.1" | "localhost" | "[::1]"));
    match url.scheme() {
        "https" => Ok(url),
        "http" if loopback => Ok(url),
        _ => Err(format!("the sign-in server's {what} is not https")),
    }
}

#[derive(serde::Deserialize, Default)]
struct ResourceMetadata {
    #[serde(default)]
    resource: String,
    #[serde(default)]
    authorization_servers: Vec<String>,
    #[serde(default)]
    scopes_supported: Vec<String>,
}

#[derive(serde::Deserialize, Default, Clone)]
pub(super) struct AuthServer {
    #[serde(default)]
    issuer: String,
    #[serde(default)]
    authorization_endpoint: String,
    #[serde(default)]
    token_endpoint: String,
    #[serde(default)]
    registration_endpoint: Option<String>,
    #[serde(default)]
    code_challenge_methods_supported: Option<Vec<String>>,
    #[serde(default)]
    scopes_supported: Vec<String>,
    #[serde(default)]
    authorization_response_iss_parameter_supported: bool,
}

async fn read_body(resp: reqwest::Response) -> Result<Vec<u8>, String> {
    if resp.content_length().unwrap_or(0) as usize > MAX_BODY_BYTES {
        return Err("response too large".into());
    }
    let raw = resp
        .bytes()
        .await
        .map_err(|e| format!("read response: {e}"))?;
    if raw.len() > MAX_BODY_BYTES {
        return Err("response too large".into());
    }
    Ok(raw.to_vec())
}

/// GET a JSON document; Ok(None) for a status that means "not here".
async fn get_json<T: serde::de::DeserializeOwned>(
    client: &reqwest::Client,
    url: &str,
) -> Result<Option<T>, String> {
    let resp = client
        .get(url)
        .header(ACCEPT, "application/json")
        .send()
        .await
        .map_err(|e| format!("could not reach {url}: {e}"))?;
    if !resp.status().is_success() {
        return Ok(None);
    }
    let raw = read_body(resp).await?;
    serde_json::from_slice(&raw)
        .map(Some)
        .map_err(|_| format!("{url} did not answer with valid metadata"))
}

/// What discovery found: the resource, its authorization server and the
/// scopes to ask for.
pub(super) struct Discovered {
    pub(super) resource: String,
    pub(super) server: AuthServer,
    pub(super) scope: Option<String>,
}

/// Steps 1 and 2: from the MCP server to its authorization server.
pub(super) async fn discover(
    client: &reqwest::Client,
    spec: &OAuthSpec,
) -> Result<Discovered, String> {
    let resource = canonical_resource(&spec.url)?;
    let probe = client
        .post(&spec.url)
        .header(CONTENT_TYPE, "application/json")
        .header(ACCEPT, "application/json, text/event-stream")
        .body(
            r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"bnbloc-desktop","version":"1"}}}"#,
        )
        .send()
        .await
        .map_err(|e| format!("could not reach the tool's server: {e}"))?;
    let challenge = if probe.status() == StatusCode::UNAUTHORIZED {
        probe
            .headers()
            .get_all(WWW_AUTHENTICATE)
            .iter()
            .filter_map(|v| v.to_str().ok())
            .map(parse_challenge)
            .find(|c| c.resource_metadata.is_some() || c.scope.is_some())
            .unwrap_or_default()
    } else {
        Challenge::default()
    };
    let candidates = match &challenge.resource_metadata {
        Some(url) => vec![secure_endpoint(url, "resource metadata")?.to_string()],
        None => resource_metadata_urls(&resource)?,
    };
    let mut prm = None;
    for url in candidates {
        if let Some(found) = get_json::<ResourceMetadata>(client, &url).await? {
            prm = Some(found);
            break;
        }
    }
    let prm = prm.ok_or(
        "the tool's server does not publish how to sign in (no protected resource metadata)",
    )?;
    if canonical_resource(&prm.resource).ok().as_deref() != Some(resource.as_str()) {
        return Err("the tool's server describes a different resource; not signing in".into());
    }
    let issuer = prm
        .authorization_servers
        .first()
        .cloned()
        .ok_or("the tool's server names no sign-in server")?;
    secure_endpoint(&issuer, "address")?;
    let mut server = None;
    for url in auth_server_metadata_urls(&issuer)? {
        if let Some(found) = get_json::<AuthServer>(client, &url).await? {
            server = Some(found);
            break;
        }
    }
    let server: AuthServer = server.ok_or("the sign-in server publishes no metadata")?;
    if server.issuer.trim_end_matches('/') != issuer.trim_end_matches('/') {
        return Err("the sign-in server's metadata names another issuer; not signing in".into());
    }
    if !server
        .code_challenge_methods_supported
        .as_ref()
        .is_some_and(|m| m.iter().any(|x| x == "S256"))
    {
        return Err(
            "the sign-in server does not support PKCE (S256), so bnbloc does not sign in to it"
                .into(),
        );
    }
    secure_endpoint(&server.authorization_endpoint, "authorization endpoint")?;
    secure_endpoint(&server.token_endpoint, "token endpoint")?;
    let mut scopes: Vec<String> = match &challenge.scope {
        Some(s) => s.split_whitespace().map(str::to_string).collect(),
        None if !spec.scopes.is_empty() => spec.scopes.clone(),
        None => prm.scopes_supported.clone(),
    };
    // Ask for a refresh token where the server offers one.
    if server
        .scopes_supported
        .iter()
        .any(|s| s == "offline_access")
        && !scopes.iter().any(|s| s == "offline_access")
    {
        scopes.push("offline_access".into());
    }
    Ok(Discovered {
        resource,
        server,
        scope: (!scopes.is_empty()).then(|| scopes.join(" ")),
    })
}

// ---------------------------------------------------------------- registration

struct Client {
    id: String,
    secret: String,
    token_auth: String,
}

#[derive(serde::Deserialize)]
struct Registered {
    client_id: String,
    #[serde(default)]
    client_secret: Option<String>,
    #[serde(default)]
    token_endpoint_auth_method: Option<String>,
}

/// Step 3: the package's client, else dynamic registration.
async fn client_for(
    http: &reqwest::Client,
    spec: &OAuthSpec,
    server: &AuthServer,
    redirect_uri: &str,
    tool: &str,
) -> Result<Client, String> {
    if let Some(id) = spec.client_id.as_ref().filter(|s| !s.is_empty()) {
        return Ok(Client {
            id: id.clone(),
            secret: String::new(),
            token_auth: "none".into(),
        });
    }
    let Some(endpoint) = server.registration_endpoint.as_deref() else {
        return Err(format!(
            "{tool}'s sign-in server needs a registered client. Ask an admin to add a client id to the tool's package."
        ));
    };
    let endpoint = secure_endpoint(endpoint, "registration endpoint")?;
    let body = serde_json::json!({
        "client_name": CLIENT_NAME,
        "redirect_uris": [redirect_uri],
        "grant_types": ["authorization_code", "refresh_token"],
        "response_types": ["code"],
        "token_endpoint_auth_method": "none",
    });
    let resp = http
        .post(endpoint)
        .header(CONTENT_TYPE, "application/json")
        .header(ACCEPT, "application/json")
        .body(body.to_string())
        .send()
        .await
        .map_err(|e| format!("could not register with the sign-in server: {e}"))?;
    if !resp.status().is_success() {
        return Err(format!(
            "the sign-in server refused to register bnbloc ({})",
            resp.status()
        ));
    }
    let reg: Registered = serde_json::from_slice(&read_body(resp).await?)
        .map_err(|_| "the sign-in server's registration answer is invalid".to_string())?;
    let secret = reg.client_secret.unwrap_or_default();
    let token_auth = reg.token_endpoint_auth_method.unwrap_or_else(|| {
        if secret.is_empty() {
            "none".into()
        } else {
            "client_secret_basic".into()
        }
    });
    Ok(Client {
        id: reg.client_id,
        secret,
        token_auth,
    })
}

// ---------------------------------------------------------------- the browser step

/// The parameters of the authorization response.
#[derive(Debug, Default, PartialEq)]
pub(super) struct Callback {
    pub(super) code: Option<String>,
    pub(super) state: Option<String>,
    pub(super) iss: Option<String>,
    pub(super) error: Option<String>,
    pub(super) error_description: Option<String>,
}

/// Check the authorization response (RFC 9207 first: on an issuer mismatch
/// nothing else in it is acted on or shown), then the state, then the code.
pub(super) fn check_callback(
    cb: &Callback,
    issuer: &str,
    iss_supported: bool,
    state: &str,
) -> Result<String, String> {
    match (&cb.iss, iss_supported) {
        (Some(iss), _) if iss != issuer => {
            return Err("the sign-in answer came from another server; it was ignored".into())
        }
        (None, true) => {
            return Err(
                "the sign-in answer did not say which server sent it; it was ignored".into(),
            )
        }
        _ => {}
    }
    if cb.state.as_deref() != Some(state) {
        return Err("the sign-in answer does not belong to this sign-in; it was ignored".into());
    }
    if let Some(error) = &cb.error {
        let detail = cb
            .error_description
            .as_deref()
            .map(|d| format!(": {d}"))
            .unwrap_or_default();
        return Err(format!("the sign-in was not completed ({error}{detail})"));
    }
    cb.code
        .clone()
        .filter(|c| !c.is_empty())
        .ok_or_else(|| "the sign-in answer carried no code".to_string())
}

const CALLBACK_PATH: &str = "/callback";

/// Wait for the browser to come back to the loopback redirect. Other paths
/// (a favicon) are answered 404 and the wait goes on.
async fn wait_callback(listener: TcpListener, timeout: Duration) -> Result<Callback, String> {
    let wait = async {
        loop {
            let (mut sock, _) = listener
                .accept()
                .await
                .map_err(|e| format!("sign-in callback: {e}"))?;
            let mut buf = Vec::new();
            let mut chunk = [0u8; 2048];
            while !buf.windows(4).any(|w| w == b"\r\n\r\n") && buf.len() < MAX_CALLBACK_BYTES {
                match sock.read(&mut chunk).await {
                    Ok(0) | Err(_) => break,
                    Ok(n) => buf.extend_from_slice(&chunk[..n]),
                }
            }
            let head = String::from_utf8_lossy(&buf);
            let target = head
                .lines()
                .next()
                .and_then(|l| l.strip_prefix("GET "))
                .and_then(|l| l.split(' ').next())
                .unwrap_or_default()
                .to_string();
            let Ok(url) = Url::parse(&format!("http://127.0.0.1{target}")) else {
                let _ = respond(&mut sock, "400 Bad Request", "Bad request.").await;
                continue;
            };
            if url.path() != CALLBACK_PATH {
                let _ = respond(&mut sock, "404 Not Found", "Not found.").await;
                continue;
            }
            let mut cb = Callback::default();
            for (k, v) in url.query_pairs() {
                let v = Some(v.into_owned());
                match k.as_ref() {
                    "code" => cb.code = v,
                    "state" => cb.state = v,
                    "iss" => cb.iss = v,
                    "error" => cb.error = v,
                    "error_description" => cb.error_description = v,
                    _ => {}
                }
            }
            let _ = respond(
                &mut sock,
                "200 OK",
                "bnbloc received the sign-in. You can close this window and return to the app.",
            )
            .await;
            return Ok(cb);
        }
    };
    tokio::time::timeout(timeout, wait)
        .await
        .map_err(|_| "the sign-in was not finished in the browser in time".to_string())?
}

async fn respond(
    sock: &mut tokio::net::TcpStream,
    status: &str,
    text: &str,
) -> std::io::Result<()> {
    let body = format!(
        "<!doctype html><meta charset=utf-8><title>bnbloc</title><p style=\"font-family:sans-serif\">{text}</p>"
    );
    let resp = format!(
        "HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    sock.write_all(resp.as_bytes()).await?;
    sock.shutdown().await
}

// ---------------------------------------------------------------- tokens

#[derive(serde::Deserialize)]
struct TokenResponse {
    access_token: String,
    #[serde(default)]
    token_type: String,
    #[serde(default)]
    expires_in: Option<i64>,
    #[serde(default)]
    refresh_token: Option<String>,
    #[serde(default)]
    scope: Option<String>,
}

#[derive(serde::Deserialize)]
struct TokenError {
    #[serde(default)]
    error: String,
}

/// Why a token request failed: `Refused` means the grant is no longer good
/// (the sign-in must be redone).
#[derive(Debug, PartialEq)]
pub(super) enum TokenFailure {
    Refused(String),
    Failed(String),
}

impl std::fmt::Display for TokenFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            TokenFailure::Refused(m) | TokenFailure::Failed(m) => f.write_str(m),
        }
    }
}

async fn token_request(
    http: &reqwest::Client,
    endpoint: &str,
    client_id: &str,
    client_secret: &str,
    token_auth: &str,
    mut form: Vec<(&str, String)>,
) -> Result<TokenResponse, TokenFailure> {
    let endpoint = secure_endpoint(endpoint, "token endpoint").map_err(TokenFailure::Failed)?;
    let mut req = http.post(endpoint).header(ACCEPT, "application/json");
    match token_auth {
        "client_secret_basic" if !client_secret.is_empty() => {
            req = req.basic_auth(client_id, Some(client_secret));
        }
        "client_secret_post" if !client_secret.is_empty() => {
            form.push(("client_id", client_id.to_string()));
            form.push(("client_secret", client_secret.to_string()));
        }
        _ => form.push(("client_id", client_id.to_string())),
    }
    let resp =
        req.form(&form).send().await.map_err(|e| {
            TokenFailure::Failed(format!("could not reach the sign-in server: {e}"))
        })?;
    let status = resp.status();
    let raw = read_body(resp).await.map_err(TokenFailure::Failed)?;
    if !status.is_success() {
        let error = serde_json::from_slice::<TokenError>(&raw)
            .map(|e| e.error)
            .unwrap_or_default();
        let message = format!(
            "the sign-in server refused the token request ({status}{})",
            if error.is_empty() {
                String::new()
            } else {
                format!(", {error}")
            }
        );
        return Err(if error == "invalid_grant" || error == "invalid_client" {
            TokenFailure::Refused(message)
        } else {
            TokenFailure::Failed(message)
        });
    }
    let tokens: TokenResponse = serde_json::from_slice(&raw)
        .map_err(|_| TokenFailure::Failed("the sign-in server's token answer is invalid".into()))?;
    if tokens.access_token.is_empty() || !tokens.token_type.eq_ignore_ascii_case("bearer") {
        return Err(TokenFailure::Failed(
            "the sign-in server did not issue a bearer token".into(),
        ));
    }
    Ok(tokens)
}

fn expires_at(now: i64, expires_in: Option<i64>) -> i64 {
    expires_in
        .filter(|s| *s > 0)
        .map(|s| now + s.saturating_mul(1000))
        .unwrap_or(0)
}

/// The whole sign-in: discovery, client, the browser step (`open` shows the
/// authorization URL to the member), the code exchange.
pub(super) async fn sign_in(
    http: &reqwest::Client,
    tool: &str,
    spec: &OAuthSpec,
    open: impl FnOnce(String) -> Result<(), String>,
    timeout: Duration,
) -> Result<SignIn, String> {
    let found = discover(http, spec).await?;
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .map_err(|e| format!("open the sign-in callback: {e}"))?;
    let port = listener
        .local_addr()
        .map_err(|e| format!("open the sign-in callback: {e}"))?
        .port();
    let redirect_uri = format!("http://127.0.0.1:{port}{CALLBACK_PATH}");
    let client = client_for(http, spec, &found.server, &redirect_uri, tool).await?;
    let verifier = random_token(32)?;
    let state = random_token(16)?;
    let mut url = secure_endpoint(
        &found.server.authorization_endpoint,
        "authorization endpoint",
    )?;
    {
        let mut q = url.query_pairs_mut();
        q.append_pair("response_type", "code")
            .append_pair("client_id", &client.id)
            .append_pair("redirect_uri", &redirect_uri)
            .append_pair("code_challenge", &pkce_challenge(&verifier))
            .append_pair("code_challenge_method", "S256")
            .append_pair("state", &state)
            .append_pair("resource", &found.resource);
        if let Some(scope) = &found.scope {
            q.append_pair("scope", scope);
        }
    }
    open(url.to_string())?;
    let cb = wait_callback(listener, timeout).await?;
    let code = check_callback(
        &cb,
        &found.server.issuer,
        found.server.authorization_response_iss_parameter_supported,
        &state,
    )?;
    let now = now_ms();
    let tokens = token_request(
        http,
        &found.server.token_endpoint,
        &client.id,
        &client.secret,
        &client.token_auth,
        vec![
            ("grant_type", "authorization_code".into()),
            ("code", code),
            ("redirect_uri", redirect_uri),
            ("code_verifier", verifier),
            ("resource", found.resource.clone()),
        ],
    )
    .await
    .map_err(|e| e.to_string())?;
    Ok(SignIn {
        access_token: tokens.access_token,
        expires_at_ms: expires_at(now, tokens.expires_in),
        refresh_token: tokens.refresh_token.unwrap_or_default(),
        scope: tokens.scope.or(found.scope).unwrap_or_default(),
        issuer: found.server.issuer,
        token_endpoint: found.server.token_endpoint,
        resource: found.resource,
        client_id: client.id,
        client_secret: client.secret,
        token_auth: client.token_auth,
        signed_in_at_ms: now,
    })
}

/// Refresh a sign-in. A rotated refresh token replaces the old one; a
/// server that returns none keeps the old one valid.
pub(super) async fn refresh(
    http: &reqwest::Client,
    current: &SignIn,
) -> Result<SignIn, TokenFailure> {
    if current.refresh_token.is_empty() {
        return Err(TokenFailure::Refused(
            "the sign-in cannot be refreshed".into(),
        ));
    }
    let now = now_ms();
    let tokens = token_request(
        http,
        &current.token_endpoint,
        &current.client_id,
        &current.client_secret,
        &current.token_auth,
        vec![
            ("grant_type", "refresh_token".into()),
            ("refresh_token", current.refresh_token.clone()),
            ("resource", current.resource.clone()),
        ],
    )
    .await?;
    let mut next = current.clone();
    next.access_token = tokens.access_token;
    next.expires_at_ms = expires_at(now, tokens.expires_in);
    if let Some(rt) = tokens.refresh_token.filter(|t| !t.is_empty()) {
        next.refresh_token = rt;
    }
    if let Some(scope) = tokens.scope {
        next.scope = scope;
    }
    Ok(next)
}

#[cfg(test)]
#[path = "mcp_oauth_tests.rs"]
mod mcp_oauth_tests;
