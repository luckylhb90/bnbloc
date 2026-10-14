//! bnbloc desktop shell: loads the shared web bundle and supervises the
//! embedded `bnbloc-bridge` sidecar. The shell contributes process supervision
//! and thin config/status IPC only — no business logic (that stays in the
//! control plane and the bridge/core).

mod app_commands;
mod artifact_scheme;
mod bridge;
mod child_process;
mod dirs;
mod files;
mod internal_ext;
mod local_files;
mod previewer_scheme;
mod secret;
mod settings;
mod shell_env;
mod site_scheme;
mod sso_deep_link;
mod updater_client;

use std::path::PathBuf;

use parking_lot::Mutex;

use serde_json::json;
use tauri::{Manager, State};

use bridge::{Status, Supervisor};
use settings::{gateway_url, Settings};

/// The main window's label. Its close ends the app; any other window (the
/// `preview-*` pop-outs) is just a view and never touches supervision.
pub(crate) const MAIN_WINDOW: &str = "main";

/// Bound on the app-exit stop of the bridge + sidecars (graceful, then kill).
const EXIT_GRACE: std::time::Duration = std::time::Duration::from_secs(3);

/// App-managed supervisor handle.
struct AppState {
    supervisor: Supervisor,
    /// Cached last-known settings; written only by `update_settings`.
    settings: Mutex<Settings>,
    settings_path: Option<PathBuf>,
}

impl AppState {
    /// The one writer of the settings: `edit` changes the current settings
    /// under the lock, the result is saved, then cached and returned.
    /// Concurrent writers (the web's form, the sandbox mode) apply in order,
    /// each over the other's result, never over a stale copy.
    fn update_settings(&self, edit: impl FnOnce(&mut Settings)) -> Result<Settings, String> {
        let mut current = self.settings.lock();
        let path = self
            .settings_path
            .as_deref()
            .ok_or("settings dir unavailable")?;
        let mut next = current.clone();
        edit(&mut next);
        settings::save(path, &next)?;
        *current = next.clone();
        Ok(next)
    }
}

#[tauri::command]
fn bridge_status(state: State<'_, AppState>) -> Status {
    state.supervisor.status()
}

/// Start the bridge, or restart it when it runs (the supervisor stops the
/// current run first): the one start/restart command.
#[tauri::command]
fn bridge_start(state: State<'_, AppState>) -> Result<(), String> {
    let settings = state.settings.lock().clone();
    state.supervisor.start(settings)
}

#[tauri::command]
fn bridge_stop(state: State<'_, AppState>) -> Result<(), String> {
    state.supervisor.stop();
    Ok(())
}

#[tauri::command]
fn get_settings(state: State<'_, AppState>) -> Settings {
    state.settings.lock().clone()
}

/// The web's settings form. The web owns every field but the shell's own
/// (`Settings::apply_web`): a stale snapshot never reverts those.
#[tauri::command]
fn set_settings(state: State<'_, AppState>, settings: Settings) -> Result<(), String> {
    let settings = state.update_settings(|current| current.apply_web(settings))?;
    // Apply immediately: (re)start if runnable, else stop.
    if settings.is_runnable() {
        state.supervisor.start(settings)?;
    } else {
        state.supervisor.stop();
    }
    Ok(())
}

/// Hand the web session to the shell. The component sidecar manager gets the
/// session bearer; the bridge supervisor gets the member's refresh token
/// (memory only), which the bridge renews its own session from — the
/// supervisor restarts the bridge only when it is running without one. An
/// empty session token is the web's sign-out: the refresh token and the
/// manager's session are cleared. Returns immediately: the
/// catalog refresh runs in the background (a gateway outage must not stall
/// the UI's boot).
#[tauri::command]
fn internal_adapter_session(
    state: State<'_, AppState>,
    adapter: State<'_, internal_ext::Adapter>,
    gateway_url: String,
    session_token: String,
    refresh_token: Option<String>,
) {
    if session_token.trim().is_empty() {
        // Signed out: nothing acts as the member any more (no catalog
        // fetches, downloads, member stream or plugin chat with the old
        // session).
        state.supervisor.set_refresh_token(None);
        adapter.update_gateway(&gateway_url, String::new());
        return;
    }
    state.supervisor.set_refresh_token(refresh_token);
    adapter.update_gateway(&gateway_url, session_token);
    adapter.start();
    adapter.refresh_in_background();
}

#[tauri::command]
fn internal_adapter_status(
    state: State<'_, internal_ext::Adapter>,
) -> Vec<internal_ext::AdapterStatus> {
    state.status()
}

/// The member's "apply now" for component updates that wait for the
/// bridge's running turns to finish (internal_ext/update_gate.rs).
#[tauri::command]
fn internal_adapter_apply_updates(state: State<'_, internal_ext::Adapter>) {
    state.apply_pending_now();
}

