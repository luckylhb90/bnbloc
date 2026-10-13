use std::io::{BufRead, BufReader, Read, Write};
use std::sync::{Arc, Mutex as StdMutex};

use super::*;

/// The gateway path (and query) a scheme request maps to.
fn upstream_path(path: &str, query: Option<&str>) -> Option<String> {
    site_route(path, query).map(|(upstream, _)| upstream)
}

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
    assert_eq!(
        upstream_path("/v1/sites/T/~app/c/votes/01ab:x", Some("limit=5")).as_deref(),
        Some("/v1/sites/T/~app/c/votes/01ab:x?limit=5")
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
        "/v1/sites/T/~app/../../rooms",
    ] {
        assert_eq!(upstream_path(path, None), None, "{path:?} must be refused");
    }
    let long = format!("/v1/sites/{}/", "a".repeat(MAX_TICKET + 1));
    assert_eq!(upstream_path(&long, None), None, "overlong ticket");
}

#[test]
fn a_site_file_is_read_and_the_app_api_takes_every_method_it_uses() {
    for method in [
        Method::POST,
        Method::PUT,
        Method::PATCH,
        Method::DELETE,
        Method::OPTIONS,
    ] {
        let resp = preflight(&method, "/v1/sites/T/index.html", None, 0).expect_err("refused");
        assert_eq!(
            resp.status(),
            StatusCode::METHOD_NOT_ALLOWED,
            "{method} on a site file"
        );
    }
    for method in [Method::GET, Method::HEAD] {
        let call = preflight(&method, "/v1/sites/T/", None, 0).expect("a read");
        assert!(!call.app && !call.feed);
    }
    for method in [
        Method::GET,
        Method::HEAD,
        Method::POST,
        Method::PUT,
        Method::PATCH,
        Method::DELETE,
        Method::OPTIONS,
    ] {
        let call = preflight(&method, "/v1/sites/T/~app/c/votes/d1", None, 10).expect("app API");
        assert!(call.app && !call.feed, "{method}");
    }
    for method in [Method::TRACE, Method::CONNECT] {
        let resp = preflight(&method, "/v1/sites/T/~app/c/votes", None, 0).expect_err("refused");
        assert_eq!(resp.status(), StatusCode::METHOD_NOT_ALLOWED);
    }
    assert_eq!(
        preflight(
            &Method::POST,
            "/v1/sites/T/~app/c/votes",
            None,
            MAX_APP_BODY + 1
        )
        .expect_err("refused")
        .status(),
        StatusCode::PAYLOAD_TOO_LARGE
    );
    let feed = preflight(&Method::GET, "/v1/sites/T/~app/events", None, 0).expect("feed");
    assert!(feed.app && feed.feed);
    assert!(
        !preflight(&Method::OPTIONS, "/v1/sites/T/~app/events", None, 0)
            .expect("preflight")
            .feed
    );
    assert_eq!(
        preflight(&Method::GET, "/v1/nope", None, 0)
            .expect_err("refused")
            .status(),
        StatusCode::NOT_FOUND
    );
}

#[test]
fn forwards_only_the_app_api_headers() {
    let mut from = HeaderMap::new();
    for (name, value) in [
        ("content-type", "application/json"),
        ("if-match", "3"),
        ("if-none-match", "*"),
        ("accept", "text/event-stream"),
        ("x-bnbloc-visitor", "vis-token"),
        ("access-control-request-method", "PUT"),
        ("access-control-request-headers", "if-match"),
        ("authorization", "Bearer s"),
        ("cookie", "s=1"),
        ("x-bnbloc-user", "maya"),
        ("origin", "null"),
        ("referer", "bnbloc-site://localhost/"),
    ] {
        from.insert(HeaderName::from_static(name), value.parse().unwrap());
    }
    let out = forwarded_headers(&from);
    let mut names: Vec<_> = out.keys().map(|k| k.as_str().to_string()).collect();
    names.sort();
    assert_eq!(
        names,
        [
            "accept",
            "access-control-request-headers",
            "access-control-request-method",
            "content-type",
            "if-match",
            "if-none-match",
            "x-bnbloc-visitor"
        ]
    );
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
        header::ACCESS_CONTROL_ALLOW_METHODS,
        "GET, POST".parse().unwrap(),
    );
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
    assert_eq!(
        header_of(&resp, "access-control-allow-methods"),
        "GET, POST"
    );
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

