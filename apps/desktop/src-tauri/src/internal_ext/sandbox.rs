//! Desktop supervision of the sandbox component (`bnbloc.sandbox.package/v1`)
//! and its one machine. Contract: docs/prd/2026-10-09-agent-vm-sandbox.md,
//! "Phase 1 contracts".
//!
//! The generic sidecar manager downloads and runs the sandbox sidecar like
//! any component (sidecar_runtime), with three sandbox-only launch inputs
//! wired here: its files laid out in one per-version bundle
//! (sandbox_bundle), a fresh loopback port, and a fresh bearer + private data
//! dir. Once the sidecar runs, this module:
//!
//! 1. polls Sandbox Protocol discover until it answers (else kills it and
//!    the supervise loop restarts it with backoff);
//! 2. prewarms the shared machine (`bnbloc`): pull the package's guest
//!    image, create (or start) the machine with the shared workspaces root;
//!    mode `off` deletes it instead;
//! 3. runs the member's own machine (`bnbloc-own`) beside it while their own
//!    runs are sandboxed and private hosts are open to them that the runtime
//!    can open (docs/prd/2026-10-21-sandbox-private-hosts.md): the same
//!    machine but for its egress, recreated for a change of hosts only when
//!    no own turn runs there;
//! 4. watches the machines and re-prewarms one that is gone;
//! 5. hands the bridge `BNBLOC_SANDBOX_*` (memory/env only) while the
//!    sandbox is ready; the bridge-stabilizing watcher restarts the bridge
//!    when that handoff appears, disappears or the mode changes.
//!
//! What it does is the sandbox component's own settings (its manifest
//! `settings`, set in Components through the one settings dialog like any
//! component's; component_settings.rs): the mode, the environment let into
//! the member's own runs, their private hosts, and the organization's hosts
//! and whether members may allow their own. The desktop reads them by the
//! keys pkgmeta's sandbox contract names (`sandbox_settings`): the member's
//! from the member store, the organization's from the gateway when the
//! catalog changes. A change reconciles the machines; it never restarts the
//! sidecar.
//!
//! At most one sandbox component is active per desktop (sidecar_model).
use std::path::PathBuf;
use std::time::{Duration, Instant};

use tauri::Manager;

use crate::child_process::STOP_GRACE;
use crate::internal_ext::component_settings::{split_lines, Setting, Values, SCOPE_MEMBER};
use crate::internal_ext::sandbox_api::{
    create_machine_request, Discovery, MachineView, SandboxApi, CAPABILITY_EGRESS,
    CAPABILITY_EGRESS_HOSTS,
};
use crate::internal_ext::sandbox_bundle;
use crate::internal_ext::sidecar_model::{
    active_sandbox, ComponentKind, ComponentsResponse, GuestProfile, SidecarSpec,
};
use crate::internal_ext::sidecar_util::{cache_stem, home_env};
use crate::internal_ext::Adapter;
use crate::secret::Secret;

/// The shared machine (`BNBLOC_SANDBOX_MACHINE`).
pub(super) const MACHINE_NAME: &str = "bnbloc";
/// The member's own machine (`BNBLOC_SANDBOX_OWN_MACHINE`): only their own
/// runs, egress opening their private hosts.
pub(super) const OWN_MACHINE_NAME: &str = "bnbloc-own";
/// The share carrying the workspaces root into the machine.
const WORKSPACES_SHARE: &str = "workspaces";
/// The one host directory shared into the machine, under the member's home
/// (`BNBLOC_SANDBOX_WORKSPACES_ROOT`). Nothing else of the home is shared.
const WORKSPACES_DIR: &str = "bnbloc-workspaces";
/// The bridge env names (Phase 1 contracts).
pub(super) const ENV_URL: &str = "BNBLOC_SANDBOX_URL";
pub(super) const ENV_TOKEN: &str = "BNBLOC_SANDBOX_TOKEN";
pub(super) const ENV_VMEXEC: &str = "BNBLOC_SANDBOX_VMEXEC";
pub(super) const ENV_MODE: &str = "BNBLOC_SANDBOX_MODE";
pub(super) const ENV_MACHINE: &str = "BNBLOC_SANDBOX_MACHINE";
pub(super) const ENV_WORKSPACES_ROOT: &str = "BNBLOC_SANDBOX_WORKSPACES_ROOT";
pub(super) const ENV_OWN_MACHINE: &str = "BNBLOC_SANDBOX_OWN_MACHINE";
/// The names the member lets into their own sandboxed runs (comma list).
pub(super) const ENV_FORWARD: &str = "BNBLOC_SANDBOX_ENV";
/// The bridge-stabilizing watcher's key for the sandbox handoff (a slash
/// never appears in a core name, so it cannot collide with one).
pub(super) const BRIDGE_KEY: &str = "sandbox/handoff";

/// How long a fresh sidecar may take to answer discover.
const READY_TIMEOUT: Duration = Duration::from_secs(60);
const READY_POLL: Duration = Duration::from_millis(250);
/// Machine watch cadence while the sidecar runs.
const WATCH_INTERVAL: Duration = Duration::from_secs(15);
/// Minimum spacing of automatic re-prewarms after a failure.
const REPREWARM_BACKOFF: Duration = Duration::from_secs(60);

/// Where a launch is in its bring-up.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) enum Phase {
    /// Spawned; waiting for discover.
    Starting,
    /// Preparing the guest image.
    PullingImage,
    /// Creating / starting the machine.
    StartingMachine,
    /// Machine running.
    Ready,
    /// Mode off: the sidecar runs, no machine.
    Off,
    /// The package declares no guest image: nothing to prewarm.
    NoGuest,
    Error(String),
}

/// One running launch of the active sandbox sidecar.
pub(super) struct Launch {
    pub(super) seq: u64,
    pub(super) id: String,
    pub(super) name: String,
    pub(super) api: SandboxApi,
    pub(super) vmexec: Option<PathBuf>,
    pub(super) guest: Option<GuestProfile>,
    pub(super) spawned_at: Instant,
    pub(super) discovery: Option<Discovery>,
    pub(super) sidecar_ready_ms: Option<u64>,
    pub(super) phase: Phase,
    pub(super) machine: Option<MachineView>,
    pub(super) prewarm_ms: Option<u64>,
    /// The bridge may drive this sandbox: set when a prewarm succeeds,
    /// cleared when the machine is lost, the mode turns off, or the launch
    /// ends. A deliberate VM restart keeps it (same URL, token and machine).
    pub(super) handoff: bool,
    /// A lifecycle operation (prewarm / restart / delete, of either
    /// machine) is in flight.
    pub(super) busy: bool,
    pub(super) last_prewarm_attempt: Option<Instant>,
    pub(super) own: OwnLaunch,
}

/// The member's own machine within a launch.
#[derive(Default)]
pub(super) struct OwnLaunch {
    /// As last observed or created.
    pub(super) machine: Option<MachineView>,
    /// The bridge sends own drives there: set once it runs with the wanted
    /// hosts; cleared when it is not wanted or could not be created (own
    /// drives then run on the shared machine, the hosts blocked).
    pub(super) handoff: bool,
    /// A change of hosts waits for the own turns running there to end.
    pub(super) waiting: bool,
    pub(super) error: Option<String>,
    pub(super) last_attempt: Option<Instant>,
}

/// What the own machine needs now (plan_own_machine).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum OwnPlan {
    /// Not wanted and not there.
    Absent,
    /// Runs with the wanted hosts.
    Keep,
    Create,
    /// Delete, then create with the wanted hosts.
    Recreate,
    Delete,
    /// A change is due, but own turns run there: never stopped.
    Wait,
}

/// The one rule for the own machine: `want` its hosts (None = not wanted),
/// `observed` the machine as the runtime reports it, `own_turns` the turns
/// running on it, `force` the member's "Apply again" (a host's addresses
/// are resolved when the machine is created). A running machine is only
/// recreated or deleted while no own turn runs there.
pub(super) fn plan_own_machine(
    want: Option<&[String]>,
    observed: Option<&MachineView>,
    own_turns: u32,
    force: bool,
) -> OwnPlan {
    let busy = |m: &MachineView| own_turns > 0 && m.state == "running";
    match (want, observed) {
        (None, None) => OwnPlan::Absent,
        (None, Some(m)) if busy(m) => OwnPlan::Wait,
        (None, Some(_)) => OwnPlan::Delete,
        (Some(_), None) => OwnPlan::Create,
        (Some(hosts), Some(m)) => {
            let same = hosts.len() == m.egress_hosts.len()
                && hosts.iter().all(|h| m.egress_hosts.contains(h));
            if m.state == "running" && same && !force {
                OwnPlan::Keep
            } else if busy(m) {
                OwnPlan::Wait
            } else {
                OwnPlan::Recreate
            }
        }
    }
}

#[derive(Default)]
pub(super) struct SandboxState {
    seq: u64,
    /// The component catalog has been read once: until then nobody knows
    /// whether a sandbox is installed or which mode it is set to.
    pub(super) catalog_read: bool,
    pub(super) launch: Option<Launch>,
    pub(super) last_error: Option<String>,
    /// The active sandbox component's settings declaration, as the catalog
    /// lists it.
    pub(super) declared: Option<Declared>,
    /// What its settings say now (sandbox_settings).
    pub(super) settings: SandboxSettings,
}

