use super::*;
use crate::settings::{CoreConfig, DefaultAgent};
use std::sync::atomic::AtomicU64;
use std::time::Duration;

/// Each snapshot of a test is newer than the one before.
fn next_revision() -> u64 {
    static REVISION: AtomicU64 = AtomicU64::new(1);
    REVISION.fetch_add(1, Ordering::SeqCst)
}

const GW: &str = "http://127.0.0.1:9";

fn key(subject: &str) -> AccountKey {
    AccountKey::new(GW, subject).unwrap()
}

fn keys(subjects: &[&str]) -> BTreeSet<AccountKey> {
    subjects.iter().map(|s| key(s)).collect()
}

/// The runtimes to create, keep and stop for a vault snapshot: the missing
/// are created, the absent stopped, the rest handed again.
#[test]
fn a_snapshot_creates_keeps_and_stops_runtimes() {
    let running = keys(&["maya", "jack", "qa"]);
    let plan = plan(&running, &keys(&["maya", "ada", "qa"]));
    assert_eq!(plan.create, vec![key("ada")]);
    let mut keep = plan.keep.clone();
    keep.sort();
    assert_eq!(keep, vec![key("maya"), key("qa")]);
    assert_eq!(plan.remove, vec![key("jack")]);

    let empty = BTreeSet::new();
    assert_eq!(
        super::plan(&empty, &empty),
        Plan::default(),
        "nothing to do is nothing done"
    );
    assert_eq!(
        super::plan(&running, &empty).remove.len(),
        3,
        "an empty vault stops everyone"
    );
    assert_eq!(super::plan(&empty, &running).create.len(), 3);
}

