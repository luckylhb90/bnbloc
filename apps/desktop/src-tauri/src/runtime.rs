//! The account runtimes, and the registry that maps accounts and windows to
//! them (docs/prd/2026-10-22-multi-account.md §2.3).
//!
//! Every signed-in account has a runtime: its own session keeper, adapter
//! (catalog, plugins, sandbox, MCP tools), bridge supervisor, member
//! directory and account settings. The web hands the shell a snapshot of its
//! vault (`Registry::reconcile`); the shell makes its runtimes match: creates
//! the missing, re-scopes a changed tenant, and stops and forgets the absent.
//! There is no imperative start or sign-out: an account leaves the vault and
//! its runtime stops, then its family is revoked.
//!
//! A window is bound to one account (`bind`); a command resolves its runtime
//! from the calling window (`Account`), a scheme handler from the request's
//! webview label.
use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, OnceLock, Weak};

use parking_lot::Mutex;
use serde::Deserialize;
use tauri::ipc::{CommandArg, CommandItem, InvokeError};
use tauri::{Manager, Runtime};

use crate::account::{
    load_account_settings, save_account_settings, save_machine_settings, AccountKey,
    AccountSettings, MachineSettings,
};
use crate::bridge::{State, Status, StatusEmit, Supervisor};
use crate::child_process::{self, Child};
use crate::dirs::Dirs;
use crate::internal_ext::{Adapter, BridgeControl, Shared};
use crate::keeper::{Credential, KeeperSink, KeeperStatus, SessionKeeper, Timing};
use crate::settings::Settings;

/// What a command from a window that shows no account answers. The web
/// recognizes it (web/src/lib/desktopAccounts.ts `NOT_BOUND`): a call that
/// raced the window's binding waits for it and goes again.
pub(crate) const NOT_BOUND: &str = "no account is bound to this window";

/// One account of the web's vault, as the web hands it.
#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AccountEntry {
    pub(crate) gateway_url: String,
    pub(crate) subject: String,
    /// The tenant the account acts in ("" = the gateway's default).
    #[serde(default)]
    pub(crate) tenant: String,
    pub(crate) credential: Credential,
}

/// What reconciling the registry to a snapshot does.
#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct Plan {
    pub(crate) create: Vec<AccountKey>,
    /// Accounts that stay (their credential and tenant are handed again).
    pub(crate) keep: Vec<AccountKey>,
    pub(crate) remove: Vec<AccountKey>,
}

/// The runtimes to create, keep and stop for a snapshot: the missing are
/// created, the absent stopped, the rest handed again. (An account never
/// changes what it is: a person's subject renews from a refresh token, a test
/// account's `svc:` subject from its API token.)
pub(crate) fn plan(current: &BTreeSet<AccountKey>, desired: &BTreeSet<AccountKey>) -> Plan {
    Plan {
        create: desired.difference(current).cloned().collect(),
        keep: desired.intersection(current).cloned().collect(),
        remove: current.difference(desired).cloned().collect(),
    }
}

/// What every runtime shares.
#[derive(Clone)]
struct Machine {
    dirs: Result<Dirs, String>,
    /// The live catalogs and the core daemons every account's adapter shares.
    shared: Shared,
    settings: Arc<Mutex<MachineSettings>>,
    settings_path: Option<PathBuf>,
}

pub(crate) struct AccountRuntime {
    key: AccountKey,
    dir: Option<PathBuf>,
    pub(crate) adapter: Adapter,
    pub(crate) supervisor: Supervisor,
    keeper: OnceLock<SessionKeeper>,
    machine: Machine,
    account: Mutex<AccountSettings>,
    tenant: Mutex<String>,
    /// A refresh credential: the account can serve agents from this machine.
    can_serve: bool,
    /// The bridge starts with the next session (the account just came up, or
    /// was signed out): a renewal never starts one the person stopped.
    bridge_pending: AtomicBool,
    had_session: AtomicBool,
    /// Whether the account is leaving (`shutdown` set it first): nothing the
    /// keeper makes after that starts anything again. Held for the whole of
    /// what a session or a sign-out starts, so a shutdown waits for it and
    /// then stops what it started.
    lifecycle: Mutex<bool>,
}

/// What an adapter asks of its account's bridge.
struct RuntimeBridge(Weak<AccountRuntime>);

