use std::collections::HashSet;
use std::path::Path;
use std::time::Duration;

use crate::internal_ext::loopback::{self, LoopbackHttp};

pub(super) fn download_message(name: &str, written: u64, total: Option<u64>) -> String {
    match total {
        Some(total) if total > 0 => format!(
            "Downloading {name} {} / {}",
            human_bytes(written),
            human_bytes(total)
        ),
        _ => format!("Downloading {name} {}", human_bytes(written)),
    }
}

fn human_bytes(n: u64) -> String {
    if n >= 1024 * 1024 {
        format!("{:.1} MB", n as f64 / (1024.0 * 1024.0))
    } else if n >= 1024 {
        format!("{:.1} KB", n as f64 / 1024.0)
    } else {
        format!("{n} B")
    }
}

/// The cache file name for component `id` at artifact `sha256`:
/// `<cache stem>-<sha256>[.exe]`.
pub(super) fn cache_file_name(id: &str, sha256: &str) -> String {
    format!("{}-{sha256}{}", cache_stem(id), exe_suffix())
}

/// The file-name stem of component `id` (binary cache, spawn records,
/// sandbox dirs): the id with its kind separator `/` written as `@`. A
/// package name never contains `@` (sidecar_model::valid_package_name), so
/// two components never share a stem: core `plugin-foo` is `plugin-foo`,
/// plugin `foo` is `plugin@foo`.
pub(super) fn cache_stem(id: &str) -> String {
    id.replace('/', "@")
}

/// The owner stem and pinned sha256 of a cache file name
/// (`<stem>-<64 hex>[.exe]`) or of its partial download
/// (`files::owner_name`), or None for any other name. Component `foo` never
/// claims `foo-bar`'s files: the stem is everything before the final `-<sha>`.
pub(super) fn parse_cache_file(name: &str) -> Option<(&str, String)> {
    let name = crate::files::owner_name(name);
    let base = name.strip_suffix(exe_suffix()).unwrap_or(name);
    let split = base.len().checked_sub(65)?;
    if !base.is_char_boundary(split) {
        return None;
    }
    let (owner, tail) = base.split_at(split);
    let sha = tail.strip_prefix('-')?;
    (!owner.is_empty() && sha.chars().all(|c| c.is_ascii_hexdigit()))
        .then(|| (owner, sha.to_ascii_lowercase()))
}

/// Whether the cache entry `name` is a build some live catalog pins
/// (`live` holds `(stem, sha256)` pairs); None for a name that is not a cache
/// name, which is not ours to judge. A partial download is judged by the
/// build it is for.
pub(super) fn cache_file_is_live(name: &str, live: &HashSet<(String, String)>) -> Option<bool> {
    let (stem, sha) = parse_cache_file(name)?;
    Some(live.contains(&(stem.to_string(), sha)))
}

/// Resolve a catalog artifact URL against the configured gateway and require
/// the result to be on the gateway's own origin (scheme, host, port). The
/// member's session bearer is attached to the download, so it must never be
/// sent to a URL a package (or a tampered catalog entry) points elsewhere.
pub(super) fn artifact_download_url(
    gateway_base: &str,
    artifact_url: &str,
) -> Result<String, String> {
    let base = tauri::Url::parse(gateway_base.trim())
        .map_err(|e| format!("gateway url {gateway_base:?} is invalid: {e}"))?;
    let resolved = base
        .join(artifact_url.trim())
        .map_err(|e| format!("artifact url {artifact_url:?} is invalid: {e}"))?;
    if resolved.origin() != base.origin() {
        return Err(format!(
            "artifact url {resolved} is not on the gateway origin {}",
            base.origin().ascii_serialization()
        ));
    }
    Ok(resolved.into())
}

/// The download request of a gateway artifact URL: `/v1/artifacts/{key}`
/// gains `direct=1`, which lets the gateway answer 302 to a short-lived
/// presigned blob-store URL instead of streaming the bytes itself (it streams
/// when direct downloads are off). The redirect hop never carries the
/// session: reqwest drops `Authorization` on a cross-host redirect, and the
/// sha256 pin is checked on whatever bytes arrive.
pub(super) fn direct_download_url(resolved: &str) -> String {
    let Ok(mut url) = tauri::Url::parse(resolved) else {
        return resolved.to_string();
    };
    if !url.path().starts_with("/v1/artifacts/") || url.query_pairs().any(|(k, _)| k == "direct") {
        return resolved.to_string();
    }
    url.query_pairs_mut().append_pair("direct", "1");
    url.into()
}

