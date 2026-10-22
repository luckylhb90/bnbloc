use tauri::{Manager, State};

use crate::account::AccountKey;
use crate::gateway_init_script;
use crate::runtime::Registry;
use crate::windows::{self, AuxKind};

/// Open an http(s) URL in the OS default browser (ShellExecuteExW on Windows,
/// `open`/`xdg-open` elsewhere — never through a command interpreter).
#[tauri::command]
pub(crate) async fn open_url(url: String) -> Result<(), String> {
    let url = validate_external_url(&url)?;
    tauri::async_runtime::spawn_blocking(move || open::that_detached(url.as_str()))
        .await
        .map_err(|e| format!("could not open url: {e}"))?
        .map_err(|e| format!("could not open url: {e}"))
}

/// Only well-formed http(s) URLs leave the app; the normalized serialization
/// (percent-encoded, no raw spaces/quotes) is what reaches the OS opener.
fn validate_external_url(raw: &str) -> Result<tauri::Url, String> {
    let url = tauri::Url::parse(raw.trim()).map_err(|_| "invalid url".to_string())?;
    match url.scheme() {
        "http" | "https" if url.host_str().is_some() => Ok(url),
        _ => Err("only http(s) URLs may be opened".into()),
    }
}

#[tauri::command]
pub(crate) fn pop_out_preview(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
    registry: State<'_, Registry>,
    room_id: String,
    artifact_id: String,
) -> Result<(), String> {
    open_aux_window(
        &app,
        &window,
        &registry,
        AuxWindow {
            kind: AuxKind::Preview,
            id: artifact_id.clone(),
            route: format!(
                "preview/{}/{}",
                windows::percent_encode(&room_id),
                windows::percent_encode(&artifact_id)
            ),
            title: "bnbloc preview",
            size: (960.0, 720.0),
            min_size: (480.0, 420.0),
        },
    )
}

/// The room's people chat in its own window: the web UI renders only that
/// conversation for `#/people-chat/<roomId>`. One window per room; asking
/// again focuses it.
#[tauri::command]
pub(crate) fn pop_out_people_chat(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
    registry: State<'_, Registry>,
    room_id: String,
) -> Result<(), String> {
    open_aux_window(
        &app,
        &window,
        &registry,
        AuxWindow {
            kind: AuxKind::People,
            id: room_id.clone(),
            route: format!("people-chat/{}", windows::percent_encode(&room_id)),
            title: "bnbloc people chat",
            size: (420.0, 680.0),
            min_size: (320.0, 420.0),
        },
    )
}

/// A running job's live view (the page an agent works on, with take over)
/// in its own window: the web UI renders it for `#/live/<roomId>/<jobId>`.
/// One window per job; asking again focuses it.
#[tauri::command]
pub(crate) fn pop_out_live(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
    registry: State<'_, Registry>,
    room_id: String,
    job_id: String,
) -> Result<(), String> {
    open_aux_window(
        &app,
        &window,
        &registry,
        AuxWindow {
            kind: AuxKind::Live,
            id: job_id.clone(),
            route: format!(
                "live/{}/{}",
                windows::percent_encode(&room_id),
                windows::percent_encode(&job_id)
            ),
            title: "bnbloc live page",
            size: (1120.0, 800.0),
            min_size: (520.0, 420.0),
        },
    )
}

/// An auxiliary window of the shared web UI (a torn-out preview, the people
/// chat, a live page): just a view — closing it never ends the app or
/// touches supervision.
struct AuxWindow {
    kind: AuxKind,
    /// The thing it shows (an artifact, a room, a job): one window per account
    /// and id.
    id: String,
    /// Hash route (without `#/`) the web UI renders for this window.
    route: String,
    title: &'static str,
    size: (f64, f64),
    min_size: (f64, f64),
}

