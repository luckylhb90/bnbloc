//! The slice of Sandbox Protocol v1 (docs/architecture/sandbox-protocol.md)
//! the desktop speaks to its local sandbox sidecar: discover, image prepare,
//! and the lifecycle of the ONE machine it owns. Everything else (spaces,
//! exec, exec tokens) is the bridge's.
use std::time::Duration;

use serde_json::{json, Value};

/// The contract a sandbox implementation's discovery echoes.
pub(super) const CONTRACT: &str = "bnbloc.sandbox.contract/v1";
/// The Sandbox Protocol version the desktop speaks (a protocol constant).
const PROTOCOL_VERSION: (u32, u32) = (1, 0);
/// Capabilities the desktop's shared-machine mode needs: commands run in
/// per-room spaces whose workspaces come from the shared workspaces root.
pub(super) const REQUIRED_CAPABILITIES: [&str; 3] = ["exec", "spaces", "shares"];
/// Advertised when the implementation offers the `public` egress mode.
pub(super) const CAPABILITY_EGRESS: &str = "egressPolicy";
/// Advertised when `public` egress may carry rules: hosts reachable beside
/// the public internet, private ones included.
pub(super) const CAPABILITY_EGRESS_HOSTS: &str = "egressHosts";

const DISCOVER_TIMEOUT: Duration = Duration::from_secs(2);
const LIFECYCLE_TIMEOUT: Duration = Duration::from_secs(120);
/// An image pull or a cold create may fetch hundreds of MB from a registry.
const PREPARE_TIMEOUT: Duration = Duration::from_secs(30 * 60);

use crate::secret::Secret;

/// What the desktop keeps from discovery.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(super) struct Discovery {
    pub(super) name: String,
    pub(super) version: String,
    pub(super) isolation: String,
    pub(super) capabilities: Vec<String>,
}

impl Discovery {
    pub(super) fn has(&self, capability: &str) -> bool {
        self.capabilities.iter().any(|c| c == capability)
    }
}

/// Validate a discovery document: the sandbox contract, a protocol range
/// containing the desktop's version, and the capabilities shared-machine mode
/// needs.
pub(super) fn parse_discovery(raw: &[u8]) -> Result<Discovery, String> {
    let value: Value = serde_json::from_slice(raw).map_err(|e| format!("decode discovery: {e}"))?;
    let text = |pointer: &str| {
        value
            .pointer(pointer)
            .and_then(Value::as_str)
            .unwrap_or_default()
    };
    if text("/contract") != CONTRACT {
        return Err(format!(
            "the sandbox speaks contract {:?}, not {CONTRACT}",
            text("/contract")
        ));
    }
    if !crate::internal_ext::sidecar_util::protocol_range_contains(
        Some(text("/protocol/min")),
        Some(text("/protocol/max")),
        PROTOCOL_VERSION,
    ) {
        return Err(format!(
            "the sandbox speaks protocol {}–{}, not {}.{}",
            text("/protocol/min"),
            text("/protocol/max"),
            PROTOCOL_VERSION.0,
            PROTOCOL_VERSION.1
        ));
    }
    let capabilities: Vec<String> = value
        .get("capabilities")
        .and_then(Value::as_array)
        .map(|caps| {
            caps.iter()
                .filter_map(Value::as_str)
                .map(str::to_string)
                .collect()
        })
        .unwrap_or_default();
    let discovery = Discovery {
        name: text("/name").to_string(),
        version: text("/version").to_string(),
        isolation: text("/backend/isolation").to_string(),
        capabilities,
    };
    let missing: Vec<&str> = REQUIRED_CAPABILITIES
        .iter()
        .copied()
        .filter(|c| !discovery.has(c))
        .collect();
    if !missing.is_empty() {
        return Err(format!(
            "the sandbox runtime lacks required capabilities: {}",
            missing.join(", ")
        ));
    }
    Ok(discovery)
}

/// The machine as the status surface shows it.
#[derive(Clone, Debug, Default, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MachineView {
    pub name: String,
    pub state: String,
    pub image: String,
    /// Server-measured create (boot) time.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub boot_ms: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_op: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_op_ms: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    /// The hosts its egress opens beside the public internet.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub egress_hosts: Vec<String>,
}