pub(crate) fn hex_lower(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{b:02x}"));
    }
    s
}

/// This build's catalog platform (`<goos>-<goarch>`, the artifact key), from
/// the one Rust → Go table the sidecar build script reads too
/// (`apps/desktop/go-platforms.json`). A value the table lacks maps to
/// `unsupported`, which names no artifact: the same refusal as the build
/// script's.
pub(super) fn current_platform() -> String {
    platform_for(std::env::consts::OS, std::env::consts::ARCH)
}

fn platform_for(os: &str, arch: &str) -> String {
    #[derive(serde::Deserialize)]
    struct Table {
        goos: std::collections::HashMap<String, String>,
        goarch: std::collections::HashMap<String, String>,
    }
    static TABLE: std::sync::OnceLock<Table> = std::sync::OnceLock::new();
    let table = TABLE.get_or_init(|| {
        serde_json::from_str(include_str!("../../../go-platforms.json"))
            .expect("go-platforms.json is valid")
    });
    let go = |map: &std::collections::HashMap<String, String>, key: &str| {
        map.get(key)
            .cloned()
            .unwrap_or_else(|| "unsupported".to_string())
    };
    format!("{}-{}", go(&table.goos, os), go(&table.goarch, arch))
}

pub(super) fn exe_suffix() -> &'static str {
    if cfg!(windows) {
        ".exe"
    } else {
        ""
    }
}

#[cfg(unix)]
pub(super) fn set_executable(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    let mut perms = std::fs::metadata(path)
        .map_err(|e| format!("stat temp: {e}"))?
        .permissions();
    perms.set_mode(0o755);
    std::fs::set_permissions(path, perms).map_err(|e| format!("chmod temp: {e}"))
}

#[cfg(not(unix))]
pub(super) fn set_executable(_path: &Path) -> Result<(), String> {
    Ok(())
}

pub(super) struct RuntimeListener {
    pub(super) url: String,
    pub(super) listener: Option<std::net::TcpListener>,
}

/// Reserve a fresh loopback port for a sidecar that is told where to listen
/// (its `listenEnv`): its callers learn the address from the desktop.
pub(super) fn ephemeral_listener(what: &str) -> Result<RuntimeListener, String> {
    use crate::internal_ext::loopback;
    let listener = loopback::bind_ephemeral().map_err(|e| format!("reserve {what} port: {e}"))?;
    let addr = listener
        .local_addr()
        .map_err(|e| format!("reserve {what} port: {e}"))?;
    Ok(RuntimeListener {
        url: loopback::LoopbackHttp::of_addr(addr),
        listener: Some(listener),
    })
}

/// Reserve a sidecar's fixed `localUrl` (loopback::LoopbackHttp) until the
/// manager launches it.
pub(super) fn runtime_listener_for(local_url: &str) -> Result<RuntimeListener, String> {
    let trimmed = local_url.trim();
    let url = LoopbackHttp::parse(trimmed).map_err(|e| format!("sidecar localUrl {e}"))?;
    let listener = std::net::TcpListener::bind(url.authority()).map_err(|error| {
        if error.kind() == std::io::ErrorKind::AddrInUse {
            format!("sidecar listener {trimmed} is already in use; stop the stale process before retrying")
        } else {
            format!("check sidecar listener {trimmed}: {error}")
        }
    })?;
    Ok(RuntimeListener {
        url: trimmed.to_string(),
        listener: Some(listener),
    })
}
/// The Core Protocol version the bridge speaks — a constant of the protocol,
/// not of any deployment.
const CORE_PROTOCOL_VERSION: (u32, u32) = (1, 0);