/// Opens (or focuses, when already open) an auxiliary window. It loads the
/// same bundle as the main window with the same gateway init script, offset
/// from the main window so it never lands exactly on top of it.
fn open_aux_window(
    app: &tauri::AppHandle,
    opener: &tauri::WebviewWindow,
    registry: &Registry,
    win: AuxWindow,
) -> Result<(), String> {
    // It shows the account its opener shows, and its label names that
    // account: the same artifact of two accounts is two windows.
    let account = registry
        .account_of_window(opener.label())
        .ok_or(crate::runtime::NOT_BOUND)?;
    let label = windows::aux_label(win.kind, &account, &win.id);
    if let Some(existing) = app.get_webview_window(&label) {
        let _ = existing.set_focus();
        return Ok(());
    }
    registry.inherit(&label, opener.label())?;
    let gateway = account.gateway().to_string();
    let path = format!("index.html#/{}", win.route);
    let mut builder =
        tauri::WebviewWindowBuilder::new(app, &label, tauri::WebviewUrl::App(path.into()))
            .title(win.title)
            .inner_size(win.size.0, win.size.1)
            .min_inner_size(win.min_size.0, win.min_size.1)
            .resizable(true)
            .initialization_script(gateway_init_script(&gateway));
    {
        if let Ok(pos) = opener.outer_position() {
            let offset: i32 = 40 + 24 * (app.webview_windows().len() % 5) as i32;
            builder = builder.position(
                (pos.x + offset).max(0) as f64,
                (pos.y + offset).max(0) as f64,
            );
        }
    }
    builder.build().map_err(|e| {
        registry.unbind(&label);
        format!("could not open {} window: {e}", win.title)
    })?;
    Ok(())
}

/// Open a window for an account (`gateway_url` and `subject`: it shows that
/// account, which must be live; a window that already shows it is brought to
/// the front instead), or, with neither, a window to sign another account in.
/// Two accounts side by side are two windows (docs/prd/2026-10-22-multi-
/// account.md §2.2). The window is bound to its account before its page loads.
#[tauri::command]
pub(crate) fn open_account_window(
    app: tauri::AppHandle,
    opener: tauri::WebviewWindow,
    registry: State<'_, Registry>,
    gateway_url: Option<String>,
    subject: Option<String>,
) -> Result<(), String> {
    let account = match (gateway_url.as_deref(), subject.as_deref()) {
        (Some(gateway), Some(subject)) => Some((
            AccountKey::new(gateway, subject).ok_or("an account needs a gateway and a subject")?,
            // The web's own key for it, which its window reads back.
            format!("{}\n{}", gateway.trim(), subject.trim()),
        )),
        _ => None,
    };
    if let Some((key, _)) = &account {
        let open = registry
            .windows_of(key)
            .into_iter()
            .find(|label| windows::is_app_window(label) && app.get_webview_window(label).is_some());
        if let Some(window) = open.and_then(|label| app.get_webview_window(&label)) {
            let _ = window.unminimize();
            let _ = window.set_focus();
            return Ok(());
        }
    }
    let label = windows::app_label(account.as_ref().map(|(key, _)| key));
    let gateway = match &account {
        Some((key, _)) => {
            registry.bind(&label, key)?;
            key.gateway().to_string()
        }
        None => crate::settings::default_gateway(),
    };
    let path = format!(
        "index.html{}",
        windows::intent_hash(account.as_ref().map(|(_, web_key)| web_key.as_str()))
    );
    let mut builder =
        tauri::WebviewWindowBuilder::new(&app, &label, tauri::WebviewUrl::App(path.into()))
            .title("bnbloc")
            .inner_size(1200.0, 820.0)
            .min_inner_size(720.0, 560.0)
            .resizable(true)
            .disable_drag_drop_handler()
            .initialization_script(gateway_init_script(&gateway));
    if let Ok(pos) = opener.outer_position() {
        let offset: i32 = 40 + 24 * (app.webview_windows().len() % 5) as i32;
        builder = builder.position(
            (pos.x + offset).max(0) as f64,
            (pos.y + offset).max(0) as f64,
        );
    }
    builder.build().map_err(|e| {
        registry.unbind(&label);
        format!("could not open the window: {e}")
    })?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn aux_window_ids_cannot_escape_their_route() {
        assert_eq!(windows::percent_encode("room-1.a_b~"), "room-1.a_b~");
        assert_eq!(windows::percent_encode("a/b?c#d"), "a%2Fb%3Fc%23d");
    }

    #[test]
    fn external_urls_are_http_only_and_normalized() {
        let url = validate_external_url(" https://example.com/a b?x=1&y=\"2\" ").expect("https");
        assert_eq!(url.as_str(), "https://example.com/a%20b?x=1&y=%222%22");
        assert!(validate_external_url("http://localhost:7090/x").is_ok());
        for bad in [
            "javascript:alert(1)",
            "file:///etc/passwd",
            "bnbloc://sso?token=x",
            "https://",
            "not a url",
            "",
        ] {
            assert!(validate_external_url(bad).is_err(), "{bad} must be refused");
        }
    }
}