/// The active sandbox component's settings as the catalog lists them, and
/// its organization's values (None until read from the gateway).
#[derive(Clone, Debug, PartialEq)]
pub(super) struct Declared {
    pub(super) name: String,
    pub(super) version: String,
    pub(super) decl: Vec<Setting>,
    /// Which platform and organization settings are set, and the required
    /// ones that are not (never their values).
    pub(super) set: Vec<String>,
    pub(super) missing: Vec<String>,
    pub(super) org: Option<Values>,
}

// ---- the sandbox's settings (pkgmeta sandbox_settings.go) ----

/// The keys the desktop reads a sandbox component's settings by: pkgmeta's
/// contract (`SandboxSetting*`), pinned by
/// core/pkgmeta/testdata/sandbox_settings.json
/// (`sandbox_setting_keys_match_go`).
pub(super) const SETTING_MODE: &str = "mode";
pub(super) const SETTING_FORWARD_ENV: &str = "forwardEnv";
pub(super) const SETTING_PRIVATE_HOSTS: &str = "privateHosts";
pub(super) const SETTING_ORG_HOSTS: &str = "orgHosts";
pub(super) const SETTING_MEMBER_HOSTS: &str = "memberHosts";
/// memberHosts' "only the organization's hosts" (pkgmeta
/// SandboxMemberHostsOff); its other choice, "ask", is the default.
const MEMBER_HOSTS_OFF: &str = "off";

/// Which of the member's agent runs execute their commands in the VM
/// (the `mode` setting; `BNBLOC_SANDBOX_MODE`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum SandboxMode {
    /// No sandbox VM runs; every run executes on the host.
    Off,
    /// Runs driven by another member (borrowed) execute in the VM.
    #[default]
    Borrowed,
    /// Every run of this member's agents executes in the VM.
    All,
}

/// The mode the bridge is told (`BNBLOC_SANDBOX_MODE`): the sandbox's mode,
/// `off` when no sandbox component is installed, and `pending` until the
/// catalog says which. A drive the mode might sandbox waits while it is
/// pending or the sandbox is not up: it never runs on the host meanwhile
/// (bnbloc docs/bugs/2026-10-21-sandboxed-sessions-reset-every-turn.md).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum BridgeMode {
    Pending,
    Known(SandboxMode),
}

impl BridgeMode {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            BridgeMode::Pending => "pending",
            BridgeMode::Known(m) => m.as_str(),
        }
    }

    /// The handoff reaches the bridge only in a known mode that is not off.
    fn hands_over(self) -> bool {
        matches!(self, BridgeMode::Known(m) if m != SandboxMode::Off)
    }
}

impl SandboxMode {
    const ALL: [SandboxMode; 3] = [SandboxMode::Off, SandboxMode::Borrowed, SandboxMode::All];

    pub fn as_str(self) -> &'static str {
        match self {
            SandboxMode::Off => "off",
            SandboxMode::Borrowed => "borrowed",
            SandboxMode::All => "all",
        }
    }

    fn parse(v: &str) -> Option<Self> {
        Self::ALL.into_iter().find(|m| m.as_str() == v)
    }
}

/// What the sandbox's settings say.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(super) struct SandboxSettings {
    pub(super) mode: SandboxMode,
    /// The names (or `PREFIX*` patterns) let into the member's own
    /// sandboxed runs; values are read from the agent's environment.
    pub(super) forward_env: Vec<String>,
    /// The private hosts the member's own sandboxed runs reach: the
    /// organization's, and the member's own when they may allow them.
    pub(super) hosts: Vec<String>,
    /// The member may allow a private host of their own: the component
    /// declares `privateHosts`, the organization's values are known and do
    /// not turn it off.
    pub(super) member_may_allow: bool,
}

/// The sandbox's settings from its declaration, its organization's values
/// (None: not read yet) and the member's: each declared one's value from
/// its scope, else its default; an undeclared one is the platform's default
/// (mode `borrowed`, nothing forwarded or open). Until the organization's
/// values are read, a member may allow no host of their own (fail closed).
/// A line the rule refuses (a value stored before a rule tightened) is
/// left out.
pub(super) fn sandbox_settings(
    decl: &[Setting],
    org: Option<&Values>,
    member: &Values,
) -> SandboxSettings {
    use crate::internal_ext::component_settings::effective_settings;
    let mut values = Values::new();
    for s in decl {
        let from = if s.scope == SCOPE_MEMBER {
            Some(member)
        } else {
            org
        };
        if let Some(v) = from.and_then(|from| from.get(&s.key)) {
            values.insert(s.key.clone(), v.clone());
        }
    }
    let effective = effective_settings(decl, &values);
    let lines = |key: &str, valid: fn(&str) -> bool| -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for l in split_lines(effective.get(key).map(String::as_str).unwrap_or_default()) {
            if valid(l) && !out.iter().any(|o| o == l) {
                out.push(l.to_string());
            }
        }
        out
    };
    let org_known = org.is_some() || decl.iter().all(|s| s.scope == SCOPE_MEMBER);
    let member_may_allow = org_known
        && decl.iter().any(|s| s.key == SETTING_PRIVATE_HOSTS)
        && effective.get(SETTING_MEMBER_HOSTS).map(String::as_str) != Some(MEMBER_HOSTS_OFF);
    let mut hosts = lines(SETTING_ORG_HOSTS, valid_private_host);
    if member_may_allow {
        for h in lines(SETTING_PRIVATE_HOSTS, valid_private_host) {
            if !hosts.contains(&h) {
                hosts.push(h);
            }
        }
    }
    SandboxSettings {
        mode: effective
            .get(SETTING_MODE)
            .and_then(|v| SandboxMode::parse(v))
            .unwrap_or_default(),
        forward_env: lines(SETTING_FORWARD_ENV, valid_forward_env),
        hosts,
        member_may_allow,
    }
}

/// The rule for a sandbox setting's value beyond its type: every
/// `forwardEnv` line a forwardable name, every host line a private host.
/// The Go owner's (`pkgmeta.CheckSandboxSettingValue`), built from the two
/// pinned rules (valid_forward_env, valid_private_host); a member's save is
/// held to it, as an organization's is on the gateway.
pub(super) fn check_sandbox_setting_value(key: &str, v: &str) -> Result<(), String> {
    let bad: Vec<&str> = split_lines(v)
        .into_iter()
        .filter(|l| match key {
            SETTING_FORWARD_ENV => !valid_forward_env(l),
            SETTING_PRIVATE_HOSTS | SETTING_ORG_HOSTS => !valid_private_host(l),
            _ => false,
        })
        .collect();
    if bad.is_empty() {
        return Ok(());
    }
    if key == SETTING_FORWARD_ENV {
        Err(format!("not an environment variable name a sandboxed run may receive: {} (upper-case names like EXAMPLE_API_KEY, or a prefix like EXAMPLE_*; never BNBLOC_*, HOME, PATH, …)", bad.join(", ")))
    } else {
        Err(format!("not a host the sandbox may open: {} (a name like npm.internal.example or an address like 10.0.0.5; no URL, port or wildcard, never localhost, link-local or metadata addresses)", bad.join(", ")))
    }
}

/// What the bridge gets while the sandbox is ready (Phase 1 contracts).
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct BridgeHandoff {
    pub(crate) seq: u64,
    pub(crate) url: String,
    pub(crate) token: Secret,
    pub(crate) vmexec: PathBuf,
    pub(crate) machine: String,
    pub(crate) workspaces_root: PathBuf,
    /// The member's own machine, while it runs with their private hosts.
    pub(crate) own_machine: Option<String>,
}

/// The bridge env for `mode`, the member's forwarded names and the current
/// handoff: the mode always; the rest (the names included) only while
/// ready and not off.
pub(crate) fn bridge_env(
    mode: BridgeMode,
    forward: &[String],
    handoff: Option<&BridgeHandoff>,
) -> Vec<(String, String)> {
    let mut env = vec![(ENV_MODE.to_string(), mode.as_str().to_string())];
    if let Some(h) = handoff.filter(|_| mode.hands_over()) {
        env.push((ENV_URL.into(), h.url.clone()));
        env.push((ENV_TOKEN.into(), h.token.expose().to_string()));
        env.push((ENV_VMEXEC.into(), h.vmexec.to_string_lossy().into_owned()));
        env.push((ENV_MACHINE.into(), h.machine.clone()));
        env.push((
            ENV_WORKSPACES_ROOT.into(),
            h.workspaces_root.to_string_lossy().into_owned(),
        ));
        if let Some(own) = &h.own_machine {
            env.push((ENV_OWN_MACHINE.into(), own.clone()));
        }
        if !forward.is_empty() {
            env.push((ENV_FORWARD.into(), forward.join(",")));
        }
    }
    env
}

/// The bridge watcher's identity of the sandbox env: changes whenever the
/// env the bridge needs changes, the mode alone included (pending → known
/// restarts the bridge, so waiting drives run where the mode says). Never
/// contains the token; a new launch has a new seq and therefore a new token.
pub(super) fn bridge_fingerprint(
    mode: BridgeMode,
    forward: &[String],
    handoff: Option<&BridgeHandoff>,
) -> String {
    let Some(h) = handoff.filter(|_| mode.hands_over()) else {
        return mode.as_str().to_string();
    };
    format!(
        "{}|{}|{}|{}|{}|{}|{}",
        mode.as_str(),
        h.seq,
        h.url,
        h.machine,
        h.own_machine.as_deref().unwrap_or_default(),
        h.vmexec.display(),
        forward.join(",")
    )
}

