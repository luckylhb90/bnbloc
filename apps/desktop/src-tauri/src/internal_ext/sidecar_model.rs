use std::collections::{HashMap, HashSet};

use crate::child_process::Child;
pub(super) use crate::internal_ext::component_settings::Setting;
use crate::internal_ext::pin::Sha256Pin;

pub(super) struct Inner {
    pub(super) desired_up: bool,
    pub(super) generation: u64,
    pub(super) sidecars: HashMap<String, ManagedSidecar>,
    // The core set the running bridge was last started with (or the last set
    // observed while it was down): binary core name → runtime URL, manifest
    // core name → manifest content fingerprint. Drives the bridge-stabilizing
    // watcher in sidecar_bridge_sync.
    pub(super) bridge_cores: HashMap<String, String>,
}

pub(super) struct ManagedSidecar {
    pub(super) spec: Option<SidecarSpec>,
    /// Where the sidecar answers while it runs (its spawned child, or a
    /// compatible listener reused in place); None otherwise. Read it through
    /// `endpoint`.
    pub(super) runtime_url: Option<String>,
    pub(super) child: Option<Child>,
    pub(super) generation: u64,
    pub(super) supervisor_active: bool,
    /// Set when this spec's artifact was refused (wrong origin, persistent
    /// checksum mismatch): no supervisor is started and no re-download is
    /// attempted until the catalog publishes a different spec.
    pub(super) refused: Option<String>,
    /// A newer spec that waits to replace `spec` until no agent turn runs
    /// (update_gate); None when nothing waits.
    pub(super) pending: Option<SidecarSpec>,
    pub(super) status: AdapterStatus,
}

impl ManagedSidecar {
    /// The one answer to "may this sidecar be used now": where it answers
    /// while it runs. The bridge env, the bridge watcher's fingerprint, the
    /// local API calls and plugin deliveries all read it.
    pub(super) fn endpoint(&self) -> Option<&str> {
        self.runtime_url.as_deref()
    }

    pub(super) fn pending(status: AdapterStatus) -> Self {
        Self {
            spec: None,
            runtime_url: None,
            child: None,
            generation: 0,
            supervisor_active: false,
            refused: None,
            pending: None,
            status,
        }
    }
}

#[derive(Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AdapterStatus {
    pub(super) kind: ComponentKind,
    pub name: String,
    pub state: String,
    pub message: String,
    pub pid: Option<u32>,
    pub version: Option<String>,
    pub local_url: Option<String>,
    /// A newer version that waits for the bridge's running turns to finish
    /// (update_gate); the sidecar keeps running meanwhile.
    pub pending_update: Option<PendingUpdate>,
}

/// An update kept until no agent turn runs.
#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PendingUpdate {
    pub version: String,
    /// The turns it waits for (when last checked).
    pub waiting_for: u32,
}

impl AdapterStatus {
    pub(super) fn new(
        kind: ComponentKind,
        name: &str,
        state: &str,
        message: String,
        pid: Option<u32>,
        version: Option<String>,
    ) -> Self {
        Self {
            kind,
            name: name.to_string(),
            state: state.into(),
            message,
            pid,
            version,
            local_url: None,
            pending_update: None,
        }
    }

    pub(super) fn ready(spec: &SidecarSpec) -> Self {
        let what = if spec.artifact.is_some() {
            "sidecar resolved"
        } else {
            "manifest installed"
        };
        Self::new(
            spec.kind,
            &spec.name,
            "ready",
            format!("{} {what}", spec.name),
            None,
            Some(spec.version.clone()),
        )
    }
}

/// One member session: the gateway origin and its bearer (empty while
/// signed out). Calls as the member go through `send` (gateway_client.rs).
#[derive(Clone, Default)]
pub(crate) struct GatewayAccess {
    pub(crate) base_url: String,
    pub(crate) token: String,
}

