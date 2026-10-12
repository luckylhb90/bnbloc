//! The member's desktop stream: plugin hook deliveries and catalog hints.
//!
//! realtime is the hook authority (which plugin sees which room event, rollout,
//! tenant scope, opt-in installs) and the auditor, but a plugin sidecar runs
//! here, on a loopback port the server can never dial. So this desktop holds
//! the member's authenticated stream `GET /v1/my/plugin-events` (SSE through
//! the gateway) whenever its sidecar manager runs:
//!
//! - `plugin_event`: with `?plugins=1` (only while at least one plugin sidecar
//!   is installed) it posts each delivery's envelope to the running sidecar's
//!   loopback `POST /events` and reports the outcome with
//!   `POST /v1/my/plugin-events/{id}/ack`, which realtime audits. Deliveries
//!   are handled one at a time in stream order; the server bounds what it
//!   buffers for a slow desktop. A stream without the flag gets no
//!   deliveries and does not count as a plugin desktop for notifications.
//! - `components_changed`: the member's component catalog changed (an
//!   install, uninstall, rollout or tenant change, an opt-in); the manager
//!   refreshes it now (hints coalesce for HINT_COALESCE) instead of at the
//!   next poll, so a newly published MCP tool or sidecar shows up within
//!   seconds. The poll stays the safety net for a hint missed while the
//!   stream was down (a reconnect also refreshes once).
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use futures_util::StreamExt;
use reqwest::header::{ACCEPT, CONTENT_TYPE};

use crate::child_process::Backoff;
use crate::internal_ext::sidecar_model::{ComponentKind, GatewayAccess, Inner};
use crate::internal_ext::sidecar_status::component_id;
use crate::internal_ext::Adapter;

/// SSE event name of one delivery frame (wire.PluginEventKind).
const PLUGIN_EVENT: &str = "plugin_event";
/// SSE event name of a catalog hint (wire.ComponentsChangedKind).
const COMPONENTS_CHANGED: &str = "components_changed";
/// Hints arriving within this window of the first one are served by one
/// catalog refresh (an install publishes, sets rollout and tenants in a
/// row).
const HINT_COALESCE: Duration = Duration::from_secs(2);
/// How often a held stream re-checks whether it is still the right one
/// (manager stopped, session changed, plugins installed or removed) while
/// no bytes arrive.
const STREAM_CHECK: Duration = Duration::from_secs(5);
const RECONNECT_MIN: Duration = Duration::from_secs(1);
const RECONNECT_MAX: Duration = Duration::from_secs(30);
/// A stream that stayed up this long resets the reconnect backoff.
const STABLE_STREAM: Duration = Duration::from_secs(60);
/// Bound on one sidecar POST /events: a stuck sidecar must not stall the
/// stream (the delivery is acked as failed instead). A notification plugin
/// answers only once it delivered (e.g. sent a messenger message), so this
/// covers a round trip to an external service.
const SIDECAR_POST_TIMEOUT: Duration = Duration::from_secs(10);
const ACK_TIMEOUT: Duration = Duration::from_secs(10);
/// Bound on one unterminated SSE frame (the largest event payload is 1 MiB).
const MAX_FRAME_BYTES: usize = 4 << 20;

/// Coalesces catalog hints: the first hint claims the one scheduled
/// refresh; later hints are absorbed until that refresh starts (it releases
/// the claim first, so a hint arriving during the reconcile schedules
/// another: the reconcile may have read the catalog before that change).
#[derive(Default)]
pub(super) struct HintDebounce {
    scheduled: AtomicBool,
}

impl HintDebounce {
    /// True when the caller must schedule the refresh.
    fn claim(&self) -> bool {
        !self.scheduled.swap(true, Ordering::SeqCst)
    }

    fn release(&self) {
        self.scheduled.store(false, Ordering::SeqCst);
    }
}

#[derive(serde::Deserialize)]
struct PluginDelivery {
    id: String,
    plugin: String,
    envelope: serde_json::Value,
}

impl Adapter {
    /// Hold the member's desktop stream while the manager runs; reconnect
    /// with backoff; stop with the manager generation.
    pub(super) async fn run_plugin_events(&self, generation: u64) {
        let mut backoff = Backoff::new(RECONNECT_MIN, RECONNECT_MAX, STABLE_STREAM);
        let mut opened_before = false;
        loop {
            if !self.manager_current(generation) {
                return;
            }
            let started = Instant::now();
            if let Err(e) = self
                .consume_plugin_events(generation, &mut opened_before)
                .await
            {
                eprintln!("[plugin-events] {e}");
            }
            if !self.manager_current(generation) {
                return;
            }
            tokio::time::sleep(backoff.next(started.elapsed())).await;
        }
    }

