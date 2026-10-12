use std::collections::HashSet;
use std::sync::OnceLock;
use std::time::Duration;

use crate::internal_ext::sidecar_model::{
    resolve_component_specs, ComponentsResponse, GatewayAccess, SidecarSpec,
};
use crate::internal_ext::sidecar_util::current_platform;
use crate::internal_ext::Adapter;

// Every adapter HTTP call (gateway API, local sidecar API, artifact download)
// runs on one shared client with hard bounds, so a peer that accepts the
// connection but never responds surfaces an error instead of parking a
// reconcile forever. There is NO total-request timeout on the client: a large
// artifact over a slow link must not be cut off mid-transfer. Instead a
// connect timeout plus a per-read idle timeout catch dead and stalled peers,
// and small API calls add their own total bound (API_TIMEOUT).
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const READ_IDLE_TIMEOUT: Duration = Duration::from_secs(30);
/// Total bound for small JSON API calls (catalog, local targets/login).
pub(super) const API_TIMEOUT: Duration = Duration::from_secs(30);

/// Why a sidecar binary could not be made runnable.
#[derive(Debug)]
pub(crate) enum FetchError {
    /// Transient (network, disk, status code): retry with backoff.
    Retry(String),
    /// The download completed but its sha256 is not the pinned one.
    Mismatch(String),
    /// Never retried for this spec (e.g. artifact URL off the gateway origin).
    Refused(String),
}

impl Adapter {
    pub(super) fn resolve_specs(
        &self,
        components: &ComponentsResponse,
    ) -> (Vec<SidecarSpec>, HashSet<String>) {
        let platform = current_platform();
        // Previewer bundles follow the same catalog: drop uninstalled ones.
        self.retain_previewers(components);
        // So do MCP tools: materialize stdio binaries, drop removed ones.
        self.sync_mcp_tools(components);
        let resolved = resolve_component_specs(components, &platform);
        for failure in &resolved.failures {
            self.set_initial_status(
                failure.kind,
                &failure.name,
                "waiting",
                format!("Waiting for {} artifact: {}", failure.name, failure.message),
            );
        }
        for skipped in &resolved.inactive {
            eprintln!("[sidecar-manager:{}] {}", skipped.name, skipped.message);
            self.set_initial_status(
                skipped.kind,
                &skipped.name,
                "inactive",
                skipped.message.clone(),
            );
        }
        let mut specs = Vec::new();
        for spec in resolved.specs {
            let spec_id = spec.id();
            // A supervised sidecar's status is its supervisor's: this
            // background refresh must not clobber it with the
            // "resolving/Checking…" placeholder (the card froze on "Checking
            // samplecore artifact" with the daemon healthy). A refused spec
            // keeps its "failed" status for the same reason. Manifest cores
            // have no supervisor, so their placeholder is harmless.
            let keep_status = {
                let inner = self.inner.lock();
                inner.sidecars.get(&spec_id).is_some_and(|sidecar| {
                    sidecar.spec.as_ref() == Some(&spec)
                        && (sidecar.supervisor_active || sidecar.refused.is_some())
                })
            };
            if !keep_status {
                self.set_initial_status(
                    spec.kind,
                    &spec.name,
                    "resolving",
                    format!("Checking {} for {platform}", spec.name),
                );
            }
            specs.push(spec);
        }
        (specs, resolved.discovered)
    }

    pub(super) async fn fetch_components(
        &self,
        gateway: &GatewayAccess,
    ) -> Result<ComponentsResponse, String> {
        let url = format!("{}/v1/components", gateway.base_url);
        let resp = self
            .http()?
            .get(&url)
            .timeout(API_TIMEOUT)
            .bearer_auth(&gateway.token)
            .send()
            .await
            .map_err(|e| format!("list components: {e}"))?;
        if !resp.status().is_success() {
            return Err(format!("list components: status {}", resp.status()));
        }
        let body = resp
            .bytes()
            .await
            .map_err(|e| format!("list components read: {e}"))?;
        serde_json::from_slice::<ComponentsResponse>(&body)
            .map_err(|e| format!("list components decode: {e}"))
    }

    pub(super) fn http(&self) -> Result<reqwest::Client, String> {
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
}
