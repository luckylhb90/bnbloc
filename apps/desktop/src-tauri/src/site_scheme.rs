//! `bnbloc-site` URI scheme: runs a web-app (`site`) artifact in the app's
//! sandboxed site frame by proxying the gateway's ticketed site route, and
//! an interactive app's data API (`~app/…`) beside it.
//!
//! URL: `bnbloc-site://localhost/v1/sites/{ticket}/{path…}` (Windows and
//! Android: `http://bnbloc-site.localhost/…`). The web host
//! (web/src/lib/siteFrame.ts) takes the scheme root from Tauri's
//! `convertFileSrc` and appends the path of the member's `siteUrl`, then
//! loads it into an `<iframe sandbox="allow-scripts allow-forms allow-popups
//! allow-modals allow-downloads">` (opaque origin, no Tauri internals); the
//! app CSP's `frame-src` admits this scheme. The scheme exists so the app CSP
//! never has to admit the gateway's (or the sites origin's) origin in
//! `frame-src`.
//!
//! Where it forwards: the gateway's sites, which is the deployment's sites
//! origin when the gateway names one (`GET /v1/public/client-config`,
//! `sitesOrigin`: then the only origin serving sites), else the gateway
//! itself (`sites_base`). Each request goes to
//! `<sites>/v1/sites/{ticket}/{path…}[?query]` with NO session: the ticket in
//! the path is the credential (signed by the gateway, room + artifact +
//! version bound). Only the request headers the app API reads go along
//! (`FORWARDED_HEADERS`: its content type, `If-Match` / `If-None-Match`,
//! `Accept`, a public link's visitor token, a CORS preflight's); never a
//! cookie or an `Authorization`. Status, `Content-Type` and the gateway's
//! security and CORS headers pass through unchanged; redirects are not
//! followed (a `Location` inside `/v1/sites/` passes through, others drop).
//!
//! A site's files are read with GET and HEAD. Under `~app/` every method the
//! app API uses passes (GET, HEAD, POST, PUT, PATCH, DELETE and OPTIONS),
//! with its body (at most `MAX_APP_BODY`).
//!
//! The change feed (`GET ~app/events`, an endless SSE stream) cannot stream
//! through a scheme responder, which takes one body. So the shell holds the
//! feed's upstream stream itself (`Feed`, one per feed URL and visitor) and
//! answers each request with the events that arrived since the previous one,
//! waiting up to `FEED_POLL_WINDOW` for the first; the SDK reads the batch,
//! sees the response end and reconnects (1 s later), and the held stream
//! keeps every event meanwhile, so none is lost while the page polls. A feed
//! nobody polled for `FEED_IDLE` is closed. A refusal (a dead ticket) is
//! answered as the gateway answered it.
//!
//! Statuses of its own: 404 malformed path (anything outside `/v1/sites/`,
//! dot segments, encoded separators), 405 a method the path does not take,
//! 413 an app body over the cap, 502 gateway unreachable, 503 no gateway
//! configured yet.

use std::borrow::Cow;
use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, OnceLock};
use std::time::{Duration, Instant};

use parking_lot::Mutex;
use tauri::http::{header, HeaderMap, HeaderName, Method, Request, Response, StatusCode};
use tauri::{AppHandle, Manager, UriSchemeResponder};
use tokio::sync::watch;

use crate::runtime::Registry;

/// Scheme name; the web side (web/src/lib/siteFrame.ts) builds URLs with it.
pub(crate) const SCHEME: &str = "bnbloc-site";

/// The only gateway route this scheme reaches.
const SITES_PREFIX: &str = "/v1/sites/";

/// The app API's namespace inside a site, and its change feed.
const APP_PREFIX: &str = "~app/";
const FEED_FILE: &str = "~app/events";

/// Longest ticket accepted (a signed claim set; far below URL limits).
const MAX_TICKET: usize = 4096;

