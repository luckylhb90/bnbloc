use tauri::{Manager, State};

use crate::{gateway_init_script, updater_client::gateway_url, AppState};

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
    state: State<'_, AppState>,
    room_id: String,
    artifact_id: String,
) -> Result<(), String> {
    open_aux_window(
        &app,
        &state,
        AuxWindow {
            label: format!("preview-{}", window_label_part(&artifact_id)),
            route: format!(
                "preview/{}/{}",
                encode_segment(&room_id),
                encode_segment(&artifact_id)
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
    state: State<'_, AppState>,
    room_id: String,
) -> Result<(), String> {
    open_aux_window(
        &app,
        &state,
        AuxWindow {
            label: format!("people-{}", window_label_part(&room_id)),
            route: format!("people-chat/{}", encode_segment(&room_id)),
            title: "bnbloc people chat",
            size: (420.0, 680.0),
            min_size: (320.0, 420.0),
        },
    )
}

/// An auxiliary window of the shared web UI (a torn-out preview, the people
/// chat): just a view — closing it never ends the app or touches supervision.
struct AuxWindow {
    label: String,
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
    state: &State<'_, AppState>,
    win: AuxWindow,
) -> Result<(), String> {
    if let Some(existing) = app.get_webview_window(&win.label) {
        let _ = existing.set_focus();
        return Ok(());
    }
    let gateway = gateway_url(&state.settings.lock());
    let path = format!("index.html#/{}", win.route);
    let mut builder =
        tauri::WebviewWindowBuilder::new(app, &win.label, tauri::WebviewUrl::App(path.into()))
            .title(win.title)
            .inner_size(win.size.0, win.size.1)
            .min_inner_size(win.min_size.0, win.min_size.1)
            .resizable(true)
            .initialization_script(gateway_init_script(&gateway));
    if let Some(main) = app.get_webview_window(crate::MAIN_WINDOW) {
        if let Ok(pos) = main.outer_position() {
            let offset: i32 = 40 + 24 * (app.webview_windows().len() % 5) as i32;
            builder = builder.position(
                (pos.x + offset).max(0) as f64,
                (pos.y + offset).max(0) as f64,
            );
        }
    }
    builder
        .build()
        .map_err(|e| format!("could not open {} window: {e}", win.title))?;
    Ok(())
}

/// A window label carries only `[A-Za-z0-9_-]` (Tauri's label alphabet
/// minus its separators); anything else becomes `_`.
fn window_label_part(id: &str) -> String {
    id.chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '-' || c == '_' {
                c
            } else {
                '_'
            }
        })
        .collect()
}

/// Percent-encodes one hash-route path segment (the web UI decodes it with
/// decodeURIComponent), so an id can never inject a route separator.
fn encode_segment(id: &str) -> String {
    let mut out = String::with_capacity(id.len());
    for b in id.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn aux_window_ids_cannot_escape_their_label_or_route() {
        assert_eq!(window_label_part("room-1_a"), "room-1_a");
        assert_eq!(window_label_part("a/b:c d"), "a_b_c_d");
        assert_eq!(encode_segment("room-1.a_b~"), "room-1.a_b~");
        assert_eq!(encode_segment("a/b?c#d"), "a%2Fb%3Fc%23d");
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
