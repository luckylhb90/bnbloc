//! The plugin host API: what a plugin sidecar may ask its desktop to do as
//! the member.
//!
//! A plugin package that declares `permissions.host` is launched with
//! `BNBLOC_PLUGIN_HOST_URL` (this loopback listener, bound on 127.0.0.1 at a
//! random port when the first such plugin starts) and
//! `BNBLOC_PLUGIN_HOST_TOKEN` (random per launch, revoked when that process
//! exits). The token identifies the plugin; it is compared in constant time
//! (`Secret::matches`).
//!
//! Routes (JSON in, JSON out):
//!
//! - `POST /v1/chat` (`chat` permission) — `{roomId, channelId?, text,
//!   recipient?: {ownerId, provider, core?}, clientMsgId}` is posted through
//!   the gateway's normal chat route (`POST /v1/rooms/{roomId}/chat`) with
//!   this desktop's member session, so room membership, the chat rate limit,
//!   agent jobs and borrow rules are the chat path's. `clientMsgId` (the
//!   plugin's stable id of the message, e.g. the messenger message id) is
//!   namespaced per plugin, so a retry never posts twice and two plugins
//!   never collide. Answers `200 {sequence}`.
//!
//! Errors: 400 bad body, 401 bad token, 403 permission not declared (or the
//! gateway's 403: not a room member), 404/405 unknown route, 413 text over
//! 16 KB, 429 the plugin's local rate limit (or the gateway's), 502 gateway
//! unreachable or failing, 503 no member session on this desktop.
use std::collections::HashMap;
use std::convert::Infallible;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::{Duration, Instant};

use http_body_util::{BodyExt, Full, Limited};
use hyper::body::{Bytes, Incoming};
use hyper::{Method, Request, Response, StatusCode};
use parking_lot::Mutex;
use reqwest::header::CONTENT_TYPE;
use sha2::{Digest, Sha256};

use crate::internal_ext::loopback::{self, LoopbackHttp};
use crate::internal_ext::sidecar_model::GatewayAccess;
use crate::internal_ext::sidecar_util::hex_lower;
use crate::secret::Secret;

/// Env vars a host-permitted plugin is launched with.
pub(super) const HOST_URL_ENV: &str = "BNBLOC_PLUGIN_HOST_URL";
pub(super) const HOST_TOKEN_ENV: &str = "BNBLOC_PLUGIN_HOST_TOKEN";
/// The `permissions.host` capability for `POST /v1/chat` (pkgmeta.PluginHostChat).
const PERM_CHAT: &str = "chat";
/// The chat text limit (realtime's maxChatTextBytes).
const MAX_TEXT_BYTES: usize = 16 * 1024;
/// Bound on a request body (text plus envelope).
const MAX_BODY_BYTES: usize = 64 * 1024;
/// Bound on a plugin's own message id.
const MAX_CLIENT_MSG_ID_BYTES: usize = 200;
/// Per-plugin rate limit: a burst, refilled at one request per interval.
const RATE_BURST: f64 = 10.0;
const RATE_INTERVAL: Duration = Duration::from_secs(1);
const GATEWAY_TIMEOUT: Duration = Duration::from_secs(15);

/// One launched plugin's grant: its token and declared host permissions.
struct Grant {
    plugin: String,
    token: Secret,
    permissions: Vec<String>,
}

/// A token bucket per plugin (survives relaunches: a crash loop cannot
/// reset its own limit).
struct Bucket {
    tokens: f64,
    at: Instant,
}

/// The host API state: the grants of running plugins and the member session
/// requests are forwarded with.
pub(super) struct PluginHost {
    gateway: Arc<Mutex<GatewayAccess>>,
    grants: Mutex<Vec<Grant>>,
    buckets: Mutex<HashMap<String, Bucket>>,
    addr: tokio::sync::Mutex<Option<SocketAddr>>,
    http: reqwest::Client,
}