    /// Whether any plugin sidecar is installed for this member (running or
    /// still starting: a delivery for one that is not up is acked as failed,
    /// so the gap is visible in the audit instead of silent).
    fn runs_plugins(&self) -> bool {
        self.inner.lock().sidecars.values().any(|sidecar| {
            sidecar
                .spec
                .as_ref()
                .is_some_and(|spec| spec.kind == ComponentKind::Plugin && spec.artifact.is_some())
        })
    }

    /// Refresh the component catalog soon: hints within HINT_COALESCE
    /// share one reconcile (serialized with the poll by reconcile_lock).
    pub(super) fn request_catalog_refresh(&self) {
        if !self.catalog_hints.claim() {
            return;
        }
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            tokio::time::sleep(HINT_COALESCE).await;
            sup.catalog_hints.release();
            if let Err(e) = sup.reconcile_bounded().await {
                eprintln!("[sidecar-manager] hinted refresh failed: {e}");
            }
        });
    }

    async fn consume_plugin_events(
        &self,
        generation: u64,
        opened_before: &mut bool,
    ) -> Result<(), String> {
        let gateway = self.gateway.lock().clone();
        if gateway.base_url.is_empty() || gateway.token.is_empty() {
            return Err("no gateway session".into());
        }
        // The flag tells realtime whether plugin deliveries (and
        // notification routing) may use this stream; the stream is reopened
        // when it stops matching.
        let plugins = self.runs_plugins();
        let resp = self
            .http()?
            .get(stream_url(&gateway.base_url, plugins))
            .bearer_auth(&gateway.token)
            .header(ACCEPT, "text/event-stream")
            .send()
            .await
            .map_err(|e| format!("open stream: {e}"))?;
        if !resp.status().is_success() {
            return Err(format!("open stream: status {}", resp.status()));
        }
        // A hint sent while the stream was down is lost: catch up once.
        if std::mem::replace(opened_before, true) {
            self.request_catalog_refresh();
        }
        let mut body = resp.bytes_stream();
        let mut parser = SseParser::default();
        loop {
            // The server writes a keep-alive comment every 15s; the shared
            // client's read-idle timeout turns a dead connection into an
            // error. The periodic wake re-checks the stream's fit sooner.
            if let Ok(next) = tokio::time::timeout(STREAM_CHECK, body.next()).await {
                let Some(chunk) = next else {
                    return Err("stream closed by the gateway".into());
                };
                let chunk = chunk.map_err(|e| format!("read stream: {e}"))?;
                for frame in parser.push(&chunk)? {
                    match frame_action(&frame) {
                        FrameAction::Deliver => {
                            match serde_json::from_str::<PluginDelivery>(&frame.data) {
                                Ok(delivery) => self.handle_delivery(&gateway, delivery).await,
                                Err(e) => eprintln!("[plugin-events] undecodable delivery: {e}"),
                            }
                        }
                        FrameAction::RefreshCatalog => self.request_catalog_refresh(),
                        FrameAction::Ignore => {}
                    }
                }
            }
            // Stop when the manager stopped, the plugin set flipped, or the
            // session changed (a sign-in as another member must not keep
            // this stream).
            if !self.manager_current(generation)
                || self.runs_plugins() != plugins
                || !same_session(&self.gateway.lock(), &gateway)
            {
                return Ok(());
            }
        }
    }

    async fn handle_delivery(&self, gateway: &GatewayAccess, delivery: PluginDelivery) {
        let outcome = match self.forward_to_sidecar(&delivery).await {
            Ok(()) => Ok(()),
            Err(e) => {
                eprintln!("[plugin-events:{}] {e}", delivery.plugin);
                Err(e)
            }
        };
        if let Err(e) = self.ack_plugin_event(gateway, &delivery.id, outcome).await {
            eprintln!(
                "[plugin-events:{}] ack {}: {e}",
                delivery.plugin, delivery.id
            );
        }
    }

    async fn forward_to_sidecar(&self, delivery: &PluginDelivery) -> Result<(), String> {
        let base = plugin_target(&self.inner.lock(), &delivery.plugin)?;
        post_envelope(&self.http()?, &base, &delivery.envelope).await
    }

    async fn ack_plugin_event(
        &self,
        gateway: &GatewayAccess,
        id: &str,
        outcome: Result<(), String>,
    ) -> Result<(), String> {
        if id.is_empty() || !id.chars().all(|c| c.is_ascii_alphanumeric() || c == '-') {
            return Err(format!("invalid delivery id {id:?}"));
        }
        let resp = self
            .http()?
            .post(format!("{}/v1/my/plugin-events/{id}/ack", gateway.base_url))
            .timeout(ACK_TIMEOUT)
            .bearer_auth(&gateway.token)
            .header(CONTENT_TYPE, "application/json")
            .body(ack_body(outcome))
            .send()
            .await
            .map_err(|e| e.to_string())?;
        if !resp.status().is_success() {
            return Err(format!("status {}", resp.status()));
        }
        Ok(())
    }
}

