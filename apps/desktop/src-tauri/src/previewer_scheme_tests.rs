use super::*;
use crate::internal_ext::previewer_bundle::parse_ustar;
use crate::internal_ext::previewer_bundle::previewer_bundle_tests::tar_files;

const ANCESTORS: &str = "tauri://localhost";

fn header_of(resp: &Resp, name: header::HeaderName) -> &str {
    resp.headers()
        .get(name)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
}

fn bundle() -> Bundle {
    parse_ustar(&tar_files(&[
        (
            "index.html",
            b"<!doctype html><script src=\"main.js\"></script>",
        ),
        ("main.js", b"console.log(1)"),
        ("fonts/a.woff2", b"wOF2"),
    ]))
    .expect("test bundle")
}

#[test]
fn parses_name_version_and_file() {
    assert_eq!(
        parse_path("/sprite-sheet/1.0.0/assets/main.js"),
        Some(PreviewerPath {
            name: "sprite-sheet".into(),
            version: "1.0.0".into(),
            file: "assets/main.js".into(),
        })
    );
}

#[test]
fn refuses_malformed_and_traversing_paths() {
    for path in [
        "",
        "/",
        "sprite-sheet/1.0.0/index.html",
        "/sprite-sheet/1.0.0",
        "/sprite-sheet/1.0.0/",
        "//sprite-sheet/1.0.0/index.html",
        "/sprite-sheet//1.0.0/index.html",
        "/sprite-sheet/1.0.0//index.html",
        "/sprite-sheet/1.0.0/../index.html",
        "/sprite-sheet/1.0.0/a/../../x.html",
        "/sprite-sheet/1.0.0/./index.html",
        "/sprite-sheet/1.0.0/%2e%2e/index.html",
        "/sprite-sheet/1.0.0/..%2fsecret",
        "/../../etc/passwd",
        "/sprite-sheet/../etc/passwd",
        "/Sprite/1.0.0/index.html",
        "/sprite-sheet/latest/index.html",
        "/sprite-sheet/1.0.0/index.html/",
        "/sprite-sheet/1.0.0/a\\b.js",
    ] {
        assert_eq!(parse_path(path), None, "{path} must be refused");
        assert_eq!(
            preflight(&Method::GET, path).err().map(|r| r.status()),
            Some(StatusCode::NOT_FOUND),
            "{path}"
        );
    }
}

#[test]
fn refuses_non_read_methods() {
    for method in [
        Method::POST,
        Method::PUT,
        Method::DELETE,
        Method::OPTIONS,
        Method::PATCH,
    ] {
        assert_eq!(
            preflight(&method, "/sprite-sheet/1.0.0/index.html")
                .err()
                .map(|r| r.status()),
            Some(StatusCode::METHOD_NOT_ALLOWED)
        );
    }
    assert!(preflight(&Method::HEAD, "/sprite-sheet/1.0.0/index.html").is_ok());
}

#[test]
fn serves_bundle_files_with_the_previewer_policy() {
    let b = bundle();
    let resp = serve(&Method::GET, &b, "index.html", ANCESTORS);
    assert_eq!(resp.status(), StatusCode::OK);
    assert!(resp.body().starts_with(b"<!doctype html>"));
    assert!(header_of(&resp, header::CONTENT_TYPE).starts_with("text/html"));
    let csp = header_of(&resp, header::CONTENT_SECURITY_POLICY).to_string();
    assert_eq!(csp, previewer_csp(ANCESTORS));
    assert!(csp.starts_with("default-src 'none';"), "{csp}");
    assert!(csp.contains("connect-src 'none'"), "{csp}");
    assert!(
        csp.contains(&format!("frame-ancestors {ANCESTORS}")),
        "{csp}"
    );
    assert!(
        csp.contains(&format!("script-src 'self' {SCHEME_ORIGIN};")),
        "{csp}"
    );
    for directive in csp.split(';').map(str::trim) {
        if directive.starts_with("connect-src") {
            assert_eq!(directive, "connect-src 'none'");
        }
        for forbidden in ["*", "https:", "http:", "'unsafe-eval'"] {
            if directive.starts_with("script-src") || directive.starts_with("connect-src") {
                assert!(
                    !directive.split(' ').any(|t| t == forbidden),
                    "{forbidden} in {directive}"
                );
            }
        }
    }
    assert!(!csp.contains("script-src 'self' 'unsafe-inline'"));
    assert_eq!(header_of(&resp, header::X_CONTENT_TYPE_OPTIONS), "nosniff");
    assert_eq!(header_of(&resp, header::REFERRER_POLICY), "no-referrer");
    assert_eq!(header_of(&resp, header::CACHE_CONTROL), "no-store");
    assert_eq!(header_of(&resp, header::ACCESS_CONTROL_ALLOW_ORIGIN), "*");

    let js = serve(&Method::GET, &b, "main.js", ANCESTORS);
    assert!(header_of(&js, header::CONTENT_TYPE).starts_with("text/javascript"));
    let font = serve(&Method::GET, &b, "fonts/a.woff2", ANCESTORS);
    assert_eq!(header_of(&font, header::CONTENT_TYPE), "font/woff2");

    let head = serve(&Method::HEAD, &b, "main.js", ANCESTORS);
    assert_eq!(head.status(), StatusCode::OK);
    assert!(head.body().is_empty());
}

#[test]
fn unknown_files_and_resolution_errors_map_to_statuses() {
    let b = bundle();
    assert_eq!(
        serve(&Method::GET, &b, "missing.js", ANCESTORS).status(),
        StatusCode::NOT_FOUND
    );
    assert_eq!(
        serve(&Method::GET, &b, "fonts", ANCESTORS).status(),
        StatusCode::NOT_FOUND
    );
    assert_eq!(
        error_response(&PreviewerError::NotFound).status(),
        StatusCode::NOT_FOUND
    );
    assert_eq!(
        error_response(&PreviewerError::Unavailable("x".into())).status(),
        StatusCode::SERVICE_UNAVAILABLE
    );
    assert_eq!(
        error_response(&PreviewerError::BadArtifact("sha256 mismatch".into())).status(),
        StatusCode::BAD_GATEWAY
    );
}

#[test]
fn unknown_extensions_are_opaque_bytes() {
    assert_eq!(content_type("data.bin"), "application/octet-stream");
    assert_eq!(content_type("README"), "application/octet-stream");
    assert_eq!(content_type("x.SVG"), "image/svg+xml");
}
