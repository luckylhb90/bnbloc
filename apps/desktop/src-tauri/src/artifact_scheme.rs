//! `bnbloc-artifact` URI scheme: serves the live-artifact runner page from its
//! own origin with its own Content-Security-Policy.
//!
//! Live artifacts are agent-made HTML (inline `<script>`, `onclick`). Rendered
//! through `srcdoc` they inherited the app's CSP, which then had to allow
//! inline script for the whole app — so any XSS in the app could reach Tauri
//! IPC. Instead the web client loads this runner by URL into a
//! `sandbox="allow-scripts"` iframe (no `allow-same-origin`: opaque origin, no
//! Tauri internals — those are injected into the main frame only), hands it
//! the artifact document via postMessage, and the app keeps `script-src
//! 'self'`; the app CSP's `frame-src` admits only this scheme.
//!
//! The runner files are the web bundle's own `web/public/artifact-runner.*`
//! (the browser loads the same files from the gateway), compiled in so the
//! scheme works identically in dev (`devUrl`) and bundled builds. Only those
//! two exact paths are served; everything else is 404 — no file system, no
//! path resolution, nothing to traverse.

use std::borrow::Cow;

use tauri::http::{header, Method, Response, StatusCode};

/// Scheme name; the web side (`web/src/lib/artifactFrame.ts`) resolves it via
/// Tauri's `convertFileSrc`.
pub(crate) const SCHEME: &str = "bnbloc-artifact";

/// The runner's own origin as the webview sees it: Tauri maps a custom scheme
/// to `<scheme>://localhost` on macOS/Linux and `http://<scheme>.localhost` on
/// Windows/Android (this app keeps the default `http` there).
#[cfg(any(windows, target_os = "android"))]
const RUNNER_ORIGIN: &str = "http://bnbloc-artifact.localhost";
#[cfg(not(any(windows, target_os = "android")))]
const RUNNER_ORIGIN: &str = "bnbloc-artifact://localhost";

const RUNNER_HTML: &[u8] = include_bytes!("../../../../web/public/artifact-runner.html");
const RUNNER_JS: &[u8] = include_bytes!("../../../../web/public/artifact-runner.js");

/// The runner page's policy: the artifact policy the runner's written
/// document re-applies via `<meta>` (no network origin at all; data:/blob:
/// for self-embedded assets) plus the runner's own external script. Inline
/// script is allowed HERE — that is the point of a separate origin — and
/// never in the app.
fn runner_csp() -> String {
    format!(
        "default-src 'none'; script-src 'self' {RUNNER_ORIGIN} 'unsafe-inline'; \
         style-src 'unsafe-inline'; img-src data: blob:; font-src data:; \
         media-src data: blob:"
    )
}

/// Serves one request to the scheme. Pure (no app state), so the whitelist and
/// headers are unit-tested directly.
pub(crate) fn respond(method: &Method, path: &str) -> Response<Cow<'static, [u8]>> {
    if method != Method::GET && method != Method::HEAD {
        return status_only(StatusCode::METHOD_NOT_ALLOWED);
    }
    let (body, content_type, csp) = match path {
        "/artifact-runner.html" => (RUNNER_HTML, "text/html; charset=utf-8", Some(runner_csp())),
        "/artifact-runner.js" => (RUNNER_JS, "text/javascript; charset=utf-8", None),
        _ => return status_only(StatusCode::NOT_FOUND),
    };
    let mut builder = Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, content_type)
        .header(header::X_CONTENT_TYPE_OPTIONS, "nosniff")
        .header(header::REFERRER_POLICY, "no-referrer")
        // Compiled into this binary: never let the webview pair an old cached
        // runner with a newer host protocol.
        .header(header::CACHE_CONTROL, "no-store");
    if let Some(csp) = csp {
        builder = builder.header(header::CONTENT_SECURITY_POLICY, csp);
    }
    let body = if method == Method::HEAD {
        Cow::Borrowed(&[][..])
    } else {
        Cow::Borrowed(body)
    };
    builder.body(body).expect("static response parts are valid")
}

fn status_only(status: StatusCode) -> Response<Cow<'static, [u8]>> {
    Response::builder()
        .status(status)
        .header(header::CACHE_CONTROL, "no-store")
        .body(Cow::Borrowed(&[][..]))
        .expect("static response parts are valid")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn header_of<'a>(resp: &'a Response<Cow<'static, [u8]>>, name: header::HeaderName) -> &'a str {
        resp.headers()
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    }

    #[test]
    fn serves_the_runner_page_with_its_own_permissive_script_policy() {
        let resp = respond(&Method::GET, "/artifact-runner.html");
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(resp.body().as_ref(), RUNNER_HTML);
        assert!(header_of(&resp, header::CONTENT_TYPE).starts_with("text/html"));
        let csp = header_of(&resp, header::CONTENT_SECURITY_POLICY);
        assert!(csp.starts_with("default-src 'none';"), "{csp}");
        assert!(
            csp.contains("'unsafe-inline'"),
            "artifacts run inline script: {csp}"
        );
        assert!(
            csp.contains(RUNNER_ORIGIN),
            "the runner's own script loads: {csp}"
        );
        for forbidden in ["connect-src", "https:", "http: ", "'unsafe-eval'", "*"] {
            assert!(
                !csp.contains(forbidden),
                "{forbidden} must not appear in {csp}"
            );
        }
        assert_eq!(header_of(&resp, header::X_CONTENT_TYPE_OPTIONS), "nosniff");
        assert_eq!(header_of(&resp, header::CACHE_CONTROL), "no-store");
    }

    #[test]
    fn serves_the_runner_script() {
        let resp = respond(&Method::GET, "/artifact-runner.js");
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(resp.body().as_ref(), RUNNER_JS);
        assert!(header_of(&resp, header::CONTENT_TYPE).starts_with("text/javascript"));
        let head = respond(&Method::HEAD, "/artifact-runner.js");
        assert_eq!(head.status(), StatusCode::OK);
        assert!(head.body().is_empty());
    }

    #[test]
    fn only_the_two_runner_assets_exist() {
        for path in [
            "/",
            "",
            "/index.html",
            "/artifact-runner.html/",
            "/artifact-runner.HTML",
            "/./artifact-runner.html",
            "/../artifact-runner.html",
            "/..%2fartifact-runner.html",
            "/%2e%2e/%2e%2e/etc/passwd",
            "/../../../../etc/passwd",
            "//artifact-runner.js",
            "/assets/artifact-runner.js",
            "/artifact-runner.js.map",
        ] {
            assert_eq!(
                respond(&Method::GET, path).status(),
                StatusCode::NOT_FOUND,
                "{path} must not be served"
            );
        }
    }

    #[test]
    fn refuses_non_read_methods() {
        for method in [Method::POST, Method::PUT, Method::DELETE, Method::OPTIONS] {
            assert_eq!(
                respond(&method, "/artifact-runner.html").status(),
                StatusCode::METHOD_NOT_ALLOWED
            );
        }
    }

    #[test]
    fn runner_assets_are_the_web_bundle_files() {
        let html = std::str::from_utf8(RUNNER_HTML).expect("utf-8");
        assert!(html.contains(r#"<script src="artifact-runner.js"></script>"#));
        assert!(
            !html.contains("<script>"),
            "the runner page itself carries no inline script"
        );
        let js = std::str::from_utf8(RUNNER_JS).expect("utf-8");
        assert!(js.contains("artifact-runner-ready") && js.contains("artifact-load"));
    }
}
