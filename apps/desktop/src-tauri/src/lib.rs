//! bnbloc desktop shell: loads the shared web bundle and supervises the
//! embedded `bnbloc-bridge` sidecar. The shell contributes process supervision
//! and thin config/status IPC only — no business logic (that stays in the
//! control plane and the bridge/core).

mod account;
mod app_commands;
mod artifact_scheme;
mod bridge;
mod child_process;
mod dirs;
mod files;
mod internal_ext;
mod keeper;
mod local_files;
mod previewer_scheme;
mod runtime;
mod secret;
mod settings;
mod shell_env;
mod site_scheme;
mod sso_deep_link;
mod updater_client;
mod windows;

use serde_json::json;
use tauri::{Emitter, Manager, State};

use bridge::Status;
use runtime::{Account, AccountEntry, Registry};
use settings::Settings;

/// Bound on the app-exit stop of the bridge + sidecars (graceful, then kill).
const EXIT_GRACE: std::time::Duration = std::time::Duration::from_secs(3);

/// The bridge of the account the window shows.
#[tauri::command]
fn bridge_status(account: Account) -> Status {
    account.supervisor.status()
}

/// Serve the account's agents from this computer: start its bridge, or
/// restart it when it runs (the supervisor stops the current run first). The
/// person's switch, kept.
#[tauri::command]
fn bridge_start(account: Account) -> Result<(), String> {
    account.set_serving(true)
}

#[tauri::command]
fn bridge_stop(account: Account) -> Result<(), String> {
    account.set_serving(false)
}

/// The settings of the account the window shows: the machine's and its own.
#[tauri::command]
fn get_settings(account: Account) -> Settings {
    account.settings()
}

/// The web's settings form.
#[tauri::command]
fn set_settings(
    account: Account,
    registry: State<'_, Registry>,
    settings: Settings,
) -> Result<(), String> {
    registry.apply_settings(&account.0, settings)
}

/// The web's vault, as it is now: one entry per signed-in account, with its
/// credential and tenant. The shell makes its account runtimes match (creates
/// the missing, re-scopes a changed tenant, stops and forgets the absent): an
/// account that leaves the vault stops serving, and its sign-in on the
/// gateway ends (docs/bugs/2026-10-22-desktop-sign-out-leaves-the-bridge-
/// serving.md). `revision` is the vault's count of its writes: a snapshot
/// older than the newest seen is dropped. Returns at once: nothing here waits
/// for a gateway.
#[tauri::command]
fn accounts_sync(registry: State<'_, Registry>, revision: u64, accounts: Vec<AccountEntry>) {
    registry.reconcile(revision, accounts);
}

/// Every account's state beside its name: session, whether it serves agents
/// from this computer, its bridge and its running turns.
#[tauri::command]
fn accounts_status(registry: State<'_, Registry>) -> Vec<runtime::AccountStatus> {
    registry.statuses()
}

/// Forget what this computer keeps of an account that is no longer in the
/// vault (the web asks it after the account's removal, when the person chose to
/// delete its data). An account that is still live is refused.
#[tauri::command]
async fn account_forget_data(
    registry: State<'_, Registry>,
    gateway_url: String,
    subject: String,
) -> Result<(), String> {
    let key = account::AccountKey::new(&gateway_url, &subject)
        .ok_or("an account needs a gateway and a subject")?;
    registry.forget_local_data(&key).await
}

/// Bind the calling window to an account of the vault (after
/// `accounts_sync`): every command of the window, and every request of its
/// pages to the shell's schemes, is that account's.
#[tauri::command]
fn window_bind(
    window: tauri::WebviewWindow,
    registry: State<'_, Registry>,
    gateway_url: String,
    subject: String,
) -> Result<(), String> {
    let key = account::AccountKey::new(&gateway_url, &subject)
        .ok_or("an account needs a gateway and a subject")?;
    registry.bind(window.label(), &key)
}

#[tauri::command]
fn internal_adapter_status(account: Account) -> Vec<internal_ext::AdapterStatus> {
    account.adapter.status()
}

/// The member's "apply now" for component updates that wait for the
/// bridges' running turns to finish (internal_ext/update_gate.rs): every
/// account's, as the turns they wait for are every account's.
#[tauri::command]
fn internal_adapter_apply_updates(registry: State<'_, Registry>) {
    for runtime in registry.runtimes() {
        runtime.adapter.apply_pending_now();
    }
}

/// How many agent turns every account's bridge drives now: an update that
/// restarts them (the app's own, or a pending component update) warns before
/// cutting them.
#[tauri::command]
fn bridge_running_turns(registry: State<'_, Registry>) -> u32 {
    registry.running_turns()
}