/// The one rule for an environment variable pattern a member lets into
/// their own sandboxed runs: the Go owner's (`pkgmeta.ValidForwardEnv`), as
/// a copy pinned by `core/pkgmeta/testdata/forward_env_patterns.json`
/// (`forward_env_pattern_matches_go`). An upper-case identifier, optionally
/// followed by `*` for a prefix; never one that can match a name the
/// sandbox or bnbloc owns.
pub(crate) fn valid_forward_env(pattern: &str) -> bool {
    const RESERVED: &[&str] = &[
        "BNBLOC_*", "HOME", "USER", "LOGNAME", "SHELL", "PATH", "PWD", "OLDPWD", "TMPDIR",
        "HOSTNAME", "LD_*", "DYLD_*",
    ];
    let (name, is_prefix) = match pattern.strip_suffix('*') {
        Some(name) => (name, true),
        None => (pattern, false),
    };
    let mut chars = name.chars();
    let shaped = chars.next().is_some_and(|c| c.is_ascii_uppercase())
        && chars.all(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_');
    if !shaped {
        return false;
    }
    RESERVED.iter().all(|r| {
        let (owned, owned_prefix) = match r.strip_suffix('*') {
            Some(owned) => (owned, true),
            None => (*r, false),
        };
        !((is_prefix && owned.starts_with(name))
            || (owned_prefix && name.starts_with(owned))
            || name == owned)
    })
}

/// The one rule for a private host a member lets their own sandboxed runs
/// reach: the Go owner's (`sandbox/protocol.ValidPrivateHost`), as a copy
/// pinned by `sandbox/protocol/testdata/private_hosts.json`
/// (`private_host_rule_matches_go`). A lower-case DNS name of at least two
/// labels or an IP literal; never a wildcard, URL, port, localhost, or a
/// loopback, link-local, unspecified, broadcast or multicast address.
pub(crate) fn valid_private_host(host: &str) -> bool {
    use crate::internal_ext::loopback::{is_loopback_host, is_loopback_ip};
    use std::net::IpAddr;
    if let Ok(ip) = host.parse::<IpAddr>() {
        let ip = match ip {
            IpAddr::V6(v6) => v6.to_ipv4_mapped().map_or(ip, IpAddr::V4),
            v4 => v4,
        };
        return match ip {
            IpAddr::V4(v4) => {
                !(is_loopback_ip(v4.into())
                    || v4.is_link_local()
                    || v4.is_unspecified()
                    || v4.is_broadcast()
                    || v4.is_multicast())
            }
            IpAddr::V6(v6) => {
                !(is_loopback_ip(v6.into())
                    || v6.is_unspecified()
                    || v6.is_multicast()
                    || (v6.segments()[0] & 0xffc0) == 0xfe80)
            }
        };
    }
    let labels: Vec<&str> = host.split('.').collect();
    if host.len() > 253 || labels.len() < 2 {
        return false;
    }
    let shaped = labels.iter().all(|l| {
        !l.is_empty()
            && l.len() <= 63
            && !l.starts_with('-')
            && !l.ends_with('-')
            && l.chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-')
    });
    // A last label of digits only is a malformed address, and localhost is
    // the guest itself.
    let last = labels[labels.len() - 1];
    shaped && !last.chars().all(|c| c.is_ascii_digit()) && !is_loopback_host(last)
}

/// The sandbox's state on this computer, as its Components card renders it.
#[derive(Clone, Debug, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxStatus {
    pub installed: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    /// notInstalled | installing | starting | ready | off | error
    pub state: &'static str,
    pub message: String,
    pub mode: SandboxMode,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub isolation: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub machine: Option<MachineView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_error: Option<String>,
    pub timings: SandboxTimings,
    /// Commands of the selected runs go to the VM right now.
    pub handed_to_bridge: bool,
    /// The private hosts open to the member's own runs and whether they
    /// are.
    pub private_hosts: PrivateHostsStatus,
}

/// The private hosts of the sandbox's settings on this computer.
#[derive(Clone, Debug, Default, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PrivateHostsStatus {
    /// The organization's and the member's (sandbox_settings).
    pub hosts: Vec<String>,
    /// The member may allow a host of their own (an agent reply's card
    /// offers it).
    pub member_may_allow: bool,
    /// none | unsupported | inactive | waiting | applying | ready | error
    pub state: &'static str,
    pub message: String,
    /// The hosts the own machine opens now.
    pub applied: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SandboxTimings {
    /// Sidecar spawn → discover answered.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sidecar_ready_ms: Option<u64>,
    /// Prewarm (image prepare + create/start), client-measured.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub prewarm_ms: Option<u64>,
}

impl Adapter {
    // ---------------------------------------------------------------- launch

    /// The private data dir of sandbox `name` (its VM disks), created
    /// owner-only in the signed-in member's own directory (member_dir:
    /// member and tenant), so another member or tenant never uses it and
    /// the uninstall sweep judges it only against its own scope's catalog.
    /// The runtime derives Unix socket paths from it; macOS caps them at 104
    /// bytes and the sidecar shortens them through a /tmp link.
    pub(super) fn sandbox_data_dir(&self, name: &str) -> Result<PathBuf, String> {
        let dir = self
            .member_dir()?
            .join(crate::internal_ext::member_dir::SANDBOX_DATA_DIR)
            .join(cache_stem(name));
        create_private_dir(&dir)?;
        Ok(dir)
    }

    /// Download and verify every file of sandbox `spec` into its bundle
    /// directory; returns the sidecar executable.
    pub(super) async fn ensure_sandbox_bundle(
        &self,
        spec: &SidecarSpec,
    ) -> Result<PathBuf, crate::internal_ext::sidecar_fetch::FetchError> {
        use crate::internal_ext::sidecar_fetch::FetchError;
        let id = spec.id();
        let base = self.dirs().map_err(FetchError::Retry)?.sandbox_bundles();
        let plan = sandbox_bundle::bundle_plan(&base, spec).map_err(FetchError::Refused)?;
        let gateway = self.gateway.session();
        let started = Instant::now();
        let downloaded = sandbox_bundle::prepare_bundle(&gateway, &plan, |file, progress| {
            let (state, message) = progress.status(&format!("{} {file}", spec.name));
            self.set_status(&id, state, message, None);
        })
        .await?;
        self.set_status(
            &id,
            "ready",
            if downloaded {
                format!(
                    "{} downloaded and verified in {:.1}s",
                    spec.name,
                    started.elapsed().as_secs_f64()
                )
            } else {
                format!("{} is already downloaded", spec.name)
            },
            None,
        );
        let (root, keep) = (plan.root.clone(), plan.dir.clone());
        tauri::async_runtime::spawn_blocking(move || {
            sandbox_bundle::remove_stale_bundles(&root, Some(&keep))
        });
        Ok(plan.exe())
    }

    /// A fresh loopback listener for the sandbox sidecar (handed over in its
    /// listenEnv), after stopping a sidecar of this component an earlier app
    /// session left running (it would hold the data dir and its VMs).
    pub(super) async fn sandbox_listener(
        &self,
        spec: &SidecarSpec,
    ) -> Result<crate::internal_ext::sidecar_util::RuntimeListener, String> {
        use crate::internal_ext::loopback;
        self.replace_sandbox_orphan(spec).await;
        let listener =
            loopback::bind_ephemeral().map_err(|e| format!("reserve sandbox port: {e}"))?;
        let addr = listener
            .local_addr()
            .map_err(|e| format!("reserve sandbox port: {e}"))?;
        Ok(crate::internal_ext::sidecar_util::RuntimeListener {
            url: loopback::LoopbackHttp::of_addr(addr),
            listener: Some(listener),
        })
    }

    async fn replace_sandbox_orphan(&self, spec: &SidecarSpec) {
        let (Some(record_dir), Ok(base)) = (
            self.spawn_record_dir(),
            self.dirs().map(crate::dirs::Dirs::sandbox_bundles),
        ) else {
            return;
        };
        let id = spec.id();
        let root = base.join(cache_stem(&spec.name));
        let orphan = tauri::async_runtime::spawn_blocking(move || {
            use crate::internal_ext::sidecar_orphan::{
                owned_orphan, process_exe, read_spawn_record,
            };
            let record = read_spawn_record(&record_dir, &id)?;
            owned_orphan(
                record.pid,
                Some(&record),
                process_exe(record.pid).as_deref(),
                &root,
                std::process::id(),
            )
        })
        .await
        .ok()
        .flatten();
        let Some(pid) = orphan.map(|o| o.pid) else {
            return;
        };
        if !crate::internal_ext::sidecar_orphan::CAN_TERMINATE {
            return;
        }
        eprintln!(
            "[sandbox] stopping {} left running by an earlier session (pid {pid})",
            spec.name
        );
        let stopped = tauri::async_runtime::spawn_blocking(move || {
            crate::internal_ext::sidecar_orphan::terminate_orphan(pid, STOP_GRACE)
        })
        .await
        .unwrap_or_else(|e| Err(format!("stop task: {e}")));
        match stopped {
            Ok(()) => self.forget_spawn(&spec.id(), pid),
            Err(e) => eprintln!("[sandbox] could not stop pid {pid}: {e}"),
        }
    }

