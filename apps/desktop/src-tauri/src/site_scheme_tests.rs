use std::io::{BufRead, BufReader, Write};
use std::sync::{Arc, Mutex};

use super::*;

fn header_of<'a>(resp: &'a Resp, name: &str) -> &'a str {
    resp.headers()
        .get(name)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
}

#[test]
fn maps_site_paths_to_the_gateway_route() {
    assert_eq!(
        upstream_path("/v1/sites/abc.DEF-1_~/", None).as_deref(),
        Some("/v1/sites/abc.DEF-1_~/")
    );
    assert_eq!(
        upstream_path("/v1/sites/T/assets/index-1a2b.js", None).as_deref(),
        Some("/v1/sites/T/assets/index-1a2b.js")
    );
    assert_eq!(
        upstream_path("/v1/sites/T/docs/", Some("q=1&x=a%20b")).as_deref(),
        Some("/v1/sites/T/docs/?q=1&x=a%20b")
    );
    assert_eq!(
        upstream_path("/v1/sites/T/my%20page.html", Some("")).as_deref(),
        Some("/v1/sites/T/my%20page.html")
    );
}

#[test]
fn refuses_anything_outside_the_site_route() {
    for path in [
        "",
        "/",
        "/v1/sites/",
        "/v1/sites/T",
        "/v1/rooms/r1/artifacts",
        "/v1/sites//index.html",
        "/v1/sites/../rooms/",
        "/v1/sites/T/../../rooms/r1",
        "/v1/sites/T/a/../../x",
        "/v1/sites/T/./index.html",
        "/v1/sites/T//index.html",
        "/v1/sites/T/a//b.js",
        "/v1/sites/T/%2e%2e/x",
        "/v1/sites/T/a%2Fb",
        "/v1/sites/T/a%5cb",
        "/v1/sites/T/a\\b",
        "/v1/sites/T%2F/x",
        "/v1/sites/T!/x",
        "/x/v1/sites/T/",
    ] {
        assert_eq!(upstream_path(path, None), None, "{path:?} must be refused");
    }
    let long = format!("/v1/sites/{}/", "a".repeat(MAX_TICKET + 1));
    assert_eq!(upstream_path(&long, None), None, "overlong ticket");
}

#[test]
fn only_reads_are_forwarded() {
    for method in [Method::POST, Method::PUT, Method::DELETE, Method::OPTIONS] {
        let resp = preflight(&method, "/v1/sites/T/", None).expect_err("refused");
        assert_eq!(resp.status(), StatusCode::METHOD_NOT_ALLOWED);
    }
    assert_eq!(
        preflight(&Method::GET, "/v1/nope", None)
            .expect_err("refused")
            .status(),
        StatusCode::NOT_FOUND
    );
    assert!(preflight(&Method::HEAD, "/v1/sites/T/", None).is_ok());
}

#[test]
fn relays_status_type_and_security_headers_only() {
    let mut headers = HeaderMap::new();
    headers.insert(
        header::CONTENT_TYPE,
        "text/html; charset=utf-8".parse().unwrap(),
    );
    headers.insert(
        header::CONTENT_SECURITY_POLICY,
        "sandbox allow-scripts; frame-ancestors *".parse().unwrap(),
    );
    headers.insert(header::X_CONTENT_TYPE_OPTIONS, "nosniff".parse().unwrap());
    headers.insert(header::REFERRER_POLICY, "no-referrer".parse().unwrap());
    headers.insert(header::ACCESS_CONTROL_ALLOW_ORIGIN, "*".parse().unwrap());
    headers.insert(
        header::CACHE_CONTROL,
        "private, max-age=300".parse().unwrap(),
    );
    headers.insert(header::SET_COOKIE, "s=1".parse().unwrap());
    headers.insert(header::SERVER, "gateway".parse().unwrap());
    let resp = relay(200, &headers, b"<!doctype html>".to_vec());
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(header_of(&resp, "content-type"), "text/html; charset=utf-8");
    assert_eq!(
        header_of(&resp, "content-security-policy"),
        "sandbox allow-scripts; frame-ancestors *"
    );
    assert_eq!(header_of(&resp, "x-content-type-options"), "nosniff");
    assert_eq!(header_of(&resp, "referrer-policy"), "no-referrer");
    assert_eq!(header_of(&resp, "access-control-allow-origin"), "*");
    assert_eq!(header_of(&resp, "cache-control"), "private, max-age=300");
    assert_eq!(
        header_of(&resp, "set-cookie"),
        "",
        "cookies never reach the frame"
    );
    assert_eq!(header_of(&resp, "server"), "");
    assert_eq!(resp.body().as_ref(), b"<!doctype html>");

    let mut moved = HeaderMap::new();
    moved.insert(header::LOCATION, "/v1/sites/T/docs/".parse().unwrap());
    assert_eq!(
        header_of(&relay(301, &moved, Vec::new()), "location"),
        "/v1/sites/T/docs/"
    );
    moved.insert(
        header::LOCATION,
        "https://elsewhere.example/".parse().unwrap(),
    );
    assert_eq!(
        header_of(&relay(302, &moved, Vec::new()), "location"),
        "",
        "off-route redirect dropped"
    );
    assert_eq!(
        relay(404, &HeaderMap::new(), Vec::new()).status(),
        StatusCode::NOT_FOUND
    );
}

