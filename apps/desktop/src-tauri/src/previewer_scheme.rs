//! `bnbloc-previewer` URI scheme: serves installed previewer bundles
//! (`bnbloc.previewer.package/v1`) to the web client's sandboxed previewer
//! frame, from their own origin with their own Content-Security-Policy.
//!
//! URL: `bnbloc-previewer://localhost/{name}/{version}/{path…}` (Windows and
//! Android: `http://bnbloc-previewer.localhost/…`). The web host
//! (web/src/lib/previewerFrame.ts) builds it with Tauri's `convertFileSrc`
//! and loads it into an `<iframe sandbox="allow-scripts">` (opaque origin, no
//! Tauri internals); the app CSP's `frame-src` admits this scheme.
//!
//! A request is served only for a previewer the member catalog lists as
//! installed at exactly that version; the bundle archive is downloaded with
//! the member's session, refused unless its sha256 is the catalog pin, and
//! files come only out of the parsed archive (internal_ext/previewer_cache.rs,
//! internal_ext/previewer_bundle.rs). Bundles never get network access:
//! `connect-src 'none'` and no network source anywhere in the policy.
//!
//! Statuses: 404 bad path / not installed / unknown file, 405 non-read
//! method, 502 refused artifact (sha256 mismatch, off-origin URL, malformed
//! archive), 503 no session or catalog unreachable (retry).

use std::borrow::Cow;

use tauri::http::{header, Method, Request, Response, StatusCode};
use tauri::{AppHandle, Manager, UriSchemeResponder};

use crate::internal_ext::previewer_bundle::{
    valid_bundle_path, valid_package_name, valid_package_version, Bundle,
};
use crate::internal_ext::previewer_cache::PreviewerError;
use crate::internal_ext::Adapter;

/// Scheme name; the web side (web/src/lib/previewerFrame.ts) resolves it via
/// Tauri's `convertFileSrc(`${name}/${version}/${entry}`, "bnbloc-previewer")`.
pub(crate) const SCHEME: &str = "bnbloc-previewer";

/// The bundle's own origin as the webview sees it (see artifact_scheme.rs).
#[cfg(any(windows, target_os = "android"))]
const SCHEME_ORIGIN: &str = "http://bnbloc-previewer.localhost";
#[cfg(not(any(windows, target_os = "android")))]
const SCHEME_ORIGIN: &str = "bnbloc-previewer://localhost";

/// The app's own origin (the only page allowed to frame a previewer).
#[cfg(any(windows, target_os = "android"))]
const APP_ORIGIN: &str = "http://tauri.localhost";
#[cfg(not(any(windows, target_os = "android")))]
const APP_ORIGIN: &str = "tauri://localhost";

type Resp = Response<Cow<'static, [u8]>>;

/// A validated scheme request path.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PreviewerPath {
    pub(crate) name: String,
    pub(crate) version: String,
    pub(crate) file: String,
}

/// Parses `/{name}/{version}/{path…}`; None for anything malformed (empty or
/// dot segments, `%` escapes, absolute or doubled slashes, bad name/version).
pub(crate) fn parse_path(path: &str) -> Option<PreviewerPath> {
    let rest = path.strip_prefix('/')?;
    let mut parts = rest.splitn(3, '/');
    let name = parts.next()?;
    let version = parts.next()?;
    let file = parts.next()?;
    if !valid_package_name(name) || !valid_package_version(version) || !valid_bundle_path(file) {
        return None;
    }
    Some(PreviewerPath {
        name: name.to_string(),
        version: version.to_string(),
        file: file.to_string(),
    })
}

/// The previewer document policy: scripts/styles/fonts only from the bundle,
/// images/media only from blob:/data:, no network, framed only by the app.
pub(crate) fn previewer_csp(frame_ancestors: &str) -> String {
    format!(
        "default-src 'none'; script-src 'self' {SCHEME_ORIGIN}; \
         style-src 'self' {SCHEME_ORIGIN} 'unsafe-inline'; img-src blob: data:; \
         media-src blob: data:; font-src 'self' {SCHEME_ORIGIN} data:; \
         connect-src 'none'; frame-ancestors {frame_ancestors}"
    )
}

/// Origins allowed to frame a previewer: the app origin, plus the dev
/// server's origin in debug builds (taken from the app config's devUrl, never
/// hardcoded).
fn frame_ancestors(app: &AppHandle) -> String {
    let mut origins = APP_ORIGIN.to_string();
    if cfg!(debug_assertions) {
        if let Some(dev) = app.config().build.dev_url.as_ref() {
            let origin = dev.origin().ascii_serialization();
            if origin != "null" {
                origins.push(' ');
                origins.push_str(&origin);
            }
        }
    }
    origins
}