impl PluginHost {
    pub(super) fn new(gateway: Arc<Mutex<GatewayAccess>>) -> Arc<Self> {
        Arc::new(Self {
            gateway,
            grants: Mutex::new(Vec::new()),
            buckets: Mutex::new(HashMap::new()),
            addr: tokio::sync::Mutex::new(None),
            http: reqwest::Client::builder()
                .timeout(GATEWAY_TIMEOUT)
                .build()
                .unwrap_or_default(),
        })
    }

    /// The listener's base URL, binding 127.0.0.1:0 on first use.
    pub(super) async fn ensure_started(self: &Arc<Self>) -> Result<String, String> {
        let mut addr = self.addr.lock().await;
        if let Some(addr) = *addr {
            return Ok(LoopbackHttp::of_addr(addr));
        }
        let listener = loopback::bind_ephemeral_async()
            .await
            .map_err(|e| format!("bind plugin host API: {e}"))?;
        let bound = listener
            .local_addr()
            .map_err(|e| format!("plugin host API address: {e}"))?;
        let host = Arc::clone(self);
        tokio::spawn(async move { host.serve(listener).await });
        *addr = Some(bound);
        Ok(LoopbackHttp::of_addr(bound))
    }

    async fn serve(self: Arc<Self>, listener: tokio::net::TcpListener) {
        loop {
            let (stream, _) = match listener.accept().await {
                Ok(conn) => conn,
                Err(e) => {
                    eprintln!("[plugin-host] accept: {e}");
                    tokio::time::sleep(Duration::from_millis(100)).await;
                    continue;
                }
            };
            let host = Arc::clone(&self);
            tokio::spawn(async move {
                let service = hyper::service::service_fn(move |req| {
                    let host = Arc::clone(&host);
                    async move { Ok::<_, Infallible>(host.handle(req).await) }
                });
                let io = hyper_util::rt::TokioIo::new(stream);
                if let Err(e) = hyper::server::conn::http1::Builder::new()
                    .serve_connection(io, service)
                    .await
                {
                    eprintln!("[plugin-host] connection: {e}");
                }
            });
        }
    }

    /// Issue a fresh token for one launch of `plugin` (replacing any earlier
    /// launch's token).
    pub(super) fn grant(&self, plugin: &str, permissions: &[String]) -> Result<Secret, String> {
        let token = Secret::generate()?;
        let mut grants = self.grants.lock();
        grants.retain(|g| g.plugin != plugin);
        grants.push(Grant {
            plugin: plugin.to_string(),
            token: token.clone(),
            permissions: permissions.to_vec(),
        });
        Ok(token)
    }

    /// Revoke one launch's token (the process exited or was stopped).
    pub(super) fn revoke(&self, token: &Secret) {
        self.grants.lock().retain(|g| &g.token != token);
    }

    async fn handle(&self, req: Request<Incoming>) -> Response<Full<Bytes>> {
        let (parts, body) = req.into_parts();
        if parts.uri.path() != "/v1/chat" {
            return error(StatusCode::NOT_FOUND, "unknown host API route");
        }
        if parts.method != Method::POST {
            return error(StatusCode::METHOD_NOT_ALLOWED, "use POST");
        }
        let bearer = parts
            .headers
            .get(hyper::header::AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.strip_prefix("Bearer "))
            .unwrap_or("");
        let Some((plugin, permissions)) = self.authenticate(bearer) else {
            return error(StatusCode::UNAUTHORIZED, "invalid plugin host token");
        };
        if !permissions.iter().any(|p| p == PERM_CHAT) {
            return error(
                StatusCode::FORBIDDEN,
                "plugin does not declare permissions.host chat",
            );
        }
        let raw = match Limited::new(body, MAX_BODY_BYTES).collect().await {
            Ok(collected) => collected.to_bytes(),
            Err(_) => return error(StatusCode::PAYLOAD_TOO_LARGE, "request body too large"),
        };
        let chat = match parse_chat(&raw) {
            Ok(chat) => chat,
            Err((status, msg)) => return error(status, &msg),
        };
        if !self.admit(&plugin) {
            return error(StatusCode::TOO_MANY_REQUESTS, "plugin chat rate limit");
        }
        let gateway = self.gateway.lock().clone();
        self.forward_chat(&gateway, &plugin, chat).await
    }

