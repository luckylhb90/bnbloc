//! Where the shell keeps things on disk: the one resolution of the app's
//! cache, config and data directories (Tauri's path resolver), resolved once
//! at startup, and of every directory the shell names below them.
//!
//! - cache (rebuildable, machine-wide): sidecar binaries, previewer bundles,
//!   MCP tool binaries, core manifests, spawn records, sandbox bundles;
//! - config: the member directories (`member_dir`), one per gateway, member
//!   and tenant;
//! - data: the settings file.
use std::path::{Path, PathBuf};

#[derive(Clone, Debug)]
pub(crate) struct Dirs {
    cache: PathBuf,
    config: PathBuf,
    data: PathBuf,
}

impl Dirs {
    pub(crate) fn resolve(app: &tauri::AppHandle) -> Result<Self, String> {
        use tauri::Manager;
        let path = app.path();
        Ok(Self {
            cache: path
                .app_cache_dir()
                .map_err(|e| format!("resolve the app cache dir: {e}"))?,
            config: path
                .app_config_dir()
                .map_err(|e| format!("resolve the app config dir: {e}"))?,
            data: path
                .app_data_dir()
                .map_err(|e| format!("resolve the app data dir: {e}"))?,
        })
    }

    /// The persisted shell settings (settings.rs).
    pub(crate) fn settings_file(&self) -> PathBuf {
        self.data.join("settings.json")
    }

    /// The app config dir: the member directories live below it
    /// (member_dir).
    pub(crate) fn config(&self) -> &Path {
        &self.config
    }

    /// The app data dir (only the unkeyed state of earlier builds is
    /// removed from it, member_dir::remove_unkeyed_dirs).
    pub(crate) fn data(&self) -> &Path {
        &self.data
    }

    /// Verified sidecar binaries (cores and plugins), by cache stem.
    pub(crate) fn binaries(&self) -> PathBuf {
        self.cache.join("cores")
    }

    /// Verified previewer bundle archives, by sha256.
    pub(crate) fn previewers(&self) -> PathBuf {
        self.cache.join("previewers")
    }

    /// Materialized stdio MCP tool binaries and their index.
    pub(crate) fn mcp_tools(&self) -> PathBuf {
        self.cache.join("mcp-tools")
    }

    /// The manifest cores the bridge reads at spawn.
    pub(crate) fn manifests(&self) -> PathBuf {
        self.cache.join("core-manifests")
    }

    /// Spawn records of running sidecars (sidecar_orphan).
    pub(crate) fn spawn_records(&self) -> PathBuf {
        self.cache.join("sidecar-pids")
    }

    /// Sandbox bundles, one directory per sandbox component.
    pub(crate) fn sandbox_bundles(&self) -> PathBuf {
        self.cache.join("sandbox")
    }
}