    /// Record a spawned sandbox sidecar as the current launch and start its
    /// bring-up (discover → prewarm → watch). Returns the launch seq.
    pub(super) fn sandbox_spawned(
        &self,
        spec: &SidecarSpec,
        url: &str,
        token: Secret,
        exe: &std::path::Path,
    ) -> u64 {
        let seq = {
            let mut state = self.sandbox.lock();
            state.seq += 1;
            state.launch = Some(Launch {
                seq: state.seq,
                id: spec.id(),
                name: spec.name.clone(),
                api: SandboxApi {
                    url: url.to_string(),
                    token,
                },
                vmexec: sandbox_bundle::vmexec_path(spec, exe),
                guest: spec.sandbox.guest.clone(),
                spawned_at: Instant::now(),
                discovery: None,
                sidecar_ready_ms: None,
                phase: Phase::Starting,
                machine: None,
                prewarm_ms: None,
                handoff: false,
                busy: false,
                last_prewarm_attempt: None,
                own: OwnLaunch::default(),
            });
            state.seq
        };
        let sup = self.clone();
        tauri::async_runtime::spawn(async move { sup.sandbox_bring_up(seq).await });
        seq
    }

    /// The sidecar of launch `seq` exited: its machines went with it.
    pub(super) fn sandbox_exited(&self, seq: u64, detail: &str) {
        let mut state = self.sandbox.lock();
        if state.launch.as_ref().is_some_and(|l| l.seq == seq) {
            state.launch = None;
            if !detail.trim().is_empty() {
                state.last_error = Some(format!("The sandbox runtime exited: {}", detail.trim()));
            }
        }
    }

    fn sandbox_api(&self, seq: u64) -> Option<SandboxApi> {
        let state = self.sandbox.lock();
        state
            .launch
            .as_ref()
            .filter(|l| l.seq == seq)
            .map(|l| l.api.clone())
    }

    fn with_launch<R>(&self, seq: u64, f: impl FnOnce(&mut Launch) -> R) -> Option<R> {
        let mut state = self.sandbox.lock();
        state.launch.as_mut().filter(|l| l.seq == seq).map(f)
    }

    fn sandbox_error(&self, seq: u64, message: String) {
        eprintln!("[sandbox] {message}");
        let mut state = self.sandbox.lock();
        if let Some(launch) = state.launch.as_mut().filter(|l| l.seq == seq) {
            launch.phase = Phase::Error(message.clone());
            launch.handoff = false;
        }
        state.last_error = Some(message);
    }

    pub(super) fn sandbox_mode(&self) -> SandboxMode {
        self.sandbox.lock().settings.mode
    }

    /// The mode the bridge is told (BridgeMode): pending until the catalog
    /// was read, off without a sandbox component, else its mode.
    pub(crate) fn sandbox_bridge_mode(&self) -> BridgeMode {
        let state = self.sandbox.lock();
        match (state.catalog_read, &state.declared) {
            (false, _) => BridgeMode::Pending,
            (true, None) => BridgeMode::Known(SandboxMode::Off),
            (true, Some(_)) => BridgeMode::Known(state.settings.mode),
        }
    }

    /// The names the member lets into their own sandboxed runs.
    pub(super) fn sandbox_env(&self) -> Vec<String> {
        self.sandbox.lock().settings.forward_env.clone()
    }

    /// The hosts the own machine should open, None when it is not wanted:
    /// the member's own runs are sandboxed (mode all), hosts are open to
    /// them, and the runtime can open them.
    fn own_machine_want(&self, discovery: &Discovery, has_guest: bool) -> Option<Vec<String>> {
        let settings = self.sandbox.lock().settings.clone();
        let able = discovery.has(CAPABILITY_EGRESS) && discovery.has(CAPABILITY_EGRESS_HOSTS);
        (settings.mode == SandboxMode::All && able && has_guest && !settings.hosts.is_empty())
            .then_some(settings.hosts)
    }

    // -------------------------------------------------------------- settings

    /// Follow the catalog: the active sandbox's declaration, then its
    /// organization's values read in the background (a settings save
    /// pushes a catalog hint, so a change arrives within seconds).
    pub(super) fn sync_sandbox_settings(&self, catalog: &ComponentsResponse) {
        let next = active_sandbox(catalog).map(|c| Declared {
            name: c.name.clone(),
            version: c.version.clone(),
            decl: c.settings.clone(),
            set: c.settings_set.clone(),
            missing: c.settings_missing.clone(),
            org: None,
        });
        let read = {
            let mut state = self.sandbox.lock();
            state.catalog_read = true;
            // The values read for the same declaration stay until the
            // fresh read answers.
            state.declared = next.map(|mut d| {
                d.org = state
                    .declared
                    .as_ref()
                    .filter(|old| old.name == d.name && old.decl == d.decl)
                    .and_then(|old| old.org.clone());
                d
            });
            state
                .declared
                .as_ref()
                .filter(|d| d.decl.iter().any(|s| s.scope != SCOPE_MEMBER))
                .map(|d| (d.name.clone(), d.decl.clone()))
        };
        self.refresh_sandbox_settings();
        let Some((name, decl)) = read else {
            return;
        };
        let sup = self.clone();
        tauri::async_runtime::spawn(async move {
            match sup.resolved_settings(ComponentKind::Sandbox, &name).await {
                Ok(values) => {
                    if let Some(d) = sup
                        .sandbox
                        .lock()
                        .declared
                        .as_mut()
                        .filter(|d| d.name == name && d.decl == decl)
                    {
                        d.org = Some(values);
                    }
                    sup.refresh_sandbox_settings();
                }
                // The last values read stay; never logged with a value.
                Err(e) => eprintln!("[sandbox] {e}"),
            }
        });
    }

    /// Recompute what the settings say (the member's from the member store)
    /// and, when it changed, bring the machines to it.
    pub(super) fn refresh_sandbox_settings(&self) {
        let declared = self.sandbox.lock().declared.clone();
        let next = match &declared {
            Some(d) => {
                let member = self
                    .member_settings(ComponentKind::Sandbox, &d.name)
                    .unwrap_or_else(|e| {
                        eprintln!("[sandbox] your settings of {}: {e}", d.name);
                        Values::new()
                    });
                sandbox_settings(&d.decl, d.org.as_ref(), &member)
            }
            None => SandboxSettings::default(),
        };
        let changed = {
            let mut state = self.sandbox.lock();
            let changed = state.settings != next;
            state.settings = next;
            changed
        };
        if changed {
            self.sandbox_settings_changed();
        }
    }

    async fn sandbox_bring_up(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        let deadline = Instant::now() + READY_TIMEOUT;
        let discovery = loop {
            if self.sandbox_api(seq).is_none() {
                return;
            }
            match api.discover().await {
                Ok(discovery) => break discovery,
                Err(e) if e.code == "incompatible" => {
                    self.sandbox_error(seq, e.message);
                    return;
                }
                Err(e) if Instant::now() >= deadline => {
                    self.sandbox_error(
                        seq,
                        format!(
                            "The sandbox runtime did not become ready in {}s ({e}); restarting it",
                            READY_TIMEOUT.as_secs()
                        ),
                    );
                    self.kill_sandbox_child(seq);
                    return;
                }
                Err(_) => tokio::time::sleep(READY_POLL).await,
            }
        };
        let ok = self.with_launch(seq, |launch| {
            launch.sidecar_ready_ms = Some(launch.spawned_at.elapsed().as_millis() as u64);
            launch.discovery = Some(discovery);
        });
        if ok.is_none() {
            return;
        }
        self.sandbox.lock().last_error = None;
        self.sandbox_reconcile(seq).await;
        loop {
            tokio::time::sleep(WATCH_INTERVAL).await;
            if self.sandbox_api(seq).is_none() {
                return;
            }
            self.sandbox_watch(seq).await;
        }
    }

    /// Stop launch `seq`'s child (it never became ready): the supervise loop
    /// sees the exit and restarts it with backoff.
    fn kill_sandbox_child(&self, seq: u64) {
        let Some(id) = self.with_launch(seq, |launch| launch.id.clone()) else {
            return;
        };
        let child = self
            .inner
            .lock()
            .sidecars
            .get_mut(&id)
            .and_then(|sidecar| sidecar.child.take());
        if let Some(child) = child {
            self.stopping.stop(&id, vec![child]);
        }
    }

    /// Bring the machine to what the mode wants: off → no machine; otherwise
    /// a running prewarmed machine. Serialized per launch by `busy`.
    async fn sandbox_reconcile(&self, seq: u64) {
        let claimed = self.with_launch(seq, |launch| {
            if launch.busy || launch.discovery.is_none() {
                return false;
            }
            launch.busy = true;
            true
        });
        if claimed != Some(true) {
            return;
        }
        if self.sandbox_mode() == SandboxMode::Off {
            self.sandbox_turn_off(seq).await;
        } else {
            self.sandbox_prewarm(seq).await;
        }
        self.sandbox_own_reconcile(seq, false).await;
        self.with_launch(seq, |launch| launch.busy = false);
    }

    /// Bring the own machine to what the member's hosts want, serialized
    /// with the shared machine's operations (`busy`); a busy launch is
    /// retried by the watch.
    async fn sandbox_own_check(&self, seq: u64, force: bool) {
        let claimed = self.with_launch(seq, |launch| {
            if launch.busy || launch.discovery.is_none() {
                return false;
            }
            launch.busy = true;
            true
        });
        if claimed != Some(true) {
            return;
        }
        self.sandbox_own_reconcile(seq, force).await;
        self.with_launch(seq, |launch| launch.busy = false);
    }

