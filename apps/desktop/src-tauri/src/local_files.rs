//! Local documents the member opens in the app
//! (docs/prd/2026-10-14-ai-document-editing.md §4, M3): the one place a
//! path lives. The web never sees one: it gets an opaque token and the file
//! name, reads the bytes through the token, tells the shell which room file
//! the copy became (the binding), and saves a version back through the
//! token. The shell writes only a path the member opened.
//!
//! Entry points: the Open dialog (`local_file_pick`), and the OS handing
//! the app a file (Open With, a double-click on an associated type,
//! `bnbloc-desktop <file>`), which queues it for the web
//! (`local_file_take_pending`, announced by the `local-file-opened` event).
//!
//! State: `local-files.json` in the app data dir, one entry per path with
//! its binding and the hash of the bytes last synced with it (uploaded or
//! saved back): a reopen with other bytes is a new version, a save over
//! bytes the member changed since is a conflict.
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use tauri::{Emitter, Manager};

use crate::files;

/// The web's cue to take the queued opens.
pub(crate) const OPENED_EVENT: &str = "local-file-opened";
/// Room files are capped at the gateway (100 MiB by default); a larger
/// local file is refused before it is read.
const MAX_BYTES: u64 = 100 * 1024 * 1024;
/// Remembered paths; the least recently opened go first.
const MAX_ENTRIES: usize = 500;

/// The room file an opened document became.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
#[serde(rename_all = "camelCase")]
pub(crate) struct Binding {
    pub gateway: String,
    pub room_id: String,
    pub artifact_id: String,
    pub version: u32,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
#[serde(rename_all = "camelCase")]
struct Entry {
    token: String,
    path: PathBuf,
    /// The bytes last synced with the binding (sha256 hex).
    hash: String,
    opened_ms: u64,
    #[serde(default)]
    binding: Option<Binding>,
    /// The hash of the bytes the web last read: what a bind records.
    #[serde(skip)]
    read_hash: Option<String>,
}

/// What the web learns about an opened document.
#[derive(Serialize, Clone, Debug)]
#[serde(rename_all = "camelCase")]
pub(crate) struct Opened {
    token: String,
    name: String,
    size_bytes: u64,
    binding: Option<Binding>,
    /// Bound, and the file's bytes are not the ones last synced: the web
    /// uploads them as the next version.
    changed: bool,
}

/// The outcome of a save back.
#[derive(Serialize, Debug, PartialEq)]
#[serde(rename_all = "camelCase", tag = "status")]
pub(crate) enum Saved {
    /// The opened file now holds the version.
    Written,
    /// The file changed on disk since it was opened or last saved; nothing
    /// was written (the member chooses: overwrite or a copy).
    Changed,
    /// The file is gone; nothing was written (a copy still can be).
    Missing,
    /// Written beside the file under this name.
    Copied { name: String },
}

#[derive(Default)]
pub(crate) struct LocalFiles {
    inner: Mutex<State>,
}

#[derive(Default)]
struct State {
    file: Option<PathBuf>,
    entries: Vec<Entry>,
    pending: Vec<Opened>,
}

impl LocalFiles {
    pub(crate) fn load(file: Option<PathBuf>) -> Self {
        let entries = match file.as_deref().map(files::read_json::<Vec<Entry>>) {
            Some(Ok(Some(entries))) => entries,
            Some(Err(e)) => {
                eprintln!("[local-files] {e}; starting empty");
                Vec::new()
            }
            _ => Vec::new(),
        };
        Self {
            inner: Mutex::new(State {
                file,
                entries,
                pending: Vec::new(),
            }),
        }
    }

    fn with<T>(&self, f: impl FnOnce(&mut State) -> Result<T, String>) -> Result<T, String> {
        let mut st = self
            .inner
            .lock()
            .map_err(|_| "local files state poisoned".to_string())?;
        f(&mut st)
    }

    /// Records an opened path (a new token, or the path's existing one).
    pub(crate) fn open(&self, path: &Path) -> Result<Opened, String> {
        let path =
            std::fs::canonicalize(path).map_err(|e| format!("open {}: {e}", path.display()))?;
        let meta = std::fs::metadata(&path).map_err(|e| format!("open {}: {e}", path.display()))?;
        if !meta.is_file() {
            return Err(format!("{} is not a file", path.display()));
        }
        if meta.len() > MAX_BYTES {
            return Err(format!(
                "{} is larger than {} MiB",
                file_name(&path),
                MAX_BYTES >> 20
            ));
        }
        let hash = hash_file(&path)?;
        self.with(|st| {
            let now = now_ms();
            let at = match st.entries.iter().position(|e| e.path == path) {
                Some(i) => i,
                None => {
                    st.entries.push(Entry {
                        token: new_token()?,
                        path: path.clone(),
                        hash: hash.clone(),
                        opened_ms: now,
                        binding: None,
                        read_hash: None,
                    });
                    st.entries.len() - 1
                }
            };
            let e = &mut st.entries[at];
            e.opened_ms = now;
            let opened = Opened {
                token: e.token.clone(),
                name: file_name(&path),
                size_bytes: meta.len(),
                binding: e.binding.clone(),
                changed: e.binding.is_some() && e.hash != hash,
            };
            if e.binding.is_none() {
                e.hash = hash;
            }
            st.prune();
            st.persist()?;
            Ok(opened)
        })
    }