    /// The plugin and permissions the token was issued to (constant-time
    /// comparison against every live grant).
    fn authenticate(&self, token: &str) -> Option<(String, Vec<String>)> {
        let grants = self.grants.lock();
        let mut found = None;
        for g in grants.iter() {
            if g.token.matches(token) {
                found = Some((g.plugin.clone(), g.permissions.clone()));
            }
        }
        found
    }

    fn admit(&self, plugin: &str) -> bool {
        let now = Instant::now();
        let mut buckets = self.buckets.lock();
        let bucket = buckets.entry(plugin.to_string()).or_insert(Bucket {
            tokens: RATE_BURST,
            at: now,
        });
        let refill = now.duration_since(bucket.at).as_secs_f64() / RATE_INTERVAL.as_secs_f64();
        bucket.tokens = (bucket.tokens + refill).min(RATE_BURST);
        bucket.at = now;
        if bucket.tokens < 1.0 {
            return false;
        }
        bucket.tokens -= 1.0;
        true
    }

    async fn forward_chat(
        &self,
        gateway: &GatewayAccess,
        plugin: &str,
        chat: ChatRequest,
    ) -> Response<Full<Bytes>> {
        if gateway.base_url.is_empty() || gateway.token.is_empty() {
            return error(
                StatusCode::SERVICE_UNAVAILABLE,
                "no member session on this desktop",
            );
        }
        let Ok(mut url) = reqwest::Url::parse(&gateway.base_url) else {
            return error(StatusCode::SERVICE_UNAVAILABLE, "invalid gateway URL");
        };
        match url.path_segments_mut() {
            Ok(mut segs) => {
                segs.pop_if_empty()
                    .extend(["v1", "rooms", chat.room_id.as_str(), "chat"]);
            }
            Err(()) => return error(StatusCode::SERVICE_UNAVAILABLE, "invalid gateway URL"),
        }
        let mut body = serde_json::json!({
            "text": chat.text,
            "clientMsgId": plugin_client_msg_id(plugin, &chat.client_msg_id),
        });
        if let Some(channel) = chat.channel_id.filter(|c| !c.is_empty()) {
            body["channelId"] = channel.into();
        }
        if let Some(recipient) = chat.recipient {
            body["recipient"] = recipient;
        }
        let resp = match self
            .http
            .post(url)
            .bearer_auth(&gateway.token)
            .header(CONTENT_TYPE, "application/json")
            .body(body.to_string())
            .send()
            .await
        {
            Ok(resp) => resp,
            Err(e) => {
                return error(
                    StatusCode::BAD_GATEWAY,
                    &format!("gateway unreachable: {e}"),
                )
            }
        };
        let status = resp.status();
        let text = resp.text().await.unwrap_or_default();
        if status.is_success() {
            let sequence = serde_json::from_str::<serde_json::Value>(&text)
                .ok()
                .and_then(|v| v.get("sequence").and_then(serde_json::Value::as_i64));
            return match sequence {
                Some(sequence) => json(StatusCode::OK, serde_json::json!({ "sequence": sequence })),
                None => error(
                    StatusCode::BAD_GATEWAY,
                    "gateway answered without a sequence",
                ),
            };
        }
        let reason = text.trim();
        match status.as_u16() {
            // The member session expired or was revoked: transient for the
            // plugin (the web hands the desktop a fresh one).
            401 => error(StatusCode::SERVICE_UNAVAILABLE, "member session expired"),
            400 | 403 | 404 | 409 | 413 | 429 => error(
                StatusCode::from_u16(status.as_u16()).unwrap_or(StatusCode::BAD_GATEWAY),
                if reason.is_empty() {
                    "refused by the gateway"
                } else {
                    reason
                },
            ),
            code => error(
                StatusCode::BAD_GATEWAY,
                &format!("gateway answered HTTP {code}"),
            ),
        }
    }
}

