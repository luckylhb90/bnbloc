use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::Duration;

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

/// Whether the cached binary at `path` exists and hashes to `sha256`. The
/// file is hashed with streaming reads on a blocking thread, never on the
/// async runtime.
pub(super) async fn binary_ready(path: &Path, sha256: &str) -> bool {
    let path = path.to_path_buf();
    let want = sha256.to_string();
    tauri::async_runtime::spawn_blocking(move || file_sha256(&path).is_ok_and(|got| got == want))
        .await
        .unwrap_or(false)
}

fn file_sha256(path: &Path) -> std::io::Result<String> {
    use sha2::{Digest, Sha256};
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

/// The cache file name for component `id` at artifact `sha256`:
/// `<id with / as ->-<sha256>[.exe]`.
pub(super) fn cache_file_name(id: &str, sha256: &str) -> String {
    format!("{}-{sha256}{}", cache_stem(id), exe_suffix())
}

pub(super) fn cache_stem(id: &str) -> String {
    id.replace('/', "-")
}

/// The pinned sha256 encoded in a cache file name of component `id`
/// (`<stem>-<64 hex>[.exe]`), or None when `name` is not one of its cached
/// binaries. Component `foo` never claims `foo-bar`'s files.
pub(super) fn cache_file_sha(name: &str, id: &str) -> Option<String> {
    let base = name.strip_suffix(exe_suffix()).unwrap_or(name);
    let split = base.len().checked_sub(65)?;
    if !base.is_char_boundary(split) {
        return None;
    }
    let (owner, tail) = base.split_at(split);
    let sha = tail.strip_prefix('-')?;
    (owner == cache_stem(id) && sha.chars().all(|c| c.is_ascii_hexdigit()))
        .then(|| sha.to_ascii_lowercase())
}

/// The cached binaries and partial downloads in `names` that belong to
/// component `id` and are not `keep`: its cache files (see `cache_file_sha`)
/// and their `.<…>.part` temps.
pub(super) fn stale_cache_files(names: &[String], id: &str, keep: Option<&str>) -> Vec<String> {
    names
        .iter()
        .filter(|name| Some(name.as_str()) != keep)
        .filter(|name| {
            let base = name
                .strip_prefix('.')
                .and_then(|n| n.strip_suffix(".part"))
                .unwrap_or(name);
            cache_file_sha(base, id).is_some()
        })
        .cloned()
        .collect()
}

/// Resolve a catalog artifact URL against the configured gateway and require
/// the result to be on the gateway's own origin (scheme, host, port). The
/// member's session bearer is attached to the download, so it must never be
/// sent to a URL a package (or a tampered catalog entry) points elsewhere.
pub(super) fn artifact_download_url(gateway_base: &str, artifact_url: &str) -> Result<String, String> {
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

pub(super) fn hex_lower(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{b:02x}"));
    }
    s
}

pub(super) fn current_platform() -> String {
    let goos = match std::env::consts::OS {
        "macos" => "darwin",
        other => other,
    };
    let goarch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    };
    format!("{goos}-{goarch}")
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

pub(super) fn listen_addr(local_url: &str) -> Result<String, String> {
    let trimmed = local_url.trim();
    let without_scheme = trimmed
        .split_once("://")
        .map(|(_, rest)| rest)
        .unwrap_or(trimmed);
    let authority = without_scheme
        .split('/')
        .next()
        .unwrap_or(without_scheme)
        .trim();
    if authority.is_empty() {
        return Err(format!("sidecar localUrl has no host:port: {local_url}"));
    }
    Ok(authority.to_string())
}

pub(super) struct RuntimeListener {
    pub(super) url: String,
    pub(super) listener: Option<std::net::TcpListener>,
}
pub(super) fn runtime_listener_for(local_url: &str) -> Result<RuntimeListener, String> {
    let trimmed = local_url.trim();
    let (scheme, rest) = trimmed
        .split_once("://")
        .ok_or_else(|| format!("sidecar localUrl must include a scheme: {local_url}"))?;
    if scheme != "http" {
        return Err(format!("sidecar localUrl must use http: {local_url}"));
    }
    let (authority, _) = rest.split_once('/').unwrap_or((rest, ""));
    let host = authority
        .rsplit_once(':')
        .map(|(host, _)| host)
        .ok_or_else(|| format!("sidecar localUrl has no port: {local_url}"))?;
    if host != "127.0.0.1" && host != "localhost" {
        return Err(format!("sidecar localUrl must be loopback: {local_url}"));
    }
    let listener = std::net::TcpListener::bind(authority).map_err(|error| {
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
    let client = reqwest::Client::builder()
        .connect_timeout(Duration::from_millis(250))
        .timeout(Duration::from_millis(500))
        .build()
        .map_err(|e| format!("build sidecar listener probe: {e}"))?;
    let response = client.get(&endpoint).send().await;
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

pub(super) fn protocol_range_contains(min: Option<&str>, max: Option<&str>, version: (u32, u32)) -> bool {
    let parse = |raw: Option<&str>| {
        let (major, minor) = raw?.trim().split_once('.')?;
        Some((major.parse::<u32>().ok()?, minor.parse::<u32>().ok()?))
    };
    matches!((parse(min), parse(max)), (Some(min), Some(max)) if min <= version && version <= max)
}

pub(super) fn sidecar_path_env(current_path: Option<&str>, home: Option<PathBuf>) -> String {
    let mut parts = Vec::new();
    if let Some(path) = current_path {
        parts.extend(std::env::split_paths(path));
    }
    if let Some(home) = home {
        for dir in [
            ".local/bin",
            ".opencode/bin",
            ".local/share/mise/shims",
            ".cargo/bin",
        ] {
            parts.push(home.join(dir));
        }
    }
    #[cfg(unix)]
    parts.extend([
        PathBuf::from("/opt/homebrew/bin"),
        PathBuf::from("/usr/local/bin"),
    ]);
    let mut out = Vec::new();
    for part in parts {
        if !out.iter().any(|seen| seen == &part) {
            out.push(part);
        }
    }
    std::env::join_paths(out)
        .unwrap_or_default()
        .to_string_lossy()
        .into_owned()
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

pub(super) fn env_key(name: &str) -> String {
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
    fn stale_cache_selection_keeps_current_and_other_components() {
        let keep = cache_file_name("foo", SHA_B);
        let names: Vec<String> = vec![
            cache_file_name("foo", SHA_A),             // old foo build: stale
            keep.clone(),                              // current foo build: kept
            format!(".{}.part", cache_file_name("foo", SHA_A)), // stale partial
            cache_file_name("foo-bar", SHA_A),         // another component
            cache_file_name("plugin/foo", SHA_A),      // the plugin namesake
            format!("foo-{}", &SHA_A[..10]),           // not a cache name
            "foo-notes.txt".into(),
        ];

        let mut stale = stale_cache_files(&names, "foo", Some(&keep));
        stale.sort();
        let mut want = vec![
            cache_file_name("foo", SHA_A),
            format!(".{}.part", cache_file_name("foo", SHA_A)),
        ];
        want.sort();
        assert_eq!(stale, want);

        // Uninstall: every cached build of the plugin goes, nothing else.
        assert_eq!(
            stale_cache_files(&names, "plugin/foo", None),
            vec![cache_file_name("plugin/foo", SHA_A)]
        );
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
            "http://gw.example.com/v1/artifacts/x",       // scheme downgrade
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

    #[tokio::test]
    async fn binary_ready_hashes_the_file_contents() {
        let path = std::env::temp_dir().join(format!("bnbloc-hash-test-{}", std::process::id()));
        std::fs::write(&path, b"abc").expect("write");
        let abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";
        assert!(binary_ready(&path, abc).await);
        assert!(!binary_ready(&path, SHA_A).await);
        std::fs::remove_file(&path).expect("cleanup");
        assert!(!binary_ready(&path, abc).await);
    }

    #[test]
    fn sidecar_path_env_includes_user_cli_dirs_when_gui_path_is_sparse() {
        let path = sidecar_path_env(Some("/usr/bin:/bin"), Some(PathBuf::from("/Users/example")));

        assert!(path.contains("/Users/example/.local/bin"), "got {path}");
        assert!(path.contains("/opt/homebrew/bin"), "got {path}");
        assert!(path.starts_with("/usr/bin:/bin"), "got {path}");
    }

    #[test]
    fn occupied_fixed_port_without_listener_override_fails_without_reassignment() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("reserve test port");
        let requested = format!("http://{}", listener.local_addr().expect("test address"));

        let err = match runtime_listener_for(&requested) {
            Ok(_) => panic!("occupied fixed port must not be assigned to a sidecar"),
            Err(err) => err,
        };

        assert!(err.contains("already in use") && err.contains(&requested));
    }

    fn serve_once(body: &'static str) -> String {
        let server = std::net::TcpListener::bind("127.0.0.1:0").expect("bind test listener");
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
    fn cache_file_sha_reads_only_this_components_names() {
        assert_eq!(
            cache_file_sha(&cache_file_name("foo", SHA_A), "foo").as_deref(),
            Some(SHA_A)
        );
        assert_eq!(
            cache_file_sha(&cache_file_name("foo-bar", SHA_A), "foo"),
            None
        );
        assert_eq!(
            cache_file_sha(&cache_file_name("plugin/foo", SHA_B), "plugin/foo").as_deref(),
            Some(SHA_B)
        );
        assert_eq!(cache_file_sha("foo-notes.txt", "foo"), None);
    }

    #[test]
    fn selected_listener_stays_reserved_until_manager_launches_child() {
        let probe = std::net::TcpListener::bind("127.0.0.1:0").expect("select test port");
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