#[test]
fn sites_are_where_the_gateway_says() {
    let gw = "https://bnbloc.example.com";
    let cfg = |origin: &str| ClientConfig {
        sites_origin: origin.to_string(),
    };
    assert_eq!(sites_base_of(gw, &cfg("")).as_deref(), Ok(gw));
    assert_eq!(
        sites_base_of(gw, &cfg("https://sites.bnbloc.example.com")).as_deref(),
        Ok("https://sites.bnbloc.example.com")
    );
    assert_eq!(
        sites_base_of(gw, &cfg("https://bnbloc-usercontent.example:8443")).as_deref(),
        Ok("https://bnbloc-usercontent.example:8443")
    );
    for bad in [
        "http://sites.bnbloc.example.com",
        "https://",
        "https://sites.bnbloc.example.com/",
        "https://sites.bnbloc.example.com/x",
        "https://u@sites.bnbloc.example.com",
        "https://a b",
        "sites.bnbloc.example.com",
    ] {
        assert!(
            sites_base_of(gw, &cfg(bad)).is_err(),
            "{bad:?} must be refused"
        );
    }
    let parsed: ClientConfig =
        serde_json::from_str(r#"{"sitesOrigin":"https://sites.bnbloc.example.com"}"#).unwrap();
    assert_eq!(parsed.sites_origin, "https://sites.bnbloc.example.com");
    let empty: ClientConfig = serde_json::from_str("{}").unwrap();
    assert_eq!(empty.sites_origin, "");
}

#[test]
fn takes_whole_events_and_drops_keep_alives() {
    let mut buf =
        b": keep-alive\n\nevent: app_changed\ndata: {\"id\":\"a\"}\n\nevent: app_ch".to_vec();
    assert_eq!(
        take_events(&mut buf),
        vec![b"event: app_changed\ndata: {\"id\":\"a\"}\n\n".to_vec()]
    );
    assert_eq!(buf, b"event: app_ch".to_vec(), "a partial event waits");
    buf.extend_from_slice(b"anged\ndata: {}\n\n");
    assert_eq!(take_events(&mut buf).len(), 1);
    assert!(buf.is_empty());
}

/// One request a stand-in gateway received.
#[derive(Debug, Clone)]
struct Seen {
    line: String,
    headers: Vec<String>,
    body: Vec<u8>,
}

/// How the stand-in answers.
enum Reply {
    Plain {
        status: &'static str,
        headers: Vec<&'static str>,
        body: Vec<u8>,
    },
    /// An SSE stream: each (delay, bytes) written in turn, then held open.
    Stream(Vec<(Duration, Vec<u8>)>),
}

type Seens = Arc<StdMutex<Vec<Seen>>>;

/// A gateway stand-in: one thread per connection, every request recorded,
/// answered by `answer`.
fn gateway(answer: impl Fn(&Seen) -> Reply + Send + Sync + 'static) -> (String, Seens) {
    let server = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
    let base = format!("http://{}", server.local_addr().expect("addr"));
    let seen: Seens = Arc::new(StdMutex::new(Vec::new()));
    let log = Arc::clone(&seen);
    let answer = Arc::new(answer);
    std::thread::spawn(move || {
        for stream in server.incoming() {
            let Ok(mut stream) = stream else { continue };
            let log = Arc::clone(&log);
            let answer = Arc::clone(&answer);
            std::thread::spawn(move || {
                let mut reader = BufReader::new(stream.try_clone().expect("clone"));
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() {
                    return;
                }
                let mut headers = Vec::new();
                let mut length = 0usize;
                loop {
                    let mut header = String::new();
                    if reader.read_line(&mut header).is_err() || header.trim().is_empty() {
                        break;
                    }
                    let header = header.trim().to_ascii_lowercase();
                    if let Some(n) = header.strip_prefix("content-length:") {
                        length = n.trim().parse().unwrap_or(0);
                    }
                    headers.push(header);
                }
                let mut body = vec![0; length];
                let _ = reader.read_exact(&mut body);
                let seen = Seen {
                    line: line.trim().to_string(),
                    headers,
                    body,
                };
                log.lock().expect("log").push(seen.clone());
                match answer(&seen) {
                    Reply::Plain {
                        status,
                        headers,
                        body,
                    } => {
                        let mut head = format!("HTTP/1.1 {status}\r\n");
                        for h in headers {
                            head.push_str(h);
                            head.push_str("\r\n");
                        }
                        head.push_str(&format!(
                            "Content-Length: {}\r\nConnection: close\r\n\r\n",
                            body.len()
                        ));
                        let _ = stream.write_all(head.as_bytes());
                        let _ = stream.write_all(&body);
                    }
                    Reply::Stream(parts) => {
                        let _ = stream.write_all(
                            b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n",
                        );
                        for (delay, bytes) in parts {
                            std::thread::sleep(delay);
                            if stream
                                .write_all(&bytes)
                                .and_then(|_| stream.flush())
                                .is_err()
                            {
                                return;
                            }
                        }
                        // Held open with keep-alives until the reader goes.
                        while stream.write_all(b": keep-alive\n\n").is_ok() {
                            std::thread::sleep(Duration::from_millis(50));
                        }
                    }
                }
            });
        }
    });
    (base, seen)
}

fn plain(status: &'static str, headers: Vec<&'static str>, body: &[u8]) -> Reply {
    Reply::Plain {
        status,
        headers,
        body: body.to_vec(),
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn forwards_a_site_file_without_a_session() {
    let (base, seen) = gateway(|_| {
        plain(
            "200 OK",
            vec![
                "Content-Type: text/css",
                "Content-Security-Policy: sandbox allow-scripts",
                "Set-Cookie: s=1",
            ],
            b"body{}",
        )
    });
    let upstream = upstream_path("/v1/sites/TK/assets/app.css", Some("v=2")).expect("valid");
    let resp = forward(&base, &upstream, &Method::GET, HeaderMap::new(), Vec::new()).await;
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(header_of(&resp, "content-type"), "text/css");
    assert_eq!(
        header_of(&resp, "content-security-policy"),
        "sandbox allow-scripts"
    );
    assert_eq!(header_of(&resp, "set-cookie"), "");
    assert_eq!(resp.body().as_ref(), b"body{}");
    let head = forward(
        &base,
        &upstream,
        &Method::HEAD,
        HeaderMap::new(),
        Vec::new(),
    )
    .await;
    assert!(head.body().is_empty());

    let seen = seen.lock().expect("log").clone();
    assert_eq!(seen[0].line, "GET /v1/sites/TK/assets/app.css?v=2 HTTP/1.1");
    assert_eq!(
        seen[1].line,
        "HEAD /v1/sites/TK/assets/app.css?v=2 HTTP/1.1"
    );
    for s in &seen {
        assert!(
            !s.headers.iter().any(|h| h.starts_with("authorization:")
                || h.starts_with("cookie:")
                || h.starts_with("x-bnbloc-")),
            "the ticket is the only credential: {:?}",
            s.headers
        );
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn forwards_app_writes_with_their_body_and_conditions() {
    let (base, seen) = gateway(|s| {
        if s.line.starts_with("OPTIONS") {
            plain(
                "204 No Content",
                vec![
                    "Access-Control-Allow-Origin: *",
                    "Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS",
                    "Access-Control-Allow-Headers: Content-Type, If-Match, If-None-Match, Accept, X-Bnbloc-Visitor",
                    "Access-Control-Max-Age: 600",
                ],
                b"",
            )
        } else {
            plain(
                "201 Created",
                vec![
                    "Content-Type: application/json",
                    "Access-Control-Allow-Origin: *",
                    "Cache-Control: no-store",
                ],
                br#"{"_id":"me","_version":1}"#,
            )
        }
    });
    let mut from = HeaderMap::new();
    from.insert(header::CONTENT_TYPE, "application/json".parse().unwrap());
    from.insert(header::IF_NONE_MATCH, "*".parse().unwrap());
    from.insert(
        HeaderName::from_static("x-bnbloc-visitor"),
        "vis-tok".parse().unwrap(),
    );
    from.insert(header::AUTHORIZATION, "Bearer secret".parse().unwrap());
    let body = br#"{"doc":{"choice":"B"}}"#.to_vec();
    let call = preflight(
        &Method::PUT,
        "/v1/sites/TK/~app/c/votes/me",
        None,
        body.len(),
    )
    .expect("app write");
    let resp = forward(
        &base,
        &call.upstream,
        &Method::PUT,
        forwarded_headers(&from),
        body.clone(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::CREATED);
    assert_eq!(header_of(&resp, "access-control-allow-origin"), "*");
    assert_eq!(resp.body().as_ref(), br#"{"_id":"me","_version":1}"#);

    for method in [Method::POST, Method::PATCH, Method::DELETE] {
        let resp = forward(
            &base,
            &call.upstream,
            &method,
            forwarded_headers(&from),
            body.clone(),
        )
        .await;
        assert_eq!(resp.status(), StatusCode::CREATED, "{method}");
    }
    let mut pre = HeaderMap::new();
    pre.insert(
        header::ACCESS_CONTROL_REQUEST_METHOD,
        "PUT".parse().unwrap(),
    );
    pre.insert(
        header::ACCESS_CONTROL_REQUEST_HEADERS,
        "if-none-match".parse().unwrap(),
    );
    let resp = forward(
        &base,
        &call.upstream,
        &Method::OPTIONS,
        forwarded_headers(&pre),
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::NO_CONTENT);
    assert_eq!(header_of(&resp, "access-control-allow-origin"), "*");
    assert!(header_of(&resp, "access-control-allow-headers").contains("If-None-Match"));
    assert_eq!(header_of(&resp, "access-control-max-age"), "600");

    let seen = seen.lock().expect("log").clone();
    assert_eq!(seen[0].line, "PUT /v1/sites/TK/~app/c/votes/me HTTP/1.1");
    assert_eq!(seen[0].body, body, "the body goes along");
    for want in [
        "content-type: application/json",
        "if-none-match: *",
        "x-bnbloc-visitor: vis-tok",
    ] {
        assert!(
            seen[0].headers.iter().any(|h| h == want),
            "{want} missing: {:?}",
            seen[0].headers
        );
    }
    assert!(!seen[0]
        .headers
        .iter()
        .any(|h| h.starts_with("authorization:")));
    let methods: Vec<_> = seen
        .iter()
        .map(|s| s.line.split(' ').next().unwrap().to_string())
        .collect();
    assert_eq!(methods, ["PUT", "POST", "PATCH", "DELETE", "OPTIONS"]);
    assert!(seen[1..4].iter().all(|s| s.body == body));
    assert!(seen[4]
        .headers
        .iter()
        .any(|h| h == "access-control-request-method: put"));
}

fn leaked_map() -> &'static FeedMap {
    Box::leak(Box::new(FeedMap::default()))
}

const FAST: FeedTimes = FeedTimes {
    window: Duration::from_secs(3),
    idle: Duration::from_secs(5),
};

fn event(id: &str) -> Vec<u8> {
    format!("event: app_changed\ndata: {{\"appId\":\"A\",\"collection\":\"votes\",\"id\":\"{id}\",\"version\":1}}\n\n").into_bytes()
}

#[tokio::test(flavor = "multi_thread")]
async fn the_change_feed_reaches_the_frame_poll_by_poll_without_losing_events() {
    let (base, seen) = gateway(|_| {
        Reply::Stream(vec![
            (Duration::ZERO, b": keep-alive\n\n".to_vec()),
            (Duration::from_millis(100), event("d1")),
            (
                Duration::from_millis(400),
                [event("d2"), event("d3")].concat(),
            ),
        ])
    });
    let map = leaked_map();
    let upstream = "/v1/sites/TK/~app/events";

    // The first poll holds the feed and answers its first event.
    let first = poll_feed_in(map, FAST, &base, upstream, HeaderMap::new()).await;
    assert_eq!(first.status(), StatusCode::OK);
    assert_eq!(header_of(&first, "content-type"), "text/event-stream");
    assert_eq!(header_of(&first, "access-control-allow-origin"), "*");
    assert_eq!(header_of(&first, "referrer-policy"), "no-referrer");
    assert_eq!(first.body().as_ref(), event("d1").as_slice());

    // Events that arrive between polls wait in the held feed.
    tokio::time::sleep(Duration::from_millis(800)).await;
    let second = poll_feed_in(map, FAST, &base, upstream, HeaderMap::new()).await;
    assert_eq!(
        second.body().as_ref(),
        [event("d2"), event("d3")].concat().as_slice()
    );

    // Nothing new within the window: a keep-alive, and the frame polls again.
    let quiet = poll_feed_in(map, FAST, &base, upstream, HeaderMap::new()).await;
    assert_eq!(quiet.status(), StatusCode::OK);
    assert_eq!(quiet.body().as_ref(), b": keep-alive\n\n");

    let seen = seen.lock().expect("log").clone();
    assert_eq!(
        seen.len(),
        1,
        "one upstream stream for every poll: {seen:?}"
    );
    assert_eq!(seen[0].line, "GET /v1/sites/TK/~app/events HTTP/1.1");
    assert!(seen[0]
        .headers
        .iter()
        .any(|h| h == "accept: text/event-stream"));
}

#[tokio::test(flavor = "multi_thread")]
async fn a_refused_feed_is_answered_as_refused_and_forgotten() {
    let (base, seen) = gateway(|_| {
        plain(
            "404 Not Found",
            vec![
                "Content-Type: application/json",
                "Access-Control-Allow-Origin: *",
            ],
            br#"{"error":"app not found"}"#,
        )
    });
    let map = leaked_map();
    let upstream = "/v1/sites/DEAD/~app/events";
    let resp = poll_feed_in(map, FAST, &base, upstream, HeaderMap::new()).await;
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
    assert_eq!(header_of(&resp, "access-control-allow-origin"), "*");
    assert_eq!(resp.body().as_ref(), br#"{"error":"app not found"}"#);
    assert!(map.0.lock().is_empty(), "a refused feed is not held");
    let again = poll_feed_in(map, FAST, &base, upstream, HeaderMap::new()).await;
    assert_eq!(again.status(), StatusCode::NOT_FOUND);
    assert_eq!(
        seen.lock().expect("log").len(),
        2,
        "the next poll asks again"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn an_unpolled_feed_is_closed() {
    let (base, _seen) = gateway(|_| Reply::Stream(vec![(Duration::ZERO, event("d1"))]));
    let map = leaked_map();
    let times = FeedTimes {
        window: Duration::from_secs(2),
        idle: Duration::from_millis(300),
    };
    let resp = poll_feed_in(
        map,
        times,
        &base,
        "/v1/sites/TK/~app/events",
        HeaderMap::new(),
    )
    .await;
    assert_eq!(resp.body().as_ref(), event("d1").as_slice());
    assert_eq!(map.0.lock().len(), 1, "held between polls");
    tokio::time::sleep(Duration::from_secs(2)).await;
    assert!(map.0.lock().is_empty(), "an unpolled feed is let go");
}

#[tokio::test(flavor = "multi_thread")]
async fn an_unreachable_gateway_is_a_bad_gateway() {
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    let base = format!("http://127.0.0.1:{port}");
    let resp = forward(
        &base,
        "/v1/sites/T/",
        &Method::GET,
        HeaderMap::new(),
        Vec::new(),
    )
    .await;
    assert_eq!(resp.status(), StatusCode::BAD_GATEWAY);
    let feed = poll_feed_in(
        leaked_map(),
        FAST,
        &base,
        "/v1/sites/T/~app/events",
        HeaderMap::new(),
    )
    .await;
    assert_eq!(feed.status(), StatusCode::BAD_GATEWAY);
}
