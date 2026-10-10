// Thin binary entry: delegates to the shared library so desktop and mobile
// entrypoints run identical setup.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[cfg(target_os = "macos")]
fn prevent_app_nap() {
    // The embedded bridge + sidecars hold long-lived SSE/claim loops. macOS
    // App Nap freezes a backgrounded app's processes entirely (observed: both
    // children parked in state SN with zero progress), which kills room
    // subscriptions, heartbeats, and job claiming until the user refocuses
    // the window. Declaring a user-initiated activity for the process
    // lifetime opts the app out of napping.
    use objc2_foundation::{NSActivityOptions, NSProcessInfo, NSString};
    let info = NSProcessInfo::processInfo();
    let reason = NSString::from_str("bnbloc runs local agent bridges and room streams");
    let activity = info.beginActivityWithOptions_reason(
        NSActivityOptions::UserInitiatedAllowingIdleSystemSleep,
        &reason,
    );
    // Leak the activity handle: it must outlive the declaration scope for the
    // whole process lifetime; ending it would re-enable napping.
    std::mem::forget(activity);
}

#[cfg(not(target_os = "macos"))]
fn prevent_app_nap() {}

fn main() {
    prevent_app_nap();
    bnbloc_desktop_lib::run();
}