struct Fixture {
    root: PathBuf,
    registry: Arc<Registry>,
    /// Set when a status event reached the registry's window lookup.
    emitted: Arc<AtomicBool>,
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

/// A registry whose status events look the windows up the way the app's do:
/// under the registry's own lock.
fn fixture(tag: &str) -> Fixture {
    let root = std::env::temp_dir().join(format!("bnbloc-runtime-{tag}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    let slot: Arc<OnceLock<Weak<Registry>>> = Arc::default();
    let emitted = Arc::new(AtomicBool::new(false));
    let (looked_up, flag) = (Arc::clone(&slot), Arc::clone(&emitted));
    let registry = Arc::new(Registry::new(
        Ok(Dirs::under(&root)),
        MachineSettings::default(),
        Arc::new(move |key, _| {
            if let Some(registry) = looked_up.get().and_then(Weak::upgrade) {
                let _ = registry.windows_of(key);
                flag.store(true, Ordering::SeqCst);
            }
        }),
    ));
    let _ = slot.set(Arc::downgrade(&registry));
    Fixture {
        root,
        registry,
        emitted,
    }
}

fn entry(subject: &str, tenant: &str, credential: Credential) -> AccountEntry {
    AccountEntry {
        gateway_url: GW.into(),
        subject: subject.into(),
        tenant: tenant.into(),
        credential,
    }
}

fn person(subject: &str) -> AccountEntry {
    entry(
        subject,
        "org-a",
        Credential::Refresh(format!("refresh-{subject}")),
    )
}

fn tester(subject: &str) -> AccountEntry {
    entry(
        subject,
        "org-a",
        Credential::ApiToken(format!("svc-{subject}")),
    )
}

fn runtime(f: &Fixture, subject: &str) -> Arc<AccountRuntime> {
    f.registry
        .runtimes()
        .into_iter()
        .find(|rt| rt.key() == &key(subject))
        .unwrap_or_else(|| panic!("{subject} is not live"))
}

/// Windows resolve their own account and nothing else: a binding is to a
/// live account, a pop-out inherits its opener's, a gone window shows none,
/// and an account that leaves the vault leaves its windows unbound.
#[test]
fn windows_show_their_own_account() {
    let f = fixture("windows");
    f.registry
        .reconcile(next_revision(), vec![person("maya"), person("jack")]);
    assert_eq!(f.registry.runtimes().len(), 2);

    assert!(
        f.registry.runtime_of_window("main").is_none(),
        "unbound until the page binds"
    );
    assert!(
        f.registry.bind("main", &key("ada")).is_err(),
        "not a live account"
    );
    f.registry.bind("main", &key("maya")).unwrap();
    f.registry.bind("second", &key("jack")).unwrap();
    assert_eq!(
        f.registry.runtime_of_window("main").unwrap().key(),
        &key("maya")
    );
    assert_eq!(
        f.registry.runtime_of_window("second").unwrap().key(),
        &key("jack")
    );

    assert_eq!(f.registry.inherit("pop", "main").unwrap(), key("maya"));
    let mut labels = f.registry.windows_of(&key("maya"));
    labels.sort();
    assert_eq!(labels, vec!["main".to_string(), "pop".to_string()]);
    assert!(
        f.registry.inherit("orphan", "nowhere").is_err(),
        "an opener that shows nothing"
    );

    f.registry.unbind("pop");
    assert!(f.registry.runtime_of_window("pop").is_none());

    // Maya leaves the vault: her runtime and her windows go, Jack is untouched.
    f.registry.reconcile(next_revision(), vec![person("jack")]);
    assert!(f.registry.runtime_of_window("main").is_none());
    assert_eq!(
        f.registry.runtime_of_window("second").unwrap().key(),
        &key("jack")
    );
    assert_eq!(f.registry.runtimes().len(), 1);
}

/// Handing the same account again keeps its runtime; only its tenant moves.
#[test]
fn a_new_handoff_keeps_the_runtime_and_moves_the_tenant() {
    let f = fixture("hand");
    f.registry.reconcile(
        next_revision(),
        vec![entry("maya", "org-a", Credential::Refresh("r1".into()))],
    );
    let first = runtime(&f, "maya");
    assert_eq!(first.settings().tenant, "org-a");
    f.registry.reconcile(
        next_revision(),
        vec![entry("maya", "org-b", Credential::Refresh("r2".into()))],
    );
    let again = runtime(&f, "maya");
    assert!(Arc::ptr_eq(&first, &again), "the same runtime");
    assert_eq!(again.settings().tenant, "org-b");
    assert_eq!(
        (
            again.settings().owner.as_str(),
            again.settings().gateway_url.as_str()
        ),
        ("maya", GW),
        "the bridge acts as the account on its gateway"
    );
}

/// Each account keeps its own choices in its own directory; the machine's
/// settings are shared, and a change of them reaches every account.
#[test]
fn settings_are_split_between_the_machine_and_each_account() {
    let f = fixture("settings");
    f.registry
        .reconcile(next_revision(), vec![person("maya"), person("jack")]);
    let (maya, jack) = (runtime(&f, "maya"), runtime(&f, "jack"));
    // Neither serves from this test (no bridge binary to start).
    maya.set_serving(false).unwrap();
    jack.set_serving(false).unwrap();

    let mut form = maya.settings();
    form.default_agent = DefaultAgent {
        core: "acme".into(),
        provider: "codex".into(),
    };
    form.disabled_providers = vec!["acme/cursor".into()];
    form.sync_secret = "maya-secret".into();
    form.cores = vec![CoreConfig {
        name: "acme".into(),
        kind: "local".into(),
        url: None,
        token: None,
    }];
    // What the form says about the account itself is not a setting.
    form.owner = "someone-else".into();
    form.gateway_url = "https://elsewhere.example.com".into();
    f.registry.apply_settings(&maya, form).unwrap();

    let m = maya.settings();
    assert_eq!(m.default_agent.provider, "codex");
    assert_eq!(m.sync_secret, "maya-secret");
    assert_eq!((m.owner.as_str(), m.gateway_url.as_str()), ("maya", GW));

    let j = jack.settings();
    assert_eq!(j.cores.len(), 1, "the machine's cores are shared");
    assert_eq!(
        j.default_agent,
        DefaultAgent::default(),
        "Maya's choices are not Jack's"
    );
    assert!(j.sync_secret.is_empty() && j.disabled_providers.is_empty());

    // On disk: the machine's file carries cores only, each account's own
    // file its own choices, owner-only.
    let machine_file = Dirs::under(&f.root).settings_file();
    let machine: MachineSettings =
        serde_json::from_slice(&std::fs::read(&machine_file).unwrap()).unwrap();
    assert_eq!(machine.cores.len(), 1);
    let raw = std::fs::read_to_string(&machine_file).unwrap();
    assert!(
        !raw.contains("maya-secret"),
        "no account's secret in the machine's file: {raw}"
    );
    let dir = key("maya").dir(Dirs::under(&f.root).config());
    let saved = load_account_settings(&dir).unwrap();
    assert_eq!(saved.sync_secret, "maya-secret");
    assert!(!saved.serve, "the switch is kept");
    assert!(
        load_account_settings(&key("jack").dir(Dirs::under(&f.root).config()))
            .unwrap()
            .sync_secret
            .is_empty()
    );
}

/// Whether this computer serves an account's agents is the person's switch
/// and a test account has no agents to serve.
#[test]
fn a_test_account_cannot_serve_agents() {
    let f = fixture("serve");
    f.registry
        .reconcile(next_revision(), vec![tester("qa"), person("maya")]);
    let qa = runtime(&f, "qa");
    assert!(
        qa.set_serving(true).is_err(),
        "a test account has no bridge"
    );
    assert!(!qa.status().serving && !qa.status().can_serve);
    let maya = runtime(&f, "maya");
    maya.set_serving(false).unwrap();
    assert!(maya.status().can_serve && !maya.status().serving);
    assert_eq!(maya.status().session, "retrying", "no gateway answered yet");
}

/// An unreadable account-settings file does not stop the account: it starts
/// on defaults and says why in the bridge log.
#[test]
fn an_unreadable_account_settings_file_starts_on_defaults() {
    let f = fixture("corrupt");
    let dir = key("maya").dir(Dirs::under(&f.root).config());
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(
        dir.join(crate::account::ACCOUNT_SETTINGS_FILE),
        b"{\"syncSecret\":\"keep",
    )
    .unwrap();
    f.registry.reconcile(next_revision(), vec![person("maya")]);
    let maya = runtime(&f, "maya");
    assert!(maya.settings().sync_secret.is_empty());
    assert!(
        maya.supervisor
            .status()
            .logs_tail
            .iter()
            .any(|line| line.contains("moved aside")),
        "the reason is in the log: {:?}",
        maya.supervisor.status().logs_tail
    );
}

/// Entries that name no gateway or no subject are not accounts.
#[test]
fn entries_without_a_gateway_or_a_subject_are_skipped() {
    let f = fixture("skip");
    let mut no_gateway = person("maya");
    no_gateway.gateway_url.clear();
    let mut no_subject = person("jack");
    no_subject.subject = "  ".into();
    f.registry
        .reconcile(next_revision(), vec![no_gateway, no_subject, person("ada")]);
    let live: Vec<_> = f
        .registry
        .runtimes()
        .iter()
        .map(|rt| rt.key().subject().to_string())
        .collect();
    assert_eq!(live, vec!["ada".to_string()]);
}

/// The web's snapshot, as JSON, is what the shell reads.
#[test]
fn the_web_snapshot_parses() {
    let entries: Vec<AccountEntry> = serde_json::from_str(
        r#"[
            {"gatewayUrl":"https://gw.example.com","subject":"maya","tenant":"org-a",
             "credential":{"kind":"refresh","token":"r"}},
            {"gatewayUrl":"https://gw.example.com","subject":"svc:qa",
             "credential":{"kind":"apiToken","token":"t"}}
        ]"#,
    )
    .unwrap();
    assert_eq!(entries.len(), 2);
    assert!(entries[0].credential.can_serve() && !entries[1].credential.can_serve());
    assert_eq!(entries[1].tenant, "", "a tenant is optional");
}

/// The web waits and retries on exactly the answer the shell gives.
#[test]
fn the_web_recognizes_the_unbound_answer() {
    let web = include_str!("../../../../web/src/lib/desktopAccounts.ts");
    assert!(
        web.contains(&format!("NOT_BOUND = \"{NOT_BOUND}\"")),
        "web/src/lib/desktopAccounts.ts NOT_BOUND must be {NOT_BOUND:?}"
    );
}

/// Handing an account again never reports a bridge's status while the registry
/// is locked: the status event looks the windows up in the registry, which
/// would wait on itself.
#[test]
fn handing_an_account_again_emits_nothing_under_the_registry_lock() {
    let f = fixture("tenant-restart");
    f.registry.reconcile(
        next_revision(),
        vec![entry("maya", "org-a", Credential::Refresh("r".into()))],
    );
    let maya = runtime(&f, "maya");
    maya.supervisor.start(maya.settings()).unwrap();
    f.emitted.store(false, Ordering::SeqCst);

    let registry = Arc::clone(&f.registry);
    let (done, finished) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        registry.reconcile(
            next_revision(),
            vec![entry("maya", "org-b", Credential::Refresh("r".into()))],
        );
        let _ = done.send(());
    });
    finished
        .recv_timeout(Duration::from_secs(10))
        .expect("reconcile deadlocked");
    assert_eq!(maya.settings().tenant, "org-b");
    assert!(
        !f.emitted.load(Ordering::SeqCst),
        "handing restarts nothing by itself: the session that follows does"
    );
    maya.supervisor.stop();
}