pub(super) fn parse_machine(raw: &[u8]) -> Result<MachineView, String> {
    let value: Value = serde_json::from_slice(raw).map_err(|e| format!("decode machine: {e}"))?;
    let text = |pointer: &str| {
        value
            .pointer(pointer)
            .and_then(Value::as_str)
            .map(str::to_string)
    };
    let num = |pointer: &str| {
        value
            .pointer(pointer)
            .and_then(Value::as_u64)
            .filter(|n| *n > 0)
    };
    Ok(MachineView {
        name: text("/name").unwrap_or_default(),
        state: text("/state").unwrap_or_default(),
        image: text("/image").unwrap_or_default(),
        boot_ms: num("/timings/createMs"),
        last_op: text("/timings/lastOp").filter(|s| !s.is_empty()),
        last_op_ms: num("/timings/lastOpMs"),
        error: text("/error").filter(|s| !s.is_empty()),
        egress_hosts: value
            .pointer("/egress/rules")
            .and_then(Value::as_array)
            .map(|rules| {
                rules
                    .iter()
                    .filter_map(|r| r.get("host").and_then(Value::as_str))
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default(),
    })
}

/// A machine the desktop creates: the package's guest profile, the one
/// workspaces root shared at the identical guest path, public-internet
/// egress when offered (private ranges, host loopback and link-local stay
/// blocked, but for `hosts` when the runtime can open them), labelled with
/// the member.
pub(super) fn create_machine_request(
    name: &str,
    guest: &crate::internal_ext::sidecar_model::GuestProfile,
    workspaces_root: &std::path::Path,
    share: &str,
    discovery: &Discovery,
    owner: &str,
    hosts: &[String],
) -> Value {
    let root = workspaces_root.to_string_lossy();
    let mut req = json!({
        "name": name,
        "image": guest.image,
        "shares": [{"name": share, "hostPath": root, "guestPath": root}],
    });
    if guest.cpus > 0 {
        req["cpus"] = json!(guest.cpus);
    }
    if guest.memory_mib > 0 {
        req["memoryMiB"] = json!(guest.memory_mib);
    }
    if discovery.has(CAPABILITY_EGRESS) {
        req["egress"] = json!({"mode": "public"});
        if discovery.has(CAPABILITY_EGRESS_HOSTS) && !hosts.is_empty() {
            req["egress"]["rules"] = hosts.iter().map(|h| json!({"host": h})).collect();
        }
    }
    if !owner.trim().is_empty() {
        req["labels"] = json!({"owner": owner.trim()});
    }
    req
}

/// A protocol error: the envelope's code (empty for transport errors) and a
/// human message.
#[derive(Debug)]
pub(super) struct ApiError {
    pub(super) code: String,
    pub(super) message: String,
}

impl std::fmt::Display for ApiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.message)
    }
}

fn api_error(status: u16, body: &[u8]) -> ApiError {
    let value: Value = serde_json::from_slice(body).unwrap_or(Value::Null);
    let code = value
        .pointer("/error/code")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    let message = value
        .pointer("/error/message")
        .and_then(Value::as_str)
        .map(str::to_string)
        .unwrap_or_else(|| format!("status {status}"));
    ApiError {
        message: if code.is_empty() {
            message
        } else {
            format!("{code}: {message}")
        },
        code,
    }
}

/// One sidecar launch's API: its loopback URL and bearer.
#[derive(Clone)]
pub(super) struct SandboxApi {
    pub(super) url: String,
    pub(super) token: Secret,
}

impl SandboxApi {
    fn client() -> Result<reqwest::Client, ApiError> {
        // No read-idle bound: an image pull answers only when done. Every
        // request carries its own total timeout instead.
        crate::internal_ext::loopback::client().map_err(|message| ApiError {
            code: String::new(),
            message,
        })
    }

