//! Resolving and caching installed previewer bundles for the
//! `bnbloc-previewer` scheme (src/previewer_scheme.rs).
//!
//! A bundle is served only when the member catalog (`GET /v1/components`, the
//! same rollout + tenant gated catalog the sidecar manager follows) lists that
//! exact previewer name + version as installed. Its archive is downloaded
//! with the member's session from the gateway origin only (the sidecar
//! downloader's rule), refused unless its sha256 is the catalog pin, kept on
//! disk under `<app cache>/previewers/<sha256>.tar`, and parsed into memory.
//!
//! Caches: name+version → pin for RESOLVE_TTL (so opening a previewer does not
//! re-list the catalog per file request), and parsed bundles by sha256
//! (bounded LRU). Every sidecar reconcile drops entries whose previewer left
//! the catalog, so an uninstall or rollout change stops serving within one
//! poll. A component's settings page bundle is the same format, named by
//! its pin instead (settings_page_bundle) and kept in the same caches.

use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::Mutex;
use tauri::Manager;

use crate::internal_ext::pin::Sha256Pin;
use crate::internal_ext::previewer_bundle::{parse_ustar, Bundle, MAX_BUNDLE_BYTES};
use crate::internal_ext::sidecar_cache::fetch_verified;
use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::{ComponentKind, ComponentsResponse};
use crate::internal_ext::Adapter;

/// How long a name+version → bundle pin resolution is reused.
const RESOLVE_TTL: Duration = Duration::from_secs(60);
/// Parsed bundles kept in memory.
const MAX_CACHED_BUNDLES: usize = 16;

/// Why a previewer file could not be served.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum PreviewerError {
    /// Not an installed previewer at that version for this member (404).
    NotFound,
    /// No gateway session / catalog unreachable (503, retry later).
    Unavailable(String),
    /// The bundle artifact was refused (sha256 mismatch, off-origin URL,
    /// malformed archive) — never served (502).
    BadArtifact(String),
}

/// One installed previewer's bundle pin from the member catalog.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct BundlePin {
    pub(crate) url: String,
    pub(crate) sha256: Sha256Pin,
}

/// Managed state: the resolution and parsed-bundle caches.
#[derive(Default)]
pub(crate) struct PreviewerBundles {
    inner: Mutex<CacheInner>,
    /// One fetch per bundle at a time: frames opening the same uncached
    /// bundle together wait for the first one's download.
    fetching: Mutex<HashMap<Sha256Pin, Arc<tokio::sync::Mutex<()>>>>,
}

#[derive(Default)]
struct CacheInner {
    resolved: HashMap<(String, String), (BundlePin, Instant)>,
    bundles: HashMap<Sha256Pin, Arc<Bundle>>,
    /// Bundle pins, least recently used first.
    order: VecDeque<Sha256Pin>,
}

impl PreviewerBundles {
    fn cached_pin(&self, name: &str, version: &str) -> Option<BundlePin> {
        let inner = self.inner.lock();
        inner
            .resolved
            .get(&(name.to_string(), version.to_string()))
            .filter(|(_, at)| at.elapsed() < RESOLVE_TTL)
            .map(|(pin, _)| pin.clone())
    }

    fn remember_pin(&self, name: &str, version: &str, pin: BundlePin) {
        self.inner.lock().resolved.insert(
            (name.to_string(), version.to_string()),
            (pin, Instant::now()),
        );
    }

    fn cached_bundle(&self, sha256: &Sha256Pin) -> Option<Arc<Bundle>> {
        let mut inner = self.inner.lock();
        let bundle = inner.bundles.get(sha256).cloned()?;
        inner.order.retain(|s| s != sha256);
        inner.order.push_back(sha256.clone());
        Some(bundle)
    }

    fn remember_bundle(&self, sha256: &Sha256Pin, bundle: Arc<Bundle>) {
        let mut inner = self.inner.lock();
        inner.order.retain(|s| s != sha256);
        inner.order.push_back(sha256.clone());
        inner.bundles.insert(sha256.clone(), bundle);
        while inner.order.len() > MAX_CACHED_BUNDLES {
            if let Some(evicted) = inner.order.pop_front() {
                inner.bundles.remove(&evicted);
            }
        }
    }

    /// Drops every cached resolution and bundle not pinned by `live`
    /// (the previewers the current catalog lists as installed). Returns the
    /// pins still live, for the disk cache sweep.
    fn retain(&self, live: &HashMap<(String, String), BundlePin>) -> HashSet<Sha256Pin> {
        let keep: HashSet<Sha256Pin> = live.values().map(|pin| pin.sha256.clone()).collect();
        let mut inner = self.inner.lock();
        inner
            .resolved
            .retain(|key, (pin, _)| live.get(key) == Some(pin));
        inner.bundles.retain(|sha, _| keep.contains(sha));
        inner.order.retain(|sha| keep.contains(sha));
        self.fetching.lock().retain(|sha, _| keep.contains(sha));
        keep
    }
}