#[derive(Clone, PartialEq, Eq)]
pub(super) struct SidecarSpec {
    pub(super) kind: ComponentKind,
    pub(super) name: String,
    pub(super) version: String,
    /// This platform's binary; None for a manifest core: no binary to
    /// download or supervise, the manifest alone is the installable content.
    pub(super) artifact: Option<ComponentArtifact>,
    pub(super) manifest: Option<serde_json::Value>,
    pub(super) local_url: String,
    pub(super) listen_env: String,
    /// Package-declared env var set to "1" so the sidecar knows it is
    /// supervised and exits when its desktop-owned stdin closes.
    pub(super) supervised_env: String,
    pub(super) args: Vec<String>,
    /// Plugin launch metadata (empty for cores).
    pub(super) plugin: PluginLaunch,
    /// Sandbox launch metadata (empty for cores and plugins).
    pub(super) sandbox: SandboxLaunch,
}

/// What a sandbox sidecar (`bnbloc.sandbox.package/v1`) is launched with
/// beyond the common sidecar fields: a fresh bearer in `token_env`, a private
/// data dir in `data_dir_env`, the extra per-platform files laid out beside
/// the executable (each path handed over in its env), and the guest profile
/// the desktop prewarms.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(super) struct SandboxLaunch {
    pub(super) token_env: String,
    pub(super) data_dir_env: String,
    pub(super) files: Vec<SidecarFileSpec>,
    pub(super) guest: Option<GuestProfile>,
}

/// One declared sidecar file resolved for this platform
/// (`artifacts["<platform>/<name>"]`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct SidecarFileSpec {
    pub(super) name: String,
    pub(super) env: String,
    pub(super) executable: bool,
    pub(super) url: String,
    pub(super) sha256: Sha256Pin,
}

/// A sandbox package's machine profile (pkgmeta.Guest).
#[derive(Clone, Debug, PartialEq, Eq, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct GuestProfile {
    pub(super) image: String,
    #[serde(default)]
    pub(super) cpus: u32,
    #[serde(default, rename = "memoryMiB")]
    pub(super) memory_mib: u32,
}

/// What a plugin sidecar is launched with beyond the common sidecar fields:
/// the host API capabilities it may use (`permissions.host`: it then gets
/// `BNBLOC_PLUGIN_HOST_URL`/`_TOKEN`), its settings (the declaration, and
/// which platform and organization ones are set or missing for the member's
/// organization; component_settings.rs hands it the values in its settings
/// file and each `deliver.env`), and what its
/// capabilities earn it (skills_provider.rs): a bearer for the
/// `skills-provider` job, a data dir, and for `sandbox-mounts` the
/// descriptor file and mirror folder, each in the env var the package names.
/// A change to any of it restarts the plugin (the spec differs).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(super) struct PluginLaunch {
    pub(super) host: Vec<String>,
    pub(super) settings: Vec<Setting>,
    pub(super) settings_set: Vec<String>,
    pub(super) settings_missing: Vec<String>,
    pub(super) capabilities: Vec<String>,
    pub(super) token_env: String,
    pub(super) data_dir_env: String,
    pub(super) mounts_env: String,
    pub(super) mount_root_env: String,
}

impl PluginLaunch {
    pub(super) fn has(&self, capability: &str) -> bool {
        self.capabilities.iter().any(|c| c == capability)
    }
}

impl SidecarSpec {
    pub(super) fn id(&self) -> String {
        super::sidecar_status::component_id(self.kind, &self.name)
    }