/// A one-route gateway stand-in that records each request line + headers.
fn gateway(body: &'static [u8]) -> (String, Arc<Mutex<Vec<(String, Vec<String>)>>>) {
    let server = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
    let base = format!("http://{}", server.local_addr().expect("addr"));
    let seen = Arc::new(Mutex::new(Vec::new()));
    let log = Arc::clone(&seen);
    std::thread::spawn(move || {
        for stream in server.incoming() {
            let Ok(mut stream) = stream else { continue };
            let mut reader = BufReader::new(stream.try_clone().expect("clone"));
            let mut line = String::new();
            if reader.read_line(&mut line).is_err() {
                continue;
            }
            let mut headers = Vec::new();
            loop {
                let mut header = String::new();
                if reader.read_line(&mut header).is_err() || header.trim().is_empty() {
                    break;
                }
                headers.push(header.trim().to_ascii_lowercase());
            }
            log.lock()
                .expect("log")
                .push((line.trim().to_string(), headers));
            let _ = write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: text/css\r\nContent-Security-Policy: sandbox allow-scripts\r\nSet-Cookie: s=1\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                body.len()
            );
            let _ = stream.write_all(body);
        }
    });
    (base, seen)
}

#[tokio::test(flavor = "multi_thread")]
async fn forwards_to_the_gateway_without_a_session() {
    let (base, seen) = gateway(b"body{}");
    let upstream = upstream_path("/v1/sites/TK/assets/app.css", Some("v=2")).expect("valid");
    let resp = forward(&base, &upstream, &Method::GET).await;
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(header_of(&resp, "content-type"), "text/css");
    assert_eq!(
        header_of(&resp, "content-security-policy"),
        "sandbox allow-scripts"
    );
    assert_eq!(header_of(&resp, "set-cookie"), "");
    assert_eq!(resp.body().as_ref(), b"body{}");
    let head = forward(&base, &upstream, &Method::HEAD).await;
    assert!(head.body().is_empty());

    let seen = seen.lock().expect("log").clone();
    assert_eq!(seen[0].0, "GET /v1/sites/TK/assets/app.css?v=2 HTTP/1.1");
    assert_eq!(seen[1].0, "HEAD /v1/sites/TK/assets/app.css?v=2 HTTP/1.1");
    for (_, headers) in &seen {
        assert!(
            !headers.iter().any(|h| h.starts_with("authorization:")
                || h.starts_with("cookie:")
                || h.starts_with("x-bnbloc-")),
            "the ticket is the only credential: {headers:?}"
        );
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn an_unreachable_gateway_is_a_bad_gateway() {
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    let resp = forward(
        &format!("http://127.0.0.1:{port}"),
        "/v1/sites/T/",
        &Method::GET,
    )
    .await;
    assert_eq!(resp.status(), StatusCode::BAD_GATEWAY);
}