/// The installed previewers of a member catalog and their bundle pins.
pub(super) fn installed_previewers(
    catalog: &ComponentsResponse,
) -> HashMap<(String, String), BundlePin> {
    catalog
        .components
        .iter()
        .filter(|c| c.kind == ComponentKind::Previewer && c.installed && c.rollout != "off")
        .filter_map(|c| {
            let p = c.previewer.as_ref().filter(|p| !p.bundle_url.is_empty())?;
            Some((
                (c.name.clone(), c.version.clone()),
                BundlePin {
                    url: p.bundle_url.clone(),
                    sha256: p.bundle_sha256.clone()?,
                },
            ))
        })
        .collect()
}

impl Adapter {
    /// The verified bundle of installed previewer `name` at `version`.
    pub(crate) async fn previewer_bundle(
        &self,
        name: &str,
        version: &str,
    ) -> Result<Arc<Bundle>, PreviewerError> {
        let state = self
            .app
            .try_state::<PreviewerBundles>()
            .ok_or_else(|| PreviewerError::Unavailable("previewer cache not ready".into()))?;
        let pin = match state.cached_pin(name, version) {
            Some(pin) => pin,
            None => {
                let gateway = self.gateway.session();
                let catalog = self
                    .fetch_components(&gateway)
                    .await
                    .map_err(PreviewerError::Unavailable)?;
                let installed = installed_previewers(&catalog);
                state.retain(&installed);
                let pin = installed
                    .get(&(name.to_string(), version.to_string()))
                    .cloned()
                    .ok_or(PreviewerError::NotFound)?;
                state.remember_pin(name, version, pin.clone());
                pin
            }
        };
        self.verified_bundle(&state, &pin).await
    }

    /// The verified bundle of a component's settings page, by the pin of
    /// the settings view or catalog naming it (src/previewer_scheme.rs):
    /// downloaded from the gateway's artifact route with the member's
    /// session, which decides whether they may read it. Kept like a
    /// previewer's bundle; a reconcile drops it from memory and disk (the
    /// next frame downloads it again).
    pub(crate) async fn settings_page_bundle(
        &self,
        sha256: &Sha256Pin,
    ) -> Result<Arc<Bundle>, PreviewerError> {
        let state = self
            .app
            .try_state::<PreviewerBundles>()
            .ok_or_else(|| PreviewerError::Unavailable("previewer cache not ready".into()))?;
        let pin = BundlePin {
            // The gateway's content-addressed artifact path of the pin
            // (pkgmeta.GatewayArtifactPath), on the signed-in gateway.
            url: format!("/v1/artifacts/sha256/{sha256}"),
            sha256: sha256.clone(),
        };
        self.verified_bundle(&state, &pin).await
    }

    /// The parsed bundle `pin` names: from memory, else its verified
    /// archive (one fetch per bundle at a time).
    async fn verified_bundle(
        &self,
        state: &PreviewerBundles,
        pin: &BundlePin,
    ) -> Result<Arc<Bundle>, PreviewerError> {
        if let Some(bundle) = state.cached_bundle(&pin.sha256) {
            return Ok(bundle);
        }
        let fetching = Arc::clone(state.fetching.lock().entry(pin.sha256.clone()).or_default());
        let _one = fetching.lock().await;
        if let Some(bundle) = state.cached_bundle(&pin.sha256) {
            return Ok(bundle);
        }
        let raw = self.bundle_bytes(pin).await?;
        let bundle = Arc::new(parse_ustar(&raw).map_err(PreviewerError::BadArtifact)?);
        state.remember_bundle(&pin.sha256, Arc::clone(&bundle));
        Ok(bundle)
    }

    /// The archive bytes for `pin`: the on-disk copy when it still hashes to
    /// the pin, else a fresh verified download.
    async fn bundle_bytes(&self, pin: &BundlePin) -> Result<Vec<u8>, PreviewerError> {
        let path = self
            .dirs()
            .map_err(PreviewerError::Unavailable)?
            .previewers()
            .join(format!("{}.tar", pin.sha256));
        if let Some(raw) = read_verified(&path, &pin.sha256).await {
            return Ok(raw);
        }
        let gateway = self.gateway.session();
        fetch_verified(&gateway, &pin.url, &pin.sha256, &path, false, |_| {})
            .await
            .map_err(|e| match e {
                FetchError::Retry(msg) => PreviewerError::Unavailable(msg),
                FetchError::Mismatch(msg) | FetchError::Refused(msg) => {
                    PreviewerError::BadArtifact(msg)
                }
            })?;
        // fetch_verified checked these bytes against the pin as it wrote
        // them: no second hash.
        tokio::fs::read(&path)
            .await
            .map_err(|e| PreviewerError::Unavailable(format!("read the downloaded bundle: {e}")))
    }