    /// Queues an open the OS handed the app, for the web to take.
    pub(crate) fn queue(&self, app: &tauri::AppHandle, path: &Path) {
        match self.open(path) {
            Ok(opened) => {
                let _ = self.with(|st| {
                    st.pending.retain(|p| p.token != opened.token);
                    st.pending.push(opened);
                    Ok(())
                });
                // To the app window in front (a person with two accounts open
                // two windows): its page takes the file.
                match crate::windows::focus_app_window(app) {
                    Some(label) => {
                        let _ = app.emit_to(label, OPENED_EVENT, ());
                    }
                    None => {
                        let _ = app.emit(OPENED_EVENT, ());
                    }
                }
            }
            Err(e) => eprintln!("[local-files] {e}"),
        }
    }

    fn entry_path(&self, token: &str) -> Result<PathBuf, String> {
        self.with(|st| Ok(st.entry(token)?.path.clone()))
    }
}

impl State {
    fn entry(&mut self, token: &str) -> Result<&mut Entry, String> {
        self.entries
            .iter_mut()
            .find(|e| e.token == token)
            .ok_or_else(|| "that file is no longer open in the app".to_string())
    }

    fn prune(&mut self) {
        if self.entries.len() > MAX_ENTRIES {
            self.entries.sort_by_key(|e| std::cmp::Reverse(e.opened_ms));
            self.entries.truncate(MAX_ENTRIES);
        }
    }