/// The largest site file proxied: a whole site's byte cap
/// (internal/tarbundle SiteLimits.MaxBytes), so no one file can exceed it.
const MAX_SITE_FILE_BYTES: usize = 64 << 20;

/// The largest app API request body: wire.MaxAppRequestBytes (a document's
/// 64 KiB plus 4 KiB of envelope; the gateway refuses more anyway).
const MAX_APP_BODY: usize = (64 << 10) + (4 << 10);

const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
/// No byte for this long ends a read; the change feed's keep-alives come
/// every 15 s.
const READ_IDLE_TIMEOUT: Duration = Duration::from_secs(30);

/// How long one change-feed request waits for an event before it answers
/// an empty batch.
const FEED_POLL_WINDOW: Duration = Duration::from_secs(20);
/// A held feed nobody polled for this long is closed.
const FEED_IDLE: Duration = Duration::from_secs(45);
/// How often a held feed checks whether it is still polled.
const FEED_IDLE_CHECK: Duration = Duration::from_secs(1);
/// Events a held feed keeps between polls (the oldest go first).
const FEED_MAX_EVENTS: usize = 256;
/// The longest event (or unfinished line run) of the feed relayed.
const FEED_MAX_EVENT_BYTES: usize = 16 << 10;
/// A refused feed's body is at most this long.
const FEED_MAX_REFUSAL_BYTES: usize = 16 << 10;

/// How long the gateway's answer to "where are sites served" is reused.
const CONFIG_TTL: Duration = Duration::from_secs(300);

/// Gateway response headers a site file or an app call keeps. Everything
/// else (cookies, server identity, hop-by-hop headers) is dropped.
const PASSED_HEADERS: [HeaderName; 12] = [
    header::CONTENT_TYPE,
    header::CONTENT_SECURITY_POLICY,
    header::X_CONTENT_TYPE_OPTIONS,
    header::REFERRER_POLICY,
    header::ACCESS_CONTROL_ALLOW_ORIGIN,
    header::ACCESS_CONTROL_ALLOW_METHODS,
    header::ACCESS_CONTROL_ALLOW_HEADERS,
    header::ACCESS_CONTROL_MAX_AGE,
    header::CACHE_CONTROL,
    header::CONTENT_DISPOSITION,
    header::ETAG,
    header::LAST_MODIFIED,
];

/// Request headers forwarded: what the app API reads (wire
/// AppVisitorHeader included) and a CORS preflight's. Never a cookie, an
/// `Authorization` or an identity header.
const FORWARDED_HEADERS: [HeaderName; 7] = [
    header::CONTENT_TYPE,
    header::IF_MATCH,
    header::IF_NONE_MATCH,
    header::ACCEPT,
    HeaderName::from_static("x-bnbloc-visitor"),
    header::ACCESS_CONTROL_REQUEST_METHOD,
    header::ACCESS_CONTROL_REQUEST_HEADERS,
];

type Resp = Response<Cow<'static, [u8]>>;

/// One scheme request, checked: the gateway path (and query) it maps to,
/// whether it is the app API and whether it is the change feed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Call {
    pub(crate) upstream: String,
    pub(crate) app: bool,
    pub(crate) feed: bool,
}

fn valid_ticket(ticket: &str) -> bool {
    !ticket.is_empty()
        && ticket.len() <= MAX_TICKET
        && ticket != "."
        && ticket != ".."
        && ticket
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._~-".contains(&b))
}

/// A site file path below the ticket: empty (the site root) or `/`-separated
/// segments with no empty, `.` or `..` segment (a trailing `/` names a
/// directory), no backslash or control byte, and no percent-encoded `.`, `/`
/// or `\` that the gateway URL could normalize into another route.
fn valid_file_path(path: &str) -> bool {
    if path.is_empty() {
        return true;
    }
    if path.bytes().any(|b| b == b'\\' || b.is_ascii_control()) {
        return false;
    }
    let lower = path.to_ascii_lowercase();
    if ["%2e", "%2f", "%5c", "%00"]
        .iter()
        .any(|bad| lower.contains(bad))
    {
        return false;
    }
    let trimmed = path.strip_suffix('/').unwrap_or(path);
    trimmed
        .split('/')
        .all(|seg| !seg.is_empty() && seg != "." && seg != "..")
}

