//! sha256 pins: the one parse of a catalog pin and the one check of bytes and
//! files against it.
//!
//! Every artifact the desktop runs or serves (core and plugin binaries,
//! sandbox bundle files, stdio MCP tool binaries, previewer bundles) carries
//! the sha256 the gateway pinned in the member catalog. The pin is parsed
//! once, when the catalog is decoded: 64 hex digits, normalized to lower
//! case, so an upper-case pin of any kind compares like a lower-case one and
//! a malformed pin drops only its catalog entry. Spawn records carry the
//! same type.
//!
//! A file is checked against a pin by hashing it on a blocking thread. A
//! file verified before and unchanged since (same length and modification
//! time) is not hashed again, so a reconcile does not re-read every binary.
use std::collections::HashMap;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::sync::OnceLock;
use std::time::SystemTime;

use parking_lot::Mutex;
use sha2::{Digest, Sha256};

use crate::internal_ext::sidecar_util::hex_lower;

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub(crate) struct Sha256Pin(String);

impl Sha256Pin {
    /// A pin from its text: 64 hex digits, any case.
    pub(crate) fn parse(raw: &str) -> Result<Self, String> {
        let pin = raw.trim().to_ascii_lowercase();
        if pin.len() == 64 && pin.bytes().all(|b| b.is_ascii_hexdigit()) {
            Ok(Self(pin))
        } else {
            Err(format!("{raw:?} is not a sha256 pin (64 hex digits)"))
        }
    }

    /// The pin of `bytes`.
    pub(crate) fn of(bytes: &[u8]) -> Self {
        Self(hex_lower(&Sha256::digest(bytes)))
    }

    /// The lower-case hex form.
    pub(crate) fn as_str(&self) -> &str {
        &self.0
    }

    /// Refuse bytes that do not hash to this pin.
    pub(crate) fn check(&self, bytes: &[u8]) -> Result<(), String> {
        let got = Self::of(bytes);
        if &got == self {
            Ok(())
        } else {
            Err(format!("sha256 mismatch: got {got}, want {self}"))
        }
    }

    /// Whether the file at `path` exists and hashes to this pin.
    pub(crate) async fn file_matches(&self, path: &Path) -> bool {
        let (pin, path) = (self.clone(), path.to_path_buf());
        tauri::async_runtime::spawn_blocking(move || pin.file_matches_blocking(&path))
            .await
            .unwrap_or(false)
    }

    fn file_matches_blocking(&self, path: &Path) -> bool {
        let Some(stamp) = Stamp::of(path) else {
            verified().lock().remove(path);
            return false;
        };
        if verified().lock().get(path) == Some(&(stamp.clone(), self.clone())) {
            return true;
        }
        let ok = file_sha256(path).is_ok_and(|got| got == self.0);
        if ok {
            self.remember(path, stamp);
        } else {
            verified().lock().remove(path);
        }
        ok
    }

    /// Record that the file at `path` was just verified against this pin
    /// (a download hashed its bytes while writing them).
    pub(crate) fn verified(&self, path: &Path) {
        if let Some(stamp) = Stamp::of(path) {
            self.remember(path, stamp);
        }
    }

    fn remember(&self, path: &Path, stamp: Stamp) {
        verified()
            .lock()
            .insert(path.to_path_buf(), (stamp, self.clone()));
    }
}

impl std::fmt::Display for Sha256Pin {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl<'de> serde::Deserialize<'de> for Sha256Pin {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let raw = String::deserialize(deserializer)?;
        Self::parse(&raw).map_err(serde::de::Error::custom)
    }
}

impl serde::Serialize for Sha256Pin {
    fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&self.0)
    }
}

/// What identifies a file's content cheaply: its length and modification
/// time (None where the platform has no modification time: never cached).
#[derive(Clone, Debug, PartialEq, Eq)]
struct Stamp(u64, SystemTime);

impl Stamp {
    fn of(path: &Path) -> Option<Self> {
        let meta = std::fs::metadata(path).ok()?;
        Some(Self(meta.len(), meta.modified().ok()?))
    }
}

/// Files verified against a pin, by path.
fn verified() -> &'static Mutex<HashMap<PathBuf, (Stamp, Sha256Pin)>> {
    static VERIFIED: OnceLock<Mutex<HashMap<PathBuf, (Stamp, Sha256Pin)>>> = OnceLock::new();
    VERIFIED.get_or_init(Default::default)
}

fn file_sha256(path: &Path) -> std::io::Result<String> {
    let mut file = std::fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0_u8; 64 * 1024];
    loop {
        let n = file.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hex_lower(&hasher.finalize()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pins_are_parsed_once_and_normalized() {
        let upper = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789";
        let pin = Sha256Pin::parse(&format!(" {upper} ")).expect("pin");
        assert_eq!(pin.as_str(), upper.to_ascii_lowercase());
        assert_eq!(pin, Sha256Pin::parse(&upper.to_ascii_lowercase()).unwrap());
        for bad in ["", "abc", &"g".repeat(64), &"a".repeat(65)] {
            assert!(Sha256Pin::parse(bad).is_err(), "{bad:?} accepted");
        }
        let decoded: Sha256Pin = serde_json::from_str(&format!("\"{upper}\"")).expect("decode");
        assert_eq!(decoded, pin);
        assert!(serde_json::from_str::<Sha256Pin>("\"abc\"").is_err());
        assert_eq!(Sha256Pin::of(b"abc").check(b"abc"), Ok(()));
        assert!(Sha256Pin::of(b"abc").check(b"abd").is_err());
    }

    #[tokio::test]
    async fn files_are_checked_and_a_change_is_seen() {
        let path = std::env::temp_dir().join(format!("bnbloc-pin-test-{}", std::process::id()));
        std::fs::write(&path, b"abc").expect("write");
        let abc = Sha256Pin::of(b"abc");
        assert!(abc.file_matches(&path).await);
        assert!(abc.file_matches(&path).await, "cached");
        assert!(!Sha256Pin::of(b"xyz").file_matches(&path).await);
        std::fs::write(&path, b"abcd").expect("rewrite");
        assert!(
            !abc.file_matches(&path).await,
            "a changed file is hashed again"
        );
        std::fs::remove_file(&path).expect("cleanup");
        assert!(!abc.file_matches(&path).await);
    }
}
