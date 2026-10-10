use reqwest::Method;
use serde_json::Value;

use crate::internal_ext::sidecar_fetch::API_TIMEOUT;
use crate::internal_ext::Adapter;

/// A sidecar's sign-in state from the OPTIONAL sidecar-contract endpoint
/// `GET {localUrl}/v1/auth/status` → `{"authenticated", "user", "checkedAtMs"}`.
/// A sidecar without it (404) has no sign-in concept: `supported` is false and
/// the UI never offers sign-in for it.
#[derive(Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SidecarAuthStatus {
    pub supported: bool,
    pub authenticated: bool,
    pub user: String,
    pub checked_at_ms: i64,
}

impl Adapter {
    pub async fn login(&self, name: &str) -> Result<Value, String> {
        let (_, status, body) = self
            .local_call(name, Method::POST, "/v1/auth/login", "authenticate")
            .await?;
        decode_ok(name, "authenticate", status, &body)
    }

    pub async fn auth_status(&self, name: &str) -> Result<SidecarAuthStatus, String> {
        let (_, status, body) = self
            .local_call(name, Method::GET, "/v1/auth/status", "auth status")
            .await?;
        parse_auth_status(name, status, &body)
    }

    pub async fn targets(&self, name: &str) -> Result<Value, String> {
        let (local_url, status, body) = self
            .local_call(name, Method::GET, "/v1/agent-targets", "list targets")
            .await?;
        let mut value = decode_ok(name, "list targets", status, &body)?;
        if let Some(targets) = value.get_mut("targets").and_then(Value::as_array_mut) {
            for target in targets {
                qualify_target(name, &local_url, target);
            }
        }
        Ok(value)
    }

    /// One bounded request to a running sidecar's local API: its runtime URL,
    /// the response status, and the body.
    async fn local_call(
        &self,
        name: &str,
        method: Method,
        path: &str,
        what: &str,
    ) -> Result<(String, u16, Vec<u8>), String> {
        let local_url = self
            .running_local_url(name)
            .ok_or_else(|| format!("managed sidecar {name} is not running"))?;
        let url = format!("{}{path}", local_url.trim_end_matches('/'));
        let response = self
            .http()?
            .request(method, &url)
            .timeout(API_TIMEOUT)
            .send()
            .await
            .map_err(|e| format!("{what} {name}: {e}"))?;
        let status = response.status().as_u16();
        let body = response
            .bytes()
            .await
            .map_err(|e| format!("{what} {name}: read response: {e}"))?;
        Ok((local_url, status, body.to_vec()))
    }

    fn running_local_url(&self, name: &str) -> Option<String> {
        self.inner.lock().sidecars.get(name).and_then(|sidecar| {
            if sidecar.status.state == "running" {
                sidecar.runtime_url.clone()
            } else {
                None
            }
        })
    }
}

fn decode_ok(name: &str, what: &str, status: u16, body: &[u8]) -> Result<Value, String> {
    if !(200..300).contains(&status) {
        return Err(format!("{what} {name}: status {status}"));
    }
    serde_json::from_slice(body).map_err(|e| format!("{what} {name}: decode response: {e}"))
}

fn parse_auth_status(name: &str, status: u16, body: &[u8]) -> Result<SidecarAuthStatus, String> {
    if status == 404 {
        return Ok(SidecarAuthStatus {
            supported: false,
            authenticated: false,
            user: String::new(),
            checked_at_ms: 0,
        });
    }
    let value = decode_ok(name, "auth status", status, body)?;
    let authenticated = value
        .get("authenticated")
        .and_then(Value::as_bool)
        .ok_or_else(|| format!("auth status {name}: response has no boolean \"authenticated\""))?;
    Ok(SidecarAuthStatus {
        supported: true,
        authenticated,
        user: value
            .get("user")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string(),
        checked_at_ms: value
            .get("checkedAtMs")
            .and_then(Value::as_i64)
            .unwrap_or_default(),
    })
}

fn qualify_target(core: &str, local_url: &str, target: &mut Value) {
    let Some(obj) = target.as_object_mut() else {
        return;
    };
    obj.insert("core".to_string(), Value::String(core.to_string()));
    obj.insert(
        "localAgentUrl".to_string(),
        Value::String(local_url.to_string()),
    );
    obj.insert("runViaGateway".to_string(), Value::Bool(false));
    if let Some(id) = obj.get("id").and_then(Value::as_str) {
        obj.insert("id".to_string(), Value::String(format!("{core}/{id}")));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Map;

    #[test]
    fn qualify_target_adds_core_and_qualified_id() {
        let mut target = Value::Object(Map::from_iter([
            ("id".to_string(), Value::String("sample-codex".to_string())),
            ("provider".to_string(), Value::String("codex".to_string())),
        ]));

        qualify_target("samplecore", "http://127.0.0.1:7100", &mut target);

        assert_eq!(target["core"], "samplecore");
        assert_eq!(target["id"], "samplecore/sample-codex");
        assert_eq!(target["localAgentUrl"], "http://127.0.0.1:7100");
        assert_eq!(target["runViaGateway"], false);
    }

    #[test]
    fn auth_status_without_the_endpoint_means_no_sign_in_concept() {
        let status = parse_auth_status("samplecore", 404, b"404 page not found").expect("404");
        assert!(!status.supported);
    }

    #[test]
    fn auth_status_parses_the_contract_shape() {
        let body = br#"{"authenticated":false,"user":"","checkedAtMs":1700000000000}"#;
        assert_eq!(
            parse_auth_status("samplecore", 200, body).expect("signed out"),
            SidecarAuthStatus {
                supported: true,
                authenticated: false,
                user: String::new(),
                checked_at_ms: 1_700_000_000_000,
            }
        );
        let body = br#"{"authenticated":true,"user":"alice"}"#;
        let status = parse_auth_status("samplecore", 200, body).expect("signed in");
        assert!(status.supported && status.authenticated);
        assert_eq!(status.user, "alice");
        assert_eq!(
            serde_json::to_value(&status).expect("encode")["checkedAtMs"],
            0
        );
    }

    #[test]
    fn auth_status_errors_are_surfaced_not_guessed() {
        assert!(parse_auth_status("samplecore", 500, b"{}").is_err());
        assert!(parse_auth_status("samplecore", 200, b"not json").is_err());
        assert!(parse_auth_status("samplecore", 200, br#"{"user":"alice"}"#).is_err());
    }
}