/// An older snapshot (a window's, arriving late) never undoes a newer one:
/// it would stop and revoke an account another window just added.
#[test]
fn an_older_snapshot_is_dropped() {
    let f = fixture("stale");
    let (old, new) = (next_revision(), next_revision());
    assert!(f
        .registry
        .reconcile(new, vec![person("maya"), person("jack")]));
    assert!(
        !f.registry.reconcile(old, vec![person("maya")]),
        "older: dropped"
    );
    assert_eq!(f.registry.runtimes().len(), 2, "Jack is still live");
    assert!(
        f.registry
            .reconcile(new, vec![person("maya"), person("jack")]),
        "the same revision is the same vault"
    );
    assert!(
        f.registry.reconcile(next_revision(), vec![person("maya")]),
        "newer: applied"
    );
    assert_eq!(f.registry.runtimes().len(), 1);
}

/// A session the keeper makes while the account is leaving starts nothing:
/// the shutdown sets the flag first, and what a session in flight started
/// is stopped after it.
#[tokio::test]
async fn a_session_after_the_shutdown_starts_nothing() {
    let f = fixture("closing");
    f.registry.reconcile(next_revision(), vec![person("maya")]);
    let maya = runtime(&f, "maya");
    maya.set_serving(false).unwrap();
    let leaving = Arc::clone(&maya);
    tauri::async_runtime::spawn(leaving.shutdown())
        .await
        .unwrap();
    // The keeper (still making sessions in the real app) now reports one.
    maya.on_session("late-session".into());
    assert!(
        !maya.had_session.load(Ordering::SeqCst),
        "a late session was not taken (it would start the adapter and the bridge again)"
    );
    assert!(
        maya.adapter.status().is_empty() && maya.supervisor.status().state == State::Stopped,
        "a late session started nothing"
    );
    assert_eq!(
        maya.supervisor.status().pid,
        None,
        "and the bridge did not come back"
    );
}

