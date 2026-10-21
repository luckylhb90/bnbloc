//! Loopback: the one rule for what counts as this machine, and the one way
//! the desktop talks to and listens on it.
//!
//! - `LoopbackHttp::parse` is the rule for a sidecar's `localUrl` (the
//!   gateway's `pkgmeta.ValidateLoopbackHTTPURL`: http on `127.0.0.1` or
//!   `localhost`, plus an explicit port the desktop can bind). The listen
//!   address, the fixed port and the base URL a delivery goes to all come
//!   from it, so a URL accepted in one place is accepted everywhere.
//! - `is_loopback_host` is the same host rule for other URLs (an OAuth
//!   server on this machine may use http).
//! - `bind_ephemeral` / `bind_ephemeral_async` open the desktop's own
//!   loopback listeners (sandbox sidecar port, plugin host API, OAuth
//!   callback) on a fresh port.
//! - `client` talks to loopback services: no redirects (a sidecar's 307 must
//!   never re-post a room envelope off the machine) and no proxy.
use std::net::SocketAddr;
use std::sync::OnceLock;
use std::time::Duration;

use reqwest::Url;

/// The address the desktop binds its own listeners on.
const LOOPBACK_IP: &str = "127.0.0.1";
const CONNECT_TIMEOUT: Duration = Duration::from_secs(2);

/// Whether `host` (as a URL names it) is this machine.
pub(crate) fn is_loopback_host(host: &str) -> bool {
    host == LOOPBACK_IP || host == "localhost"
}

/// Whether `ip` is a loopback address (IPv4 127/8, IPv6 ::1).
pub(crate) fn is_loopback_ip(ip: std::net::IpAddr) -> bool {
    ip.is_loopback()
}

/// A validated loopback http URL with an explicit port.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct LoopbackHttp {
    url: Url,
    authority: String,
    port: u16,
}

impl LoopbackHttp {
    pub(crate) fn parse(raw: &str) -> Result<Self, String> {
        let url = Url::parse(raw.trim()).map_err(|_| format!("{raw:?} is not a URL"))?;
        if url.scheme() != "http" {
            return Err(format!("{raw:?} must use http"));
        }
        let host = url.host_str().unwrap_or_default().to_string();
        if !is_loopback_host(&host) {
            return Err(format!("{raw:?} must be loopback"));
        }
        let port = url.port().ok_or_else(|| format!("{raw:?} names no port"))?;
        Ok(Self {
            authority: format!("{host}:{port}"),
            port,
            url,
        })
    }

    /// The URL of a listener bound at `addr`.
    pub(crate) fn of_addr(addr: SocketAddr) -> String {
        format!("http://{addr}")
    }

    /// `host:port`: what the sidecar listens on (its listenEnv) and the
    /// desktop reserves.
    pub(crate) fn authority(&self) -> &str {
        &self.authority
    }

    pub(crate) fn port(&self) -> u16 {
        self.port
    }

    /// The URL without a trailing slash, to append API paths to.
    pub(crate) fn base(&self) -> String {
        self.url.as_str().trim_end_matches('/').to_string()
    }
}

/// A listener on a fresh loopback port.
pub(crate) fn bind_ephemeral() -> std::io::Result<std::net::TcpListener> {
    std::net::TcpListener::bind((LOOPBACK_IP, 0))
}

/// `bind_ephemeral` for the async runtime.
pub(crate) async fn bind_ephemeral_async() -> std::io::Result<tokio::net::TcpListener> {
    tokio::net::TcpListener::bind((LOOPBACK_IP, 0)).await
}

/// The client for loopback services: never follows a redirect, never goes
/// through a proxy, connects quickly. Requests set their own total timeout.
pub(crate) fn client() -> Result<reqwest::Client, String> {
    static CLIENT: OnceLock<Result<reqwest::Client, String>> = OnceLock::new();
    CLIENT
        .get_or_init(|| {
            reqwest::Client::builder()
                .connect_timeout(CONNECT_TIMEOUT)
                .redirect(reqwest::redirect::Policy::none())
                .no_proxy()
                .build()
                .map_err(|e| format!("build loopback client: {e}"))
        })
        .clone()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// One loopback rule: the same URL is accepted (or refused) for a
    /// sidecar's listen address, its port, and a delivery to it.
    #[test]
    fn one_loopback_rule() {
        let ok = LoopbackHttp::parse(" http://127.0.0.1:7190/base/ ").expect("loopback");
        assert_eq!(ok.authority(), "127.0.0.1:7190");
        assert_eq!(ok.port(), 7190);
        assert_eq!(ok.base(), "http://127.0.0.1:7190/base");
        let local = LoopbackHttp::parse("http://localhost:7191").expect("localhost");
        assert_eq!(local.authority(), "localhost:7191");
        assert_eq!(local.base(), "http://localhost:7191");
        for bad in [
            "http://[::1]:7190",
            "http://127.0.0.2:7190",
            "http://user@evil.example:7190",
            "http://evil.example:7190",
            "https://127.0.0.1:7190",
            "http://127.0.0.1",
            "127.0.0.1:7190",
            "",
        ] {
            assert!(LoopbackHttp::parse(bad).is_err(), "{bad:?} accepted");
        }
        assert!(is_loopback_host("localhost") && is_loopback_host("127.0.0.1"));
        assert!(!is_loopback_host("[::1]") && !is_loopback_host("example.com"));
        let listener = bind_ephemeral().expect("bind");
        let url = LoopbackHttp::of_addr(listener.local_addr().unwrap());
        assert!(LoopbackHttp::parse(&url).is_ok(), "{url}");
    }

    #[tokio::test]
    async fn the_loopback_client_never_follows_a_redirect() {
        use std::io::{Read, Write};
        let server = bind_ephemeral().expect("bind");
        let base = LoopbackHttp::of_addr(server.local_addr().unwrap());
        std::thread::spawn(move || {
            let (mut stream, _) = server.accept().expect("accept");
            let _ = stream.read(&mut [0u8; 1024]);
            let _ = stream.write_all(
                b"HTTP/1.1 307 Temporary Redirect\r\nLocation: http://evil.example/events\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
            );
        });
        let resp = client()
            .unwrap()
            .post(format!("{base}/events"))
            .body("{}")
            .send()
            .await
            .expect("send");
        assert_eq!(resp.status().as_u16(), 307);
    }
}