/// How many agent turns the bridge drives now: an update that restarts it
/// (the app's own, or a pending component update) warns before cutting them.
#[tauri::command]
fn bridge_running_turns(state: State<'_, internal_ext::Adapter>) -> u32 {
    state.running_turns()
}

#[tauri::command]
async fn internal_adapter_targets(
    state: State<'_, internal_ext::Adapter>,
    name: String,
) -> Result<serde_json::Value, String> {
    state.targets(&name).await
}

#[tauri::command]
async fn internal_adapter_auth_status(
    state: State<'_, internal_ext::Adapter>,
    name: String,
) -> Result<internal_ext::SidecarAuthStatus, String> {
    state.auth_status(&name).await
}

#[tauri::command]
async fn internal_adapter_login(
    state: State<'_, internal_ext::Adapter>,
    name: String,
) -> Result<serde_json::Value, String> {
    state.login(&name).await
}

/// Installed plugins with desktop-local settings (Settings → Notifications).
/// Secret values are never returned.
#[tauri::command]
fn plugin_local_settings(
    state: State<'_, internal_ext::Adapter>,
) -> Result<Vec<internal_ext::PluginLocalSettings>, String> {
    state.plugin_local_settings()
}

/// Store a plugin's local settings on this machine (an empty secret keeps
/// the stored one) and restart its sidecar. Never sent to the gateway.
#[tauri::command]
fn plugin_local_settings_save(
    state: State<'_, internal_ext::Adapter>,
    plugin: String,
    values: std::collections::BTreeMap<String, String>,
) -> Result<(), String> {
    state.save_plugin_local_settings(&plugin, values)
}

/// Forget a plugin's local settings (the plugin stops when it requires them).
#[tauri::command]
fn plugin_local_settings_clear(
    state: State<'_, internal_ext::Adapter>,
    plugin: String,
) -> Result<(), String> {
    state.clear_plugin_local_settings(&plugin)
}

/// Installed MCP tools with this desktop's state (Settings → Tools): the
/// stdio binary's download state and the member credentials. Secret values
/// are never returned.
#[tauri::command]
fn mcp_tool_settings(
    state: State<'_, internal_ext::Adapter>,
) -> Result<Vec<internal_ext::McpToolSettings>, String> {
    state.mcp_tool_settings()
}

/// Store the member's credentials for an MCP tool on this machine (an empty
/// secret keeps the stored one). Never sent to the gateway.
#[tauri::command]
fn mcp_tool_settings_save(
    state: State<'_, internal_ext::Adapter>,
    tool: String,
    values: std::collections::BTreeMap<String, String>,
) -> Result<(), String> {
    state.save_mcp_tool_settings(&tool, values)
}

/// Forget the member's credentials for an MCP tool.
#[tauri::command]
fn mcp_tool_settings_clear(
    state: State<'_, internal_ext::Adapter>,
    tool: String,
) -> Result<(), String> {
    state.clear_mcp_tool_settings(&tool)
}

/// Sign the member in to an MCP tool that takes an OAuth sign-in: the tool's
/// sign-in page opens in the browser; the tokens stay on this machine.
#[tauri::command]
async fn mcp_tool_sign_in(
    state: State<'_, internal_ext::Adapter>,
    tool: String,
) -> Result<(), String> {
    state.sign_in_mcp_tool(&tool).await
}

