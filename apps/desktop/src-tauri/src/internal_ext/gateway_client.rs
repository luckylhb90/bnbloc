//! The member's gateway session and the one reading of a gateway answer.
//!
//! Everything the desktop asks the gateway as the member (the component
//! catalog, artifact downloads, the member stream and its acks, a plugin's
//! chat through the host API) goes through `GatewayAccess::send` on a
//! session taken from the `GatewayClient`, and every answer is read once by
//! `check`:
//!
//! - 2xx: the response;
//! - no session (signed out) or 401: `SessionExpired`. The member signs in
//!   again and the web hands the desktop a fresh session; nothing is
//!   "refused" or retried as if the artifact were at fault;
//! - 429 and 5xx, or no answer: `Failed`, retried;
//! - any other 4xx: `Refused` (403 not entitled, 404 gone, 400/409/413 a
//!   request the gateway will not take).
//!
//! The session the web hands over is the only one: the desktop starts with
//! none (never the bridge's static gateway token) and clears it at sign-out.
use std::sync::{Arc, OnceLock};
use std::time::Duration;

use parking_lot::Mutex;

use crate::internal_ext::sidecar_model::GatewayAccess;

// One shared client with hard bounds, so a peer that accepts the connection
// but never answers surfaces an error instead of parking a reconcile
// forever. There is NO total-request timeout on the client: a large artifact
// over a slow link must not be cut off mid-transfer. A connect timeout plus a
// per-read idle timeout catch dead and stalled peers; small API calls add
// their own total bound (API_TIMEOUT).
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const READ_IDLE_TIMEOUT: Duration = Duration::from_secs(30);
/// Total bound for small JSON API calls (catalog, acks).
pub(super) const API_TIMEOUT: Duration = Duration::from_secs(30);
/// Bound on the error text kept from a refusal.
const MAX_REASON_BYTES: usize = 512;

/// Why a gateway call did not succeed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum GatewayError {
    /// No member session, or the gateway no longer takes it (401).
    SessionExpired,
    /// The gateway refused the request (a 4xx other than 401 and 429).
    Refused { status: u16, reason: String },
    /// No answer, a 429 or a 5xx: worth retrying.
    Failed { status: Option<u16>, reason: String },
}

impl std::fmt::Display for GatewayError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GatewayError::SessionExpired => {
                f.write_str("the member session expired; sign in again")
            }
            GatewayError::Refused { status, reason } if reason.is_empty() => {
                write!(f, "refused by the gateway (HTTP {status})")
            }
            GatewayError::Refused { status, reason } => {
                write!(f, "refused by the gateway (HTTP {status}): {reason}")
            }
            GatewayError::Failed {
                status: Some(status),
                ..
            } => write!(f, "the gateway answered HTTP {status}"),
            GatewayError::Failed { reason, .. } => f.write_str(reason),
        }
    }
}

/// Read a gateway answer (the mapping in the module docs).
pub(crate) async fn check(resp: reqwest::Response) -> Result<reqwest::Response, GatewayError> {
    let status = resp.status().as_u16();
    match status {
        200..=299 => Ok(resp),
        401 => Err(GatewayError::SessionExpired),
        429 | 500.. => Err(GatewayError::Failed {
            status: Some(status),
            reason: String::new(),
        }),
        _ => {
            let mut reason = resp.text().await.unwrap_or_default().trim().to_string();
            if reason.len() > MAX_REASON_BYTES {
                let mut cut = MAX_REASON_BYTES;
                while !reason.is_char_boundary(cut) {
                    cut -= 1;
                }
                reason.truncate(cut);
            }
            Err(GatewayError::Refused { status, reason })
        }
    }
}

/// The shared gateway client (bounded connect and read idle, see above).
pub(crate) fn client() -> Result<reqwest::Client, String> {
    static CLIENT: OnceLock<Result<reqwest::Client, String>> = OnceLock::new();
    CLIENT
        .get_or_init(|| {
            reqwest::Client::builder()
                .connect_timeout(CONNECT_TIMEOUT)
                .read_timeout(READ_IDLE_TIMEOUT)
                .build()
                .map_err(|e| format!("build http client: {e}"))
        })
        .clone()
}

