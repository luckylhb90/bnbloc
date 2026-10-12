//! The signed-in member's private directory on this computer.
//!
//! Desktop-local secrets (plugin `localSettings`, MCP tool member
//! credentials, MCP OAuth sign-ins) belong to one member of one gateway, and
//! several members may sign in on the same desktop one after the other. They
//! live under `<app config>/members/<key>/`, `<key>` a hash of the gateway
//! origin and the member's subject, so member B never launches a plugin with
//! A's bot credentials or drives with A's MCP tokens, and A finds their
//! sign-ins again when they come back.
//!
//! `member_dir` is the one rule for that directory. The sidecar manager uses
//! the subject of the session it runs with; the bridge env uses the bridge's
//! own owner, so a bridge only ever reads its owner's credentials.
//!
//! The directories that predate this layout (`<app config>/plugin-settings`,
//! `<app config>/mcp-settings`) held the secrets of whichever member signed
//! in; they are deleted at startup, not migrated.
use std::path::{Path, PathBuf};

use reqwest::Url;
use sha2::{Digest, Sha256};

use crate::internal_ext::sidecar_util::hex_lower;

/// `<app config>/members`: one directory per (gateway, member).
const MEMBERS_DIR: &str = "members";
/// A member's plugin `localSettings` (`<member>/plugin-settings/<plugin>.json`).
pub(super) const PLUGIN_SETTINGS_DIR: &str = "plugin-settings";
/// A member's MCP tool credentials and sign-ins (`<member>/mcp-settings/`).
pub(super) const MCP_SETTINGS_DIR: &str = "mcp-settings";

/// The private directory of `subject` on the gateway at `gateway_url`, under
/// the app config dir. None without a gateway origin or a subject.
pub(crate) fn member_dir(config_dir: &Path, gateway_url: &str, subject: &str) -> Option<PathBuf> {
    let url = Url::parse(gateway_url.trim()).ok()?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return None;
    }
    let subject = subject.trim();
    if subject.is_empty() {
        return None;
    }
    let mut hasher = Sha256::new();
    hasher.update(url.origin().ascii_serialization().as_bytes());
    hasher.update(b"\n");
    hasher.update(subject.as_bytes());
    let key = hex_lower(&hasher.finalize());
    Some(config_dir.join(MEMBERS_DIR).join(&key[..32]))
}

/// The subject (`sub`) a gateway session token names. The token is
/// `<base64url JSON claims>.<signature>`; it is not verified here (the
/// gateway does that on every call): the subject only picks which member's
/// directory this desktop uses for the catalog fetched with that token.
pub(super) fn token_subject(token: &str) -> Option<String> {
    let (payload, _) = token.trim().split_once('.')?;
    let claims: serde_json::Value = serde_json::from_slice(&b64url_decode(payload)?).ok()?;
    claims["sub"]
        .as_str()
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_string)
}

/// Delete the member-unkeyed secret directories of earlier builds. Blocking,
/// best-effort.
pub(super) fn remove_unkeyed_dirs(config_dir: &Path) {
    for name in [PLUGIN_SETTINGS_DIR, MCP_SETTINGS_DIR] {
        let dir = config_dir.join(name);
        match std::fs::remove_dir_all(&dir) {
            Ok(()) => eprintln!("[sidecar-manager] removed unkeyed {}", dir.display()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => eprintln!("[sidecar-manager] could not remove {}: {e}", dir.display()),
        }
    }
}

/// base64url (RFC 4648 §5), padding optional.
fn b64url_decode(s: &str) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len() * 3 / 4);
    let (mut acc, mut bits) = (0u32, 0u32);
    for c in s.trim_end_matches('=').bytes() {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'-' => 62,
            b'_' => 63,
            _ => return None,
        };
        acc = (acc << 6) | u32::from(v);
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
            acc &= (1 << bits) - 1;
        }
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn token(claims: serde_json::Value) -> String {
        const ALPHABET: &[u8; 64] =
            b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
        let data = claims.to_string().into_bytes();
        let mut enc = String::new();
        for chunk in data.chunks(3) {
            let b = [
                chunk[0],
                *chunk.get(1).unwrap_or(&0),
                *chunk.get(2).unwrap_or(&0),
            ];
            let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
            for i in 0..chunk.len() + 1 {
                enc.push(ALPHABET[((n >> (18 - 6 * i)) & 63) as usize] as char);
            }
        }
        format!("{enc}.signature")
    }

    #[test]
    fn the_session_token_names_the_member() {
        for sub in ["maya", "jack@example.com", "a", "ab", "abc"] {
            let t = token(serde_json::json!({"sub": sub, "tenant": "default", "exp": 1}));
            assert_eq!(token_subject(&t).as_deref(), Some(sub));
        }
        assert_eq!(token_subject(""), None);
        assert_eq!(token_subject("opaque-api-token"), None);
        assert_eq!(token_subject("not*base64.sig"), None);
        assert_eq!(
            token_subject(&token(serde_json::json!({"tenant": "default"}))),
            None
        );
    }

    #[test]
    fn each_member_of_each_gateway_has_its_own_directory() {
        let config = Path::new("/config");
        let maya = member_dir(config, "https://gw.example.com", "maya").expect("dir");
        assert!(maya.starts_with("/config/members"));
        // One gateway origin, however it is written.
        assert_eq!(
            member_dir(config, " https://GW.example.com/app/ ", " maya "),
            Some(maya.clone())
        );
        // Another member, another gateway: another directory.
        assert_ne!(
            member_dir(config, "https://gw.example.com", "jack"),
            Some(maya.clone())
        );
        assert_ne!(
            member_dir(config, "https://other.example.com", "maya"),
            Some(maya.clone())
        );
        // Subjects are case-sensitive.
        assert_ne!(
            member_dir(config, "https://gw.example.com", "Maya"),
            Some(maya)
        );
        assert_eq!(member_dir(config, "", "maya"), None);
        assert_eq!(member_dir(config, "file:///x", "maya"), None);
        assert_eq!(member_dir(config, "https://gw.example.com", " "), None);
    }

    #[test]
    fn unkeyed_secret_dirs_are_deleted() {
        let config =
            std::env::temp_dir().join(format!("bnbloc-member-dir-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&config);
        for dir in [PLUGIN_SETTINGS_DIR, MCP_SETTINGS_DIR] {
            std::fs::create_dir_all(config.join(dir).join("oauth")).unwrap();
            std::fs::write(config.join(dir).join("x.json"), b"{}").unwrap();
        }
        let member = member_dir(&config, "https://gw.example.com", "maya").unwrap();
        std::fs::create_dir_all(member.join(PLUGIN_SETTINGS_DIR)).unwrap();

        remove_unkeyed_dirs(&config);
        remove_unkeyed_dirs(&config);

        assert!(!config.join(PLUGIN_SETTINGS_DIR).exists());
        assert!(!config.join(MCP_SETTINGS_DIR).exists());
        assert!(member.join(PLUGIN_SETTINGS_DIR).exists(), "keyed dirs stay");
        std::fs::remove_dir_all(&config).unwrap();
    }
}