#[tauri::command]
async fn internal_adapter_targets(
    account: Account,
    name: String,
) -> Result<serde_json::Value, String> {
    account.adapter.targets(&name).await
}

#[tauri::command]
async fn internal_adapter_auth_status(
    account: Account,
    name: String,
) -> Result<internal_ext::SidecarAuthStatus, String> {
    account.adapter.auth_status(&name).await
}

#[tauri::command]
async fn internal_adapter_login(
    account: Account,
    name: String,
) -> Result<serde_json::Value, String> {
    account.adapter.login(&name).await
}

/// Installed components with settings (the Components page): every MCP
/// tool (its binary's state, its sign-in) and every plugin that declares
/// settings. Secret values, and platform or organization values, are never
/// returned.
#[tauri::command]
fn component_settings(
    account: Account,
) -> Result<Vec<internal_ext::ComponentSettingsView>, String> {
    account.adapter.component_settings()
}

/// Store the member's own values of a component's settings on this machine
/// (an absent key is left, an empty secret keeps the stored one, `clear`
/// clears keys) and restart a plugin with them. Never sent to the gateway.
#[tauri::command]
fn component_settings_save(
    account: Account,
    kind: String,
    name: String,
    values: std::collections::BTreeMap<String, String>,
    clear: Vec<String>,
) -> Result<(), internal_ext::SettingsSaveError> {
    account
        .adapter
        .save_component_settings(&kind, &name, values, clear)
}

/// Installed plugins that provide skills (the skills panel their card opens),
/// with their account.adapter.
#[tauri::command]
fn skills_providers(account: Account) -> Vec<internal_ext::SkillsProviderView> {
    account.adapter.skills_providers()
}

/// A skills provider's state (`bnbloc.skills.provider/v1`, GET /v1/state).
#[tauri::command]
async fn skills_provider_state(
    account: Account,
    plugin: String,
) -> Result<serde_json::Value, String> {
    account.adapter.skills_provider_state(&plugin).await
}

/// Replace a skills provider's settings (PUT /v1/settings); answers its account.adapter.
#[tauri::command]
async fn skills_provider_settings(
    account: Account,
    plugin: String,
    settings: serde_json::Value,
) -> Result<serde_json::Value, String> {
    account
        .adapter
        .skills_provider_apply(&plugin, settings)
        .await
}

/// One skill packed by its provider (POST /v1/pack): its hash, a line feed,
/// then the tar, as raw bytes.
#[tauri::command]
async fn skills_provider_pack(
    account: Account,
    plugin: String,
    name: String,
) -> Result<tauri::ipc::Response, String> {
    account
        .adapter
        .skills_provider_pack(&plugin, &name)
        .await
        .map(tauri::ipc::Response::new)
}

/// The folder picker for adding a skills source. None when the member cancels.
/// The provider is told the path; this command only learns it from the dialog.
#[tauri::command]
async fn skills_pick_folder(app: tauri::AppHandle) -> Result<Option<String>, String> {
    use tauri_plugin_dialog::DialogExt;
    let picked = tauri::async_runtime::spawn_blocking(move || {
        app.dialog()
            .file()
            .set_title("Choose a folder of skills")
            .blocking_pick_folder()
    })
    .await
    .map_err(|e| e.to_string())?;
    let Some(path) = picked else { return Ok(None) };
    let path = path.into_path().map_err(|e| e.to_string())?;
    Ok(Some(path.to_string_lossy().into_owned()))
}

/// Sign the member in to an MCP tool that takes an OAuth sign-in: the tool's
/// sign-in page opens in the browser; the tokens stay on this machine.
#[tauri::command]
async fn mcp_tool_sign_in(account: Account, tool: String) -> Result<(), String> {
    account.adapter.sign_in_mcp_tool(&tool).await
}

/// Forget the member's sign-in to an MCP tool on this machine.
#[tauri::command]
fn mcp_tool_sign_out(account: Account, tool: String) -> Result<(), String> {
    account.adapter.sign_out_mcp_tool(&tool)
}

#[tauri::command]
fn app_info(app: tauri::AppHandle) -> serde_json::Value {
    json!({
        // The bundle version (tauri.conf.json -> package.json): the same
        // value the updater compares against.
        "version": app.package_info().version.to_string(),
        "platform": std::env::consts::OS,
        "inShell": true,
    })
}

/// Detach every supervised child (every account's bridge and component
/// sidecars) for one joint stop. Used at app exit and before an update
/// install.
pub(crate) fn detach_children(app: &tauri::AppHandle) -> Vec<child_process::Child> {
    app.try_state::<Registry>()
        .map(|registry| registry.detach_all())
        .unwrap_or_default()
}