impl BridgeControl for RuntimeBridge {
    fn settings(&self) -> Settings {
        self.0.upgrade().map(|rt| rt.settings()).unwrap_or_default()
    }

    fn status(&self) -> Status {
        self.0
            .upgrade()
            .map(|rt| rt.supervisor.status())
            .unwrap_or_else(Supervisor::stopped_status)
    }

    fn start(&self, settings: Settings) -> Result<(), String> {
        match self.0.upgrade() {
            Some(rt) => rt.supervisor.start(settings),
            None => Ok(()),
        }
    }
}

/// What the account's keeper makes, for the runtime to apply.
struct RuntimeSink(Weak<AccountRuntime>);

impl KeeperSink for RuntimeSink {
    fn session(&self, token: String) {
        if let Some(rt) = self.0.upgrade() {
            rt.on_session(token);
        }
    }

    fn family(&self, token: Option<String>) {
        if let Some(rt) = self.0.upgrade() {
            rt.supervisor.set_refresh_token(token);
        }
    }

    fn signed_out(&self) {
        if let Some(rt) = self.0.upgrade() {
            rt.on_signed_out();
        }
    }
}

impl AccountRuntime {
    fn create(
        machine: &Machine,
        emit: &AccountEmit,
        entry: &AccountEntry,
        key: AccountKey,
    ) -> Arc<Self> {
        let dir = machine
            .dirs
            .as_ref()
            .ok()
            .map(|dirs| key.dir(dirs.config()));
        let supervisor = {
            let (emit, key) = (Arc::clone(emit), key.clone());
            let sink: StatusEmit = Arc::new(move |status| emit(&key, status));
            Supervisor::new(sink)
        };
        let account = match dir.as_deref().map(load_account_settings) {
            Some(Ok(settings)) => settings,
            Some(Err(e)) => {
                // Shown in the account's bridge log; the unreadable file was
                // moved aside, never overwritten.
                supervisor.note(format!("[settings] {e}; starting with defaults"));
                AccountSettings::default()
            }
            None => AccountSettings::default(),
        };
        let can_serve = entry.credential.can_serve();
        let runtime = Arc::new_cyclic(|weak: &Weak<AccountRuntime>| {
            let adapter = Adapter::new(
                machine.dirs.clone(),
                key.gateway(),
                machine.shared.clone(),
                &key.dir_name(),
                can_serve,
                Arc::new(RuntimeBridge(weak.clone())),
            );
            AccountRuntime {
                key: key.clone(),
                dir,
                adapter,
                supervisor,
                keeper: OnceLock::new(),
                machine: machine.clone(),
                account: Mutex::new(account),
                tenant: Mutex::new(entry.tenant.trim().to_string()),
                can_serve,
                bridge_pending: AtomicBool::new(true),
                had_session: AtomicBool::new(false),
                lifecycle: Mutex::new(false),
            }
        });
        runtime.supervisor.attach(runtime.adapter.clone());
        let keeper = SessionKeeper::start(
            key.gateway(),
            &entry.tenant,
            entry.credential.clone(),
            Arc::new(RuntimeSink(Arc::downgrade(&runtime))),
            Timing::LIVE,
        );
        let _ = runtime.keeper.set(keeper);
        eprintln!("[accounts] {} is live", runtime.key);
        runtime
    }

    pub(crate) fn key(&self) -> &AccountKey {
        &self.key
    }

    /// The merged settings the web and the bridge read.
    pub(crate) fn settings(&self) -> Settings {
        Settings::merged(
            &self.machine.settings.lock(),
            &self.account.lock(),
            &self.key,
            &self.tenant.lock(),
        )
    }

    pub(crate) fn keeper_status(&self) -> KeeperStatus {
        self.keeper
            .get()
            .map(SessionKeeper::status)
            .unwrap_or(KeeperStatus::Retrying)
    }

    /// Whether this computer serves the account's agents (a refresh account
    /// whose person did not turn it off).
    fn serving(&self) -> bool {
        self.can_serve && self.account.lock().serve
    }