    /// The own machine's step (plan_own_machine); the caller holds `busy`.
    /// It is created only beside a ready shared machine (whose prewarm
    /// pulled the image); after a create, the next one waits
    /// REPREWARM_BACKOFF unless the member changed the hosts.
    async fn sandbox_own_reconcile(&self, seq: u64, force: bool) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        let Some((guest, discovery, shared_ready, retry_due)) = self.with_launch(seq, |l| {
            (
                l.guest.clone(),
                l.discovery.clone().unwrap_or_default(),
                l.phase == Phase::Ready,
                l.own
                    .last_attempt
                    .map_or(true, |at| at.elapsed() >= REPREWARM_BACKOFF),
            )
        }) else {
            return;
        };
        let want = self.own_machine_want(&discovery, guest.is_some());
        if want.is_none() {
            // Not wanted: own drives go back to the shared machine at once
            // (the bridge restarts when idle); the machine goes when no own
            // turn runs there.
            self.with_launch(seq, |l| l.own.handoff = false);
        }
        let observed = match api.machine(OWN_MACHINE_NAME).await {
            Ok(observed) => observed,
            Err(e) => {
                self.with_launch(seq, |l| {
                    l.own.error = Some(format!("Could not reach the sandbox runtime: {e}"))
                });
                return;
            }
        };
        let plan = plan_own_machine(
            want.as_deref(),
            observed.as_ref(),
            self.own_machine_turns(),
            force,
        );
        self.with_launch(seq, |l| {
            l.own.machine = observed.clone();
            l.own.waiting = plan == OwnPlan::Wait;
        });
        match plan {
            OwnPlan::Absent | OwnPlan::Wait => {
                if plan == OwnPlan::Absent {
                    self.with_launch(seq, |l| l.own.error = None);
                }
            }
            OwnPlan::Keep => {
                self.with_launch(seq, |l| {
                    l.own.handoff = true;
                    l.own.error = None;
                });
            }
            OwnPlan::Delete => {
                let result = api.delete_machine(OWN_MACHINE_NAME).await;
                eprintln!("[sandbox] own VM {OWN_MACHINE_NAME} deleted (no private hosts wanted)");
                self.with_launch(seq, |l| {
                    l.own.machine = None;
                    l.own.error = result
                        .err()
                        .map(|e| format!("Could not stop your own VM: {e}"));
                });
            }
            OwnPlan::Create | OwnPlan::Recreate => {
                // A shared machine not up (its image not pulled yet, or being
                // restarted) leaves the own one as it is.
                if !shared_ready || (!retry_due && !force) {
                    return;
                }
                let (Some(guest), Some(hosts)) = (guest, want) else {
                    return;
                };
                self.with_launch(seq, |l| l.own.last_attempt = Some(Instant::now()));
                let started = Instant::now();
                let result = async {
                    if plan == OwnPlan::Recreate {
                        api.delete_machine(OWN_MACHINE_NAME)
                            .await
                            .map_err(|e| e.to_string())?;
                    }
                    let root =
                        workspaces_root().ok_or("no home directory for the sandbox workspaces")?;
                    let owner = self
                        .app
                        .try_state::<crate::AppState>()
                        .map(|s| s.settings.lock().owner.clone())
                        .unwrap_or_default();
                    let req = create_machine_request(
                        OWN_MACHINE_NAME,
                        &guest,
                        &root,
                        WORKSPACES_SHARE,
                        &discovery,
                        &owner,
                        &hosts,
                    );
                    api.create_machine(req).await.map_err(|e| e.to_string())
                }
                .await;
                match result {
                    Ok(machine) if machine.state == "running" => {
                        eprintln!(
                            "[sandbox] own VM {OWN_MACHINE_NAME} running with {} ({} ms)",
                            hosts.join(", "),
                            started.elapsed().as_millis()
                        );
                        self.with_launch(seq, |l| {
                            l.own.machine = Some(machine);
                            l.own.handoff = true;
                            l.own.error = None;
                        });
                    }
                    other => {
                        let why = match other {
                            Ok(m) => format!("it is {}", m.state),
                            Err(e) => e,
                        };
                        eprintln!("[sandbox] own VM {OWN_MACHINE_NAME} not started: {why}");
                        self.with_launch(seq, |l| {
                            l.own.handoff = false;
                            l.own.error = Some(format!(
                                "Your own VM with private hosts did not start: {why}"
                            ));
                        });
                    }
                }
            }
        }
    }

    /// The member asked to apply the private hosts again (their addresses
    /// are resolved again): recreate the own machine in the background.
    pub(super) fn sandbox_private_hosts_apply(&self) {
        let Some(seq) = self.sandbox.lock().launch.as_ref().map(|l| l.seq) else {
            return;
        };
        // The member's ask is not a retry: no backoff.
        self.with_launch(seq, |l| l.own.last_attempt = None);
        let sup = self.clone();
        tauri::async_runtime::spawn(async move { sup.sandbox_own_check(seq, true).await });
    }

    async fn sandbox_turn_off(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        let result = api.delete_machine(MACHINE_NAME).await;
        self.with_launch(seq, |launch| {
            launch.handoff = false;
            launch.machine = None;
            launch.phase = Phase::Off;
        });
        if let Err(e) = result {
            self.sandbox_error(seq, format!("Could not stop the sandbox VM: {e}"));
        }
    }

    async fn sandbox_prewarm(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        let Some((guest, discovery)) = self.with_launch(seq, |launch| {
            launch.last_prewarm_attempt = Some(Instant::now());
            (
                launch.guest.clone(),
                launch.discovery.clone().unwrap_or_default(),
            )
        }) else {
            return;
        };
        let Some(guest) = guest else {
            self.with_launch(seq, |launch| {
                launch.phase = Phase::NoGuest;
                launch.handoff = false;
            });
            return;
        };
        let started = Instant::now();
        let result = async {
            let root = workspaces_root().ok_or("no home directory for the sandbox workspaces")?;
            create_private_dir(&root)?;
            // A machine that survived (a reused launch) is reused.
            let existing = api.machine(MACHINE_NAME).await.map_err(|e| e.to_string())?;
            let machine = match existing {
                Some(m) if m.state == "running" => m,
                Some(m) if m.state == "paused" => {
                    self.with_launch(seq, |l| l.phase = Phase::StartingMachine);
                    api.lifecycle(MACHINE_NAME, "resume")
                        .await
                        .map_err(|e| e.to_string())?
                }
                Some(m) if m.state == "stopped" => {
                    self.with_launch(seq, |l| l.phase = Phase::StartingMachine);
                    api.lifecycle(MACHINE_NAME, "start")
                        .await
                        .map_err(|e| e.to_string())?
                }
                existing => {
                    if existing.is_some() {
                        // failed / creating leftovers: start clean.
                        api.delete_machine(MACHINE_NAME)
                            .await
                            .map_err(|e| e.to_string())?;
                    }
                    self.with_launch(seq, |l| l.phase = Phase::PullingImage);
                    api.pull_image(&guest.image).await.map_err(|e| {
                        format!("Could not prepare the VM image {}: {e}", guest.image)
                    })?;
                    self.with_launch(seq, |l| l.phase = Phase::StartingMachine);
                    let owner = self
                        .app
                        .try_state::<crate::AppState>()
                        .map(|s| s.settings.lock().owner.clone())
                        .unwrap_or_default();
                    let req = create_machine_request(
                        MACHINE_NAME,
                        &guest,
                        &root,
                        WORKSPACES_SHARE,
                        &discovery,
                        &owner,
                        // Never a private host: borrowed and platform runs
                        // execute here.
                        &[],
                    );
                    api.create_machine(req)
                        .await
                        .map_err(|e| format!("Could not start the VM: {e}"))?
                }
            };
            Ok::<MachineView, String>(machine)
        }
        .await;
        match result {
            Ok(machine) if machine.state == "running" => {
                let elapsed = started.elapsed().as_millis() as u64;
                eprintln!(
                    "[sandbox] VM {MACHINE_NAME} running (prewarm {elapsed} ms, boot {} ms)",
                    machine.boot_ms.unwrap_or_default()
                );
                self.with_launch(seq, |launch| {
                    launch.machine = Some(machine);
                    launch.phase = Phase::Ready;
                    launch.prewarm_ms = Some(elapsed);
                    launch.handoff = true;
                });
                self.sandbox.lock().last_error = None;
            }
            Ok(machine) => {
                let state = machine.state.clone();
                let error = machine.error.clone();
                self.with_launch(seq, |launch| launch.machine = Some(machine));
                self.sandbox_error(
                    seq,
                    format!(
                        "The VM is {state}{}",
                        error.map(|e| format!(": {e}")).unwrap_or_default()
                    ),
                );
            }
            Err(e) => self.sandbox_error(seq, e),
        }
    }

    /// Periodic check: refresh the machine state; re-prewarm a lost machine
    /// (spaced by REPREWARM_BACKOFF).
    async fn sandbox_watch(&self, seq: u64) {
        let Some(api) = self.sandbox_api(seq) else {
            return;
        };
        if self.with_launch(seq, |l| l.busy || l.discovery.is_none()) != Some(false) {
            return;
        }
        let mode = self.sandbox_mode();
        let observed = api.machine(MACHINE_NAME).await;
        let want_machine = mode != SandboxMode::Off;
        let retry_due = self
            .with_launch(seq, |launch| {
                launch
                    .last_prewarm_attempt
                    .map_or(true, |at| at.elapsed() >= REPREWARM_BACKOFF)
            })
            .unwrap_or(false);
        match observed {
            Ok(Some(machine)) if machine.state == "running" => {
                self.with_launch(seq, |launch| {
                    launch.machine = Some(machine);
                    if launch.phase != Phase::Ready && want_machine {
                        launch.phase = Phase::Ready;
                        launch.handoff = true;
                    }
                });
                if !want_machine {
                    self.sandbox_reconcile(seq).await;
                }
            }
            Ok(observed) => {
                let gone = observed.is_none();
                self.with_launch(seq, |launch| {
                    launch.machine = observed;
                    if want_machine && launch.phase == Phase::Ready {
                        launch.phase = Phase::Error(if gone {
                            "The VM is gone; starting it again".into()
                        } else {
                            "The VM stopped; starting it again".into()
                        });
                        launch.handoff = false;
                    }
                });
                if want_machine && retry_due {
                    self.sandbox_reconcile(seq).await;
                }
            }
            Err(e) => self.sandbox_error(seq, format!("Could not reach the sandbox runtime: {e}")),
        }
        // The own machine follows the hosts and the mode; a change that
        // waited for own turns to end applies here.
        self.sandbox_own_check(seq, false).await;
    }

    /// The settings changed: reconcile both machines in the background (a
    /// change is not a retry: no backoff). A reconcile already running
    /// leaves it to the watch.
    fn sandbox_settings_changed(&self) {
        let Some(seq) = self.sandbox.lock().launch.as_ref().map(|l| l.seq) else {
            return;
        };
        self.with_launch(seq, |l| l.own.last_attempt = None);
        let sup = self.clone();
        tauri::async_runtime::spawn(async move { sup.sandbox_reconcile(seq).await });
    }

    /// Restart the VM (its Components card's "Restart VM"): stop + start the same
    /// machine (spaces are re-established by the runtime), recreating it when
    /// that fails. The bridge keeps its env (same URL, token and machine).
    pub(super) async fn sandbox_restart_vm(&self) -> Result<(), String> {
        if self.sandbox_mode() == SandboxMode::Off {
            return Err("The agent sandbox is off".into());
        }
        let seq = self
            .sandbox
            .lock()
            .launch
            .as_ref()
            .filter(|l| l.discovery.is_some())
            .map(|l| l.seq)
            .ok_or("The sandbox runtime is not running")?;
        let claimed = self.with_launch(seq, |launch| {
            if launch.busy {
                return false;
            }
            launch.busy = true;
            launch.phase = Phase::StartingMachine;
            true
        });
        if claimed != Some(true) {
            return Err("The VM is busy; try again in a moment".into());
        }
        let api = self
            .sandbox_api(seq)
            .ok_or("The sandbox runtime is not running")?;
        let started = Instant::now();
        let restarted = async {
            api.lifecycle(MACHINE_NAME, "stop").await?;
            api.lifecycle(MACHINE_NAME, "start").await
        }
        .await;
        match restarted {
            Ok(machine) if machine.state == "running" => {
                let elapsed = started.elapsed().as_millis() as u64;
                self.with_launch(seq, |launch| {
                    launch.machine = Some(machine);
                    launch.phase = Phase::Ready;
                    launch.prewarm_ms = Some(elapsed);
                    launch.handoff = true;
                    launch.busy = false;
                });
                self.sandbox.lock().last_error = None;
                Ok(())
            }
            other => {
                if let Err(e) = &other {
                    eprintln!("[sandbox] restart failed ({e}); recreating the VM");
                }
                if let Err(e) = api.delete_machine(MACHINE_NAME).await {
                    eprintln!("[sandbox] could not delete the VM before recreating: {e}");
                }
                self.sandbox_prewarm(seq).await;
                self.with_launch(seq, |launch| launch.busy = false);
                match self.with_launch(seq, |l| l.phase.clone()) {
                    Some(Phase::Ready) => Ok(()),
                    Some(Phase::Error(e)) => Err(e),
                    _ => Err("The VM did not start".into()),
                }
            }
        }
    }

    // ----------------------------------------------------- bridge + status

    /// The handoff while the sandbox is ready.
    pub(crate) fn sandbox_handoff(&self) -> Option<BridgeHandoff> {
        let state = self.sandbox.lock();
        let launch = state.launch.as_ref()?;
        if !launch.handoff {
            return None;
        }
        Some(BridgeHandoff {
            seq: launch.seq,
            url: launch.api.url.clone(),
            token: launch.api.token.clone(),
            vmexec: launch.vmexec.clone()?,
            machine: MACHINE_NAME.to_string(),
            workspaces_root: workspaces_root()?,
            own_machine: launch.own.handoff.then(|| OWN_MACHINE_NAME.to_string()),
        })
    }

    pub fn sandbox_status(&self) -> SandboxStatus {
        let mode = self.sandbox_mode();
        let component = self
            .inner
            .lock()
            .sidecars
            .values()
            .find(|s| {
                s.spec
                    .as_ref()
                    .is_some_and(|spec| spec.kind == ComponentKind::Sandbox)
            })
            .map(|s| (s.spec.clone(), s.status.clone(), s.child.is_some()));
        let handoff = self.sandbox_handoff().is_some();
        let bridge_current = self.sandbox_bridge_current();
        let state = self.sandbox.lock();
        let mut status = SandboxStatus {
            installed: false,
            name: None,
            version: None,
            state: "notInstalled",
            message: "No sandbox component is installed".into(),
            mode,
            isolation: None,
            machine: None,
            last_error: state.last_error.clone(),
            timings: SandboxTimings::default(),
            handed_to_bridge: handoff && mode != SandboxMode::Off,
            private_hosts: PrivateHostsStatus {
                hosts: state.settings.hosts.clone(),
                member_may_allow: state.settings.member_may_allow,
                ..Default::default()
            },
        };
        status.private_hosts.state = if status.private_hosts.hosts.is_empty() {
            "none"
        } else {
            "inactive"
        };
        let Some((Some(spec), sidecar_status, running)) = component else {
            return status;
        };
        status.installed = true;
        status.name = Some(spec.name.clone());
        status.version = Some(spec.version.clone());
        let Some(launch) = state.launch.as_ref().filter(|l| l.id == spec.id()) else {
            let (state_name, message) = match sidecar_status.state.as_str() {
                "failed" => ("error", sidecar_status.message),
                "downloading" | "verifying" | "resolving" => ("installing", sidecar_status.message),
                _ if running => ("starting", "Starting the sandbox runtime".into()),
                _ => ("starting", sidecar_status.message),
            };
            status.state = state_name;
            status.message = message;
            return status;
        };
        status.isolation = launch.discovery.as_ref().map(|d| d.isolation.clone());
        if let Some(discovery) = &launch.discovery {
            private_hosts_status(&mut status.private_hosts, mode, discovery, &launch.own);
            // Open on the own VM, but the bridge still runs without it (it
            // restarts once no turn runs).
            if status.private_hosts.state == "ready" && !bridge_current {
                status.private_hosts.state = "applying";
                status.private_hosts.message =
                    "Handing them to your agents when no run of theirs is going on…".into();
            }
        }
        status.machine = launch.machine.clone();
        status.timings = SandboxTimings {
            sidecar_ready_ms: launch.sidecar_ready_ms,
            prewarm_ms: launch.prewarm_ms,
        };
        let (state_name, message) = match &launch.phase {
            Phase::Starting => ("starting", "Starting the sandbox runtime".to_string()),
            Phase::PullingImage => ("starting", "Preparing the VM image".to_string()),
            Phase::StartingMachine => ("starting", "Starting the VM".to_string()),
            Phase::Ready if launch.vmexec.is_none() => (
                "error",
                format!(
                    "The VM is up, but {} ships no {} helper, so agents cannot use it",
                    launch.name,
                    sandbox_bundle::VMEXEC_FILE
                ),
            ),
            Phase::Ready => ("ready", "VM up".to_string()),
            Phase::Off => ("off", "The agent sandbox is off".to_string()),
            Phase::NoGuest => (
                "error",
                format!(
                    "{} runs, but no guest image declared: no VM is prewarmed",
                    launch.name
                ),
            ),
            Phase::Error(e) => ("error", e.clone()),
        };
        status.state = if mode == SandboxMode::Off && state_name == "ready" {
            "off"
        } else {
            state_name
        };
        status.message = message;
        status
    }
}