/// An account that moves to another tenant restarts its bridge once, when its
/// session is in that tenant (not when the vault says so: the bridge would
/// start before the session is there to hand it its cores, and again when it
/// is).
#[test]
fn a_tenant_change_restarts_the_bridge_when_the_session_follows() {
    let f = fixture("tenant-once");
    f.registry.reconcile(
        next_revision(),
        vec![entry("maya", "org-a", Credential::Refresh("r".into()))],
    );
    let maya = runtime(&f, "maya");
    let session = |tenant: &str| {
        crate::internal_ext::member_dir::session_token(
            serde_json::json!({"sub": "maya", "tenant": tenant}),
        )
    };
    maya.on_session(session("org-a")); // first session: the bridge starts for org-a
    assert_eq!(maya.supervisor.running_tenant().as_deref(), Some("org-a"));
    maya.supervisor.stop();
    maya.supervisor.start(maya.settings()).unwrap(); // running in org-a

    // The vault moves the account: nothing restarts yet.
    f.registry.reconcile(
        next_revision(),
        vec![entry("maya", "org-b", Credential::Refresh("r".into()))],
    );
    assert_eq!(
        maya.supervisor.running_tenant().as_deref(),
        Some("org-a"),
        "not before the session follows"
    );
    // A renewal that is still in the old tenant changes nothing.
    maya.on_session(session("org-a"));
    assert_eq!(maya.supervisor.running_tenant().as_deref(), Some("org-a"));
    // The session arrives in the new tenant: the bridge restarts, once.
    maya.on_session(session("org-b"));
    assert_eq!(maya.supervisor.running_tenant().as_deref(), Some("org-b"));
    maya.on_session(session("org-b"));
    maya.supervisor.stop();
}