/// The gateway path (and query) a scheme request maps to, and the file path
/// below the ticket; None when the path is not a well-formed
/// `/v1/sites/{ticket}/{path…}`.
fn site_route<'a>(path: &'a str, query: Option<&str>) -> Option<(String, &'a str)> {
    let rest = path.strip_prefix(SITES_PREFIX)?;
    let (ticket, file) = rest.split_once('/')?;
    if !valid_ticket(ticket) || !valid_file_path(file) {
        return None;
    }
    let mut out = format!("{SITES_PREFIX}{ticket}/{file}");
    if let Some(q) = query.filter(|q| !q.is_empty()) {
        if q.bytes().any(|b| b.is_ascii_control() || b == b'#') {
            return None;
        }
        out.push('?');
        out.push_str(q);
    }
    Some((out, file))
}

/// Checks the method, path and body before anything is forwarded: a site
/// file takes GET and HEAD, the app API every method it uses.
pub(crate) fn preflight(
    method: &Method,
    path: &str,
    query: Option<&str>,
    body_len: usize,
) -> Result<Call, Resp> {
    let (upstream, file) =
        site_route(path, query).ok_or_else(|| status_only(StatusCode::NOT_FOUND))?;
    let app = file.starts_with(APP_PREFIX);
    let allowed = method == Method::GET
        || method == Method::HEAD
        || (app
            && [
                Method::POST,
                Method::PUT,
                Method::PATCH,
                Method::DELETE,
                Method::OPTIONS,
            ]
            .contains(method));
    if !allowed {
        return Err(status_only(StatusCode::METHOD_NOT_ALLOWED));
    }
    if body_len > MAX_APP_BODY {
        return Err(status_only(StatusCode::PAYLOAD_TOO_LARGE));
    }
    let feed = app && method == Method::GET && file == FEED_FILE;
    Ok(Call {
        upstream,
        app,
        feed,
    })
}

/// The request headers a scheme request carries upstream
/// (FORWARDED_HEADERS only).
pub(crate) fn forwarded_headers(from: &HeaderMap) -> HeaderMap {
    let mut out = HeaderMap::new();
    for name in FORWARDED_HEADERS.iter() {
        if let Some(value) = from.get(name) {
            out.insert(name.clone(), value.clone());
        }
    }
    out
}

/// The scheme response for a gateway response. Pure, so header handling is
/// unit-tested.
pub(crate) fn relay(status: u16, headers: &HeaderMap, body: Vec<u8>) -> Resp {
    let status = StatusCode::from_u16(status).unwrap_or(StatusCode::BAD_GATEWAY);
    let mut builder = Response::builder().status(status);
    for name in PASSED_HEADERS.iter() {
        for value in headers.get_all(name) {
            builder = builder.header(name, value);
        }
    }
    // A redirect stays on the site route of this scheme (relative to its
    // origin); one leaving it is dropped rather than followed.
    if let Some(location) = headers
        .get(header::LOCATION)
        .and_then(|v| v.to_str().ok())
        .filter(|l| l.starts_with(SITES_PREFIX))
    {
        builder = builder.header(header::LOCATION, location);
    }
    builder
        .body(Cow::Owned(body))
        .unwrap_or_else(|_| status_only(StatusCode::BAD_GATEWAY))
}

fn status_only(status: StatusCode) -> Resp {
    Response::builder()
        .status(status)
        .header(header::CACHE_CONTROL, "no-store")
        .body(Cow::Borrowed(&[][..]))
        .expect("static response parts are valid")
}

