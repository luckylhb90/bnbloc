//! The windows of the app, and the one scheme of their labels
//! (docs/prd/2026-10-22-multi-account.md §2.2).
//!
//! An **app window** shows one account: `main` (the first) or `app-<account>-
//! <n>` (opened for an account, or `app-new-<n>` to sign one in). An
//! **auxiliary window** (a torn-out preview, a people chat, a live page) is
//! just a view of the account of the window that opened it:
//! `<kind>-<account>-<id>`. The app ends when its last app window closes;
//! closing an auxiliary window never does. Every label must match a
//! capability of `capabilities/default.json`, or the window gets no IPC (a test
//! holds it).
use std::sync::atomic::{AtomicU64, Ordering};

use tauri::Manager;

use crate::account::AccountKey;

/// The first app window.
pub(crate) const MAIN: &str = "main";
/// What every other app window's label starts with.
const APP_PREFIX: &str = "app-";

/// The kinds of auxiliary window; each is a prefix of its labels.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum AuxKind {
    Preview,
    People,
    Live,
}

impl AuxKind {
    pub(crate) fn prefix(self) -> &'static str {
        match self {
            AuxKind::Preview => "preview",
            AuxKind::People => "people",
            AuxKind::Live => "live",
        }
    }
}

/// A label carries only `[A-Za-z0-9_-]` (Tauri's label alphabet minus its
/// separators); anything else of an id becomes `_`.
pub(crate) fn label_part(id: &str) -> String {
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

/// The label of a new app window: for `account`, or to sign one in.
pub(crate) fn app_label(account: Option<&AccountKey>) -> String {
    static NEXT: AtomicU64 = AtomicU64::new(1);
    let n = NEXT.fetch_add(1, Ordering::SeqCst);
    let who = account.map_or_else(|| "new".to_string(), AccountKey::short);
    format!("{APP_PREFIX}{who}-{n}")
}

/// The label of the auxiliary window of `kind` for `id`, of `account`: the
/// same artifact of two accounts is two windows.
pub(crate) fn aux_label(kind: AuxKind, account: &AccountKey, id: &str) -> String {
    format!("{}-{}-{}", kind.prefix(), account.short(), label_part(id))
}

/// Whether a window is an app window: it shows an account for its own sake,
/// and the app ends with the last one.
pub(crate) fn is_app_window(label: &str) -> bool {
    label == MAIN || label.starts_with(APP_PREFIX)
}

/// The app windows of `labels` other than `closing`: when none is left, the
/// app ends.
pub(crate) fn app_windows_left<'a>(
    labels: impl IntoIterator<Item = &'a str>,
    closing: &str,
) -> usize {
    labels
        .into_iter()
        .filter(|l| *l != closing && is_app_window(l))
        .count()
}

/// Whether closing `label` ends the app.
pub(crate) fn closing_ends_app(app: &tauri::AppHandle, label: &str) -> bool {
    is_app_window(label)
        && app_windows_left(app.webview_windows().keys().map(String::as_str), label) == 0
}

/// Bring an app window to the front (the one in front already, else the first
/// by label) and say which; None when none is open.
pub(crate) fn focus_app_window(app: &tauri::AppHandle) -> Option<String> {
    let windows = app.webview_windows();
    let mut labels: Vec<&String> = windows.keys().filter(|l| is_app_window(l)).collect();
    labels.sort();
    let focused = labels
        .iter()
        .find(|l| windows[**l].is_focused().unwrap_or(false))
        .or_else(|| labels.first())
        .copied()?;
    let win = &windows[focused];
    let _ = win.show();
    let _ = win.unminimize();
    let _ = win.set_focus();
    Some(focused.clone())
}

/// The hash a new window opens on, which says what it is for (the web reads
/// it at boot, web/src/lib/windowBinding.ts `intentHash`): to show the account
/// whose key (the web's `accountKey`: gateway, a line feed, subject) it names,
/// or to sign another in.
pub(crate) fn intent_hash(account_key: Option<&str>) -> String {
    match account_key {
        Some(key) => format!("#!account={}", percent_encode(key)),
        None => "#!add-account".to_string(),
    }
}

/// Percent-encodes everything but the RFC 3986 unreserved characters, as
/// `encodeURIComponent` does (the web decodes with it).
pub(crate) fn percent_encode(raw: &str) -> String {
    let mut out = String::with_capacity(raw.len());
    for b in raw.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

#[cfg(test)]
#[path = "windows_tests.rs"]
mod windows_tests;