/// The Private hosts line for the member's list, mode, the runtime and the
/// own machine.
fn private_hosts_status(
    status: &mut PrivateHostsStatus,
    mode: SandboxMode,
    discovery: &Discovery,
    own: &OwnLaunch,
) {
    status.applied = own
        .machine
        .as_ref()
        .filter(|m| m.state == "running")
        .map(|m| m.egress_hosts.clone())
        .unwrap_or_default();
    let wanted: Vec<&str> = status.hosts.iter().map(String::as_str).collect();
    // "unsupported" first: an agent reply's card offers no host to allow
    // on a runtime that cannot open one.
    let (state, message) = if !(discovery.has(CAPABILITY_EGRESS)
        && discovery.has(CAPABILITY_EGRESS_HOSTS))
    {
        (
            "unsupported",
            format!(
                "{} cannot open private hosts; update it to use them.",
                discovery.name
            ),
        )
    } else if wanted.is_empty() {
        ("none", String::new())
    } else if mode != SandboxMode::All {
        (
            "inactive",
            "Used when all your agent runs are sandboxed. Now your own runs execute on this computer, which reaches these hosts itself; runs by other members never do.".into(),
        )
    } else if own.waiting {
        (
            "waiting",
            "Applies when your agent's current run ends.".into(),
        )
    } else if let Some(e) = &own.error {
        ("error", e.clone())
    } else if own.handoff
        && wanted.len() == status.applied.len()
        && wanted.iter().all(|h| status.applied.iter().any(|a| a == h))
    {
        ("ready", "Open to your own agents' sandboxed runs.".into())
    } else {
        ("applying", "Starting your own VM with these hosts…".into())
    };
    status.state = state;
    status.message = message;
}