    /// The pinned sha256 of this platform's binary (None: a manifest core).
    pub(super) fn pin(&self) -> Option<&Sha256Pin> {
        self.artifact.as_ref().map(|a| &a.sha256)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum ComponentKind {
    Core,
    Plugin,
    /// A Sandbox Protocol runtime (`bnbloc.sandbox.package/v1`): supervised
    /// like any sidecar plus its extra files, token and data dir; at most one
    /// is active (internal_ext/sandbox.rs).
    Sandbox,
    /// A previewer bundle (`bnbloc.previewer.package/v1`, runtime "bundle"):
    /// static web code, never a process. Not desktop-supervised; its bundle
    /// is fetched on demand by the `bnbloc-previewer` scheme
    /// (internal_ext/previewer_cache.rs).
    Previewer,
    /// An MCP tool (`bnbloc.mcp.package/v1`): never a supervised process. A
    /// stdio tool's binary for this platform is materialized into the tools
    /// dir the bridge reads and spawned by the agent per session; an http
    /// tool needs nothing here (internal_ext/mcp_tools.rs).
    Mcp,
    /// A kind added to the gateway after this desktop shipped. Such entries
    /// are dropped when the catalog is decoded (`ComponentsResponse`), so
    /// nothing downstream ever sees this variant.
    #[serde(other)]
    Unknown,
}

/// The member catalog (`GET /v1/components`). Entries are decoded one at a
/// time: an entry this desktop cannot read (a kind added after it shipped,
/// a malformed block) is skipped and logged, and the rest of the catalog
/// still reconciles. One bad entry never stops installs, stops, MCP or
/// previewer updates for every other component.
pub(super) struct ComponentsResponse {
    pub(super) components: Vec<ComponentEntry>,
}

impl<'de> serde::Deserialize<'de> for ComponentsResponse {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        #[derive(serde::Deserialize)]
        struct Raw {
            components: Vec<serde_json::Value>,
        }
        let raw = Raw::deserialize(deserializer)?;
        Ok(Self {
            components: raw
                .components
                .into_iter()
                .filter_map(decode_entry)
                .collect(),
        })
    }
}

/// A package name (`pkgmeta` packageNameRE, `^[a-z0-9][a-z0-9._-]*$`, at
/// most 128 bytes): the one rule for every name the desktop turns into a
/// path (a component's files, settings and data dirs, a sandbox bundle's
/// files) or a scheme route. Catalog entries are checked when decoded.
pub(crate) fn valid_package_name(name: &str) -> bool {
    let mut bytes = name.bytes();
    matches!(bytes.next(), Some(b) if b.is_ascii_lowercase() || b.is_ascii_digit())
        && name.len() <= 128
        && bytes.all(|b| {
            b.is_ascii_lowercase() || b.is_ascii_digit() || matches!(b, b'.' | b'_' | b'-')
        })
}

fn decode_entry(raw: serde_json::Value) -> Option<ComponentEntry> {
    let label = format!(
        "{} {}",
        raw["kind"].as_str().unwrap_or("?"),
        raw["name"].as_str().unwrap_or("?")
    );
    match serde_json::from_value::<ComponentEntry>(raw) {
        Ok(entry) if entry.kind == ComponentKind::Unknown => {
            eprintln!(
                "[sidecar-manager] skipping catalog entry {label}: kind unknown to this desktop"
            );
            None
        }
        Ok(entry) => match entry.invalid_name() {
            Some(name) => {
                eprintln!(
                    "[sidecar-manager] skipping catalog entry {label}: {name:?} is not a package name"
                );
                None
            }
            None => Some(entry),
        },
        Err(e) => {
            eprintln!("[sidecar-manager] skipping unreadable catalog entry {label}: {e}");
            None
        }
    }
}

pub(super) struct ResolvedSpecs {
    pub(super) specs: Vec<SidecarSpec>,
    pub(super) discovered: HashSet<String>,
    pub(super) failures: Vec<SpecFailure>,
    /// Installed components deliberately not run (a second sandbox runtime:
    /// only one is active per desktop).
    pub(super) inactive: Vec<SpecFailure>,
}

pub(super) struct SpecFailure {
    pub(super) kind: ComponentKind,
    pub(super) name: String,
    pub(super) message: String,
}

#[derive(serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct ComponentEntry {
    pub(super) kind: ComponentKind,
    pub(super) name: String,
    pub(super) version: String,
    pub(super) runtime: Option<String>,
    pub(super) requires_desktop: Option<bool>,
    pub(super) rollout: String,
    pub(super) installed: bool,
    pub(super) artifacts: Option<HashMap<String, ComponentArtifact>>,
    pub(super) sidecar: Option<ArtifactSidecar>,
    pub(super) manifest: Option<serde_json::Value>,
    #[serde(default)]
    pub(super) permissions: Option<ComponentPermissions>,
    /// The component's settings declaration, and which platform and
    /// organization settings are set or missing for the member's
    /// organization (never their values).
    #[serde(default)]
    pub(super) settings: Vec<Setting>,
    #[serde(default)]
    pub(super) settings_set: Vec<String>,
    #[serde(default)]
    pub(super) settings_missing: Vec<String>,
    /// What a plugin is for (`skills-provider`, `sandbox-mounts`, …).
    #[serde(default)]
    pub(super) capabilities: Vec<String>,
    #[serde(default)]
    pub(super) guest: Option<GuestProfile>,
    /// Present for an installed previewer: its bundle pin.
    #[serde(default)]
    pub(super) previewer: Option<PreviewerEntry>,
    /// Present for an installed MCP tool: its tools, transport and auth.
    #[serde(default)]
    pub(super) mcp: Option<McpEntry>,
    /// An endpoint component's address (an http MCP tool's server URL).
    #[serde(default)]
    pub(super) endpoint: Option<EndpointEntry>,
}