/// A validated `POST /v1/chat` body.
#[derive(Debug, PartialEq)]
struct ChatRequest {
    room_id: String,
    channel_id: Option<String>,
    text: String,
    recipient: Option<serde_json::Value>,
    client_msg_id: String,
}

#[derive(serde::Deserialize)]
#[serde(rename_all = "camelCase")]
struct ChatBody {
    room_id: String,
    #[serde(default)]
    channel_id: Option<String>,
    text: String,
    #[serde(default)]
    recipient: Option<Recipient>,
    client_msg_id: String,
}

#[derive(serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct Recipient {
    owner_id: String,
    provider: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    core: Option<String>,
}

fn parse_chat(raw: &[u8]) -> Result<ChatRequest, (StatusCode, String)> {
    let bad = |msg: &str| (StatusCode::BAD_REQUEST, msg.to_string());
    let body: ChatBody =
        serde_json::from_slice(raw).map_err(|e| bad(&format!("invalid chat body: {e}")))?;
    let room_id = body.room_id.trim().to_string();
    if room_id.is_empty() {
        return Err(bad("roomId is required"));
    }
    if body.text.trim().is_empty() {
        return Err(bad("text is required"));
    }
    if body.text.len() > MAX_TEXT_BYTES {
        return Err((StatusCode::PAYLOAD_TOO_LARGE, "text over 16 KB".into()));
    }
    let client_msg_id = body.client_msg_id.trim().to_string();
    if client_msg_id.is_empty() || client_msg_id.len() > MAX_CLIENT_MSG_ID_BYTES {
        return Err(bad("clientMsgId is required (at most 200 bytes)"));
    }
    let recipient = match body.recipient {
        Some(r) if r.owner_id.trim().is_empty() || r.provider.trim().is_empty() => {
            return Err(bad("recipient needs ownerId and provider"))
        }
        Some(r) => Some(serde_json::to_value(r).map_err(|e| bad(&e.to_string()))?),
        None => None,
    };
    Ok(ChatRequest {
        room_id,
        channel_id: body.channel_id.map(|c| c.trim().to_string()),
        text: body.text,
        recipient,
        client_msg_id,
    })
}

/// The chat clientMsgId of a plugin's message id: namespaced per plugin and
/// bounded (`plg_` + 48 hex), stable across retries.
fn plugin_client_msg_id(plugin: &str, id: &str) -> String {
    let mut h = Sha256::new();
    h.update(plugin.as_bytes());
    h.update([0u8]);
    h.update(id.as_bytes());
    format!("plg_{}", &hex_lower(&h.finalize())[..48])
}

fn json(status: StatusCode, value: serde_json::Value) -> Response<Full<Bytes>> {
    let mut resp = Response::new(Full::new(Bytes::from(value.to_string())));
    *resp.status_mut() = status;
    resp.headers_mut().insert(
        hyper::header::CONTENT_TYPE,
        hyper::header::HeaderValue::from_static("application/json"),
    );
    resp
}