    async fn call(
        &self,
        method: reqwest::Method,
        path: &str,
        body: Option<Value>,
        timeout: Duration,
    ) -> Result<(u16, Vec<u8>), ApiError> {
        let url = format!("{}/sandbox/v1{path}", self.url.trim_end_matches('/'));
        let mut req = Self::client()?
            .request(method, url)
            .timeout(timeout)
            .bearer_auth(self.token.expose());
        if let Some(body) = body {
            req = req
                .header(reqwest::header::CONTENT_TYPE, "application/json")
                .body(body.to_string());
        }
        let transport = |e: reqwest::Error| ApiError {
            code: String::new(),
            // Without the URL: the error is surfaced in status lines.
            message: e.without_url().to_string(),
        };
        let resp = req.send().await.map_err(transport)?;
        let status = resp.status().as_u16();
        let body = resp.bytes().await.map_err(transport)?.to_vec();
        if !(200..300).contains(&status) {
            return Err(api_error(status, &body));
        }
        Ok((status, body))
    }

    pub(super) async fn discover(&self) -> Result<Discovery, ApiError> {
        let (_, body) = self
            .call(reqwest::Method::GET, "/discover", None, DISCOVER_TIMEOUT)
            .await?;
        parse_discovery(&body).map_err(|message| ApiError {
            code: "incompatible".into(),
            message,
        })
    }

    pub(super) async fn pull_image(&self, image: &str) -> Result<(), ApiError> {
        self.call(
            reqwest::Method::POST,
            "/images",
            Some(json!({"ref": image})),
            PREPARE_TIMEOUT,
        )
        .await
        .map(|_| ())
    }

    /// The machine by name; None when it does not exist.
    pub(super) async fn machine(&self, name: &str) -> Result<Option<MachineView>, ApiError> {
        match self
            .call(
                reqwest::Method::GET,
                &format!("/machines/{name}"),
                None,
                LIFECYCLE_TIMEOUT,
            )
            .await
        {
            Ok((_, body)) => parse_machine(&body).map(Some).map_err(decode_error),
            Err(e) if e.code == "machine_not_found" => Ok(None),
            Err(e) => Err(e),
        }
    }

    pub(super) async fn create_machine(&self, req: Value) -> Result<MachineView, ApiError> {
        let (_, body) = self
            .call(
                reqwest::Method::POST,
                "/machines",
                Some(req),
                PREPARE_TIMEOUT,
            )
            .await?;
        parse_machine(&body).map_err(decode_error)
    }

    pub(super) async fn lifecycle(
        &self,
        name: &str,
        action: &str,
    ) -> Result<MachineView, ApiError> {
        let (_, body) = self
            .call(
                reqwest::Method::POST,
                &format!("/machines/{name}/{action}"),
                None,
                LIFECYCLE_TIMEOUT,
            )
            .await?;
        parse_machine(&body).map_err(decode_error)
    }

    pub(super) async fn delete_machine(&self, name: &str) -> Result<(), ApiError> {
        match self
            .call(
                reqwest::Method::DELETE,
                &format!("/machines/{name}"),
                None,
                LIFECYCLE_TIMEOUT,
            )
            .await
        {
            Ok(_) => Ok(()),
            Err(e) if e.code == "machine_not_found" => Ok(()),
            Err(e) => Err(e),
        }
    }
}

