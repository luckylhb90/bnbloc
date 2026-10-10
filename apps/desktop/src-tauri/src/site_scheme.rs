//! `bnbloc-site` URI scheme: runs a web-app (`site`) artifact in the app's
//! sandboxed site frame by proxying the gateway's ticketed site route.
//!
//! URL: `bnbloc-site://localhost/v1/sites/{ticket}/{path…}` (Windows and
//! Android: `http://bnbloc-site.localhost/…`). The web host
//! (web/src/lib/siteFrame.ts) takes the scheme root from Tauri's
//! `convertFileSrc` and appends the member's `siteUrl` verbatim, then loads
//! it into an `<iframe sandbox="allow-scripts allow-forms allow-popups
//! allow-modals allow-downloads">` (opaque origin, no Tauri internals); the
//! app CSP's `frame-src` admits this scheme. The scheme exists so the app CSP
//! never has to admit the gateway origin in `frame-src`.
//!
//! Each request is forwarded to `<gateway>/v1/sites/{ticket}/{path…}[?query]`
//! with NO session: the ticket in the path is the credential (signed by the
//! gateway, room + artifact + version bound, 1 h). Status, `Content-Type`
//! and the gateway's security headers (its `sandbox` CSP, nosniff,
//! no-referrer, CORS, caching) pass through unchanged; redirects are not
//! followed (a `Location` inside `/v1/sites/` passes through, others drop).
//!
//! Statuses of its own: 404 malformed path (anything outside `/v1/sites/`,
//! dot segments, encoded separators), 405 non-read method, 502 gateway
//! unreachable, 503 no gateway configured yet.

use std::borrow::Cow;
use std::sync::OnceLock;
use std::time::Duration;

use tauri::http::{header, HeaderMap, HeaderName, Method, Request, Response, StatusCode};
use tauri::{AppHandle, Manager, UriSchemeResponder};

use crate::internal_ext::Adapter;

/// Scheme name; the web side (web/src/lib/siteFrame.ts) builds URLs with it.
pub(crate) const SCHEME: &str = "bnbloc-site";

/// The only gateway route this scheme reaches.
const SITES_PREFIX: &str = "/v1/sites/";

/// Longest ticket accepted (a signed claim set; far below URL limits).
const MAX_TICKET: usize = 4096;

const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const READ_IDLE_TIMEOUT: Duration = Duration::from_secs(30);

/// Gateway response headers a site file keeps. Everything else (cookies,
/// server identity, hop-by-hop headers) is dropped.
const PASSED_HEADERS: [HeaderName; 9] = [
    header::CONTENT_TYPE,
    header::CONTENT_SECURITY_POLICY,
    header::X_CONTENT_TYPE_OPTIONS,
    header::REFERRER_POLICY,
    header::ACCESS_CONTROL_ALLOW_ORIGIN,
    header::CACHE_CONTROL,
    header::CONTENT_DISPOSITION,
    header::ETAG,
    header::LAST_MODIFIED,
];

type Resp = Response<Cow<'static, [u8]>>;

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

/// The gateway path (and query) a scheme request maps to; None when the path
/// is not a well-formed `/v1/sites/{ticket}/{path…}`.
pub(crate) fn upstream_path(path: &str, query: Option<&str>) -> Option<String> {
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
    Some(out)
}

/// Checks method and path before anything is forwarded.
pub(crate) fn preflight(method: &Method, path: &str, query: Option<&str>) -> Result<String, Resp> {
    if method != Method::GET && method != Method::HEAD {
        return Err(status_only(StatusCode::METHOD_NOT_ALLOWED));
    }
    upstream_path(path, query).ok_or_else(|| status_only(StatusCode::NOT_FOUND))
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

/// Forwards one site request to the gateway at `base` (no trailing slash).
pub(crate) async fn forward(base: &str, upstream: &str, method: &Method) -> Resp {
    let client = match client() {
        Ok(client) => client,
        Err(err) => {
            eprintln!("[site] {err}");
            return status_only(StatusCode::BAD_GATEWAY);
        }
    };
    let url = format!("{base}{upstream}");
    let request = if method == Method::HEAD {
        client.head(&url)
    } else {
        client.get(&url)
    };
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
        match resp.bytes().await {
            Ok(bytes) => bytes.to_vec(),
            Err(err) => {
                eprintln!("[site] read failed: {}", err.without_url());
                return status_only(StatusCode::BAD_GATEWAY);
            }
        }
    };
    relay(status, &headers, body)
}

/// Handles one scheme request asynchronously (it goes to the network).
pub(crate) fn handle(app: &AppHandle, request: Request<Vec<u8>>, responder: UriSchemeResponder) {
    let upstream = match preflight(
        request.method(),
        request.uri().path(),
        request.uri().query(),
    ) {
        Ok(upstream) => upstream,
        Err(resp) => return responder.respond(resp),
    };
    let app = app.clone();
    let method = request.method().clone();
    tauri::async_runtime::spawn(async move {
        let base = app
            .try_state::<Adapter>()
            .map(|adapter| adapter.gateway_base_url())
            .unwrap_or_default();
        if base.is_empty() {
            return responder.respond(status_only(StatusCode::SERVICE_UNAVAILABLE));
        }
        responder.respond(forward(&base, &upstream, &method).await);
    });
}

#[cfg(test)]
#[path = "site_scheme_tests.rs"]
mod site_scheme_tests;