/// The part of a catalog endpoint block the desktop needs.
#[derive(Clone, Default, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct EndpointEntry {
    #[serde(default)]
    pub(super) base_url: String,
}

/// The part of a catalog MCP block the desktop needs (pkgmeta.MCP plus the
/// gateway's transport). Every field is defaulted so one malformed entry
/// never fails the whole catalog decode.
#[derive(Clone, Default, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct McpEntry {
    #[serde(default)]
    pub(super) transport: String,
    #[serde(default)]
    pub(super) tools: Vec<McpToolEntry>,
    /// An endpoint tool's auth (pkgmeta.MCPHTTP).
    #[serde(default)]
    pub(super) http: Option<McpHttpEntry>,
}

/// An endpoint MCP tool's auth (pkgmeta.MCPHTTP).
#[derive(Clone, Default, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct McpHttpEntry {
    #[serde(default)]
    pub(super) auth: McpAuthEntry,
}

/// pkgmeta.MCPHTTPAuth: the parts a member's sign-in needs (scheme
/// "oauth"); bearer and header values are settings the bridge delivers.
#[derive(Clone, Default, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct McpAuthEntry {
    #[serde(default)]
    pub(super) scheme: String,
    #[serde(default)]
    pub(super) client_id: Option<String>,
    #[serde(default)]
    pub(super) scopes: Vec<String>,
}

/// One tool an MCP server offers (display only).
#[derive(Clone, Debug, Default, PartialEq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct McpToolEntry {
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
}

/// The part of a catalog previewer block the desktop needs: the gateway
/// artifact URL of the bundle archive and its sha256 pin.
#[derive(Clone, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub(super) struct PreviewerEntry {
    // Defaulted so one malformed entry can never fail the whole catalog
    // decode (and with it sidecar supervision); empty pins are not served.
    #[serde(default)]
    pub(super) bundle_url: String,
    #[serde(default)]
    pub(super) bundle_sha256: Option<Sha256Pin>,
}

#[derive(Clone, Default, serde::Deserialize)]
pub(super) struct ComponentPermissions {
    #[serde(default)]
    pub(super) host: Option<Vec<String>>,
}

/// One artifact of a catalog entry: its gateway URL and sha256 pin (a
/// malformed pin drops the entry when the catalog is decoded).
#[derive(Clone, Debug, PartialEq, Eq, serde::Deserialize)]
pub(super) struct ComponentArtifact {
    pub(super) url: String,
    pub(super) sha256: Sha256Pin,
}

