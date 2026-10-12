//! The signed-in member's private directory on this computer.
//!
//! What a member keeps on a desktop (plugin `localSettings`, MCP tool member
//! credentials, MCP OAuth sign-ins, the sandbox's data dir with its VM
//! disks) belongs to one member acting in one tenant of one gateway, and the
//! catalog that installs those components is scoped the same way: a
//! super-admin's tenant switch hands the desktop a whole other catalog.
//! Several members, or one member in several tenants, may use the same
//! desktop one after the other. Their state lives under
//! `<app config>/members/<key>/`, `<key>` a hash of the gateway origin, the
//! member's subject and the tenant, so member B never launches a plugin with
//! A's bot credentials or drives with A's MCP tokens, and the uninstall sweep
//! (sidecar_sweep) only ever judges a scope's state against that same
//! scope's catalog: switching tenants and back loses nothing.
//!
//! `member_dir` is the one rule for that directory. The sidecar manager
//! takes subject and tenant from the session token it runs with; the bridge
//! gets the manager's directory only when it runs for that same member.
//!
//! The locations that predate this layout (`<app config>/plugin-settings`,
//! `<app config>/mcp-settings`, `<app data>/sandbox`) held the state of
//! whichever member signed in; they are deleted at startup, not migrated.
use std::path::{Path, PathBuf};

use reqwest::Url;
use sha2::{Digest, Sha256};

use crate::internal_ext::sidecar_util::hex_lower;

/// `<app config>/members`: one directory per (gateway, member, tenant).
const MEMBERS_DIR: &str = "members";
/// A member's plugin `localSettings` (`<member>/plugin-settings/<plugin>.json`).
pub(super) const PLUGIN_SETTINGS_DIR: &str = "plugin-settings";
/// A member's MCP tool credentials and sign-ins (`<member>/mcp-settings/`).
pub(super) const MCP_SETTINGS_DIR: &str = "mcp-settings";
/// A member's sandbox data dirs (`<member>/sandbox/<name>`: VM disks).
pub(super) const SANDBOX_DATA_DIR: &str = "sandbox";

/// The bridge-stabilizing watcher's key for the bridge's member directory
/// (a slash never appears in a core name, so it cannot collide with one).
pub(super) const BRIDGE_KEY: &str = "member/dir";

/// Who a session acts as: the subject and the tenant its token names.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct SessionMember {
    pub(super) subject: String,
    pub(super) tenant: String,
}

/// The private directory of `subject` acting in `tenant` on the gateway at
/// `gateway_url`, under the app config dir. None without a gateway origin,
/// a subject or a tenant.
pub(crate) fn member_dir(
    config_dir: &Path,
    gateway_url: &str,
    subject: &str,
    tenant: &str,
) -> Option<PathBuf> {
    let url = Url::parse(gateway_url.trim()).ok()?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return None;
    }
    let (subject, tenant) = (subject.trim(), tenant.trim());
    if subject.is_empty() || tenant.is_empty() {
        return None;
    }
    let mut hasher = Sha256::new();
    for part in [url.origin().ascii_serialization().as_str(), subject, tenant] {
        hasher.update(part.as_bytes());
        hasher.update(b"\n");
    }
    let key = hex_lower(&hasher.finalize());
    Some(config_dir.join(MEMBERS_DIR).join(&key[..32]))
}

/// The subject (`sub`) and tenant (`tenant`) a gateway session token names
/// (auth.Claims). The token is `<base64url JSON claims>.<signature>`; it is
/// not verified here (the gateway does that on every call): the claims only
/// pick which member directory this desktop uses for the catalog fetched
/// with that token. None when either claim is missing.
pub(super) fn token_member(token: &str) -> Option<SessionMember> {
    let (payload, _) = token.trim().split_once('.')?;
    let claims: serde_json::Value = serde_json::from_slice(&b64url_decode(payload)?).ok()?;
    let claim = |name: &str| {
        claims[name]
            .as_str()
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .map(str::to_string)
    };
    Some(SessionMember {
        subject: claim("sub")?,
        tenant: claim("tenant")?,
    })
}

