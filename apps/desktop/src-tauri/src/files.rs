//! The one writer and reader of the shell's state files: settings, plugin
//! local settings, MCP member credentials and sign-ins, core manifests, the
//! MCP tools index, spawn records.
//!
//! - `atomic_write`: owner-only (0600 on Unix), through a temp file with a
//!   name unique to this write (two writers of one path never share a temp),
//!   synced before the rename, so a reader sees the old file or the new one,
//!   never half of either.
//! - `read_json`: a missing file is `None`; a file that does not parse is
//!   moved aside (`<name>.corrupt-<unix ms>`) and reported, never silently
//!   treated as empty and then overwritten (that erased credentials).
//! - `owner_name`: which file a temp, partial download or quarantined copy
//!   belongs to, so a sweep forgets them with their component.
use std::io::Write;
use std::path::{Path, PathBuf};

/// Suffix of a quarantined unreadable file: `<name>.corrupt-<unix ms>`.
const CORRUPT: &str = ".corrupt-";

/// Write `raw` to `path` atomically and owner-only.
pub(crate) fn atomic_write(path: &Path, raw: &[u8]) -> Result<(), String> {
    let dir = path
        .parent()
        .ok_or_else(|| format!("{} has no parent dir", path.display()))?;
    std::fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
    let tmp = temp_path(path, "tmp")?;
    let written = (|| {
        let mut options = std::fs::OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let mut file = options.open(&tmp)?;
        file.write_all(raw)?;
        file.sync_all()
    })();
    if let Err(e) = written {
        let _ = std::fs::remove_file(&tmp);
        return Err(format!("write {}: {e}", path.display()));
    }
    std::fs::rename(&tmp, path).map_err(|e| {
        let _ = std::fs::remove_file(&tmp);
        format!("replace {}: {e}", path.display())
    })
}

/// A sibling of `path` unique to one writer: `.<name>.<16 hex>.<ext>`
/// (`tmp` for `atomic_write`, `part` for a streamed download).
pub(crate) fn temp_path(path: &Path, ext: &str) -> Result<PathBuf, String> {
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .ok_or_else(|| format!("{} names no file", path.display()))?;
    let tag: String = crate::secret::random_bytes(8)?
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    Ok(path.with_file_name(format!(".{name}.{tag}.{ext}")))
}

/// The file a directory entry belongs to: itself, or the target of a temp
/// (`.<name>.<16 hex>.tmp`), a partial download (`.<name>.<16 hex>.part`) or
/// a quarantined copy (`<name>.corrupt-<ms>`).
pub(crate) fn owner_name(entry: &str) -> &str {
    if let Some(inner) = entry
        .strip_prefix('.')
        .and_then(|e| e.strip_suffix(".tmp").or_else(|| e.strip_suffix(".part")))
    {
        if let Some((name, tag)) = inner.rsplit_once('.') {
            if tag.len() == 16 && tag.bytes().all(|b| b.is_ascii_hexdigit()) && !name.is_empty() {
                return name;
            }
        }
    }
    if let Some((name, ms)) = entry.rsplit_once(CORRUPT) {
        if !name.is_empty() && !ms.is_empty() && ms.bytes().all(|b| b.is_ascii_digit()) {
            return name;
        }
    }
    entry
}

/// Decode the JSON file at `path`: `Ok(None)` when it does not exist. A file
/// that cannot be read or decoded is moved aside and reported (`Err` names
/// where it went), so the caller shows the problem instead of overwriting
/// what was there.
pub(crate) fn read_json<T: serde::de::DeserializeOwned>(path: &Path) -> Result<Option<T>, String> {
    let raw = match std::fs::read(path) {
        Ok(raw) => raw,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(quarantine(path, &e.to_string())),
    };
    serde_json::from_slice(&raw)
        .map(Some)
        .map_err(|e| quarantine(path, &e.to_string()))
}

fn quarantine(path: &Path, why: &str) -> String {
    let ms = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or_default();
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    let aside = path.with_file_name(format!("{name}{CORRUPT}{ms}"));
    let message = match std::fs::rename(path, &aside) {
        Ok(()) => format!(
            "{} was unreadable ({why}); it was moved aside to {}",
            path.display(),
            aside.display()
        ),
        Err(e) => format!(
            "{} is unreadable ({why}) and could not be moved aside: {e}",
            path.display()
        ),
    };
    eprintln!("[files] {message}");
    message
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(tag: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("bnbloc-files-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        dir
    }

    #[test]
    fn atomic_write_is_owner_only_and_leaves_no_temp() {
        let dir = temp_dir("write");
        let path = dir.join("state.json");
        atomic_write(&path, b"one").expect("write");
        atomic_write(&path, b"two").expect("replace");
        assert_eq!(std::fs::read(&path).unwrap(), b"two");
        let names: Vec<String> = std::fs::read_dir(&dir)
            .unwrap()
            .map(|e| e.unwrap().file_name().into_string().unwrap())
            .collect();
        assert_eq!(names, vec!["state.json".to_string()]);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&path).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o600);
        }
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn two_writers_never_share_a_temp() {
        let path = Path::new("/x/state.json");
        let a = temp_path(path, "tmp").unwrap();
        let b = temp_path(path, "tmp").unwrap();
        assert_ne!(a, b);
        for temp in [&a, &b] {
            let name = temp.file_name().unwrap().to_str().unwrap();
            assert_eq!(owner_name(name), "state.json", "{name}");
        }
    }

    #[test]
    fn owner_name_maps_temps_parts_and_quarantined_copies() {
        assert_eq!(owner_name("x.json"), "x.json");
        assert_eq!(owner_name(".x.json.0123456789abcdef.tmp"), "x.json");
        assert_eq!(owner_name(".core-aa.0123456789abcdef.part"), "core-aa");
        assert_eq!(owner_name("x.json.corrupt-1700000000000"), "x.json");
        // Not ours: unchanged.
        assert_eq!(owner_name(".x.json.tmp"), ".x.json.tmp");
        assert_eq!(owner_name(".x.json.short.tmp"), ".x.json.short.tmp");
        assert_eq!(owner_name("x.corrupt-later"), "x.corrupt-later");
    }

    #[test]
    fn an_unreadable_file_is_moved_aside_not_reset() {
        let dir = temp_dir("corrupt");
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("settings.json");
        assert_eq!(read_json::<serde_json::Value>(&path), Ok(None));
        std::fs::write(&path, b"{\"gatewayToken\": \"keep me\"").unwrap();
        let err = read_json::<serde_json::Value>(&path).expect_err("corrupt");
        assert!(err.contains("moved aside"), "{err}");
        assert!(!path.exists());
        let aside: Vec<String> = std::fs::read_dir(&dir)
            .unwrap()
            .map(|e| e.unwrap().file_name().into_string().unwrap())
            .collect();
        assert_eq!(aside.len(), 1);
        assert_eq!(owner_name(&aside[0]), "settings.json");
        assert_eq!(
            std::fs::read(dir.join(&aside[0])).unwrap(),
            b"{\"gatewayToken\": \"keep me\""
        );
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
