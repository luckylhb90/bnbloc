fn main() {
    // BNBLOC_DEFAULT_GATEWAY is the deployment's gateway baked into a release
    // build (src/updater_client.rs). It is deployment data, so the source has
    // no fallback; a release built without it starts with no gateway and the
    // member must pick one in the UI. Say so loudly at build time.
    println!("cargo:rerun-if-env-changed=BNBLOC_DEFAULT_GATEWAY");
    let unset = std::env::var("BNBLOC_DEFAULT_GATEWAY")
        .map(|v| v.trim().is_empty())
        .unwrap_or(true);
    if unset && std::env::var("PROFILE").as_deref() == Ok("release") {
        println!(
            "cargo:warning=BNBLOC_DEFAULT_GATEWAY is not set: this release build has no default gateway (see docs/desktop-update-release-runbook.md)"
        );
    }
    tauri_build::build()
}