/// The catalog entries this desktop runs or keeps (installed, rollout not
/// off, desktop-managed: manifest cores and desktop sidecars), including a
/// second sandbox that stays inactive.
pub(super) fn desktop_components(
    response: &ComponentsResponse,
) -> impl Iterator<Item = &ComponentEntry> {
    response
        .components
        .iter()
        .filter(|c| c.active_desktop_component())
}

/// The one sandbox runtime a desktop runs (it owns the machines): the
/// lexically first installed name wins, deterministically.
pub(super) fn active_sandbox(response: &ComponentsResponse) -> Option<&ComponentEntry> {
    desktop_components(response)
        .filter(|c| c.kind == ComponentKind::Sandbox)
        .min_by(|a, b| a.name.cmp(&b.name))
}

pub(super) fn resolve_component_specs(
    response: &ComponentsResponse,
    platform: &str,
) -> ResolvedSpecs {
    let mut specs = Vec::new();
    let mut discovered = HashSet::new();
    let mut failures = Vec::new();
    let mut inactive = Vec::new();
    let active_sandbox = active_sandbox(response).map(|c| c.name.as_str());

    for component in desktop_components(response) {
        discovered.insert(component.id());
        if component.kind == ComponentKind::Sandbox
            && Some(component.name.as_str()) != active_sandbox
        {
            inactive.push(SpecFailure {
                kind: component.kind,
                name: component.name.clone(),
                message: format!(
                    "Not running: only one sandbox runtime runs per computer and {} is active",
                    active_sandbox.unwrap_or_default()
                ),
            });
            continue;
        }
        match component.to_sidecar_spec(platform) {
            Ok(spec) => specs.push(spec),
            Err(message) => failures.push(SpecFailure {
                kind: component.kind,
                name: component.name.clone(),
                message,
            }),
        }
    }

    ResolvedSpecs {
        specs,
        discovered,
        failures,
        inactive,
    }
}

impl ComponentEntry {
    fn id(&self) -> String {
        super::sidecar_status::component_id(self.kind, &self.name)
    }

    /// The first name of this entry that is not a package name (its own, a
    /// declared sidecar file's).
    fn invalid_name(&self) -> Option<&str> {
        std::iter::once(self.name.as_str())
            .chain(
                self.sidecar
                    .iter()
                    .flat_map(|s| s.files.iter().flatten())
                    .map(|f| f.name.as_str()),
            )
            .find(|name| !valid_package_name(name))
    }

    /// Plugin launch metadata from the catalog entry (cores get none).
    fn plugin_launch(&self, sidecar: &ArtifactSidecar) -> PluginLaunch {
        if self.kind != ComponentKind::Plugin {
            return PluginLaunch::default();
        }
        PluginLaunch {
            capabilities: self.capabilities.clone(),
            token_env: sidecar.token_env.clone().unwrap_or_default(),
            data_dir_env: sidecar.data_dir_env.clone().unwrap_or_default(),
            mounts_env: sidecar.mounts_env.clone().unwrap_or_default(),
            mount_root_env: sidecar.mount_root_env.clone().unwrap_or_default(),
            host: self
                .permissions
                .as_ref()
                .and_then(|p| p.host.clone())
                .unwrap_or_default(),
            settings: self.settings.clone(),
            settings_set: self.settings_set.clone(),
            settings_missing: self.settings_missing.clone(),
        }
    }