/// The proxy client: bounded connect/read, no redirects followed, no cookie
/// store, and never a session header.
fn client() -> Result<reqwest::Client, String> {
    static CLIENT: OnceLock<Result<reqwest::Client, String>> = OnceLock::new();
    CLIENT
        .get_or_init(|| {
            reqwest::Client::builder()
                .connect_timeout(CONNECT_TIMEOUT)
                .read_timeout(READ_IDLE_TIMEOUT)
                .redirect(reqwest::redirect::Policy::none())
                .build()
                .map_err(|e| format!("build site client: {e}"))
        })
        .clone()
}

/// The gateway's client config (wire.ClientConfig).
#[derive(Debug, Default, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct ClientConfig {
    #[serde(default)]
    pub(crate) sites_origin: String,
}

/// Where the gateway at `gateway` serves sites: its sites origin (a bare
/// https origin) when it names one, else itself. Anything else the gateway
/// answered is refused, never guessed around.
pub(crate) fn sites_base_of(gateway: &str, config: &ClientConfig) -> Result<String, String> {
    let origin = config.sites_origin.trim();
    if origin.is_empty() {
        return Ok(gateway.to_string());
    }
    let host = origin.strip_prefix("https://").unwrap_or("");
    if host.is_empty()
        || host.contains(['/', '?', '#', '@', '\\'])
        || host.bytes().any(|b| b.is_ascii_control() || b == b' ')
    {
        return Err(format!(
            "the gateway names an invalid sites origin {origin:?}"
        ));
    }
    Ok(origin.to_string())
}

/// Where the configured gateway serves sites (sites_base_of over its
/// `GET /v1/public/client-config`), reused for CONFIG_TTL per gateway.
async fn sites_base(gateway: &str) -> Result<String, String> {
    static CACHE: OnceLock<Mutex<Option<(String, String, Instant)>>> = OnceLock::new();
    let cache = CACHE.get_or_init(|| Mutex::new(None));
    if let Some((gw, base, at)) = cache.lock().as_ref() {
        if gw == gateway && at.elapsed() < CONFIG_TTL {
            return Ok(base.clone());
        }
    }
    let resp = client()?
        .get(format!("{gateway}/v1/public/client-config"))
        .send()
        .await
        .map_err(|e| format!("gateway unreachable: {}", e.without_url()))?;
    if !resp.status().is_success() {
        return Err(format!(
            "the gateway's client config answered {}",
            resp.status()
        ));
    }
    let raw = read_bounded(resp, FEED_MAX_REFUSAL_BYTES).await?;
    let config: ClientConfig =
        serde_json::from_slice(&raw).map_err(|e| format!("the gateway's client config: {e}"))?;
    let base = sites_base_of(gateway, &config)?;
    *cache.lock() = Some((gateway.to_string(), base.clone(), Instant::now()));
    Ok(base)
}

/// Forwards one site file or app call to the sites at `base` (no trailing
/// slash): the method, the forwarded headers and (for a write) the body.
pub(crate) async fn forward(
    base: &str,
    upstream: &str,
    method: &Method,
    headers: HeaderMap,
    body: Vec<u8>,
) -> Resp {
    let client = match client() {
        Ok(client) => client,
        Err(err) => {
            eprintln!("[site] {err}");
            return status_only(StatusCode::BAD_GATEWAY);
        }
    };
    let mut request = client
        .request(method.clone(), format!("{base}{upstream}"))
        .headers(headers);
    if [Method::POST, Method::PUT, Method::PATCH, Method::DELETE].contains(method)
        && !body.is_empty()
    {
        request = request.body(body);
    }
    let resp = match request.send().await {
        Ok(resp) => resp,
        Err(err) => {
            eprintln!("[site] gateway unreachable: {}", err.without_url());
            return status_only(StatusCode::BAD_GATEWAY);
        }
    };
    let status = resp.status().as_u16();
    let headers = resp.headers().clone();
    let body = if method == Method::HEAD {
        Vec::new()
    } else {
        match read_bounded(resp, MAX_SITE_FILE_BYTES).await {
            Ok(body) => body,
            Err(err) => {
                eprintln!("[site] {err}");
                return status_only(StatusCode::BAD_GATEWAY);
            }
        }
    };
    relay(status, &headers, body)
}