/// A listener that identified itself as the named Core Protocol sidecar.
#[derive(Debug, PartialEq, Eq)]
pub(super) struct CoreListener {
    /// The daemon's own version from discover (its version space, not the
    /// package's); for display only.
    pub(super) version: String,
}

/// Probes the listener already bound at `local_url`: it is the named Core
/// Protocol sidecar when `/core/v1/discover` returns the same `name` and a
/// protocol range containing the bridge's version. Anything else (another
/// service, another core, an incompatible version) yields None.
pub(super) async fn probe_core_protocol_listener(
    local_url: &str,
    name: &str,
) -> Result<Option<CoreListener>, String> {
    let endpoint = format!("{}/core/v1/discover", local_url.trim_end_matches('/'));
    let response = loopback::client()?
        .get(&endpoint)
        .timeout(Duration::from_millis(500))
        .send()
        .await;
    match response {
        Ok(response) if response.status().is_success() => {
            let bytes = response
                .bytes()
                .await
                .map_err(|e| format!("read sidecar discovery: {e}"))?;
            let discovery: serde_json::Value = serde_json::from_slice(&bytes)
                .map_err(|e| format!("decode sidecar discovery: {e}"))?;
            let text = |pointer: &str| {
                discovery
                    .pointer(pointer)
                    .and_then(serde_json::Value::as_str)
            };
            let matches = text("/name") == Some(name)
                && protocol_range_contains(
                    text("/protocol/min"),
                    text("/protocol/max"),
                    CORE_PROTOCOL_VERSION,
                );
            Ok(matches.then(|| CoreListener {
                version: text("/version").unwrap_or_default().trim().to_string(),
            }))
        }
        Ok(_) => Ok(None),
        Err(error) if error.is_connect() => Ok(None),
        Err(error) => Err(format!("probe sidecar listener: {error}")),
    }
}

pub(super) fn protocol_range_contains(
    min: Option<&str>,
    max: Option<&str>,
    version: (u32, u32),
) -> bool {
    let parse = |raw: Option<&str>| {
        let (major, minor) = raw?.trim().split_once('.')?;
        Some((major.parse::<u32>().ok()?, minor.parse::<u32>().ok()?))
    };
    matches!((parse(min), parse(max)), (Some(min), Some(max)) if min <= version && version <= max)
}

pub(super) fn home_env() -> Option<(&'static str, std::ffi::OsString)> {
    #[cfg(windows)]
    let key = "USERPROFILE";
    #[cfg(not(windows))]
    let key = "HOME";
    std::env::var_os(key)
        .filter(|v| !v.is_empty())
        .map(|home| (key, home))
}

/// A core's env override `BNBLOC_CORE_<env_key(name)>_<field>` (`URL`,
/// `TOKEN`): the only place the desktop names one.
pub(crate) fn core_env_var(name: &str, field: &str) -> String {
    format!("BNBLOC_CORE_{}_{field}", env_key(name))
}