impl GatewayAccess {
    /// Send `method url` as this member (`url` absolute, or a path on this
    /// gateway), after `build` adds what the call needs (a body, a
    /// timeout), and read the answer with `check`.
    pub(crate) async fn send(
        &self,
        method: reqwest::Method,
        url: &str,
        build: impl FnOnce(reqwest::RequestBuilder) -> reqwest::RequestBuilder,
    ) -> Result<reqwest::Response, GatewayError> {
        if self.base_url.is_empty() || self.token.is_empty() {
            return Err(GatewayError::SessionExpired);
        }
        let url = if url.starts_with('/') {
            format!("{}{url}", self.base_url)
        } else {
            url.to_string()
        };
        let http = client().map_err(|reason| GatewayError::Failed {
            status: None,
            reason,
        })?;
        let resp = build(http.request(method, url).bearer_auth(&self.token))
            .send()
            .await
            .map_err(|e| GatewayError::Failed {
                status: None,
                reason: format!("gateway unreachable: {}", e.without_url()),
            })?;
        check(resp).await
    }
}

/// The member session the desktop acts with, shared by the sidecar manager
/// and the plugin host API.
#[derive(Clone, Default)]
pub(crate) struct GatewayClient {
    access: Arc<Mutex<GatewayAccess>>,
}

impl GatewayClient {
    pub(crate) fn new(base_url: &str) -> Self {
        let client = Self::default();
        client.set(base_url, String::new());
        client
    }

    /// The current session (its token is empty while signed out).
    pub(crate) fn session(&self) -> GatewayAccess {
        self.access.lock().clone()
    }

    /// The gateway origin (no trailing slash; "" before one is known).
    pub(crate) fn base_url(&self) -> String {
        self.access.lock().base_url.clone()
    }

    /// Take over the web's session; an empty token is a sign-out.
    pub(crate) fn set(&self, base_url: &str, token: String) {
        let mut access = self.access.lock();
        access.base_url = crate::settings::normalize_gateway_url(base_url);
        access.token = token;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};

    fn answer(status: &'static str, body: &'static str) -> String {
        let server = crate::internal_ext::loopback::bind_ephemeral().expect("bind");
        let base =
            crate::internal_ext::loopback::LoopbackHttp::of_addr(server.local_addr().unwrap());
        std::thread::spawn(move || {
            let (mut stream, _) = server.accept().expect("accept");
            let _ = stream.read(&mut [0u8; 2048]);
            let _ = write!(
                stream,
                "HTTP/1.1 {status}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
        });
        base
    }

    async fn get(status: &'static str, body: &'static str) -> Result<u16, GatewayError> {
        let session = GatewayAccess {
            base_url: answer(status, body),
            token: "member".into(),
        };
        session
            .send(reqwest::Method::GET, "/v1/components", |r| r)
            .await
            .map(|resp| resp.status().as_u16())
    }

    /// Every call reads the gateway's answer the same way: an expired
    /// session is never a refusal or a retry-forever, a refusal is never
    /// retried.
    #[tokio::test]
    async fn gateway_answers_are_read_once() {
        assert_eq!(get("200 OK", "{}").await, Ok(200));
        assert_eq!(
            get("401 Unauthorized", "").await,
            Err(GatewayError::SessionExpired)
        );
        assert_eq!(
            get("403 Forbidden", "not entitled").await,
            Err(GatewayError::Refused {
                status: 403,
                reason: "not entitled".into()
            })
        );
        assert!(matches!(
            get("404 Not Found", "").await,
            Err(GatewayError::Refused { status: 404, .. })
        ));
        assert!(matches!(
            get("429 Too Many Requests", "").await,
            Err(GatewayError::Failed {
                status: Some(429),
                ..
            })
        ));
        assert!(matches!(
            get("503 Service Unavailable", "").await,
            Err(GatewayError::Failed {
                status: Some(503),
                ..
            })
        ));
        let signed_out = GatewayAccess {
            base_url: "http://127.0.0.1:9".into(),
            token: String::new(),
        };
        assert_eq!(
            signed_out
                .send(reqwest::Method::GET, "/v1/components", |r| r)
                .await
                .map(|_| ()),
            Err(GatewayError::SessionExpired)
        );
    }

    #[test]
    fn a_sign_out_clears_the_session_but_keeps_the_gateway() {
        let client = GatewayClient::new("https://gw.example.com/");
        assert_eq!(client.base_url(), "https://gw.example.com");
        assert!(
            client.session().token.is_empty(),
            "no session until handed one"
        );
        client.set("https://gw.example.com", "t".into());
        assert_eq!(client.session().token, "t");
        client.set("https://gw.example.com", String::new());
        assert!(client.session().token.is_empty());
        assert_eq!(client.base_url(), "https://gw.example.com");
    }
}