fn error(status: StatusCode, msg: &str) -> Response<Full<Bytes>> {
    json(status, serde_json::json!({ "error": msg }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};

    /// A one-shot fake gateway answering `status`/`body`; returns its base
    /// URL and the raw request it received.
    fn gateway_once(
        status: &'static str,
        body: &'static str,
    ) -> (String, std::sync::mpsc::Receiver<String>) {
        let server = loopback::bind_ephemeral().expect("bind");
        let address = server.local_addr().expect("addr");
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let (mut stream, _) = server.accept().expect("accept");
            stream
                .set_read_timeout(Some(Duration::from_millis(500)))
                .ok();
            let mut request = Vec::new();
            let mut buf = [0u8; 8192];
            loop {
                match stream.read(&mut buf) {
                    Ok(0) | Err(_) => break,
                    Ok(n) => {
                        request.extend_from_slice(&buf[..n]);
                        let text = String::from_utf8_lossy(&request);
                        if let Some((head, rest)) = text.split_once("\r\n\r\n") {
                            let len = head
                                .lines()
                                .find_map(|l| {
                                    l.to_ascii_lowercase()
                                        .strip_prefix("content-length:")
                                        .map(|v| v.trim().parse::<usize>().unwrap_or(0))
                                })
                                .unwrap_or(0);
                            if rest.len() >= len {
                                break;
                            }
                        }
                    }
                }
            }
            write!(
                stream,
                "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            )
            .expect("respond");
            let _ = tx.send(String::from_utf8_lossy(&request).into_owned());
        });
        (format!("http://{address}"), rx)
    }

    async fn host_with(gateway_url: &str, session: &str) -> (Arc<PluginHost>, String) {
        let host = PluginHost::new(Arc::new(Mutex::new(GatewayAccess {
            base_url: gateway_url.to_string(),
            token: session.to_string(),
        })));
        let base = host.ensure_started().await.expect("start");
        (host, base)
    }

    async fn post(base: &str, token: &str, body: &str) -> (u16, serde_json::Value) {
        let resp = reqwest::Client::new()
            .post(format!("{base}/v1/chat"))
            .bearer_auth(token)
            .header(CONTENT_TYPE, "application/json")
            .body(body.to_string())
            .send()
            .await
            .expect("send");
        let status = resp.status().as_u16();
        let text = resp.text().await.unwrap_or_default();
        let value = serde_json::from_str(&text).unwrap_or(serde_json::Value::Null);
        (status, value)
    }

    const CHAT: &str = r#"{"roomId":"room-1","channelId":"ch_1","text":"also add a test","recipient":{"ownerId":"carol","provider":"codex","core":"local"},"clientMsgId":"v1:1791:+123"}"#;

    #[tokio::test]
    async fn chat_is_forwarded_with_the_member_session_and_a_namespaced_id() {
        let (gw, request) = gateway_once("200 OK", r#"{"sequence":42}"#);
        let (host, base) = host_with(&gw, "session-token").await;
        assert!(base.starts_with("http://127.0.0.1:"), "{base}");
        let token = host.grant("personal-bot", &["chat".into()]).expect("grant");

        let (status, body) = post(&base, token.expose(), CHAT).await;
        assert_eq!((status, body["sequence"].as_i64()), (200, Some(42)));

        let raw = request
            .recv_timeout(Duration::from_secs(2))
            .expect("forwarded");
        assert!(
            raw.starts_with("POST /v1/rooms/room-1/chat HTTP/1.1"),
            "{raw}"
        );
        assert!(
            raw.to_ascii_lowercase()
                .contains("authorization: bearer session-token"),
            "{raw}"
        );
        let sent: serde_json::Value =
            serde_json::from_str(raw.split_once("\r\n\r\n").unwrap().1).expect("json");
        assert_eq!(sent["text"], "also add a test");
        assert_eq!(sent["channelId"], "ch_1");
        assert_eq!(sent["recipient"]["provider"], "codex");
        assert_eq!(sent["recipient"]["core"], "local");
        assert_eq!(
            sent["clientMsgId"].as_str(),
            Some(plugin_client_msg_id("personal-bot", "v1:1791:+123").as_str())
        );
        assert_ne!(
            plugin_client_msg_id("personal-bot", "x"),
            plugin_client_msg_id("other-plugin", "x"),
            "ids are namespaced per plugin"
        );
    }

    #[tokio::test]
    async fn requests_are_authenticated_and_permission_checked() {
        let (host, base) = host_with("http://127.0.0.1:9", "s").await;
        let token = host.grant("reader", &[]).expect("grant");
        assert_eq!(post(&base, "", CHAT).await.0, 401);
        assert_eq!(post(&base, "nope", CHAT).await.0, 401);
        assert_eq!(
            post(&base, token.expose(), CHAT).await.0,
            403,
            "chat not declared"
        );

        // A relaunch replaces the token; a revoked one stops working.
        let first = host.grant("writer", &["chat".into()]).expect("grant");
        let second = host.grant("writer", &["chat".into()]).expect("grant");
        assert_ne!(first, second);
        assert_eq!(post(&base, first.expose(), CHAT).await.0, 401);
        host.revoke(&second);
        assert_eq!(post(&base, second.expose(), CHAT).await.0, 401);

        let resp = reqwest::Client::new()
            .get(format!("{base}/v1/chat"))
            .send()
            .await
            .expect("send");
        assert_eq!(resp.status().as_u16(), 405);
        let resp = reqwest::Client::new()
            .post(format!("{base}/v1/files"))
            .send()
            .await
            .expect("send");
        assert_eq!(resp.status().as_u16(), 404);
    }

    #[tokio::test]
    async fn bad_bodies_limits_and_missing_session() {
        let (host, base) = host_with("", "").await;
        let token = host.grant("p", &["chat".into()]).expect("grant");
        assert_eq!(post(&base, token.expose(), "{").await.0, 400);
        assert_eq!(
            post(
                &base,
                token.expose(),
                r#"{"roomId":"r","text":" ","clientMsgId":"m"}"#
            )
            .await
            .0,
            400
        );
        assert_eq!(
            post(
                &base,
                token.expose(),
                r#"{"roomId":"r","text":"hi","clientMsgId":""}"#
            )
            .await
            .0,
            400
        );
        assert_eq!(
            post(&base, token.expose(), r#"{"roomId":"r","text":"hi","clientMsgId":"m","recipient":{"ownerId":"","provider":"codex"}}"#).await.0,
            400
        );
        let long = format!(
            r#"{{"roomId":"r","text":"{}","clientMsgId":"m"}}"#,
            "x".repeat(MAX_TEXT_BYTES + 1)
        );
        assert_eq!(post(&base, token.expose(), &long).await.0, 413);
        let (status, body) = post(
            &base,
            token.expose(),
            r#"{"roomId":"r","text":"hi","clientMsgId":"m"}"#,
        )
        .await;
        assert_eq!(status, 503, "{body}");
    }

    #[tokio::test]
    async fn gateway_refusals_pass_through_and_expired_sessions_are_transient() {
        let (gw, _r) = gateway_once("403 Forbidden", "caller is not a member of room");
        let (host, base) = host_with(&gw, "s").await;
        let token = host.grant("p", &["chat".into()]).expect("grant");
        let (status, body) = post(&base, token.expose(), CHAT).await;
        assert_eq!(status, 403);
        assert_eq!(body["error"], "caller is not a member of room");

        let (gw, _r) = gateway_once("401 Unauthorized", "unauthorized");
        host.gateway.lock().base_url = gw;
        assert_eq!(post(&base, token.expose(), CHAT).await.0, 503);

        let (gw, _r) = gateway_once("500 Internal Server Error", "boom");
        host.gateway.lock().base_url = gw;
        assert_eq!(post(&base, token.expose(), CHAT).await.0, 502);
    }

    #[test]
    fn rate_limit_is_per_plugin() {
        let host = PluginHost::new(Arc::new(Mutex::new(GatewayAccess {
            base_url: String::new(),
            token: String::new(),
        })));
        for _ in 0..RATE_BURST as usize {
            assert!(host.admit("busy"));
        }
        assert!(!host.admit("busy"), "burst exhausted");
        assert!(host.admit("quiet"), "another plugin has its own bucket");
    }
}
