use std::collections::HashSet;

use crate::internal_ext::gateway_client::{GatewayError, API_TIMEOUT};
use crate::internal_ext::sidecar_model::{
    resolve_component_specs, ComponentsResponse, GatewayAccess, SidecarSpec,
};
use crate::internal_ext::sidecar_util::current_platform;
use crate::internal_ext::Adapter;

/// Why an artifact could not be made usable. Every kind shows it the same
/// way: `Retry` waits (and retries), `Mismatch` and `Refused` fail.
#[derive(Debug)]
pub(crate) enum FetchError {
    /// Transient (network, disk, a 429/5xx, an expired session): retry
    /// with backoff.
    Retry(String),
    /// The download completed but its sha256 is not the pinned one.
    Mismatch(String),
    /// Never retried for this spec (an artifact URL off the gateway origin,
    /// a gateway refusal, an artifact over the size cap).
    Refused(String),
}

impl From<GatewayError> for FetchError {
    fn from(e: GatewayError) -> Self {
        match e {
            GatewayError::Refused { .. } => FetchError::Refused(format!("download: {e}")),
            GatewayError::SessionExpired | GatewayError::Failed { .. } => {
                FetchError::Retry(format!("download: {e}"))
            }
        }
    }
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
        let resp = gateway
            .send(reqwest::Method::GET, "/v1/components", |r| {
                r.timeout(API_TIMEOUT)
            })
            .await
            .map_err(|e| format!("list components: {e}"))?;
        let body = resp
            .bytes()
            .await
            .map_err(|e| format!("list components read: {e}"))?;
        serde_json::from_slice::<ComponentsResponse>(&body)
            .map_err(|e| format!("list components decode: {e}"))
    }
}