    fn on_session(&self, token: String) {
        let closed = self.lifecycle.lock();
        if *closed {
            return;
        }
        let first = !self.had_session.swap(true, Ordering::SeqCst);
        let in_tenant = crate::internal_ext::member_dir::token_member(&token).map(|m| m.tenant);
        self.adapter.update_gateway(self.key.gateway(), token);
        self.adapter.start();
        if first {
            self.adapter.refresh_in_background();
        }
        if self.bridge_pending.swap(false, Ordering::SeqCst) {
            self.ensure_bridge();
        } else if self.bridge_acts_in_another_tenant(in_tenant.as_deref()) {
            // The account moved to another tenant and its session now says
            // so: the bridge restarts once, with the tenant's cores and
            // sandbox (it would otherwise start before the session
            // is there to hand them, and again when it is).
            self.ensure_bridge();
        }
        drop(closed);
    }

    /// Whether a running bridge acts in a tenant other than the one the vault
    /// wants and `session_tenant` (the tenant the session just minted is in)
    /// is that one.
    fn bridge_acts_in_another_tenant(&self, session_tenant: Option<&str>) -> bool {
        let wanted = self.tenant.lock().clone();
        !wanted.is_empty()
            && session_tenant == Some(wanted.as_str())
            && self
                .supervisor
                .running_tenant()
                .is_some_and(|t| t != wanted)
    }

    /// The account cannot renew: nothing may keep acting as it (no catalog
    /// fetch, no member stream, no bridge); a new credential brings it back.
    fn on_signed_out(&self) {
        let closed = self.lifecycle.lock();
        if *closed {
            return;
        }
        self.had_session.store(false, Ordering::SeqCst);
        self.bridge_pending.store(true, Ordering::SeqCst);
        self.supervisor.set_refresh_token(None);
        self.supervisor.stop();
        self.adapter
            .update_gateway(self.key.gateway(), String::new());
        drop(closed);
    }

    /// Start the bridge when the account serves from this computer.
    fn ensure_bridge(&self) {
        if !self.serving() {
            return;
        }
        let settings = self.settings();
        if let Err(e) = self.supervisor.start(settings) {
            self.supervisor
                .note(format!("[accounts] bridge not started: {e}"));
        }
    }

    /// The person's switch: serve this account's agents from this computer
    /// (starts, or restarts, the bridge) or not (stops it). Kept.
    pub(crate) fn set_serving(&self, serve: bool) -> Result<(), String> {
        if serve && !self.can_serve {
            return Err("a test account has no agents to serve".into());
        }
        self.update_account(|account| account.serve = serve)?;
        if serve {
            self.supervisor.start(self.settings())
        } else {
            self.supervisor.stop();
            Ok(())
        }
    }

    /// Change the account's own settings and keep them.
    fn update_account(&self, edit: impl FnOnce(&mut AccountSettings)) -> Result<(), String> {
        let mut account = self.account.lock();
        let mut next = account.clone();
        edit(&mut next);
        if let Some(dir) = &self.dir {
            save_account_settings(dir, &next)?;
        } else {
            return Err("settings dir unavailable".into());
        }
        *account = next;
        Ok(())
    }

    /// The vault handed this account again: its credential (used when the
    /// keeper has no family of its own) and its tenant.
    fn hand(&self, entry: &AccountEntry) {
        let Some(keeper) = self.keeper.get() else {
            return;
        };
        keeper.hand(entry.credential.clone());
        let tenant = entry.tenant.trim().to_string();
        let changed = {
            let mut current = self.tenant.lock();
            std::mem::replace(&mut *current, tenant.clone()) != tenant
        };
        // The keeper moves the account's session to the tenant; the bridge
        // follows when that session arrives (`on_session`).
        if changed {
            keeper.rescope(&tenant);
        }
    }

    /// Stop everything that acts as this account, then end its sign-in on the
    /// gateway (the bridge's fork goes with it). A failed revoke leaves the
    /// family to expire: the runtime is stopped first either way.
    async fn shutdown(self: Arc<Self>) {
        // From here the keeper's sessions start nothing (a session in flight
        // finishes first, and what it started is stopped below).
        *self.lifecycle.lock() = true;
        self.supervisor.stop();
        let children = self.adapter.detach_all();
        child_process::terminate(children, child_process::STOP_GRACE).await;
        self.adapter
            .update_gateway(self.key.gateway(), String::new());
        // Its hold on the shared cores ends here: they keep running for the
        // accounts that still hold them.
        self.adapter.leave();
        if let Some(keeper) = self.keeper.get() {
            keeper.stop().await;
        }
        self.supervisor.release();
        eprintln!("[accounts] {} stopped", self.key);
    }