/// The member stream URL; `?plugins=1` while a plugin sidecar is installed.
fn stream_url(base_url: &str, plugins: bool) -> String {
    let query = if plugins { "?plugins=1" } else { "" };
    format!("{base_url}/v1/my/plugin-events{query}")
}

/// What a stream frame asks of the desktop.
#[derive(Debug, PartialEq)]
enum FrameAction {
    Deliver,
    RefreshCatalog,
    Ignore,
}

fn frame_action(frame: &SseFrame) -> FrameAction {
    match frame.event.as_str() {
        PLUGIN_EVENT => FrameAction::Deliver,
        COMPONENTS_CHANGED => FrameAction::RefreshCatalog,
        _ => FrameAction::Ignore,
    }
}

fn same_session(current: &GatewayAccess, opened: &GatewayAccess) -> bool {
    current.base_url == opened.base_url && current.token == opened.token
}

/// The ack body (wire.PluginDeliveryAck).
fn ack_body(outcome: Result<(), String>) -> String {
    match outcome {
        Ok(()) => serde_json::json!({ "status": "delivered" }),
        Err(detail) => serde_json::json!({ "status": "failed", "detail": detail }),
    }
    .to_string()
}

/// The loopback base URL of a running plugin sidecar. The desktop is the
/// only party that dials a plugin, and only on loopback: a runtime URL that is
/// not a loopback http URL is refused (the package validator already demands
/// one; this keeps the guarantee where the request is actually made).
fn plugin_target(inner: &Inner, plugin: &str) -> Result<String, String> {
    let sidecar = inner
        .sidecars
        .get(&component_id(ComponentKind::Plugin, plugin))
        .filter(|sidecar| sidecar.spec.is_some())
        .ok_or_else(|| format!("plugin {plugin} is not installed on this desktop"))?;
    let url = sidecar
        .runtime_url
        .as_deref()
        .ok_or_else(|| format!("plugin {plugin} sidecar is not running on this desktop"))?;
    loopback_http_base(url)
        .ok_or_else(|| format!("plugin {plugin} runtime URL {url} is not loopback http"))
}

fn loopback_http_base(raw: &str) -> Option<String> {
    let url = reqwest::Url::parse(raw.trim()).ok()?;
    let host = url
        .host_str()?
        .trim_start_matches('[')
        .trim_end_matches(']');
    let loopback = host == "localhost"
        || host
            .parse::<std::net::IpAddr>()
            .is_ok_and(|ip| ip.is_loopback());
    (url.scheme() == "http" && loopback).then(|| raw.trim().trim_end_matches('/').to_string())
}

/// POST the envelope verbatim to the sidecar's `/events`; any non-2xx is a
/// failure the ack reports.
async fn post_envelope(
    http: &reqwest::Client,
    base: &str,
    envelope: &serde_json::Value,
) -> Result<(), String> {
    let resp = http
        .post(format!("{base}/events"))
        .timeout(SIDECAR_POST_TIMEOUT)
        .header(CONTENT_TYPE, "application/json")
        .body(envelope.to_string())
        .send()
        .await
        .map_err(|e| format!("POST /events: {e}"))?;
    if !resp.status().is_success() {
        return Err(format!("POST /events status {}", resp.status().as_u16()));
    }
    Ok(())
}

/// One parsed Server-Sent Events frame.
#[derive(Debug, PartialEq)]
struct SseFrame {
    event: String,
    data: String,
}

/// Incremental SSE parser: bytes in (any chunking), complete frames out.
/// Buffers raw bytes so a UTF-8 sequence split across chunks stays intact.
#[derive(Default)]
struct SseParser {
    buf: Vec<u8>,
}