/// `<home>/bnbloc-workspaces`: the one host directory the machine shares.
pub(crate) fn workspaces_root() -> Option<PathBuf> {
    home_env().map(|(_, home)| PathBuf::from(home).join(WORKSPACES_DIR))
}

fn create_private_dir(dir: &std::path::Path) -> Result<(), String> {
    std::fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))
            .map_err(|e| format!("restrict {}: {e}", dir.display()))?;
    }
    Ok(())
}

/// Extend the bridge env with the sandbox's mode, forwarded names and
/// handoff (bridge::env_for); without the manager, off.
pub(crate) fn extend_bridge_env(
    app: Option<&tauri::AppHandle>,
    env: &mut std::collections::HashMap<String, String>,
) {
    let adapter = app.and_then(|app| app.try_state::<Adapter>());
    let (mode, forward, handoff) = match adapter {
        Some(a) => (
            a.sandbox_bridge_mode(),
            a.sandbox_env(),
            a.sandbox_handoff(),
        ),
        // No sandbox manager: no sandbox can run.
        None => (BridgeMode::Known(SandboxMode::Off), Vec::new(), None),
    };
    env.extend(bridge_env(mode, &forward, handoff.as_ref()));
}

// ------------------------------------------------------------------ commands

#[tauri::command]
pub(crate) fn sandbox_status(adapter: tauri::State<'_, Adapter>) -> SandboxStatus {
    adapter.sandbox_status()
}

/// Recreate the own machine with the private hosts (their addresses are
/// resolved again), when no own turn runs there.
#[tauri::command]
pub(crate) fn sandbox_private_hosts_apply(adapter: tauri::State<'_, Adapter>) -> SandboxStatus {
    adapter.sandbox_private_hosts_apply();
    adapter.sandbox_status()
}

