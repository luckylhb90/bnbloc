//! Where the shell keeps things on disk: the one resolution of the app's
//! cache, config and data directories (Tauri's path resolver), resolved once
//! at startup, and of every directory the shell names below them.
//!
//! - cache (rebuildable, machine-wide): sidecar binaries, previewer bundles,
//!   MCP tool binaries, spawn records, sandbox bundles (what a catalog
//!   decides, the core manifests and the MCP tools index, is the member's:
//!   `member_dir`);
//! - config: the member directories (`member_dir`), one per gateway, member
//!   and tenant;
//! - data: the settings file and the opened local documents.
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

    /// Every directory under `root`, for tests that need an adapter without
    /// an app (the adapter holds no app handle).
    #[cfg(test)]
    pub(crate) fn under(root: &Path) -> Self {
        Self {
            cache: root.join("cache"),
            config: root.join("config"),
            data: root.join("data"),
        }
    }

    /// The persisted shell settings (settings.rs).
    pub(crate) fn settings_file(&self) -> PathBuf {
        self.data.join("settings.json")
    }

    /// The local documents the member opened (local_files.rs).
    pub(crate) fn local_files_file(&self) -> PathBuf {
        self.data.join("local-files.json")
    }

    /// The app config dir: the member directories live below it
    /// (member_dir).
    pub(crate) fn config(&self) -> &Path {
        &self.config
    }

    /// The app cache dir: what a confined agent may read (no secret).
    pub(crate) fn cache(&self) -> &Path {
        &self.cache
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

    /// Materialized stdio MCP tool binaries (content-addressed, shared by
    /// every account; each member's index of them is in its own directory).
    pub(crate) fn mcp_tools(&self) -> PathBuf {
        self.cache.join("mcp-tools")
    }

    /// Where the manifest cores and the MCP tools index used to be kept,
    /// machine-wide: removed at startup, not migrated (the member
    /// directories hold them now).
    pub(crate) fn retired_core_manifests(&self) -> PathBuf {
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
