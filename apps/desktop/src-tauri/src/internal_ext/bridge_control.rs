//! What an account's adapter needs of that account's bridge.
//!
//! The adapter keeps the machine's managed sidecars for one account and has
//! to know three things about the bridge that serves the same account: the
//! settings it runs with (owner, tenant, gateway), whether it is up and its
//! pid (the running turns are read from its state, update_gate), and a way to
//! restart it when its env changed (sidecar_bridge_sync). It is handed this
//! at construction instead of looking the app's one bridge up in the app
//! state, so each account's adapter can have its own bridge
//! (docs/prd/2026-10-22-multi-account.md §2.3).
use crate::bridge::Status;
use crate::settings::Settings;

pub(crate) trait BridgeControl: Send + Sync {
    /// The settings the bridge runs with (or would start with).
    fn settings(&self) -> Settings;
    /// Its supervision state and pid.
    fn status(&self) -> Status;
    /// Start it, or restart it when it runs, with `settings`.
    fn start(&self, settings: Settings) -> Result<(), String>;
}