#[tauri::command]
pub(crate) async fn sandbox_restart_vm(
    adapter: tauri::State<'_, Adapter>,
) -> Result<SandboxStatus, String> {
    adapter.sandbox_restart_vm().await?;
    Ok(adapter.sandbox_status())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn handoff() -> BridgeHandoff {
        BridgeHandoff {
            seq: 3,
            url: "http://127.0.0.1:50123".into(),
            token: Secret::from_test("tok-secret-value"),
            vmexec: PathBuf::from("/cache/sandbox/vmrt/0.1.0-abc/bnbloc-vmexec"),
            machine: MACHINE_NAME.into(),
            workspaces_root: PathBuf::from("/Users/me/bnbloc-workspaces"),
            own_machine: None,
        }
    }

    #[test]
    fn bridge_env_carries_the_mode_always_and_the_rest_only_while_ready() {
        let h = handoff();
        let none: &[String] = &[];
        let env: std::collections::HashMap<_, _> =
            bridge_env(BridgeMode::Known(SandboxMode::Borrowed), none, Some(&h))
                .into_iter()
                .collect();
        assert_eq!(env[ENV_MODE], "borrowed");
        assert_eq!(env[ENV_URL], "http://127.0.0.1:50123");
        assert_eq!(env[ENV_TOKEN], "tok-secret-value");
        assert_eq!(
            env[ENV_VMEXEC],
            "/cache/sandbox/vmrt/0.1.0-abc/bnbloc-vmexec"
        );
        assert_eq!(env[ENV_MACHINE], "bnbloc");
        assert_eq!(env[ENV_WORKSPACES_ROOT], "/Users/me/bnbloc-workspaces");
        assert!(!env.contains_key(ENV_FORWARD), "no names: no variable");
        assert!(
            !env.contains_key(ENV_OWN_MACHINE),
            "no own machine: no variable"
        );
        let own = BridgeHandoff {
            own_machine: Some(OWN_MACHINE_NAME.into()),
            ..h.clone()
        };
        let env: std::collections::HashMap<_, _> =
            bridge_env(BridgeMode::Known(SandboxMode::All), none, Some(&own))
                .into_iter()
                .collect();
        assert_eq!(env[ENV_OWN_MACHINE], "bnbloc-own");

        let forward = vec!["EXAMPLE_API_KEY".to_string(), "EXAMPLE_*".to_string()];
        let env: std::collections::HashMap<_, _> =
            bridge_env(BridgeMode::Known(SandboxMode::All), &forward, Some(&h))
                .into_iter()
                .collect();
        assert_eq!(env[ENV_FORWARD], "EXAMPLE_API_KEY,EXAMPLE_*");

        assert_eq!(
            bridge_env(BridgeMode::Known(SandboxMode::All), &forward, None),
            vec![(ENV_MODE.to_string(), "all".to_string())]
        );
        assert_eq!(
            bridge_env(BridgeMode::Known(SandboxMode::Off), &forward, Some(&h)),
            vec![(ENV_MODE.to_string(), "off".to_string())],
            "off never hands the sandbox over"
        );
        // Until the catalog says whether a sandbox is installed, the bridge
        // is told so: it waits instead of guessing the host.
        assert_eq!(
            bridge_env(BridgeMode::Pending, &forward, Some(&h)),
            vec![(ENV_MODE.to_string(), "pending".to_string())]
        );
    }

    /// "Environment passed to sandboxed agents" saves names only; each value
    /// is read from the agent's environment, which carries the member's
    /// shell variables (shell_env) beneath what the desktop hands over. A
    /// shell can never replace the handoff itself.
    #[cfg(unix)]
    #[tokio::test]
    async fn forwarded_names_find_their_values_in_the_shell_environment() {
        let h = handoff();
        let shell = crate::shell_env::ShellEnv::for_test(
            &[
                ("EXAMPLE_API_KEY", "from-shell"),
                ("BNBLOC_SANDBOX_TOKEN", "from-shell"),
            ],
            None,
        );
        let mut cmd = std::process::Command::new("/bin/sh");
        cmd.args([
            "-c",
            "echo \"$BNBLOC_SANDBOX_ENV|$EXAMPLE_API_KEY|$BNBLOC_SANDBOX_TOKEN\"",
        ]);
        let forward = vec!["EXAMPLE_API_KEY".to_string()];
        cmd.envs(bridge_env(
            BridgeMode::Known(SandboxMode::All),
            &forward,
            Some(&h),
        ));
        let (mut rx, _child) = crate::child_process::spawn(cmd, &shell).expect("spawn");
        assert_eq!(
            rx.recv().await,
            Some(crate::child_process::ChildEvent::Line(
                "EXAMPLE_API_KEY|from-shell|tok-secret-value".into()
            ))
        );
    }

    #[test]
    fn the_token_never_appears_in_the_fingerprint_or_debug_output() {
        let h = handoff();
        let none: &[String] = &[];
        let fp = bridge_fingerprint(BridgeMode::Known(SandboxMode::Borrowed), none, Some(&h));
        assert!(!fp.contains("tok-secret-value"), "{fp}");
        assert!(!format!("{h:?}").contains("tok-secret-value"));
        // The fingerprint changes with everything the bridge env carries.
        assert_ne!(
            fp.clone(),
            bridge_fingerprint(BridgeMode::Known(SandboxMode::All), none, Some(&h))
        );
        assert_ne!(
            fp.clone(),
            bridge_fingerprint(
                BridgeMode::Known(SandboxMode::Borrowed),
                &["EXAMPLE_API_KEY".to_string()],
                Some(&h)
            ),
            "a changed name list restarts the bridge"
        );
        assert_ne!(
            fp.clone(),
            bridge_fingerprint(
                BridgeMode::Known(SandboxMode::Borrowed),
                none,
                Some(&BridgeHandoff {
                    own_machine: Some(OWN_MACHINE_NAME.into()),
                    ..h.clone()
                })
            ),
            "the own machine appearing restarts the bridge"
        );
        let mut relaunched = h.clone();
        relaunched.seq += 1;
        assert_ne!(
            fp,
            bridge_fingerprint(
                BridgeMode::Known(SandboxMode::Borrowed),
                none,
                Some(&relaunched)
            )
        );
        // Without a handoff it is the mode alone, so a mode change (pending
        // → known included) restarts the bridge too.
        assert_eq!(
            bridge_fingerprint(BridgeMode::Known(SandboxMode::Off), none, Some(&h)),
            "off"
        );
        assert_eq!(
            bridge_fingerprint(BridgeMode::Known(SandboxMode::Borrowed), none, None),
            "borrowed"
        );
        assert_eq!(
            bridge_fingerprint(BridgeMode::Pending, none, Some(&h)),
            "pending"
        );
    }

    /// The desktop's copy of the forwarding rule agrees with the Go owner
    /// (`pkgmeta.ValidForwardEnv`) on the shared fixture.
    #[test]
    fn forward_env_pattern_matches_go() {
        #[derive(serde::Deserialize)]
        struct Fixture {
            valid: Vec<String>,
            invalid: Vec<String>,
        }
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../../../core/pkgmeta/testdata/forward_env_patterns.json"
        ))
        .expect("fixture");
        assert!(!fixture.valid.is_empty() && !fixture.invalid.is_empty());
        for p in &fixture.valid {
            assert!(valid_forward_env(p), "valid_forward_env({p:?}) = false");
        }
        for p in &fixture.invalid {
            assert!(!valid_forward_env(p), "valid_forward_env({p:?}) = true");
        }
    }

    /// The desktop's copy of the private-host rule agrees with the Go owner
    /// (`protocol.ValidPrivateHost`) on the shared fixture.
    #[test]
    fn private_host_rule_matches_go() {
        #[derive(serde::Deserialize)]
        struct Fixture {
            valid: Vec<String>,
            invalid: Vec<String>,
        }
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../../../sandbox/protocol/testdata/private_hosts.json"
        ))
        .expect("fixture");
        assert!(!fixture.valid.is_empty() && !fixture.invalid.is_empty());
        for h in &fixture.valid {
            assert!(valid_private_host(h), "valid_private_host({h:?}) = false");
        }
        for h in &fixture.invalid {
            assert!(!valid_private_host(h), "valid_private_host({h:?}) = true");
        }
    }

    fn setting(key: &str, kind: &str, scope: &str, default: &str) -> Setting {
        Setting {
            key: key.into(),
            label: key.into(),
            kind: kind.into(),
            scope: scope.into(),
            default: default.into(),
            ..Default::default()
        }
    }

    /// The sandbox package's settings as scripts/package.sh declares them.
    fn declared() -> Vec<Setting> {
        vec![
            setting(SETTING_MODE, "choice", "member", "borrowed"),
            setting(SETTING_FORWARD_ENV, "lines", "member", ""),
            setting(SETTING_PRIVATE_HOSTS, "lines", "member", ""),
            setting(SETTING_ORG_HOSTS, "lines", "org", ""),
            setting(SETTING_MEMBER_HOSTS, "choice", "org", "ask"),
        ]
    }

    fn values(pairs: &[(&str, &str)]) -> Values {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    /// The keys the desktop reads are pkgmeta's sandbox contract, in the
    /// shapes it holds a package to (the shared fixture).
    #[test]
    fn sandbox_setting_keys_match_go() {
        #[derive(serde::Deserialize)]
        struct Shape {
            #[serde(rename = "type")]
            kind: String,
            scope: String,
            #[serde(default)]
            options: Vec<String>,
        }
        #[derive(serde::Deserialize)]
        struct Fixture {
            shapes: std::collections::BTreeMap<String, Shape>,
        }
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../../../core/pkgmeta/testdata/sandbox_settings.json"
        ))
        .expect("fixture");
        let keys: Vec<&str> = fixture.shapes.keys().map(String::as_str).collect();
        let mut ours = vec![
            SETTING_MODE,
            SETTING_FORWARD_ENV,
            SETTING_PRIVATE_HOSTS,
            SETTING_ORG_HOSTS,
            SETTING_MEMBER_HOSTS,
        ];
        ours.sort();
        assert_eq!(keys, ours);
        for s in declared() {
            let shape = &fixture.shapes[&s.key];
            assert_eq!(
                (&shape.kind, &shape.scope),
                (&s.kind, &s.scope),
                "{}",
                s.key
            );
        }
        let modes: Vec<&str> = SandboxMode::ALL.iter().map(|m| m.as_str()).collect();
        assert_eq!(fixture.shapes[SETTING_MODE].options, modes);
        assert!(fixture.shapes[SETTING_MEMBER_HOSTS]
            .options
            .iter()
            .any(|o| o == MEMBER_HOSTS_OFF));
    }

    #[test]
    fn sandbox_settings_read_each_scope_and_fail_closed() {
        let decl = declared();
        let org = values(&[(SETTING_ORG_HOSTS, "ci.internal.example")]);
        let member = values(&[
            (SETTING_MODE, "all"),
            (SETTING_FORWARD_ENV, "EXAMPLE_API_KEY\nEXAMPLE_*"),
            (
                SETTING_PRIVATE_HOSTS,
                "npm.internal.example\nci.internal.example",
            ),
            // Out of scope: an organization setting in the member store.
            (SETTING_ORG_HOSTS, "10.0.0.9"),
        ]);
        let s = sandbox_settings(&decl, Some(&org), &member);
        assert_eq!(s.mode, SandboxMode::All);
        assert_eq!(s.forward_env, vec!["EXAMPLE_API_KEY", "EXAMPLE_*"]);
        assert_eq!(s.hosts, vec!["ci.internal.example", "npm.internal.example"]);
        assert!(s.member_may_allow);

        let off = values(&[
            (SETTING_MEMBER_HOSTS, "off"),
            (SETTING_ORG_HOSTS, "ci.internal.example"),
        ]);
        let s = sandbox_settings(&decl, Some(&off), &member);
        assert_eq!(
            s.hosts,
            vec!["ci.internal.example"],
            "only the organization's"
        );
        assert!(!s.member_may_allow);

        let s = sandbox_settings(&decl, None, &member);
        assert!(
            s.hosts.is_empty() && !s.member_may_allow,
            "not read yet: none of the member's"
        );

        // Defaults: borrowed, nothing forwarded or open; an undeclared key
        // is the platform's default and a refused line is left out.
        assert_eq!(
            sandbox_settings(&decl, Some(&Values::new()), &Values::new()),
            SandboxSettings {
                member_may_allow: true,
                ..Default::default()
            }
        );
        let s = sandbox_settings(&[], None, &member);
        assert_eq!(s, SandboxSettings::default());
        let bad = values(&[
            (SETTING_FORWARD_ENV, "PATH\nEXAMPLE_TOKEN"),
            (SETTING_MODE, "sometimes"),
        ]);
        let s = sandbox_settings(&decl, Some(&Values::new()), &bad);
        assert_eq!(s.forward_env, vec!["EXAMPLE_TOKEN"]);
        assert_eq!(s.mode, SandboxMode::Borrowed);
    }

    #[test]
    fn sandbox_setting_values_follow_the_two_rules() {
        assert!(
            check_sandbox_setting_value(SETTING_FORWARD_ENV, "EXAMPLE_API_KEY\nEXAMPLE_*").is_ok()
        );
        let err = check_sandbox_setting_value(SETTING_FORWARD_ENV, "EXAMPLE_TOKEN\nPATH")
            .expect_err("PATH");
        assert!(
            err.contains(": PATH (") && !err.contains("EXAMPLE_TOKEN"),
            "{err}"
        );
        assert!(check_sandbox_setting_value(
            SETTING_PRIVATE_HOSTS,
            "npm.internal.example\n10.0.0.5"
        )
        .is_ok());
        let err = check_sandbox_setting_value(SETTING_ORG_HOSTS, "169.254.169.254")
            .expect_err("metadata");
        assert!(err.contains("169.254.169.254"), "{err}");
        assert!(
            check_sandbox_setting_value(SETTING_MODE, "anything").is_ok(),
            "its type's rule only"
        );
    }

    fn own(state: &str, hosts: &[&str]) -> MachineView {
        MachineView {
            name: OWN_MACHINE_NAME.into(),
            state: state.into(),
            egress_hosts: hosts.iter().map(|h| h.to_string()).collect(),
            ..Default::default()
        }
    }

    /// The own machine follows the hosts, and a running one is never
    /// recreated or deleted while an own turn runs there.
    #[test]
    fn own_machine_changes_only_when_no_own_turn_runs() {
        let hosts = vec!["npm.internal.example".to_string()];
        let want = Some(hosts.as_slice());
        let same = own("running", &["npm.internal.example"]);
        let other = own("running", &["ci.internal.example"]);
        assert_eq!(plan_own_machine(None, None, 0, false), OwnPlan::Absent);
        assert_eq!(plan_own_machine(want, None, 0, false), OwnPlan::Create);
        assert_eq!(plan_own_machine(want, Some(&same), 2, false), OwnPlan::Keep);
        assert_eq!(
            plan_own_machine(want, Some(&other), 0, false),
            OwnPlan::Recreate
        );
        assert_eq!(
            plan_own_machine(want, Some(&other), 1, false),
            OwnPlan::Wait
        );
        assert_eq!(
            plan_own_machine(want, Some(&same), 0, true),
            OwnPlan::Recreate,
            "apply again"
        );
        assert_eq!(plan_own_machine(want, Some(&same), 1, true), OwnPlan::Wait);
        assert_eq!(
            plan_own_machine(
                want,
                Some(&own("stopped", &["npm.internal.example"])),
                1,
                false
            ),
            OwnPlan::Recreate,
            "a stopped machine runs no turn"
        );
        assert_eq!(
            plan_own_machine(None, Some(&same), 0, false),
            OwnPlan::Delete
        );
        assert_eq!(plan_own_machine(None, Some(&same), 1, false), OwnPlan::Wait);
    }

    #[test]
    fn private_hosts_status_says_what_applies() {
        let mut discovery = Discovery {
            name: "vmrt".into(),
            capabilities: vec![CAPABILITY_EGRESS.into()],
            ..Default::default()
        };
        let mut status = PrivateHostsStatus {
            hosts: vec!["npm.internal.example".into()],
            ..Default::default()
        };
        let mut launch = OwnLaunch::default();
        let mut state = |mode, d: &Discovery, l: &OwnLaunch| {
            private_hosts_status(&mut status, mode, d, l);
            status.state
        };
        assert_eq!(state(SandboxMode::All, &discovery, &launch), "unsupported");
        discovery.capabilities.push(CAPABILITY_EGRESS_HOSTS.into());
        assert_eq!(
            state(SandboxMode::Borrowed, &discovery, &launch),
            "inactive"
        );
        assert_eq!(state(SandboxMode::All, &discovery, &launch), "applying");
        launch.waiting = true;
        assert_eq!(state(SandboxMode::All, &discovery, &launch), "waiting");
        launch.waiting = false;
        launch.error = Some("no".into());
        assert_eq!(state(SandboxMode::All, &discovery, &launch), "error");
        launch.error = None;
        launch.handoff = true;
        launch.machine = Some(own("running", &["npm.internal.example"]));
        assert_eq!(state(SandboxMode::All, &discovery, &launch), "ready");
    }
}