/// Delete the member-unkeyed state of earlier builds: the secret dirs under
/// the app config dir and the sandbox data base under the app data dir.
/// Blocking, best-effort.
pub(super) fn remove_unkeyed_dirs(config_dir: &Path, data_dir: &Path) {
    for dir in [
        config_dir.join(PLUGIN_SETTINGS_DIR),
        config_dir.join(MCP_SETTINGS_DIR),
        data_dir.join(SANDBOX_DATA_DIR),
    ] {
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
    fn the_session_token_names_the_member_and_tenant() {
        for sub in ["maya", "jack@example.com", "a", "ab", "abc"] {
            let t = token(serde_json::json!({"sub": sub, "tenant": "acme-org", "exp": 1}));
            assert_eq!(
                token_member(&t),
                Some(SessionMember {
                    subject: sub.into(),
                    tenant: "acme-org".into()
                })
            );
        }
        assert_eq!(token_member(""), None);
        assert_eq!(token_member("opaque-api-token"), None);
        assert_eq!(token_member("not*base64.sig"), None);
        // Both claims are required: no tenant, no member dir.
        assert_eq!(
            token_member(&token(serde_json::json!({"tenant": "acme-org"}))),
            None
        );
        assert_eq!(
            token_member(&token(serde_json::json!({"sub": "maya"}))),
            None
        );
        assert_eq!(
            token_member(&token(serde_json::json!({"sub": "maya", "tenant": " "}))),
            None
        );
    }

    #[test]
    fn each_member_and_tenant_of_each_gateway_has_its_own_directory() {
        let config = Path::new("/config");
        let gw = "https://gw.example.com";
        let maya = member_dir(config, gw, "maya", "t1").expect("dir");
        assert!(maya.starts_with("/config/members"));
        // One gateway origin, however it is written.
        assert_eq!(
            member_dir(config, " https://GW.example.com/app/ ", " maya ", " t1 "),
            Some(maya.clone())
        );
        // Another member, tenant or gateway: another directory.
        for other in [
            member_dir(config, gw, "jack", "t1"),
            member_dir(config, gw, "maya", "t2"),
            member_dir(config, "https://other.example.com", "maya", "t1"),
            // Subjects are case-sensitive.
            member_dir(config, gw, "Maya", "t1"),
        ] {
            assert!(other.is_some());
            assert_ne!(other, Some(maya.clone()));
        }
        assert_eq!(member_dir(config, "", "maya", "t1"), None);
        assert_eq!(member_dir(config, "file:///x", "maya", "t1"), None);
        assert_eq!(member_dir(config, gw, " ", "t1"), None);
        assert_eq!(member_dir(config, gw, "maya", ""), None);
    }

    #[test]
    fn unkeyed_state_dirs_are_deleted() {
        // One dir for config and data, as on macOS: the keyed members/ tree
        // under it stays.
        let base =
            std::env::temp_dir().join(format!("bnbloc-member-dir-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&base);
        for dir in [PLUGIN_SETTINGS_DIR, MCP_SETTINGS_DIR, SANDBOX_DATA_DIR] {
            std::fs::create_dir_all(base.join(dir).join("sub")).unwrap();
            std::fs::write(base.join(dir).join("x.json"), b"{}").unwrap();
        }
        let member = member_dir(&base, "https://gw.example.com", "maya", "t1").unwrap();
        for dir in [PLUGIN_SETTINGS_DIR, SANDBOX_DATA_DIR] {
            std::fs::create_dir_all(member.join(dir)).unwrap();
        }

        remove_unkeyed_dirs(&base, &base);
        remove_unkeyed_dirs(&base, &base);

        for dir in [PLUGIN_SETTINGS_DIR, MCP_SETTINGS_DIR, SANDBOX_DATA_DIR] {
            assert!(!base.join(dir).exists(), "{dir} survived");
        }
        assert!(member.join(PLUGIN_SETTINGS_DIR).exists(), "keyed dirs stay");
        assert!(member.join(SANDBOX_DATA_DIR).exists(), "keyed dirs stay");
        std::fs::remove_dir_all(&base).unwrap();
    }
}