/// Forgetting an account's data removes its directory (settings, every
/// tenant's member state) once its runtime is gone; one still live is
/// refused, one that is stopping is waited for; other accounts' directories
/// are never touched.
#[tokio::test]
async fn forgetting_an_accounts_data_waits_for_its_runtime_and_touches_only_its_own() {
    let f = fixture("forget");
    f.registry
        .reconcile(next_revision(), vec![person("maya"), person("jack")]);
    let config = Dirs::under(&f.root).config().to_path_buf();
    let (maya, jack) = (key("maya"), key("jack"));
    let member = |k: &AccountKey, tenant: &str| {
        crate::internal_ext::member_dir::member_dir(&config, k.gateway(), k.subject(), tenant)
            .unwrap()
    };
    for (k, tenant) in [(&maya, "org-a"), (&maya, "org-b"), (&jack, "org-a")] {
        std::fs::create_dir_all(member(k, tenant).join("mcp-sign-ins")).unwrap();
    }
    runtime(&f, "maya").set_serving(false).unwrap(); // her settings file
    assert!(maya
        .dir(&config)
        .join(crate::account::ACCOUNT_SETTINGS_FILE)
        .exists());

    // Live: refused, nothing removed.
    assert!(f.registry.forget_local_data_within(&maya, 2).await.is_err());
    assert!(member(&maya, "org-a").exists());

    // Leaving the vault stops her; the data goes once she is gone.
    f.registry.reconcile(next_revision(), vec![person("jack")]);
    f.registry
        .forget_local_data_within(&maya, 100)
        .await
        .unwrap();
    assert!(!maya.dir(&config).exists(), "her whole directory");
    assert!(
        member(&jack, "org-a").exists(),
        "another account's data stays"
    );
    // Nothing to forget is fine.
    f.registry.forget_local_data_within(&maya, 2).await.unwrap();
}

/// The app keeps one window per account: a window finds the other app window
/// that shows an account, never itself, never a pop-out, never one of another
/// account.
#[test]
fn an_account_already_open_in_another_app_window_is_found() {
    let f = fixture("one-window");
    f.registry
        .reconcile(next_revision(), vec![person("maya"), person("jack")]);
    f.registry.bind("main", &key("maya")).unwrap();
    f.registry.bind("app-x-1", &key("jack")).unwrap();
    assert_eq!(
        f.registry.app_window_of(&key("maya"), "app-x-1").as_deref(),
        Some("main")
    );
    assert_eq!(
        f.registry.app_window_of(&key("maya"), "main"),
        None,
        "never the asking window"
    );
    assert_eq!(
        f.registry.app_window_of(&key("jack"), "main").as_deref(),
        Some("app-x-1")
    );
    // A pop-out of maya's does not count as maya's window.
    f.registry.inherit("preview-ab12cd34-x", "main").unwrap();
    assert_eq!(
        f.registry.app_window_of(&key("maya"), "app-x-1").as_deref(),
        Some("main")
    );
    f.registry.unbind("main");
    assert_eq!(
        f.registry.app_window_of(&key("maya"), "app-x-1"),
        None,
        "a pop-out is not an app window"
    );
    f.registry.bind("app-y-2", &key("maya")).unwrap();
    assert_eq!(
        f.registry.app_window_of(&key("maya"), "app-x-1").as_deref(),
        Some("app-y-2")
    );
}

/// The default agent and the default models are read by the web only: saving
/// them is a save, not a restart (a restart ends the member's running turns,
/// and the Agents page saves on every switch). What the bridge is started with
/// is another matter.
#[test]
fn a_choice_only_the_web_reads_does_not_restart_the_bridge() {
    use crate::account::AccountSettings;
    let base = AccountSettings::default();
    let mut web_only = base.clone();
    web_only.default_agent = DefaultAgent {
        core: "acme".into(),
        provider: "codex".into(),
    };
    web_only.default_models = [("acme/codex".to_string(), "gpt-x".to_string())].into();
    assert!(!web_only.bridge_differs(&base));
    let mut off = base.clone();
    off.disabled_providers = vec!["acme/cursor".into()];
    assert!(off.bridge_differs(&base), "an agent turned off is the bridge's");
    let mut secret = base.clone();
    secret.sync_secret = "s".into();
    assert!(secret.bridge_differs(&base));
}