/// The one rule naming a core's env overrides (`BNBLOC_CORE_<key>_URL` /
/// `_TOKEN`), the same as Go's `coreset.EnvKey`: upper-cased, every character
/// outside `[A-Za-z0-9]` replaced by `_`. Pinned on both sides by
/// `internal/coreset/testdata/core_env_keys.json`.
pub(crate) fn env_key(name: &str) -> String {
    name.chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() {
                c.to_ascii_uppercase()
            } else {
                '_'
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    const SHA_A: &str = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    const SHA_B: &str = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";

    #[test]
    fn a_cache_entry_is_live_when_a_catalog_pins_that_build() {
        let live: HashSet<(String, String)> = [
            ("foo".to_string(), SHA_A.to_string()),
            ("foo".to_string(), SHA_B.to_string()),
            ("plugin@foo".to_string(), SHA_A.to_string()),
        ]
        .into();
        // Two accounts pin two builds of foo: both stay.
        assert_eq!(
            cache_file_is_live(&cache_file_name("foo", SHA_A), &live),
            Some(true)
        );
        assert_eq!(
            cache_file_is_live(&cache_file_name("foo", SHA_B), &live),
            Some(true)
        );
        assert_eq!(
            cache_file_is_live(&cache_file_name("plugin/foo", SHA_A), &live),
            Some(true)
        );
        // A build nobody pins any more, and its partial download, go.
        let old = "c".repeat(64);
        assert_eq!(
            cache_file_is_live(&cache_file_name("foo", &old), &live),
            Some(false)
        );
        assert_eq!(
            cache_file_is_live(
                &format!(".{}.0123456789abcdef.part", cache_file_name("foo", &old)),
                &live
            ),
            Some(false)
        );
        assert_eq!(
            cache_file_is_live(
                &format!(".{}.0123456789abcdef.part", cache_file_name("foo", SHA_A)),
                &live
            ),
            Some(true),
            "the partial of a live build stays"
        );
        // Another component with a similar name is judged on its own stem.
        assert_eq!(
            cache_file_is_live(&cache_file_name("foo-bar", SHA_A), &live),
            Some(false)
        );
        // Not cache names: never judged.
        for name in [format!("foo-{}", &SHA_A[..10]), "foo-notes.txt".to_string()] {
            assert_eq!(cache_file_is_live(&name, &live), None, "{name}");
        }
    }

    #[test]
    fn artifact_urls_must_stay_on_the_gateway_origin() {
        let gw = "https://gw.example.com";
        assert_eq!(
            artifact_download_url(gw, "https://gw.example.com/v1/artifacts/sha256/abc").unwrap(),
            "https://gw.example.com/v1/artifacts/sha256/abc"
        );
        // Relative catalog URLs resolve against the gateway.
        assert_eq!(
            artifact_download_url(gw, "/v1/artifacts/sha256/abc").unwrap(),
            "https://gw.example.com/v1/artifacts/sha256/abc"
        );
        for foreign in [
            "https://evil.example.com/v1/artifacts/x",
            "http://gw.example.com/v1/artifacts/x", // scheme downgrade
            "https://gw.example.com:8443/v1/artifacts/x", // other port
            "https://gw.example.com.evil.net/x",
            "//evil.example.com/x",
        ] {
            assert!(
                artifact_download_url(gw, foreign).is_err(),
                "{foreign} must be refused"
            );
        }
        assert!(artifact_download_url("", "/v1/artifacts/x").is_err());
    }

    #[test]
    fn artifact_downloads_ask_for_a_direct_redirect() {
        assert_eq!(
            direct_download_url("https://gw.example.com/v1/artifacts/sha256/abc"),
            "https://gw.example.com/v1/artifacts/sha256/abc?direct=1"
        );
        assert_eq!(
            direct_download_url("https://gw.example.com/v1/artifacts/k?x=1"),
            "https://gw.example.com/v1/artifacts/k?x=1&direct=1"
        );
        // Already asked, or not an artifact route: unchanged.
        assert_eq!(
            direct_download_url("https://gw.example.com/v1/artifacts/k?direct=1"),
            "https://gw.example.com/v1/artifacts/k?direct=1"
        );
        assert_eq!(
            direct_download_url("https://gw.example.com/other/k"),
            "https://gw.example.com/other/k"
        );
    }

    /// No two components share a cache stem, whatever their kinds and
    /// names: one never deletes the other's binary, spawn record or dirs.
    #[test]
    fn cache_stem_is_injective() {
        use crate::internal_ext::sidecar_model::ComponentKind;
        use crate::internal_ext::sidecar_status::component_id;
        let kinds = [
            ComponentKind::Core,
            ComponentKind::Plugin,
            ComponentKind::Sandbox,
            ComponentKind::Previewer,
            ComponentKind::Mcp,
        ];
        let names = [
            "foo",
            "plugin-foo",
            "sandbox-foo",
            "mcp-foo",
            "previewer-foo",
            "plugin.foo",
        ];
        let mut seen = std::collections::HashMap::new();
        for kind in kinds {
            for name in names {
                let stem = cache_stem(&component_id(kind, name));
                if let Some(other) = seen.insert(stem.clone(), (kind, name)) {
                    panic!("{stem} names both {other:?} and {:?}", (kind, name));
                }
            }
        }
    }

    #[test]
    fn platforms_come_from_the_shared_table() {
        assert_eq!(platform_for("macos", "aarch64"), "darwin-arm64");
        assert_eq!(platform_for("linux", "x86_64"), "linux-amd64");
        assert_eq!(platform_for("windows", "x86_64"), "windows-amd64");
        assert_eq!(platform_for("windows", "x86"), "windows-386");
        // Unknown on either side: no artifact can match.
        assert_eq!(platform_for("linux", "riscv64"), "linux-unsupported");
        assert!(
            !current_platform().contains("unsupported"),
            "this build is mapped"
        );
    }

    #[test]
    fn occupied_fixed_port_without_listener_override_fails_without_reassignment() {
        let listener = loopback::bind_ephemeral().expect("reserve test port");
        let requested = format!("http://{}", listener.local_addr().expect("test address"));

        let err = match runtime_listener_for(&requested) {
            Ok(_) => panic!("occupied fixed port must not be assigned to a sidecar"),
            Err(err) => err,
        };

        assert!(err.contains("already in use") && err.contains(&requested));
    }

    fn serve_once(body: &'static str) -> String {
        let server = loopback::bind_ephemeral().expect("bind test listener");
        let address = server.local_addr().expect("listener address");
        std::thread::spawn(move || {
            let (mut stream, _) = server.accept().expect("accept discovery request");
            let mut request = [0; 1024];
            let _ = std::io::Read::read(&mut stream, &mut request);
            use std::io::Write;
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            )
            .expect("write discovery response");
        });
        format!("http://{address}")
    }

    #[tokio::test]
    async fn occupied_port_is_reused_only_for_the_same_compatible_core() {
        let same = serve_once(
            r#"{"name":"samplecore","version":"1.2.3","protocol":{"min":"1.0","max":"1.2"}}"#,
        );
        assert_eq!(
            probe_core_protocol_listener(&same, "samplecore")
                .await
                .expect("probe same core"),
            Some(CoreListener {
                version: "1.2.3".into()
            })
        );

        let other_core = serve_once(r#"{"name":"othercore","protocol":{"min":"1.0","max":"1.0"}}"#);
        assert!(probe_core_protocol_listener(&other_core, "samplecore")
            .await
            .expect("probe other core")
            .is_none());

        let incompatible =
            serve_once(r#"{"name":"samplecore","protocol":{"min":"2.0","max":"2.1"}}"#);
        assert!(probe_core_protocol_listener(&incompatible, "samplecore")
            .await
            .expect("probe incompatible core")
            .is_none());

        let unrelated = serve_once(r#"{"name":"other"}"#);
        assert!(probe_core_protocol_listener(&unrelated, "samplecore")
            .await
            .expect("probe unrelated service")
            .is_none());
    }

    #[test]
    fn parse_cache_file_reads_the_owner_and_pin() {
        assert_eq!(
            parse_cache_file(&cache_file_name("foo", SHA_A)),
            Some(("foo", SHA_A.to_string()))
        );
        assert_eq!(
            parse_cache_file(&cache_file_name("foo-bar", SHA_A)),
            Some(("foo-bar", SHA_A.to_string()))
        );
        assert_eq!(
            parse_cache_file(&cache_file_name("plugin/foo", SHA_B)),
            Some(("plugin@foo", SHA_B.to_string()))
        );
        let part = format!(".{}.0123456789abcdef.part", cache_file_name("foo", SHA_A));
        assert_eq!(parse_cache_file(&part), Some(("foo", SHA_A.to_string())));
        assert_eq!(parse_cache_file("foo-notes.txt"), None);
        assert_eq!(parse_cache_file(&format!("-{SHA_A}")), None);
    }

    #[test]
    fn selected_listener_stays_reserved_until_manager_launches_child() {
        let probe = loopback::bind_ephemeral().expect("select test port");
        let address = probe.local_addr().expect("selected address");
        drop(probe);
        let runtime =
            runtime_listener_for(&format!("http://{address}")).expect("reserve selected port");

        assert!(
            std::net::TcpListener::bind(address).is_err(),
            "manager must keep the selected port reserved before spawn"
        );
        drop(runtime);
        assert!(
            std::net::TcpListener::bind(address).is_ok(),
            "port becomes available after manager releases reservation"
        );
    }
}