    fn persist(&self) -> Result<(), String> {
        let Some(file) = &self.file else {
            return Ok(());
        };
        let raw = serde_json::to_vec_pretty(&self.entries).map_err(|e| e.to_string())?;
        files::atomic_write(file, &raw)
    }
}

/// Paths the OS handed the app before setup managed the state (a cold
/// start through Open With can deliver them first): setup takes them.
static EARLY: Mutex<Vec<PathBuf>> = Mutex::new(Vec::new());

/// Opens the paths the OS handed the app, now or once setup ran.
pub(crate) fn hand(app: &tauri::AppHandle, paths: Vec<PathBuf>) {
    match app.try_state::<LocalFiles>() {
        Some(state) => {
            for path in paths {
                state.queue(app, &path);
            }
        }
        None => {
            if let Ok(mut early) = EARLY.lock() {
                early.extend(paths);
            }
        }
    }
}

/// The paths handed over before setup (see EARLY).
pub(crate) fn take_early() -> Vec<PathBuf> {
    EARLY
        .lock()
        .map(|mut e| std::mem::take(&mut *e))
        .unwrap_or_default()
}

/// The paths among a launch's arguments (argv[0] excluded): a shell's
/// `bnbloc-desktop <file>`, or a second launch forwarded by the
/// single-instance plugin. Deep links and flags are not paths.
pub(crate) fn paths_in_args(args: &[String]) -> Vec<PathBuf> {
    args.iter()
        .skip(1)
        .filter(|a| !a.starts_with('-') && !a.contains("://"))
        .map(PathBuf::from)
        .filter(|p| p.is_file())
        .collect()
}

/// The file paths among URLs the OS opened the app with (macOS Open With).
pub(crate) fn paths_in_urls(urls: &[tauri::Url]) -> Vec<PathBuf> {
    urls.iter()
        .filter(|u| u.scheme() == "file")
        .filter_map(|u| u.to_file_path().ok())
        .collect()
}

/// A sibling name for a copy that never replaces an existing file:
/// `deck (bnbloc v5).pptx`, then `deck (bnbloc v5) 2.pptx`, …
pub(crate) fn copy_path(path: &Path, version: u32) -> PathBuf {
    let stem = path
        .file_stem()
        .map(|s| s.to_string_lossy().into_owned())
        .unwrap_or_default();
    let ext = path
        .extension()
        .map(|e| format!(".{}", e.to_string_lossy()))
        .unwrap_or_default();
    let base = format!("{stem} (bnbloc v{version})");
    let mut n = 1;
    loop {
        let name = if n == 1 {
            format!("{base}{ext}")
        } else {
            format!("{base} {n}{ext}")
        };
        let candidate = path.with_file_name(name);
        if !candidate.exists() {
            return candidate;
        }
        n += 1;
    }
}

fn file_name(path: &Path) -> String {
    path.file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default()
}

fn hash_bytes(raw: &[u8]) -> String {
    Sha256::digest(raw)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

fn hash_file(path: &Path) -> Result<String, String> {
    let raw = std::fs::read(path).map_err(|e| format!("read {}: {e}", path.display()))?;
    Ok(hash_bytes(&raw))
}

fn new_token() -> Result<String, String> {
    Ok(crate::secret::random_bytes(16)?
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect())
}

fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn header<'a>(request: &'a tauri::ipc::Request<'_>, name: &str) -> Result<&'a str, String> {
    request
        .headers()
        .get(name)
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| format!("missing {name}"))
}

// --- commands -------------------------------------------------------------

/// The Open dialog. None when the member cancels.
#[tauri::command]
pub(crate) async fn local_file_pick(app: tauri::AppHandle) -> Result<Option<Opened>, String> {
    use tauri_plugin_dialog::DialogExt;
    let picked = tauri::async_runtime::spawn_blocking(move || {
        let path = app
            .dialog()
            .file()
            .set_title("Open a file")
            .blocking_pick_file();
        (app, path)
    })
    .await
    .map_err(|e| e.to_string())?;
    let (app, path) = picked;
    let Some(path) = path else { return Ok(None) };
    let path = path.into_path().map_err(|e| e.to_string())?;
    app.state::<LocalFiles>().open(&path).map(Some)
}

/// The opens the OS handed the app since the web last asked.
#[tauri::command]
pub(crate) fn local_file_take_pending(
    state: tauri::State<'_, LocalFiles>,
) -> Result<Vec<Opened>, String> {
    state.with(|st| Ok(std::mem::take(&mut st.pending)))
}

/// An opened file's bytes, as they are on disk now.
#[tauri::command]
pub(crate) fn local_file_read(
    state: tauri::State<'_, LocalFiles>,
    token: String,
) -> Result<tauri::ipc::Response, String> {
    let path = state.entry_path(&token)?;
    let raw = std::fs::read(&path).map_err(|e| format!("read {}: {e}", file_name(&path)))?;
    let hash = hash_bytes(&raw);
    state.with(|st| {
        st.entry(&token)?.read_hash = Some(hash);
        Ok(())
    })?;
    Ok(tauri::ipc::Response::new(raw))
}

/// Records the room file the bytes last read became.
#[tauri::command]
pub(crate) fn local_file_bind(
    state: tauri::State<'_, LocalFiles>,
    token: String,
    binding: Binding,
) -> Result<(), String> {
    state.with(|st| {
        let e = st.entry(&token)?;
        if let Some(h) = e.read_hash.take() {
            e.hash = h;
        }
        e.binding = Some(binding);
        st.persist()
    })
}

/// The opened file a room file came from, if any: its token and name
/// (the tab offers "Save to <name>").
#[tauri::command]
pub(crate) fn local_file_for_artifact(
    state: tauri::State<'_, LocalFiles>,
    gateway: String,
    artifact_id: String,
) -> Result<Option<serde_json::Value>, String> {
    state.with(|st| {
        Ok(st
            .entries
            .iter()
            .find(|e| {
                e.binding
                    .as_ref()
                    .is_some_and(|b| b.gateway == gateway && b.artifact_id == artifact_id)
            })
            .map(|e| serde_json::json!({ "token": e.token, "name": file_name(&e.path) })))
    })
}

/// Saves a version back: the raw body is the bytes; headers name the
/// token (`x-bnbloc-token`), the version (`x-bnbloc-version`) and the mode
/// (`x-bnbloc-mode`): `check` writes only over the bytes last synced,
/// `overwrite` writes regardless, `copy` writes beside the file.
#[tauri::command]
pub(crate) fn local_file_save(
    state: tauri::State<'_, LocalFiles>,
    request: tauri::ipc::Request<'_>,
) -> Result<Saved, String> {
    let tauri::ipc::InvokeBody::Raw(raw) = request.body() else {
        return Err("the bytes must be the raw body".into());
    };
    let token = header(&request, "x-bnbloc-token")?;
    let mode = header(&request, "x-bnbloc-mode")?;
    let version: u32 = header(&request, "x-bnbloc-version")?
        .parse()
        .map_err(|_| "bad version".to_string())?;
    let path = state.entry_path(token)?;
    let saved = save(
        &path,
        raw,
        version,
        mode,
        &state.with(|st| Ok(st.entry(token)?.hash.clone()))?,
    )?;
    if saved == Saved::Written {
        let hash = hash_bytes(raw);
        state.with(|st| {
            let e = st.entry(token)?;
            e.hash = hash;
            if let Some(b) = e.binding.as_mut() {
                b.version = version;
            }
            st.persist()
        })?;
    }
    Ok(saved)
}

/// The write itself: what a mode does to the file at path, given the hash
/// of the bytes last synced with it.
fn save(path: &Path, raw: &[u8], version: u32, mode: &str, synced: &str) -> Result<Saved, String> {
    match mode {
        "copy" => {
            let to = copy_path(path, version);
            files::write_document(&to, raw)?;
            Ok(Saved::Copied {
                name: file_name(&to),
            })
        }
        "check" | "overwrite" => {
            if !path.exists() {
                return Ok(Saved::Missing);
            }
            if mode == "check" && hash_file(path)? != synced {
                return Ok(Saved::Changed);
            }
            files::write_document(path, raw)?;
            Ok(Saved::Written)
        }
        _ => Err(format!("unknown save mode {mode}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(tag: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("bnbloc-local-files-{tag}-{}", new_token().unwrap()));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn a_reopen_keeps_the_token_and_sees_changed_bytes() {
        let dir = temp_dir("reopen");
        let doc = dir.join("deck.pptx");
        std::fs::write(&doc, b"v1").unwrap();
        let lf = LocalFiles::load(Some(dir.join("state.json")));
        let first = lf.open(&doc).unwrap();
        assert_eq!(first.name, "deck.pptx");
        assert!(!first.changed, "an unbound file is never changed");
        lf.with(|st| {
            st.entry(&first.token)?.binding = Some(Binding {
                gateway: "https://bnbloc.example.com".into(),
                room_id: "files".into(),
                artifact_id: "art_1".into(),
                version: 1,
            });
            st.persist()
        })
        .unwrap();

        // Restarted: the same path, same token; edited on disk meanwhile.
        std::fs::write(&doc, b"v1 edited").unwrap();
        let lf = LocalFiles::load(Some(dir.join("state.json")));
        let again = lf.open(&doc).unwrap();
        assert_eq!(again.token, first.token);
        assert!(
            again.changed,
            "bytes other than the synced ones are a new version"
        );
        assert_eq!(again.binding.unwrap().artifact_id, "art_1");
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn a_save_writes_only_over_the_synced_bytes() {
        let dir = temp_dir("save");
        let doc = dir.join("deck.pptx");
        std::fs::write(&doc, b"v1").unwrap();
        let synced = hash_bytes(b"v1");

        assert_eq!(
            save(&doc, b"v2", 2, "check", &synced).unwrap(),
            Saved::Written
        );
        assert_eq!(std::fs::read(&doc).unwrap(), b"v2");

        // The member changed the file since: a check writes nothing.
        std::fs::write(&doc, b"mine").unwrap();
        assert_eq!(
            save(&doc, b"v3", 3, "check", &hash_bytes(b"v2")).unwrap(),
            Saved::Changed
        );
        assert_eq!(std::fs::read(&doc).unwrap(), b"mine");

        // A copy goes beside it and never replaces another file.
        assert_eq!(
            save(&doc, b"v3", 3, "copy", "").unwrap(),
            Saved::Copied {
                name: "deck (bnbloc v3).pptx".into()
            }
        );
        assert_eq!(
            save(&doc, b"v3", 3, "copy", "").unwrap(),
            Saved::Copied {
                name: "deck (bnbloc v3) 2.pptx".into()
            }
        );
        assert_eq!(std::fs::read(&doc).unwrap(), b"mine");

        assert_eq!(
            save(&doc, b"v3", 3, "overwrite", "").unwrap(),
            Saved::Written
        );
        assert_eq!(std::fs::read(&doc).unwrap(), b"v3");

        std::fs::remove_file(&doc).unwrap();
        assert_eq!(save(&doc, b"v4", 4, "check", "").unwrap(), Saved::Missing);
        assert!(save(&doc, b"v4", 4, "sideways", "").is_err());
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn launch_arguments_name_only_existing_files() {
        let dir = temp_dir("args");
        let doc = dir.join("q3.xlsx");
        std::fs::write(&doc, b"x").unwrap();
        let args = vec![
            "/Applications/bnbloc.app/Contents/MacOS/bnbloc".to_string(),
            "--flag".into(),
            "bnbloc://sso?code=1".into(),
            doc.display().to_string(),
            dir.join("missing.docx").display().to_string(),
        ];
        assert_eq!(paths_in_args(&args), vec![doc.clone()]);
        let url = tauri::Url::from_file_path(&doc).unwrap();
        assert_eq!(
            paths_in_urls(&[url, tauri::Url::parse("https://x.example").unwrap()]),
            vec![doc]
        );
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