    /// Detach everything this runtime supervises, for one joint stop (app
    /// exit, an update install).
    fn detach(&self) -> Vec<Child> {
        let mut children = Vec::new();
        children.extend(self.supervisor.detach());
        children.extend(self.adapter.detach_all());
        children
    }

    /// Bring what `detach` stopped back up.
    fn resume(&self) {
        if self.serving() {
            if let Err(e) = self.supervisor.start(self.settings()) {
                eprintln!("[shell] bridge restart after failed update: {e}");
            }
        }
        self.adapter.start();
        self.adapter.refresh_in_background();
    }
}

/// What the web shows of an account beside its name (the switcher).
#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AccountStatus {
    pub(crate) gateway_url: String,
    pub(crate) subject: String,
    /// "active", "retrying" (no session yet, or the gateway is down) or
    /// "signedOut" (its credential was refused: sign in again).
    pub(crate) session: &'static str,
    /// Whether this account serves agents from this computer at all (a test
    /// account never does) and whether its person left that on.
    pub(crate) can_serve: bool,
    pub(crate) serving: bool,
    pub(crate) bridge: State,
    pub(crate) running_turns: u32,
}

impl AccountRuntime {
    pub(crate) fn status(&self) -> AccountStatus {
        AccountStatus {
            gateway_url: self.key.gateway().to_string(),
            subject: self.key.subject().to_string(),
            session: match self.keeper_status() {
                KeeperStatus::Active => "active",
                KeeperStatus::Retrying => "retrying",
                KeeperStatus::SignedOut => "signedOut",
            },
            can_serve: self.can_serve,
            serving: self.serving(),
            bridge: self.supervisor.status().state,
            running_turns: self.adapter.running_turns(),
        }
    }
}

/// Where a runtime's bridge status goes: the windows that show the account.
pub(crate) type AccountEmit = Arc<dyn Fn(&AccountKey, &Status) + Send + Sync>;

#[derive(Default)]
struct Inner {
    runtimes: BTreeMap<AccountKey, Arc<AccountRuntime>>,
    windows: HashMap<String, AccountKey>,
}

/// The accounts of this app and the windows bound to them.
pub(crate) struct Registry {
    machine: Machine,
    emit: AccountEmit,
    inner: Mutex<Inner>,
    /// The revision of the newest vault snapshot reconciled to: an older one
    /// (a window's, arriving late) is dropped, or it would stop and revoke an
    /// account another window just added.
    revision: Mutex<u64>,
    /// The accounts whose runtime is shutting down (still holding its
    /// directory).
    stopping: Arc<Mutex<BTreeSet<AccountKey>>>,
}

/// How long `forget_local_data` waits for an account that is stopping.
const FORGET_TICK: std::time::Duration = std::time::Duration::from_millis(100);
const FORGET_WAIT_TICKS: u32 = 150;

/// The event the web reads a bridge's status from.
pub(crate) const STATUS_EVENT: &str = "bridge://status";

impl Registry {
    pub(crate) fn new(
        dirs: Result<Dirs, String>,
        machine: MachineSettings,
        emit: AccountEmit,
    ) -> Self {
        let settings_path = dirs.as_ref().ok().map(Dirs::settings_file);
        if let Ok(dirs) = &dirs {
            crate::internal_ext::remove_retired_state(dirs);
        }
        Self {
            machine: Machine {
                shared: Shared::new(dirs.clone()),
                dirs,
                settings: Arc::new(Mutex::new(machine)),
                settings_path,
            },
            emit,
            inner: Mutex::new(Inner::default()),
            revision: Mutex::new(0),
            stopping: Arc::default(),
        }
    }

    /// The windows that show an account.
    pub(crate) fn windows_of(&self, key: &AccountKey) -> Vec<String> {
        self.inner
            .lock()
            .windows
            .iter()
            .filter(|(_, bound)| *bound == key)
            .map(|(label, _)| label.clone())
            .collect()
    }

