//! What an adapter hands a bridge depends on whose bridge it is: the adapter
//! of one account hands its member directory, managed cores and sandbox only
//! to the bridge that runs for that same account
//! (docs/prd/2026-10-22-multi-account.md §2.4).
use std::path::Path;
use std::sync::Arc;

use parking_lot::Mutex;

use super::sandbox::BridgeMode;
use super::{Adapter, BridgeControl, Shared};
use crate::bridge::{Status, Supervisor};
use crate::settings::Settings;

const GW: &str = "https://gw.example.com";

/// A bridge whose settings and state a test sets.
struct FakeBridge {
    settings: Mutex<Settings>,
    starts: Mutex<Vec<Settings>>,
}

impl BridgeControl for FakeBridge {
    fn settings(&self) -> Settings {
        self.settings.lock().clone()
    }
    fn status(&self) -> Status {
        Supervisor::stopped_status()
    }
    fn start(&self, settings: Settings) -> Result<(), String> {
        self.starts.lock().push(settings);
        Ok(())
    }
}

fn bridge_for(owner: &str) -> Arc<FakeBridge> {
    Arc::new(FakeBridge {
        settings: Mutex::new(Settings {
            gateway_url: GW.into(),
            owner: owner.into(),
            ..Default::default()
        }),
        starts: Mutex::new(Vec::new()),
    })
}

/// A session token naming `subject` in `tenant`.
fn token(subject: &str, tenant: &str) -> String {
    super::member_dir::session_token(serde_json::json!({"sub": subject, "tenant": tenant}))
}

/// An adapter signed in as `subject` in `tenant`, with its bridge.
fn adapter_of(root: &Path, subject: &str, tenant: &str, bridge: Arc<FakeBridge>) -> Adapter {
    let dirs = crate::dirs::Dirs::under(root);
    let adapter = Adapter::new(
        Ok(dirs.clone()),
        GW,
        Shared::new(Ok(dirs)),
        "scope",
        true,
        bridge,
    );
    adapter.update_gateway(GW, token(subject, tenant));
    adapter
}

fn temp(tag: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!("bnbloc-scope-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

/// The member directory is handed only to the bridge that runs for the
/// adapter's account, on its gateway, in its tenant.
#[test]
fn only_the_bridge_of_the_same_account_gets_the_member_directory() {
    let root = temp("dir");
    let adapter = adapter_of(&root, "maya", "org-a", bridge_for("maya"));
    let settings = |owner: &str, gateway: &str, tenant: &str| Settings {
        gateway_url: gateway.into(),
        owner: owner.into(),
        tenant: tenant.into(),
        ..Default::default()
    };
    assert!(adapter
        .bridge_member_dir_for(&settings("maya", GW, ""))
        .is_some());
    assert!(adapter
        .bridge_member_dir_for(&settings("maya", GW, "org-a"))
        .is_some());
    assert!(
        adapter
            .bridge_member_dir_for(&settings("jack", GW, ""))
            .is_none(),
        "another account"
    );
    assert!(
        adapter
            .bridge_member_dir_for(&settings("maya", GW, "org-b"))
            .is_none(),
        "another tenant"
    );
    assert!(
        adapter
            .bridge_member_dir_for(&settings("maya", "https://other.example.com", ""))
            .is_none(),
        "another gateway"
    );
    std::fs::remove_dir_all(&root).unwrap();
}

/// A bridge of another account is handed no sandbox and a mode that is still
/// pending, so a drive the mode might sandbox waits and never runs on the
/// host; the account's own bridge is told the mode the catalog says.
#[test]
fn another_accounts_bridge_is_handed_no_sandbox() {
    let root = temp("sandbox");
    let adapter = adapter_of(&root, "maya", "org-a", bridge_for("maya"));
    // The catalog has been read and declares no sandbox: mode off.
    adapter.sandbox.lock().catalog_read = true;
    let own = Settings {
        gateway_url: GW.into(),
        owner: "maya".into(),
        ..Default::default()
    };
    let other = Settings {
        owner: "jack".into(),
        ..own.clone()
    };
    let (mode, forward, handoff) = adapter.sandbox_for_bridge(&own);
    assert_eq!(
        mode.as_str(),
        BridgeMode::Known(super::sandbox::SandboxMode::Off).as_str()
    );
    assert!(forward.is_empty() && handoff.is_none());
    let (mode, forward, handoff) = adapter.sandbox_for_bridge(&other);
    assert_eq!(
        mode.as_str(),
        BridgeMode::Pending.as_str(),
        "another account's bridge waits"
    );
    assert!(forward.is_empty() && handoff.is_none());
    assert!(
        adapter.bridge_cores_for(&other).is_empty(),
        "no cores for another account's bridge"
    );
    std::fs::remove_dir_all(&root).unwrap();
}

/// The sandbox machines are labelled with the adapter's own session, not
/// with whatever owner the app's settings slot holds.
#[test]
fn the_sandbox_owner_is_the_adapters_own_session() {
    let root = temp("owner");
    let adapter = adapter_of(&root, "maya", "org-a", bridge_for("someone-else"));
    assert_eq!(adapter.session_subject(), "maya");
    adapter.update_gateway(GW, String::new());
    assert_eq!(adapter.session_subject(), "", "signed out: no owner");
    std::fs::remove_dir_all(&root).unwrap();
}

/// Each adapter's reconcile is judged against ITS catalog in the shared
/// registry; an adapter that changes scope or signs out leaves it.
#[test]
fn a_scope_leaves_the_registry_when_the_account_does() {
    let root = temp("scope");
    let dirs = crate::dirs::Dirs::under(&root);
    let shared = Shared::new(Ok(dirs.clone()));
    let live = Arc::clone(&shared.live);
    let adapter = Adapter::new(Ok(dirs), GW, shared, "scope", true, bridge_for("maya"));
    adapter.update_gateway(GW, token("maya", "org-a"));
    let scope = adapter.member_dir().unwrap();
    live.set(
        &scope.to_string_lossy(),
        super::sidecar_sweep::Live::default(),
    );
    assert_eq!(live.len(), 1);
    // Another tenant: the old scope's catalog is no longer in force.
    adapter.update_gateway(GW, token("maya", "org-b"));
    assert_eq!(live.len(), 0);
    std::fs::remove_dir_all(&root).unwrap();
}