/// Forget the member's sign-in to an MCP tool on this machine.
#[tauri::command]
fn mcp_tool_sign_out(state: State<'_, internal_ext::Adapter>, tool: String) -> Result<(), String> {
    state.sign_out_mcp_tool(&tool)
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

/// Detach every supervised child (bridge + component sidecars) for one joint
/// stop. Used at app exit and before an update install.
pub(crate) fn detach_children(app: &tauri::AppHandle) -> Vec<child_process::Child> {
    let mut children = Vec::new();
    if let Some(state) = app.try_state::<AppState>() {
        children.extend(state.supervisor.detach());
    }
    if let Some(adapter) = app.try_state::<internal_ext::Adapter>() {
        children.extend(adapter.detach_all());
    }
    children
}

/// Bring the bridge and the sidecar manager back up (after a failed update
/// install stopped them).
pub(crate) fn resume_children(app: &tauri::AppHandle) {
    if let Some(state) = app.try_state::<AppState>() {
        let settings = state.settings.lock().clone();
        if settings.is_runnable() {
            if let Err(e) = state.supervisor.start(settings) {
                eprintln!("[shell] bridge restart after failed update: {e}");
            }
        }
    }
    if let Some(adapter) = app.try_state::<internal_ext::Adapter>() {
        adapter.start();
        adapter.refresh_in_background();
    }
}

/// "Reload shell environment" (Settings → Agent sandbox): resolve the
/// member's login-shell environment again (shell_env), then restart the
/// bridge and every sidecar so they and the agents they run get it.
#[tauri::command]
async fn shell_env_reload(app: tauri::AppHandle) -> shell_env::Reloaded {
    let reloaded = shell_env::reload().await;
    child_process::terminate(detach_children(&app), child_process::STOP_GRACE).await;
    resume_children(&app);
    reloaded
}

/// Whether closing this window ends the app (and with it the bridge and the
/// sidecars). Only the main window does; preview and people-chat pop-outs
/// never do.
fn window_close_ends_app(label: &str) -> bool {
    label == MAIN_WINDOW
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
            if let Some(win) = app.get_webview_window(MAIN_WINDOW) {
                let _ = win.show();
                let _ = win.set_focus();
            }
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
        .manage(internal_ext::previewer_cache::PreviewerBundles::default())
        .register_asynchronous_uri_scheme_protocol(
            previewer_scheme::SCHEME,
            |ctx, request, responder| {
                previewer_scheme::handle(ctx.app_handle(), request, responder)
            },
        )
        // Web-app (site) artifacts: the gateway's ticketed site route,
        // proxied without a session (see site_scheme).
        .register_asynchronous_uri_scheme_protocol(
            site_scheme::SCHEME,
            |ctx, request, responder| site_scheme::handle(ctx.app_handle(), request, responder),
        )
        .setup(|app| {
            // Start resolving the member's shell environment now: the first
            // child spawn waits for it (shell_env).
            tauri::async_runtime::spawn(shell_env::current());
            let handle = app.handle().clone();
            let supervisor = Supervisor::new(handle.clone());
            let dirs = dirs::Dirs::resolve(&handle);
            if let Err(e) = &dirs {
                eprintln!("[shell] app dirs unavailable: {e}");
            }
            let settings_path = dirs.as_ref().ok().map(dirs::Dirs::settings_file);
            let settings = match settings_path.as_deref().map(settings::load) {
                Some(Ok(settings)) => settings,
                Some(Err(e)) => {
                    // Shown in the bridge log (Agents); the unreadable file
                    // was moved aside, never overwritten.
                    supervisor.note(format!("[settings] {e}; starting unconfigured"));
                    Settings::default()
                }
                None => Settings::default(),
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
            let gateway = gateway_url(&settings);
            tauri::WebviewWindowBuilder::new(app, MAIN_WINDOW, tauri::WebviewUrl::default())
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

            // Auto-start the bridge when the user is already configured.
            if settings.is_runnable() {
                let _ = supervisor.start(settings.clone());
            }

            // Desktop builds supervise installed Components sidecars only; the
            // package catalog supplies names, artifacts, and launch metadata.
            let adapter = internal_ext::Adapter::new(handle.clone(), dirs, &gateway);
            adapter.start();
            app.manage(adapter);

            app.manage(AppState {
                supervisor,
                settings: Mutex::new(settings),
                settings_path,
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            // Closing the main window quits the app; the exit handler below
            // stops the bridge and sidecars. Closing a preview pop-out is just
            // a window close.
            if let tauri::WindowEvent::CloseRequested { .. } = event {
                if window_close_ends_app(window.label()) {
                    window.app_handle().exit(0);
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            bridge_status,
            bridge_start,
            bridge_stop,
            get_settings,
            set_settings,
            internal_adapter_session,
            internal_adapter_status,
            internal_adapter_apply_updates,
            bridge_running_turns,
            internal_adapter_targets,
            internal_adapter_login,
            internal_adapter_auth_status,
            plugin_local_settings,
            plugin_local_settings_save,
            plugin_local_settings_clear,
            mcp_tool_settings,
            mcp_tool_settings_save,
            mcp_tool_settings_clear,
            mcp_tool_sign_in,
            mcp_tool_sign_out,
            app_info,
            updater_client::check_update,
            updater_client::install_update,
            app_commands::open_url,
            app_commands::pop_out_preview,
            app_commands::pop_out_people_chat,
            internal_ext::sandbox::sandbox_status,
            internal_ext::sandbox::sandbox_mode_get,
            internal_ext::sandbox::sandbox_mode_set,
            internal_ext::sandbox::sandbox_env_set,
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
            if webview.label() == MAIN_WINDOW
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
        tauri::RunEvent::Exit => child_process::terminate_all(detach_children(app), EXIT_GRACE),
        // macOS Open With / a double-click on an associated document.
        #[cfg(target_os = "macos")]
        tauri::RunEvent::Opened { urls } => local_files::hand(app, local_files::paths_in_urls(&urls)),
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
    fn only_the_main_window_close_ends_the_app() {
        assert!(window_close_ends_app("main"));
        assert!(!window_close_ends_app("preview-artifact-1"));
        assert!(!window_close_ends_app("preview-main"));
        assert!(!window_close_ends_app("people-room-1"));
    }

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