/// Content type by extension, from a code-owned map.
fn content_type(file: &str) -> &'static str {
    let ext = file
        .rsplit_once('.')
        .map(|(_, ext)| ext.to_ascii_lowercase());
    match ext.as_deref() {
        Some("html") => "text/html; charset=utf-8",
        Some("js") | Some("mjs") => "text/javascript; charset=utf-8",
        Some("css") => "text/css; charset=utf-8",
        Some("json") | Some("map") => "application/json",
        Some("svg") => "image/svg+xml",
        Some("png") => "image/png",
        Some("jpg") | Some("jpeg") => "image/jpeg",
        Some("gif") => "image/gif",
        Some("webp") => "image/webp",
        Some("woff") => "font/woff",
        Some("woff2") => "font/woff2",
        Some("ttf") => "font/ttf",
        Some("wasm") => "application/wasm",
        Some("txt") => "text/plain; charset=utf-8",
        _ => "application/octet-stream",
    }
}

/// Checks method and path before anything is resolved or downloaded.
pub(crate) fn preflight(method: &Method, path: &str) -> Result<PreviewerPath, Resp> {
    if method != Method::GET && method != Method::HEAD {
        return Err(status_only(StatusCode::METHOD_NOT_ALLOWED));
    }
    parse_path(path).ok_or_else(|| status_only(StatusCode::NOT_FOUND))
}

/// Serves `file` out of a verified bundle. Pure, so headers are unit-tested.
pub(crate) fn serve(method: &Method, bundle: &Bundle, file: &str, frame_ancestors: &str) -> Resp {
    let Some(body) = bundle.file(file) else {
        return status_only(StatusCode::NOT_FOUND);
    };
    let body = if method == Method::HEAD {
        Cow::Borrowed(&[][..])
    } else {
        Cow::Owned(body.to_vec())
    };
    Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, content_type(file))
        .header(
            header::CONTENT_SECURITY_POLICY,
            previewer_csp(frame_ancestors),
        )
        .header(header::X_CONTENT_TYPE_OPTIONS, "nosniff")
        .header(header::REFERRER_POLICY, "no-referrer")
        .header(header::CACHE_CONTROL, "no-store")
        // Module scripts and fonts load in CORS mode from the opaque origin.
        .header(header::ACCESS_CONTROL_ALLOW_ORIGIN, "*")
        .body(body)
        .expect("static response parts are valid")
}

/// The response for a failed resolution.
pub(crate) fn error_response(err: &PreviewerError) -> Resp {
    match err {
        PreviewerError::NotFound => status_only(StatusCode::NOT_FOUND),
        PreviewerError::Unavailable(_) => status_only(StatusCode::SERVICE_UNAVAILABLE),
        PreviewerError::BadArtifact(_) => status_only(StatusCode::BAD_GATEWAY),
    }
}

fn status_only(status: StatusCode) -> Resp {
    Response::builder()
        .status(status)
        .header(header::CACHE_CONTROL, "no-store")
        .body(Cow::Borrowed(&[][..]))
        .expect("static response parts are valid")
}

/// Handles one scheme request asynchronously (resolution may download).
pub(crate) fn handle(app: &AppHandle, request: Request<Vec<u8>>, responder: UriSchemeResponder) {
    let target = match preflight(request.method(), request.uri().path()) {
        Ok(target) => target,
        Err(resp) => return responder.respond(resp),
    };
    let app = app.clone();
    let method = request.method().clone();
    tauri::async_runtime::spawn(async move {
        let Some(adapter) = app.try_state::<Adapter>().map(|s| s.inner().clone()) else {
            return responder.respond(status_only(StatusCode::SERVICE_UNAVAILABLE));
        };
        let resp = match adapter
            .previewer_bundle(&target.name, &target.version)
            .await
        {
            Ok(bundle) => serve(&method, &bundle, &target.file, &frame_ancestors(&app)),
            Err(err) => {
                eprintln!("[previewer:{}@{}] {err:?}", target.name, target.version);
                error_response(&err)
            }
        };
        responder.respond(resp);
    });
}

#[cfg(test)]
#[path = "previewer_scheme_tests.rs"]
mod previewer_scheme_tests;