/// A response's bytes, refused past `cap` (announced or streamed): a
/// webview request never buffers more.
async fn read_bounded(resp: reqwest::Response, cap: usize) -> Result<Vec<u8>, String> {
    use futures_util::StreamExt;
    if resp.content_length().is_some_and(|len| len > cap as u64) {
        return Err("response over the size cap".into());
    }
    let mut body = Vec::new();
    let mut stream = resp.bytes_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|e| format!("read failed: {}", e.without_url()))?;
        if body.len() + chunk.len() > cap {
            return Err("response over the size cap".into());
        }
        body.extend_from_slice(&chunk);
    }
    Ok(body)
}

// ---- the change feed ----

/// How a held feed ended.
#[derive(Debug, Clone)]
enum Ended {
    /// The gateway refused the feed (a dead ticket, a rate limit): answered
    /// as it was.
    Refused {
        status: u16,
        headers: HeaderMap,
        body: Vec<u8>,
    },
    /// The stream ended or failed: an empty batch, and the next request
    /// opens a new one.
    Closed,
    /// Nobody polled it for FEED_IDLE.
    Idle,
}

#[derive(Debug)]
struct FeedState {
    events: VecDeque<Vec<u8>>,
    ended: Option<Ended>,
    last_poll: Instant,
}

/// One held change feed: the events that arrived since the last poll, and
/// a version bumped on every change (polls wait on it).
struct Feed {
    state: Mutex<FeedState>,
    changed: watch::Sender<u64>,
}

impl Feed {
    fn new() -> Self {
        Feed {
            state: Mutex::new(FeedState {
                events: VecDeque::new(),
                ended: None,
                last_poll: Instant::now(),
            }),
            changed: watch::channel(0).0,
        }
    }

    fn bump(&self) {
        self.changed.send_modify(|v| *v = v.wrapping_add(1));
    }

    fn push(&self, event: Vec<u8>) {
        {
            let mut st = self.state.lock();
            if st.events.len() >= FEED_MAX_EVENTS {
                st.events.pop_front();
            }
            st.events.push_back(event);
        }
        self.bump();
    }

    fn end(&self, ended: Ended) {
        self.state.lock().ended = Some(ended);
        self.bump();
    }

    fn idle_for(&self) -> Duration {
        self.state.lock().last_poll.elapsed()
    }
}

/// The held feeds by feed key (feed_key).
#[derive(Default)]
pub(crate) struct FeedMap(Mutex<HashMap<String, Arc<Feed>>>);

/// How long a poll waits and a held feed lives unpolled (tests shorten
/// them).
#[derive(Debug, Clone, Copy)]
pub(crate) struct FeedTimes {
    pub(crate) window: Duration,
    pub(crate) idle: Duration,
}

const FEED_TIMES: FeedTimes = FeedTimes {
    window: FEED_POLL_WINDOW,
    idle: FEED_IDLE,
};

fn feeds() -> &'static FeedMap {
    static FEEDS: OnceLock<FeedMap> = OnceLock::new();
    FEEDS.get_or_init(FeedMap::default)
}

/// A feed's key: where it goes and as whom (a public link's visitor).
fn feed_key(base: &str, upstream: &str, headers: &HeaderMap) -> String {
    let visitor = headers
        .get("x-bnbloc-visitor")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    format!("{base}{upstream}\n{visitor}")
}

