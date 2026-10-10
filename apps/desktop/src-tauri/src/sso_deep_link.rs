//! bnbloc://sso deep link — the SSO hand-off from a system browser.
//!
//! The app starts SSO with a random nonce (kept in its webview storage) in the
//! callback URL. For such a flow the gateway returns only a short-lived,
//! single-use code bound to that nonce — never a session token — and the
//! browser callback page forwards it via `bnbloc://sso?code=…`. The SPA then
//! redeems code + its pending nonce over HTTPS (POST /v1/auth/sso/exchange); a
//! code the app did not initiate has no pending nonce and is dropped, so a
//! crafted link cannot sign the app into someone else's account (login CSRF).
//! A link carrying a bare session token is not a supported shape and is ignored.
//!
//! Delivery reuses the SPA's boot read: point the webview at `#ssoCode=…` and
//! reload, so AuthProvider's boot performs the exchange — no race with the
//! SPA's own boot on a warm start.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;

use tauri::Manager;
use tauri_plugin_deep_link::DeepLinkExt;

// A link that arrived before the webview could receive it. Consumed exactly
// once, on the first page load (the earliest moment eval reaches a live
// page).
static PENDING: Mutex<Option<String>> = Mutex::new(None);

// DT-18: set once the main page has finished its first load. Until then an
// arriving link (macOS delivers cold-start links as events right after the
// window is built) is stashed, not eval'd into a page that is still loading.
static MAIN_PAGE_READY: AtomicBool = AtomicBool::new(false);

pub(crate) fn setup(app: &tauri::AppHandle) {
    // Linux/Windows cold start: the launch link is readable now, but this
    // runs before the window builder, so the webview does not exist yet —
    // stash the link for the first page load.
    if let Ok(Some(links)) = app.deep_link().get_current() {
        stash(links.first().map(|u| u.to_string()));
    }
    // Every other arrival (warm start, and macOS launch links — the OS
    // delivers those as events once the run loop starts, after setup has
    // built the window). The plugin's on_open_url deserializes the JSON
    // array payload; the raw event payload is ["url", …], NOT a bare string.
    let handle = app.clone();
    app.deep_link().on_open_url(move |event| {
        for url in event.urls() {
            if is_open_link(url.as_str()) {
                if let Some(win) = handle.get_webview_window("main") {
                    let _ = win.show();
                    let _ = win.set_focus();
                }
                continue;
            }
            match handle.get_webview_window("main") {
                Some(webview) if MAIN_PAGE_READY.load(Ordering::Acquire) => {
                    apply(webview.as_ref(), url.as_str())
                }
                _ => stash(Some(url.to_string())),
            }
        }
    });
}

// Called by lib.rs only for the main window's PageLoadEvent::Finished (DT-18).
pub(crate) fn on_page_load(webview: &tauri::Webview) {
    MAIN_PAGE_READY.store(true, Ordering::Release);
    let pending = PENDING.lock().unwrap().take();
    if let Some(url) = pending {
        apply(webview, &url);
    }
}

fn stash(url: Option<String>) {
    let Some(url) = url else { return };
    if sso_query(&url).is_some() {
        *PENDING.lock().unwrap() = Some(url);
    }
}

// is_open_link reports whether the URL is the plain bnbloc://open link used
// by the web UI's "Open the app" affordance: it carries no payload — the app
// is (or was) already signed in via its persisted session; activating the
// window is the entire job.
fn is_open_link(url: &str) -> bool {
    match tauri::Url::parse(url) {
        Ok(parsed) => parsed.scheme() == "bnbloc" && parsed.host_str() == Some("open"),
        Err(_) => false,
    }
}

// sso_query extracts the one-time hand-off code from bnbloc://sso?code=…;
// any other link shape (including a legacy token link) is ignored.
fn sso_query(url: &str) -> Option<String> {
    let parsed = tauri::Url::parse(url).ok()?;
    if parsed.scheme() != "bnbloc" || parsed.host_str() != Some("sso") {
        return None;
    }
    parsed
        .query_pairs()
        .find(|(k, _)| k == "code")
        .map(|(_, v)| v.into_owned())
        .filter(|c| !c.is_empty())
}

// apply loads the SPA with the hand-off code fragment and reloads. The reload
// matters on a warm start: the running SPA booted before this link arrived,
// so only a fresh boot re-runs the session read (which redeems the code).
fn apply(webview: &tauri::Webview, url: &str) {
    let Some(code) = sso_query(url) else {
        return;
    };
    let payload = serde_json::json!({ "code": code });
    let _ = webview.eval(format!(
        "(function(){{var p={p};\
window.location.hash='ssoCode='+encodeURIComponent(p.code);\
window.location.reload();}})();",
        p = payload
    ));
}

#[cfg(test)]
mod tests {
    use super::sso_query;

    #[test]
    fn sso_query_accepts_only_a_code() {
        assert_eq!(sso_query("bnbloc://sso?code=abc"), Some("abc".to_string()));
        assert_eq!(sso_query("bnbloc://sso?token=t&subject=s"), None);
        assert_eq!(sso_query("bnbloc://sso?code="), None);
        assert_eq!(sso_query("bnbloc://open?code=abc"), None);
        assert_eq!(sso_query("https://sso?code=abc"), None);
    }
}