    /// Make the runtimes match the vault's snapshot (see the module docs).
    /// `revision` is the vault's own count of its writes: a snapshot older than
    /// the newest reconciled to is dropped (false). Entries that name no
    /// gateway or subject are skipped.
    pub(crate) fn reconcile(&self, revision: u64, entries: Vec<AccountEntry>) -> bool {
        {
            let mut newest = self.revision.lock();
            if revision < *newest {
                return false;
            }
            *newest = revision;
        }
        let desired: BTreeMap<AccountKey, AccountEntry> = entries
            .into_iter()
            .filter_map(|entry| {
                let key = AccountKey::new(&entry.gateway_url, &entry.subject)?;
                Some((key, entry))
            })
            .collect();
        let mut stopping = Vec::new();
        let mut handed = Vec::new();
        {
            let mut inner = self.inner.lock();
            let current: BTreeSet<AccountKey> = inner.runtimes.keys().cloned().collect();
            let wanted: BTreeSet<AccountKey> = desired.keys().cloned().collect();
            let plan = plan(&current, &wanted);
            // Stopped accounts first: no window or command reaches them again.
            for key in &plan.remove {
                if let Some(rt) = inner.runtimes.remove(key) {
                    // Counted as stopping from the moment it leaves the map.
                    self.stopping.lock().insert(key.clone());
                    stopping.push(rt);
                }
                inner.windows.retain(|_, bound| bound != key);
            }
            for key in &plan.create {
                let runtime =
                    AccountRuntime::create(&self.machine, &self.emit, &desired[key], key.clone());
                inner.runtimes.insert(key.clone(), runtime);
            }
            for key in &plan.keep {
                if let Some(rt) = inner.runtimes.get(key) {
                    handed.push((Arc::clone(rt), &desired[key]));
                }
            }
        }
        // Outside the lock: handing may restart a bridge, whose status goes to
        // the windows that show it, which the registry looks up under it.
        for (rt, entry) in handed {
            rt.hand(entry);
        }
        for rt in stopping {
            let (key, leaving) = (rt.key().clone(), Arc::clone(&self.stopping));
            tauri::async_runtime::spawn(async move {
                rt.shutdown().await;
                leaving.lock().remove(&key);
            });
        }
        true
    }

    /// Forget what this computer keeps of an account that left the vault: its
    /// own directory (its settings, every tenant's member state: component
    /// values, sign-ins to tools, sandbox disks), removed once its runtime is
    /// gone. An account still live is refused (it stops by leaving the vault,
    /// never by this); one that is stopping is waited for, a while. The
    /// account's workspace folders under the shared workspaces root are the
    /// person's files and stay.
    pub(crate) async fn forget_local_data(&self, key: &AccountKey) -> Result<(), String> {
        self.forget_local_data_within(key, FORGET_WAIT_TICKS).await
    }

    async fn forget_local_data_within(&self, key: &AccountKey, ticks: u32) -> Result<(), String> {
        for _ in 0..ticks {
            if !self.stopping_or_live(key) {
                let Some(dir) = self.account_dir(key) else {
                    return Ok(());
                };
                return match std::fs::remove_dir_all(&dir) {
                    Ok(()) => Ok(()),
                    Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
                    Err(e) => Err(format!("remove {}: {e}", dir.display())),
                };
            }
            tokio::time::sleep(FORGET_TICK).await;
        }
        Err(format!("{key} is still signed in here: remove it first"))
    }

    fn account_dir(&self, key: &AccountKey) -> Option<PathBuf> {
        self.machine
            .dirs
            .as_ref()
            .ok()
            .map(|dirs| key.dir(dirs.config()))
    }

    /// Whether the account still has a runtime, or one that is shutting down.
    fn stopping_or_live(&self, key: &AccountKey) -> bool {
        self.inner.lock().runtimes.contains_key(key) || self.stopping.lock().contains(key)
    }

    /// The runtime of the account a window shows.
    pub(crate) fn runtime_of_window(&self, label: &str) -> Option<Arc<AccountRuntime>> {
        let inner = self.inner.lock();
        inner
            .windows
            .get(label)
            .and_then(|key| inner.runtimes.get(key))
            .cloned()
    }

    /// Bind a window to an account of the registry.
    pub(crate) fn bind(&self, label: &str, key: &AccountKey) -> Result<(), String> {
        let mut inner = self.inner.lock();
        if !inner.runtimes.contains_key(key) {
            return Err(format!("{key} is not signed in on this computer"));
        }
        inner.windows.insert(label.to_string(), key.clone());
        Ok(())
    }

    /// The account a window shows.
    pub(crate) fn account_of_window(&self, label: &str) -> Option<AccountKey> {
        let inner = self.inner.lock();
        inner
            .windows
            .get(label)
            .filter(|key| inner.runtimes.contains_key(*key))
            .cloned()
    }