/// The complete events at the head of `buf` (each block up to its blank
/// line, with it), taken out of it; keep-alive comments are dropped.
pub(crate) fn take_events(buf: &mut Vec<u8>) -> Vec<Vec<u8>> {
    let mut out = Vec::new();
    while let Some(end) = buf.windows(2).position(|w| w == b"\n\n") {
        let block: Vec<u8> = buf.drain(..end + 2).collect();
        let is_event = block[..end]
            .split(|&b| b == b'\n')
            .any(|line| !line.is_empty() && line[0] != b':');
        if is_event {
            out.push(block);
        }
    }
    out
}

/// A batch of feed events as one SSE response (an empty batch is a
/// keep-alive): the app frame reads it, then reconnects.
fn feed_batch(events: Vec<Vec<u8>>) -> Resp {
    let body: Vec<u8> = if events.is_empty() {
        b": keep-alive\n\n".to_vec()
    } else {
        events.concat()
    };
    Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "text/event-stream")
        .header(header::CACHE_CONTROL, "no-store")
        .header(header::ACCESS_CONTROL_ALLOW_ORIGIN, "*")
        .header(header::X_CONTENT_TYPE_OPTIONS, "nosniff")
        .header(header::REFERRER_POLICY, "no-referrer")
        .body(Cow::Owned(body))
        .expect("static response parts are valid")
}

/// Answers one change-feed request: the held feed's events since the last
/// request (holding the feed from the first), waiting up to the poll window
/// for one.
pub(crate) async fn poll_feed(base: &str, upstream: &str, headers: HeaderMap) -> Resp {
    poll_feed_in(feeds(), FEED_TIMES, base, upstream, headers).await
}

pub(crate) async fn poll_feed_in(
    map: &'static FeedMap,
    times: FeedTimes,
    base: &str,
    upstream: &str,
    headers: HeaderMap,
) -> Resp {
    let key = feed_key(base, upstream, &headers);
    let feed = {
        let mut held = map.0.lock();
        match held.get(&key) {
            Some(feed) => Arc::clone(feed),
            None => {
                let feed = Arc::new(Feed::new());
                held.insert(key.clone(), Arc::clone(&feed));
                tauri::async_runtime::spawn(hold_feed(
                    map,
                    key.clone(),
                    Arc::clone(&feed),
                    format!("{base}{upstream}"),
                    headers,
                    times.idle,
                ));
                feed
            }
        }
    };
    let mut changed = feed.changed.subscribe();
    let deadline = tokio::time::Instant::now() + times.window;
    loop {
        {
            let mut st = feed.state.lock();
            st.last_poll = Instant::now();
            if !st.events.is_empty() {
                return feed_batch(st.events.drain(..).collect());
            }
            if let Some(ended) = st.ended.clone() {
                drop(st);
                forget_feed(map, &key, &feed);
                return match ended {
                    Ended::Refused {
                        status,
                        headers,
                        body,
                    } => relay(status, &headers, body),
                    Ended::Closed | Ended::Idle => feed_batch(Vec::new()),
                };
            }
        }
        match tokio::time::timeout_at(deadline, changed.changed()).await {
            Ok(Ok(())) => continue,
            // The feed is gone (it cannot be while held here) or the window
            // passed: an empty batch.
            Ok(Err(_)) | Err(_) => return feed_batch(Vec::new()),
        }
    }
}

/// Drops `feed` from the map (when it is still the one held under `key`).
fn forget_feed(map: &FeedMap, key: &str, feed: &Arc<Feed>) {
    let mut held = map.0.lock();
    if held.get(key).is_some_and(|f| Arc::ptr_eq(f, feed)) {
        held.remove(key);
    }
}

/// Holds one feed's upstream stream until it ends or goes unpolled.
async fn hold_feed(
    map: &'static FeedMap,
    key: String,
    feed: Arc<Feed>,
    url: String,
    headers: HeaderMap,
    idle: Duration,
) {
    let ended = stream_feed(&feed, &url, headers, idle).await;
    let unpolled = matches!(ended, Ended::Idle);
    feed.end(ended);
    if unpolled {
        forget_feed(map, &key, &feed);
        return;
    }
    // A poll answers the end and forgets the feed; if none comes, it goes
    // after the idle time.
    tokio::time::sleep(idle).await;
    forget_feed(map, &key, &feed);
}