    /// Sandbox launch metadata from the catalog entry (other kinds get none).
    /// Every declared file must have this platform's artifact.
    fn sandbox_launch(
        &self,
        sidecar: &ArtifactSidecar,
        platform: &str,
    ) -> Result<SandboxLaunch, String> {
        if self.kind != ComponentKind::Sandbox {
            return Ok(SandboxLaunch::default());
        }
        let token_env = sidecar.token_env.clone().unwrap_or_default();
        if token_env.trim().is_empty() {
            return Err(format!("{} declares no sidecar.tokenEnv", self.name));
        }
        let mut files = Vec::new();
        for file in sidecar.files.iter().flatten() {
            let key = format!("{platform}/{}", file.name);
            let artifact = self
                .artifacts
                .as_ref()
                .and_then(|artifacts| artifacts.get(&key))
                .ok_or_else(|| format!("{} has no {key} artifact", self.name))?;
            files.push(SidecarFileSpec {
                name: file.name.clone(),
                env: file.env.clone(),
                executable: file.executable,
                url: artifact.url.clone(),
                sha256: artifact.sha256.clone(),
            });
        }
        Ok(SandboxLaunch {
            token_env,
            data_dir_env: sidecar.data_dir_env.clone().unwrap_or_default(),
            files,
            guest: self.guest.clone(),
        })
    }

    fn active_desktop_component(&self) -> bool {
        if self.rollout == "off" || !self.installed {
            return false;
        }
        match self.runtime.as_deref() {
            Some("manifest") => self.kind == ComponentKind::Core,
            Some("sidecar") => self.requires_desktop.unwrap_or(false),
            Some("endpoint") | None => false,
            Some(_) => false,
        }
    }

    fn to_sidecar_spec(&self, platform: &str) -> Result<SidecarSpec, String> {
        match self.runtime.as_deref() {
            Some("manifest") => Ok(SidecarSpec {
                kind: self.kind,
                name: self.name.clone(),
                version: self.version.clone(),
                artifact: None,
                manifest: self.manifest.clone(),
                local_url: String::new(),
                listen_env: String::new(),
                supervised_env: String::new(),
                args: Vec::new(),
                plugin: PluginLaunch::default(),
                sandbox: SandboxLaunch::default(),
            }),
            Some("sidecar") => {
                let artifact = self
                    .artifacts
                    .as_ref()
                    .and_then(|artifacts| artifacts.get(platform))
                    .ok_or_else(|| format!("{} has no {platform} artifact", self.name))?;
                let sidecar = self
                    .sidecar
                    .as_ref()
                    .ok_or_else(|| format!("{} sidecar metadata missing", self.name))?;
                Ok(SidecarSpec {
                    kind: self.kind,
                    name: self.name.clone(),
                    version: self.version.clone(),
                    artifact: Some(artifact.clone()),
                    manifest: self.manifest.clone(),
                    local_url: sidecar.local_url.clone().unwrap_or_default(),
                    listen_env: sidecar.listen_env.clone().unwrap_or_default(),
                    supervised_env: sidecar.supervised_env.clone().unwrap_or_default(),
                    args: sidecar.args.clone().unwrap_or_default(),
                    plugin: self.plugin_launch(sidecar),
                    sandbox: self.sandbox_launch(sidecar, platform)?,
                })
            }
            Some("endpoint") | None => Err(format!("{} is not desktop managed", self.name)),
            Some(runtime) => Err(format!("{} has unsupported runtime {runtime}", self.name)),
        }
    }
}

#[derive(Clone, PartialEq, Eq, serde::Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub(super) struct ArtifactSidecar {
    pub(super) local_url: Option<String>,
    pub(super) listen_env: Option<String>,
    pub(super) supervised_env: Option<String>,
    pub(super) args: Option<Vec<String>>,
    pub(super) token_env: Option<String>,
    pub(super) data_dir_env: Option<String>,
    pub(super) mounts_env: Option<String>,
    pub(super) mount_root_env: Option<String>,
    pub(super) files: Option<Vec<ArtifactSidecarFile>>,
}

/// One declared extra sidecar file (pkgmeta.SidecarFile).
#[derive(Clone, PartialEq, Eq, serde::Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub(super) struct ArtifactSidecarFile {
    pub(super) name: String,
    pub(super) env: String,
    #[serde(default)]
    pub(super) executable: bool,
}

#[cfg(test)]
#[path = "sidecar_model_tests.rs"]
mod sidecar_model_tests;