impl SseParser {
    fn push(&mut self, chunk: &[u8]) -> Result<Vec<SseFrame>, String> {
        self.buf
            .extend(chunk.iter().copied().filter(|b| *b != b'\r'));
        let mut frames = Vec::new();
        while let Some(end) = self.buf.windows(2).position(|w| w == b"\n\n") {
            let block: Vec<u8> = self.buf.drain(..end + 2).collect();
            if let Some(frame) = parse_block(&String::from_utf8_lossy(&block[..end])) {
                frames.push(frame);
            }
        }
        if self.buf.len() > MAX_FRAME_BYTES {
            return Err(format!("stream frame exceeds {MAX_FRAME_BYTES} bytes"));
        }
        Ok(frames)
    }
}

fn parse_block(block: &str) -> Option<SseFrame> {
    let mut event = String::new();
    let mut data: Vec<&str> = Vec::new();
    for line in block.lines() {
        if line.is_empty() || line.starts_with(':') {
            continue;
        }
        let (field, value) = line.split_once(':').unwrap_or((line, ""));
        let value = value.strip_prefix(' ').unwrap_or(value);
        match field {
            "event" => event = value.to_string(),
            "data" => data.push(value),
            _ => {}
        }
    }
    if data.is_empty() {
        return None;
    }
    if event.is_empty() {
        event = "message".into();
    }
    Some(SseFrame {
        event,
        data: data.join("\n"),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::sidecar_model::{AdapterStatus, ManagedSidecar, SidecarSpec};
    use std::collections::HashMap;
    use std::io::{Read, Write};

    #[test]
    fn sse_parser_reassembles_frames_across_chunks_and_skips_comments() {
        let mut parser = SseParser::default();
        let stream = ": connected\n\nid: a-1\nevent: plugin_event\ndata: {\"id\":\"a-1\",\"text\":\"h\u{e9}\"}\n\n: keep-alive\r\n\r\nevent: plugin_event\ndata: {\"id\":\"a-2\"}\n\n";
        let bytes = stream.as_bytes();
        let mut frames = Vec::new();
        // One byte at a time: splits lines, the CRLF pair, and the UTF-8 é.
        for b in bytes {
            frames.extend(parser.push(std::slice::from_ref(b)).expect("push"));
        }
        assert_eq!(
            frames,
            vec![
                SseFrame {
                    event: "plugin_event".into(),
                    data: "{\"id\":\"a-1\",\"text\":\"h\u{e9}\"}".into()
                },
                SseFrame {
                    event: "plugin_event".into(),
                    data: "{\"id\":\"a-2\"}".into()
                },
            ]
        );
    }

    #[test]
    fn components_changed_frames_refresh_the_catalog() {
        let mut parser = SseParser::default();
        let frames = parser
            .push(b"event: components_changed\ndata: {\"atMs\":42}\n\nevent: plugin_event\ndata: {}\n\nevent: other\ndata: x\n\n")
            .expect("push");
        let actions: Vec<FrameAction> = frames.iter().map(frame_action).collect();
        assert_eq!(
            actions,
            vec![
                FrameAction::RefreshCatalog,
                FrameAction::Deliver,
                FrameAction::Ignore
            ]
        );
    }

    #[test]
    fn stream_url_flags_plugin_desktops() {
        assert_eq!(
            stream_url("https://gw.example", true),
            "https://gw.example/v1/my/plugin-events?plugins=1"
        );
        assert_eq!(
            stream_url("https://gw.example", false),
            "https://gw.example/v1/my/plugin-events"
        );
    }

    #[test]
    fn hint_debounce_coalesces_until_the_refresh_starts() {
        let debounce = HintDebounce::default();
        assert!(debounce.claim(), "first hint schedules a refresh");
        assert!(!debounce.claim(), "a burst shares it");
        assert!(!debounce.claim());
        debounce.release();
        assert!(
            debounce.claim(),
            "a hint during the reconcile schedules another"
        );
    }

    #[test]
    fn sse_parser_bounds_an_unterminated_frame() {
        let mut parser = SseParser::default();
        let chunk = vec![b'x'; MAX_FRAME_BYTES + 1];
        assert!(parser.push(&chunk).is_err());
    }

    fn plugin_inner(name: &str, runtime_url: Option<&str>) -> Inner {
        let spec = SidecarSpec {
            kind: ComponentKind::Plugin,
            name: name.into(),
            version: "1.0.0".into(),
            artifact: Some(crate::internal_ext::sidecar_model::ComponentArtifact {
                url: "/v1/artifacts/x".into(),
                sha256: crate::internal_ext::pin::Sha256Pin::of(b"x"),
            }),
            manifest: None,
            local_url: "http://127.0.0.1:7191".into(),
            listen_env: String::new(),
            supervised_env: String::new(),
            args: Vec::new(),
            plugin: Default::default(),
            sandbox: Default::default(),
        };
        let mut sidecar = ManagedSidecar::pending(AdapterStatus::ready(&spec));
        sidecar.spec = Some(spec);
        sidecar.runtime_url = runtime_url.map(str::to_string);
        let mut sidecars = HashMap::new();
        sidecars.insert(component_id(ComponentKind::Plugin, name), sidecar);
        Inner {
            desired_up: true,
            generation: 1,
            sidecars,
            bridge_cores: HashMap::new(),
        }
    }

    #[test]
    fn plugin_target_requires_a_running_loopback_sidecar() {
        let running = plugin_inner("auditor", Some("http://127.0.0.1:7191/"));
        assert_eq!(
            plugin_target(&running, "auditor").as_deref(),
            Ok("http://127.0.0.1:7191")
        );
        assert!(plugin_target(&running, "other")
            .unwrap_err()
            .contains("not installed"));
        assert!(plugin_target(&plugin_inner("auditor", None), "auditor")
            .unwrap_err()
            .contains("not running"));
        for url in [
            "http://evil.example:7191",
            "https://127.0.0.1:7191",
            "http://10.0.0.5:7191",
        ] {
            assert!(
                plugin_target(&plugin_inner("auditor", Some(url)), "auditor")
                    .unwrap_err()
                    .contains("not loopback"),
                "{url} accepted"
            );
        }
        assert!(loopback_http_base("http://localhost:7191").is_some());
        assert!(loopback_http_base("http://[::1]:7191").is_some());
    }

    #[test]
    fn ack_body_matches_the_wire_contract() {
        assert_eq!(ack_body(Ok(())), r#"{"status":"delivered"}"#);
        let failed: serde_json::Value =
            serde_json::from_str(&ack_body(Err("POST /events status 502".into()))).unwrap();
        assert_eq!(failed["status"], "failed");
        assert_eq!(failed["detail"], "POST /events status 502");
    }

    /// A one-shot sidecar answering `status`; returns its base URL and the
    /// raw request it received.
    fn sidecar_once(status: &'static str) -> (String, std::sync::mpsc::Receiver<String>) {
        let server = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
        let address = server.local_addr().expect("addr");
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let (mut stream, _) = server.accept().expect("accept");
            stream
                .set_read_timeout(Some(Duration::from_millis(500)))
                .ok();
            let mut request = Vec::new();
            let mut buf = [0u8; 4096];
            loop {
                match stream.read(&mut buf) {
                    Ok(0) | Err(_) => break,
                    Ok(n) => {
                        request.extend_from_slice(&buf[..n]);
                        let text = String::from_utf8_lossy(&request);
                        if let Some((head, body)) = text.split_once("\r\n\r\n") {
                            let len = head
                                .lines()
                                .find_map(|l| {
                                    l.to_ascii_lowercase()
                                        .strip_prefix("content-length:")
                                        .map(|v| v.trim().parse::<usize>().unwrap_or(0))
                                })
                                .unwrap_or(0);
                            if body.len() >= len {
                                break;
                            }
                        }
                    }
                }
            }
            write!(
                stream,
                "HTTP/1.1 {status}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
            )
            .expect("respond");
            let _ = tx.send(String::from_utf8_lossy(&request).into_owned());
        });
        (format!("http://{address}"), rx)
    }

    #[tokio::test]
    async fn post_envelope_posts_the_envelope_verbatim_to_events() {
        let (base, request) = sidecar_once("204 No Content");
        let envelope = serde_json::json!({
            "event": "chat_message", "version": "1", "tenantId": "default",
            "roomId": "room-1", "payload": {"text": "hello"}
        });
        post_envelope(&reqwest::Client::new(), &base, &envelope)
            .await
            .expect("delivered");
        let raw = request
            .recv_timeout(Duration::from_secs(2))
            .expect("request");
        assert!(raw.starts_with("POST /events HTTP/1.1"), "{raw}");
        let body = raw.split_once("\r\n\r\n").map(|(_, b)| b).unwrap_or("");
        let got: serde_json::Value = serde_json::from_str(body).expect("json body");
        assert_eq!(got, envelope);
    }

    #[tokio::test]
    async fn post_envelope_reports_a_failing_sidecar() {
        let (base, _request) = sidecar_once("502 Bad Gateway");
        let err = post_envelope(&reqwest::Client::new(), &base, &serde_json::json!({}))
            .await
            .unwrap_err();
        assert_eq!(err, "POST /events status 502");
    }
}