/// Reads the upstream feed into `feed` until it ends, fails or goes
/// unpolled for `idle`.
async fn stream_feed(feed: &Feed, url: &str, mut headers: HeaderMap, idle: Duration) -> Ended {
    use futures_util::StreamExt;
    let client = match client() {
        Ok(client) => client,
        Err(err) => {
            eprintln!("[site] {err}");
            return Ended::Closed;
        }
    };
    headers.insert(
        header::ACCEPT,
        tauri::http::HeaderValue::from_static("text/event-stream"),
    );
    let resp = match client.get(url).headers(headers).send().await {
        Ok(resp) => resp,
        Err(err) => {
            eprintln!("[site] gateway unreachable: {}", err.without_url());
            return Ended::Refused {
                status: StatusCode::BAD_GATEWAY.as_u16(),
                headers: HeaderMap::new(),
                body: Vec::new(),
            };
        }
    };
    if resp.status() != reqwest::StatusCode::OK {
        let status = resp.status().as_u16();
        let headers = resp.headers().clone();
        let body = read_bounded(resp, FEED_MAX_REFUSAL_BYTES)
            .await
            .unwrap_or_default();
        return Ended::Refused {
            status,
            headers,
            body,
        };
    }
    let mut stream = resp.bytes_stream();
    let mut buf = Vec::new();
    loop {
        if feed.idle_for() > idle {
            return Ended::Idle;
        }
        match tokio::time::timeout(FEED_IDLE_CHECK.min(idle), stream.next()).await {
            Err(_) => continue,
            Ok(None) | Ok(Some(Err(_))) => return Ended::Closed,
            Ok(Some(Ok(chunk))) => {
                buf.extend_from_slice(&chunk);
                for event in take_events(&mut buf) {
                    feed.push(event);
                }
                if buf.len() > FEED_MAX_EVENT_BYTES {
                    eprintln!("[site] change feed event over the size cap");
                    return Ended::Closed;
                }
            }
        }
    }
}

/// Handles one scheme request asynchronously (it goes to the network).
pub(crate) fn handle(
    app: &AppHandle,
    webview_label: &str,
    request: Request<Vec<u8>>,
    responder: UriSchemeResponder,
) {
    let (parts, body) = request.into_parts();
    let call = match preflight(
        &parts.method,
        parts.uri.path(),
        parts.uri.query(),
        body.len(),
    ) {
        Ok(call) => call,
        Err(resp) => return responder.respond(resp),
    };
    let headers = forwarded_headers(&parts.headers);
    let method = parts.method;
    // The gateway of the account the requesting window shows.
    let gateway = app
        .try_state::<Registry>()
        .and_then(|registry| registry.runtime_of_window(webview_label))
        .map(|runtime| runtime.adapter.gateway_base_url())
        .unwrap_or_default();
    tauri::async_runtime::spawn(async move {
        if gateway.is_empty() {
            return responder.respond(status_only(StatusCode::SERVICE_UNAVAILABLE));
        }
        let base = match sites_base(&gateway).await {
            Ok(base) => base,
            Err(err) => {
                eprintln!("[site] {err}");
                return responder.respond(status_only(StatusCode::BAD_GATEWAY));
            }
        };
        let resp = if call.feed {
            poll_feed(&base, &call.upstream, headers).await
        } else {
            // A site file takes no body; an app call its own.
            let body = if call.app { body } else { Vec::new() };
            forward(&base, &call.upstream, &method, headers, body).await
        };
        responder.respond(resp);
    });
}

#[cfg(test)]
#[path = "site_scheme_tests.rs"]
mod site_scheme_tests;