    /// Bind a window to the account another window shows (a pop-out
    /// inherits its opener's). Err when the opener shows none.
    pub(crate) fn inherit(&self, label: &str, opener: &str) -> Result<AccountKey, String> {
        let mut inner = self.inner.lock();
        let key = inner
            .windows
            .get(opener)
            .filter(|key| inner.runtimes.contains_key(*key))
            .cloned()
            .ok_or(NOT_BOUND)?;
        inner.windows.insert(label.to_string(), key.clone());
        Ok(key)
    }

    pub(crate) fn unbind(&self, label: &str) {
        self.inner.lock().windows.remove(label);
    }

    /// Apply the web's settings form to `rt`'s account: the machine's part
    /// is shared (every running bridge restarts with it when it changed),
    /// the account's part is its own.
    pub(crate) fn apply_settings(
        &self,
        rt: &Arc<AccountRuntime>,
        settings: Settings,
    ) -> Result<(), String> {
        let (machine, account) = settings.split(rt.account.lock().serve);
        let machine_changed = {
            let mut current = self.machine.settings.lock();
            let changed = *current != machine;
            if changed {
                let path = self
                    .machine
                    .settings_path
                    .as_deref()
                    .ok_or("settings dir unavailable")?;
                save_machine_settings(path, &machine)?;
                *current = machine;
            }
            changed
        };
        rt.update_account(|current| *current = account)?;
        let affected: Vec<Arc<AccountRuntime>> = if machine_changed {
            self.inner.lock().runtimes.values().cloned().collect()
        } else {
            vec![Arc::clone(rt)]
        };
        // Every account's bridge restarts, whatever one's start says: the
        // first refusal is reported after the others have their turn.
        let mut first_error = None;
        for runtime in affected {
            if runtime.serving() {
                if let Err(e) = runtime.supervisor.start(runtime.settings()) {
                    first_error.get_or_insert(e);
                }
            } else {
                runtime.supervisor.stop();
            }
        }
        if let Some(e) = first_error {
            return Err(e);
        }
        Ok(())
    }

    /// The agent turns every account's bridge drives now: an update that
    /// restarts bridges (the app's own, a core's) warns about all of them.
    pub(crate) fn running_turns(&self) -> u32 {
        self.machine.shared.cores.running_turns()
    }

    /// Every account's status, by account.
    pub(crate) fn statuses(&self) -> Vec<AccountStatus> {
        self.runtimes().iter().map(|rt| rt.status()).collect()
    }

    pub(crate) fn runtimes(&self) -> Vec<Arc<AccountRuntime>> {
        self.inner.lock().runtimes.values().cloned().collect()
    }

    /// Detach everything every runtime supervises (app exit, an update).
    pub(crate) fn detach_all(&self) -> Vec<Child> {
        let mut children: Vec<Child> = self.runtimes().iter().flat_map(|rt| rt.detach()).collect();
        // The core daemons are the machine's: stopped with everything else.
        children.extend(self.machine.shared.cores.detach_all());
        children
    }

    pub(crate) fn resume_all(&self) {
        for rt in self.runtimes() {
            rt.resume();
        }
    }

    /// End every account's own sign-in (app exit): each family is revoked,
    /// bounded, so a restart does not leave dangling sign-ins in the
    /// member's session list.
    pub(crate) fn revoke_all(&self) {
        let keepers: Vec<SessionKeeper> = self
            .runtimes()
            .iter()
            .filter_map(|rt| rt.keeper.get().cloned())
            .collect();
        tauri::async_runtime::block_on(async move {
            futures_util::future::join_all(keepers.iter().map(SessionKeeper::stop)).await;
        });
    }
}

/// The runtime of the account the calling window shows: a command takes it as
/// an argument and never reaches another account's.
pub(crate) struct Account(pub(crate) Arc<AccountRuntime>);

impl std::ops::Deref for Account {
    type Target = AccountRuntime;
    fn deref(&self) -> &AccountRuntime {
        &self.0
    }
}

impl<'de, R: Runtime> CommandArg<'de, R> for Account {
    fn from_command(command: CommandItem<'de, R>) -> Result<Self, InvokeError> {
        let webview = command.message.webview();
        webview
            .try_state::<Registry>()
            .and_then(|registry| registry.runtime_of_window(webview.label()))
            .map(Account)
            .ok_or_else(|| InvokeError::from(NOT_BOUND))
    }
}

#[cfg(test)]
#[path = "runtime_tests.rs"]
mod runtime_tests;