    /// Reconcile sync (kind_runtime::Previewer): stop serving previewers no
    /// longer installed for this member. Their archives go with the sweep.
    pub(super) fn retain_previewers(&self, catalog: &ComponentsResponse) {
        if let Some(state) = self.app.try_state::<PreviewerBundles>() {
            state.retain(&installed_previewers(catalog));
        }
    }
}

/// Delete the cached archives in `dir` (`<sha>.tar`, and in-flight
/// downloads of one) whose pin is not in `keep`. Blocking.
pub(super) fn forget_bundles_except(dir: &std::path::Path, keep: &HashSet<Sha256Pin>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().into_owned();
        let live = crate::files::owner_name(&name)
            .strip_suffix(".tar")
            .and_then(|sha| Sha256Pin::parse(sha).ok())
            .is_some_and(|sha| keep.contains(&sha));
        if !live {
            crate::internal_ext::sidecar_sweep::remove(&entry.path());
        }
    }
}

/// Reads a cached archive and returns it only when it is within the size cap
/// and hashes to `sha256` (the bytes served are the bytes verified).
async fn read_verified(path: &std::path::Path, sha256: &Sha256Pin) -> Option<Vec<u8>> {
    let meta = tokio::fs::metadata(path).await.ok()?;
    // The archive adds headers and padding to the files' bytes; anything
    // far over the file cap cannot parse within it.
    if meta.len() > (MAX_BUNDLE_BYTES as u64) * 2 {
        return None;
    }
    let raw = tokio::fs::read(path).await.ok()?;
    sha256.check(&raw).ok().map(|()| raw)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn catalog(raw: &str) -> ComponentsResponse {
        serde_json::from_str(raw).expect("catalog")
    }

    const SHA: &str = "ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789";

    #[test]
    fn only_installed_previewers_with_a_pin_are_served() {
        let c = catalog(&format!(
            r#"{{"components":[
            {{"kind":"previewer","name":"a","version":"1.0.0","runtime":"bundle","rollout":"auto","installed":true,
              "previewer":{{"bundleUrl":"http://gw/v1/artifacts/sha256/x","bundleSha256":"{SHA}"}}}},
            {{"kind":"previewer","name":"b","version":"1.0.0","runtime":"bundle","rollout":"optin","installed":false}},
            {{"kind":"previewer","name":"c","version":"1.0.0","runtime":"bundle","rollout":"auto","installed":true,
              "previewer":{{"bundleUrl":"","bundleSha256":""}}}},
            {{"kind":"plugin","name":"a","version":"1.0.0","runtime":"sidecar","rollout":"auto","installed":true}}
        ]}}"#
        ));
        let got = installed_previewers(&c);
        assert_eq!(got.len(), 1);
        let pin = &got[&("a".to_string(), "1.0.0".to_string())];
        assert_eq!(
            pin.sha256.as_str(),
            SHA.to_ascii_lowercase(),
            "pins are compared lowercase"
        );
        assert_eq!(pin.url, "http://gw/v1/artifacts/sha256/x");
    }

    #[test]
    fn retain_drops_uninstalled_previewers_and_their_bundles() {
        let state = PreviewerBundles::default();
        let (aa, bb) = (Sha256Pin::of(b"a"), Sha256Pin::of(b"b"));
        let pin = BundlePin {
            url: "u".into(),
            sha256: aa.clone(),
        };
        state.remember_pin("a", "1.0.0", pin.clone());
        state.remember_pin(
            "b",
            "1.0.0",
            BundlePin {
                url: "u".into(),
                sha256: bb.clone(),
            },
        );
        state.remember_bundle(&aa, Arc::new(Bundle::default()));
        state.remember_bundle(&bb, Arc::new(Bundle::default()));

        let live = HashMap::from([(("a".to_string(), "1.0.0".to_string()), pin.clone())]);
        let keep = state.retain(&live);

        assert_eq!(keep, HashSet::from([aa.clone()]));
        assert_eq!(state.cached_pin("a", "1.0.0"), Some(pin));
        assert_eq!(state.cached_pin("b", "1.0.0"), None);
        assert!(state.cached_bundle(&aa).is_some());
        assert!(state.cached_bundle(&bb).is_none());
    }

    #[test]
    fn the_bundle_cache_is_bounded() {
        let state = PreviewerBundles::default();
        let pin = |i: usize| Sha256Pin::parse(&format!("{i:064}")).unwrap();
        for i in 0..MAX_CACHED_BUNDLES + 3 {
            state.remember_bundle(&pin(i), Arc::new(Bundle::default()));
        }
        assert!(state.cached_bundle(&pin(0)).is_none(), "oldest evicted");
        assert!(state.cached_bundle(&pin(MAX_CACHED_BUNDLES + 2)).is_some());
        assert_eq!(state.inner.lock().bundles.len(), MAX_CACHED_BUNDLES);
    }
}