fn decode_error(message: String) -> ApiError {
    ApiError {
        code: String::new(),
        message,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::sidecar_model::GuestProfile;

    const DISCOVERY: &str = r#"{"name":"vmrt","version":"0.1.0","contract":"bnbloc.sandbox.contract/v1",
        "protocol":{"min":"1.0","max":"1.0"},
        "capabilities":["exec","spaces","shares","egressPolicy"],
        "backend":{"name":"x","version":"y","isolation":"microvm"}}"#;

    #[test]
    fn discovery_requires_contract_protocol_and_shared_machine_capabilities() {
        let d = parse_discovery(DISCOVERY.as_bytes()).expect("valid discovery");
        assert_eq!(d.isolation, "microvm");
        assert!(d.has("egressPolicy"));

        let other_contract =
            DISCOVERY.replace("bnbloc.sandbox.contract/v1", "bnbloc.core.contract/v1");
        assert!(parse_discovery(other_contract.as_bytes())
            .unwrap_err()
            .contains("contract"));
        let major = DISCOVERY.replace(r#""min":"1.0","max":"1.0""#, r#""min":"2.0","max":"2.1""#);
        assert!(parse_discovery(major.as_bytes())
            .unwrap_err()
            .contains("protocol"));
        let no_spaces = DISCOVERY.replace(r#""spaces","#, "");
        assert!(parse_discovery(no_spaces.as_bytes())
            .unwrap_err()
            .contains("spaces"));
    }

    #[test]
    fn create_request_shares_only_the_workspaces_root_with_public_egress() {
        let d = parse_discovery(DISCOVERY.as_bytes()).unwrap();
        let guest = GuestProfile {
            image: "registry.example/toolbox:1".into(),
            cpus: 2,
            memory_mib: 2048,
        };
        let req = create_machine_request(
            "bnbloc",
            &guest,
            std::path::Path::new("/Users/me/bnbloc-workspaces"),
            "workspaces",
            &d,
            "me@example",
            &[],
        );
        assert_eq!(
            req,
            json!({
                "name": "bnbloc",
                "image": "registry.example/toolbox:1",
                "cpus": 2,
                "memoryMiB": 2048,
                "shares": [{"name": "workspaces", "hostPath": "/Users/me/bnbloc-workspaces", "guestPath": "/Users/me/bnbloc-workspaces"}],
                "egress": {"mode": "public"},
                "labels": {"owner": "me@example"},
            })
        );
        // Without the egress capability the protocol default (none) applies.
        let mut no_egress = d.clone();
        no_egress.capabilities.retain(|c| c != "egressPolicy");
        let req = create_machine_request(
            "bnbloc",
            &GuestProfile {
                image: "img:1".into(),
                cpus: 0,
                memory_mib: 0,
            },
            std::path::Path::new("/w"),
            "workspaces",
            &no_egress,
            "",
            &["npm.internal.example".to_string()],
        );
        assert!(
            req.get("egress").is_none() && req.get("cpus").is_none() && req.get("labels").is_none()
        );
    }

    /// The own machine's hosts go in as public egress rules, only when the
    /// runtime can open them.
    #[test]
    fn create_request_lists_private_hosts_only_with_egress_hosts() {
        let mut d = parse_discovery(DISCOVERY.as_bytes()).unwrap();
        let guest = GuestProfile {
            image: "img:1".into(),
            cpus: 0,
            memory_mib: 0,
        };
        let hosts = vec!["npm.internal.example".to_string(), "10.0.0.5".to_string()];
        let req = |d: &Discovery| {
            create_machine_request(
                "bnbloc-own",
                &guest,
                std::path::Path::new("/w"),
                "workspaces",
                d,
                "",
                &hosts,
            )
        };
        assert_eq!(req(&d)["egress"], json!({"mode": "public"}));
        d.capabilities.push(CAPABILITY_EGRESS_HOSTS.into());
        assert_eq!(
            req(&d)["egress"],
            json!({"mode": "public", "rules": [{"host": "npm.internal.example"}, {"host": "10.0.0.5"}]})
        );
    }

    #[test]
    fn machine_view_reads_state_and_boot_time() {
        let m = parse_machine(
            br#"{"id":"m1","name":"bnbloc","image":"img:1","state":"running","egress":{"mode":"public"},
                "source":{"kind":"image"},"spaces":0,"createdAtMs":1,
                "timings":{"createMs":512,"lastOp":"start","lastOpMs":190}}"#,
        )
        .unwrap();
        assert_eq!(m.state, "running");
        assert_eq!(m.boot_ms, Some(512));
        assert_eq!(m.last_op.as_deref(), Some("start"));
        assert_eq!(m.last_op_ms, Some(190));
        assert!(m.egress_hosts.is_empty());
        let own = parse_machine(
            br#"{"name":"bnbloc-own","state":"running","egress":{"mode":"public","rules":[{"host":"npm.internal.example"}]}}"#,
        )
        .unwrap();
        assert_eq!(own.egress_hosts, vec!["npm.internal.example".to_string()]);
    }

    #[test]
    fn api_errors_carry_the_envelope_code() {
        let e = api_error(
            404,
            br#"{"error":{"code":"machine_not_found","message":"no machine bnbloc"}}"#,
        );
        assert_eq!(e.code, "machine_not_found");
        assert_eq!(e.message, "machine_not_found: no machine bnbloc");
        let e = api_error(502, b"bad gateway");
        assert_eq!(e.code, "");
        assert_eq!(e.message, "status 502");
    }
}