/// Bring every account's bridge and sidecar manager back up (after a failed
/// update install stopped them).
pub(crate) fn resume_children(app: &tauri::AppHandle) {
    if let Some(registry) = app.try_state::<Registry>() {
        registry.resume_all();
    }
}

/// "Reload shell environment" (Agents → Local bridge): resolve the
/// member's login-shell environment again (shell_env), then restart the
/// bridge and every sidecar so they and the agents they run get it.
#[tauri::command]
async fn shell_env_reload(app: tauri::AppHandle) -> shell_env::Reloaded {
    let reloaded = shell_env::reload().await;
    child_process::terminate(detach_children(&app), child_process::STOP_GRACE).await;
    resume_children(&app);
    reloaded
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let app = tauri::Builder::default()
        // Single instance: a second launch (user double-click, OS-routed deep
        // link, stale test process) must NOT run beside the first — two
        // instances fight over the sidecar port, the deep-link target, and the
        // persisted session. The second process just focuses the existing
        // window and exits; with the `deep-link` feature its argv link is
        // forwarded to the deep-link handler first.
        .plugin(tauri_plugin_single_instance::init(|app, args, _cwd| {
            windows::focus_app_window(app);
            // `bnbloc-desktop <file>` while the app runs: open it here.
            local_files::hand(app, local_files::paths_in_args(&args));
        }))
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_deep_link::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        // The live-artifact runner's own origin (see artifact_scheme): agent
        // HTML runs there, so the app CSP never needs inline script.
        .register_uri_scheme_protocol(artifact_scheme::SCHEME, |_ctx, request| {
            artifact_scheme::respond(request.method(), request.uri().path())
        })
        // Installed previewer bundles, sha256-verified from the member
        // catalog, each from this scheme's origin (see previewer_scheme).
        .register_asynchronous_uri_scheme_protocol(
            previewer_scheme::SCHEME,
            |ctx, request, responder| {
                previewer_scheme::handle(ctx.app_handle(), ctx.webview_label(), request, responder)
            },
        )
        // Web-app (site) artifacts: the gateway's ticketed site route,
        // proxied without a session (see site_scheme).
        .register_asynchronous_uri_scheme_protocol(
            site_scheme::SCHEME,
            |ctx, request, responder| {
                site_scheme::handle(ctx.app_handle(), ctx.webview_label(), request, responder)
            },
        )
        .setup(|app| {
            // Start resolving the member's shell environment now: the first
            // child spawn waits for it (shell_env).
            tauri::async_runtime::spawn(shell_env::current());
            let handle = app.handle().clone();
            let dirs = dirs::Dirs::resolve(&handle);
            if let Err(e) = &dirs {
                eprintln!("[shell] app dirs unavailable: {e}");
            }
            let machine = match dirs.as_ref().ok().map(|d| d.settings_file()) {
                Some(path) => account::load_machine_settings(&path).unwrap_or_else(|e| {
                    // The unreadable file was moved aside, never overwritten.
                    eprintln!("[settings] {e}; starting with the defaults");
                    account::MachineSettings::default()
                }),
                None => account::MachineSettings::default(),
            };
            sso_deep_link::setup(&handle);
            app.manage(local_files::LocalFiles::load(
                dirs.as_ref().ok().map(dirs::Dirs::local_files_file),
            ));
            // A cold start with files to open: `bnbloc-desktop <file>`, or
            // Open With delivered before this ran.
            let launch: Vec<String> = std::env::args().collect();
            let mut paths = local_files::take_early();
            paths.extend(local_files::paths_in_args(&launch));
            local_files::hand(&handle, paths);

            // Inject the gateway URL before any page script runs, so the shared
            // UI's /v1 + /api calls reach the right origin in bundled mode (the
            // UI origin is tauri://, not the gateway). An initialization script
            // (vs a post-load window.eval) is not racy: it runs before the SPA
            // reads window.__BNBLOC_GATEWAY__. With no configured gateway this
            // is the build's BNBLOC_DEFAULT_GATEWAY, or empty (the user picks a
            // gateway in the UI) — never a baked-in deployment host.
            let gateway = settings::default_gateway();
            tauri::WebviewWindowBuilder::new(app, windows::MAIN, tauri::WebviewUrl::default())
                .title("bnbloc")
                .inner_size(1200.0, 820.0)
                .min_inner_size(720.0, 560.0)
                .resizable(true)
                // Hand OS file drops to the web layer: the web client implements
                // HTML5 drag-and-drop (package upload, chat attachments), which
                // Tauri's native drop handler otherwise swallows silently.
                .disable_drag_drop_handler()
                .initialization_script(gateway_init_script(&gateway))
                .build()?;

            // The accounts come with the web's vault (`accounts_sync`): each
            // gets its own runtime when the page hands it over.
            let status_app = handle.clone();
            app.manage(Registry::new(
                dirs,
                machine,
                std::sync::Arc::new(move |key, status| {
                    let Some(registry) = status_app.try_state::<Registry>() else {
                        return;
                    };
                    for label in registry.windows_of(key) {
                        let _ = status_app.emit_to(label, runtime::STATUS_EVENT, status);
                    }
                }),
            ));
            Ok(())
        })
        .on_window_event(|window, event| {
            // A window that is gone shows no account any more; the app ends
            // with its last app window (the exit handler below stops every
            // account's bridge and sidecars). Closing a pop-out is just a
            // window close.
            if let tauri::WindowEvent::Destroyed = event {
                let app = window.app_handle();
                if let Some(registry) = app.try_state::<Registry>() {
                    registry.unbind(window.label());
                }
                if windows::closing_ends_app(app, window.label()) {
                    app.exit(0);
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            bridge_status,
            bridge_start,
            bridge_stop,
            get_settings,
            set_settings,
            accounts_sync,
            accounts_status,
            account_forget_data,
            window_bind,
            internal_adapter_status,
            internal_adapter_apply_updates,
            bridge_running_turns,
            internal_adapter_targets,
            internal_adapter_login,
            internal_adapter_auth_status,
            component_settings,
            component_settings_save,
            skills_providers,
            skills_provider_state,
            skills_provider_settings,
            skills_provider_pack,
            skills_pick_folder,
            mcp_tool_sign_in,
            mcp_tool_sign_out,
            app_info,
            updater_client::check_update,
            updater_client::install_update,
            app_commands::open_url,
            app_commands::open_account_window,
            app_commands::pop_out_preview,
            app_commands::pop_out_people_chat,
            app_commands::pop_out_live,
            internal_ext::sandbox::sandbox_status,
            internal_ext::sandbox::sandbox_private_hosts_apply,
            internal_ext::sandbox::sandbox_restart_vm,
            shell_env_reload,
            local_files::local_file_pick,
            local_files::local_file_take_pending,
            local_files::local_file_read,
            local_files::local_file_bind,
            local_files::local_file_for_artifact,
            local_files::local_file_save,
        ])
        .on_page_load(|webview, payload| {
            // A pending cold-start SSO link is applied once the main page has
            // FINISHED loading: on `Started` the SPA is not there yet and the
            // one-shot link would be consumed into a page that discards it.
            if windows::is_app_window(webview.label())
                && matches!(payload.event(), tauri::webview::PageLoadEvent::Finished)
            {
                sso_deep_link::on_page_load(webview);
            }
        })
        .build(tauri::generate_context!())
        .expect("error while building bnbloc desktop");

    app.run(|app, event| match event {
        // App exit (quit, main window closed, updater restart): stop the
        // bridge and every sidecar together — gracefully, then forced.
        tauri::RunEvent::Exit => {
            child_process::terminate_all(detach_children(app), EXIT_GRACE);
            // Then every account's own sign-in on the gateway ends, so a
            // restart leaves no dangling sign-ins in the member's list.
            if let Some(registry) = app.try_state::<Registry>() {
                registry.revoke_all();
            }
        }
        // macOS Open With / a double-click on an associated document.
        #[cfg(target_os = "macos")]
        tauri::RunEvent::Opened { urls } => {
            local_files::hand(app, local_files::paths_in_urls(&urls))
        }
        _ => {}
    });
}

/// The pre-page-script that tells the shared web UI which gateway to call.
pub(crate) fn gateway_init_script(gateway: &str) -> String {
    format!(
        "window.__BNBLOC_GATEWAY__ = {};",
        serde_json::to_string(gateway).unwrap_or_else(|_| "\"\"".into())
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crate_version_matches_the_bundle_version_source() {
        // tauri.conf.json reads its version from package.json; Cargo.toml must
        // carry the same value (Cargo cannot read package.json).
        let package: serde_json::Value =
            serde_json::from_str(include_str!("../../package.json")).expect("package.json");
        assert_eq!(
            package["version"].as_str(),
            Some(env!("CARGO_PKG_VERSION")),
            "bump apps/desktop/package.json and src-tauri/Cargo.toml together"
        );
    }

    #[test]
    fn gateway_init_script_is_a_json_string_literal() {
        assert_eq!(
            gateway_init_script("https://gw\"x"),
            r#"window.__BNBLOC_GATEWAY__ = "https://gw\"x";"#
        );
    }
}
