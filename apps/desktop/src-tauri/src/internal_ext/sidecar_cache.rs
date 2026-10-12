use std::path::{Path, PathBuf};

use futures_util::StreamExt;
use sha2::{Digest, Sha256};
use tauri::Manager;
use tokio::io::AsyncWriteExt;

use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::{GatewayAccess, SidecarSpec};
use crate::internal_ext::sidecar_util::{
    artifact_download_url, binary_ready, cache_file_name, direct_download_url, download_message,
    hex_lower, set_executable, stale_cache_files,
};
use crate::internal_ext::Adapter;

impl Adapter {
    pub(super) async fn ensure_binary(&self, spec: &SidecarSpec) -> Result<PathBuf, FetchError> {
        let id = spec.id();
        let cache_path = self
            .cache_path(&id, &spec.sha256)
            .map_err(FetchError::Retry)?;
        let gateway = self.gateway.lock().clone();
        let client = self.http().map_err(FetchError::Retry)?;
        let downloaded = ensure_verified(
            &client,
            &gateway,
            &spec.url,
            &spec.sha256,
            &cache_path,
            true,
            |progress| {
                let (state, message) = progress.status(&spec.name);
                self.set_status(&id, state, message, None);
            },
        )
        .await?;
        let message = if downloaded {
            format!("{} downloaded and verified", spec.name)
        } else {
            format!("{} is already downloaded", spec.name)
        };
        self.set_status(&id, "ready", message, None);
        // Only the verified binary for the current spec stays cached.
        let keep = cache_file_name(&id, &spec.sha256);
        self.remove_cached_binaries(&id, Some(&keep));
        Ok(cache_path)
    }

    /// Delete cached binaries (and stale partial downloads) of component `id`
    /// except `keep`; `None` deletes them all (component uninstalled).
    /// Best-effort and off the async runtime.
    pub(super) fn remove_cached_binaries(&self, id: &str, keep: Option<&str>) {
        let Ok(dir) = self.cache_dir() else {
            return;
        };
        let id = id.to_string();
        let keep = keep.map(str::to_string);
        tauri::async_runtime::spawn_blocking(move || {
            let Ok(entries) = std::fs::read_dir(&dir) else {
                return;
            };
            let names: Vec<String> = entries
                .filter_map(|entry| entry.ok()?.file_name().into_string().ok())
                .collect();
            for name in stale_cache_files(&names, &id, keep.as_deref()) {
                if let Err(e) = std::fs::remove_file(dir.join(&name)) {
                    eprintln!("[sidecar-manager:{id}] could not remove cached {name}: {e}");
                }
            }
        });
    }

    pub(super) fn cache_dir(&self) -> Result<PathBuf, String> {
        self.app
            .path()
            .app_cache_dir()
            .map(|base| base.join("cores"))
            .map_err(|e| format!("cache dir: {e}"))
    }

    fn cache_path(&self, id: &str, sha256: &str) -> Result<PathBuf, String> {
        Ok(self.cache_dir()?.join(cache_file_name(id, sha256)))
    }
}

/// Progress of one verified download, for status lines.
pub(super) enum FetchProgress {
    Downloading { written: u64, total: Option<u64> },
    Verifying,
}

impl FetchProgress {
    pub(super) fn status(&self, label: &str) -> (&'static str, String) {
        match self {
            FetchProgress::Downloading { written, total } => {
                ("downloading", download_message(label, *written, *total))
            }
            FetchProgress::Verifying => ("verifying", format!("Verifying {label} checksum")),
        }
    }
}

/// Make `dest` hold the bytes pinned by `sha256`: kept when the file there
/// already hashes to the pin, else downloaded through `fetch_verified`.
/// Reports whether a download happened. The one cache-or-fetch step of every
/// pinned component binary (supervised sidecars, materialized stdio MCP
/// tools).
pub(super) async fn ensure_verified(
    client: &reqwest::Client,
    gateway: &GatewayAccess,
    url: &str,
    sha256: &str,
    dest: &Path,
    executable: bool,
    progress: impl FnMut(FetchProgress),
) -> Result<bool, FetchError> {
    if binary_ready(dest, sha256).await {
        return Ok(false);
    }
    fetch_verified(client, gateway, url, sha256, dest, executable, progress).await?;
    Ok(true)
}

/// Download one component artifact to `dest`, verifying it against the
/// pinned `sha256` before it becomes visible there (temp file + rename).
/// The one download path for every component file (core/plugin binaries,
/// sandbox bundle files).
///
/// Trust anchor = the configured gateway origin (TLS + the member's
/// session). The gateway verifies each package's signed envelope against the
/// platform's trusted component keys at install time and serves the pinned
/// sha256 in the member catalog; the desktop checks the bytes against that
/// pin. It does not re-verify the envelope: the trusted keys are
/// runtime-managed on that same gateway (/v1/platform/component-keys), so
/// fetching them from it adds no independent trust, and baking keys into the
/// app would hardcode deployment data.
///
/// The session bearer goes ONLY to the configured gateway: an artifact URL on
/// any other origin is refused before a request is made. The request asks
/// for a direct download (`direct_download_url`); the gateway's 302 to a
/// presigned blob-store URL is followed WITHOUT the bearer (reqwest strips
/// `Authorization` on a cross-host redirect), and the sha256 pin below is the
/// trust check on the bytes either way.
pub(super) async fn fetch_verified(
    client: &reqwest::Client,
    gateway: &GatewayAccess,
    url: &str,
    sha256: &str,
    dest: &Path,
    executable: bool,
    mut progress: impl FnMut(FetchProgress),
) -> Result<(), FetchError> {
    let url = direct_download_url(
        &artifact_download_url(&gateway.base_url, url).map_err(FetchError::Refused)?,
    );
    let resp = client
        .get(url)
        .bearer_auth(&gateway.token)
        .send()
        .await
        .map_err(|e| FetchError::Retry(format!("download: {e}")))?;
    if !resp.status().is_success() {
        return Err(FetchError::Retry(format!(
            "download: status {}",
            resp.status()
        )));
    }
    let total = resp.content_length();
    let dir = dest
        .parent()
        .ok_or(FetchError::Retry("download path has no parent".into()))?;
    let file_name = dest
        .file_name()
        .ok_or(FetchError::Retry("download path has no file name".into()))?
        .to_string_lossy()
        .into_owned();
    tokio::fs::create_dir_all(dir)
        .await
        .map_err(|e| FetchError::Retry(format!("create cache dir: {e}")))?;
    let tmp = dir.join(format!(".{file_name}.part"));
    let mut hasher = Sha256::new();
    {
        let mut file = tokio::fs::File::create(&tmp)
            .await
            .map_err(|e| FetchError::Retry(format!("create temp: {e}")))?;
        let mut stream = resp.bytes_stream();
        let mut written: u64 = 0;
        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|e| FetchError::Retry(format!("download stream: {e}")))?;
            hasher.update(&chunk);
            file.write_all(&chunk)
                .await
                .map_err(|e| FetchError::Retry(format!("write temp: {e}")))?;
            written += chunk.len() as u64;
            progress(FetchProgress::Downloading { written, total });
        }
        file.flush()
            .await
            .map_err(|e| FetchError::Retry(format!("flush temp: {e}")))?;
    }
    let got = hex_lower(&hasher.finalize());
    progress(FetchProgress::Verifying);
    if !got.eq_ignore_ascii_case(sha256) {
        let _ = tokio::fs::remove_file(&tmp).await;
        return Err(FetchError::Mismatch(format!(
            "sha256 mismatch: got {got}, want {sha256} — refusing to run"
        )));
    }
    if executable {
        set_executable(&tmp).map_err(FetchError::Retry)?;
    }
    tokio::fs::rename(&tmp, dest)
        .await
        .map_err(|e| FetchError::Retry(format!("cache rename: {e}")))
}

#[cfg(test)]
mod tests {
    use std::io::{BufRead, BufReader, Write};
    use std::sync::{Arc, Mutex};

    use super::*;

    /// One recorded request: target (path + query) and lower-cased headers.
    type Seen = Arc<Mutex<Vec<(String, Vec<String>)>>>;

    /// A tiny HTTP/1.1 server on `host`; `respond(target)` builds the raw
    /// response. Records every request it answers.
    fn serve(host: &str, respond: impl Fn(&str) -> Vec<u8> + Send + 'static) -> (String, Seen) {
        let server = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
        let base = format!(
            "http://{host}:{}",
            server.local_addr().expect("addr").port()
        );
        let seen: Seen = Arc::default();
        let log = Arc::clone(&seen);
        std::thread::spawn(move || {
            for stream in server.incoming() {
                let Ok(mut stream) = stream else { continue };
                let mut reader = BufReader::new(stream.try_clone().expect("clone"));
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() {
                    continue;
                }
                let target = line.split_whitespace().nth(1).unwrap_or("").to_string();
                let mut headers = Vec::new();
                loop {
                    let mut header = String::new();
                    if reader.read_line(&mut header).is_err() || header.trim().is_empty() {
                        break;
                    }
                    headers.push(header.trim().to_ascii_lowercase());
                }
                log.lock().expect("log").push((target.clone(), headers));
                let _ = stream.write_all(&respond(&target));
            }
        });
        (base, seen)
    }

    fn ok(body: &[u8]) -> Vec<u8> {
        let mut out = format!(
            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        )
        .into_bytes();
        out.extend_from_slice(body);
        out
    }

    fn has_auth(headers: &[String]) -> bool {
        headers.iter().any(|h| h.starts_with("authorization:"))
    }

    async fn download(
        tag: &str,
        redirect_body: &'static [u8],
        pin: &[u8],
    ) -> (Result<(), FetchError>, Seen, Seen) {
        // The "blob store" answers on another host name than the gateway, as
        // a presigned S3 URL does.
        let (store, store_seen) = serve("localhost", move |_| ok(redirect_body));
        let location = format!("{store}/bucket/obj?X-Amz-Signature=abc");
        let (gateway, gateway_seen) = serve("127.0.0.1", move |_| {
            format!("HTTP/1.1 302 Found\r\nLocation: {location}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
                .into_bytes()
        });
        let access = GatewayAccess {
            base_url: gateway,
            token: "member-session".into(),
        };
        let dest = std::env::temp_dir().join(format!("bnbloc-direct-{}-{tag}", std::process::id()));
        let _ = std::fs::remove_file(&dest);
        let client = reqwest::Client::new();
        let sha = hex_lower(&Sha256::digest(pin));
        let result = fetch_verified(
            &client,
            &access,
            "/v1/artifacts/sha256/k",
            &sha,
            &dest,
            false,
            |_| {},
        )
        .await;
        if result.is_ok() {
            assert_eq!(std::fs::read(&dest).expect("downloaded"), redirect_body);
        }
        let _ = std::fs::remove_file(&dest);
        (result, gateway_seen, store_seen)
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn direct_downloads_follow_the_redirect_without_the_session() {
        let (result, gateway_seen, store_seen) =
            download("ok", b"binary bytes", b"binary bytes").await;
        assert!(result.is_ok(), "download failed: {:?}", result.err());
        let gateway_seen = gateway_seen.lock().expect("log").clone();
        let store_seen = store_seen.lock().expect("log").clone();
        assert_eq!(gateway_seen.len(), 1);
        assert_eq!(gateway_seen[0].0, "/v1/artifacts/sha256/k?direct=1");
        assert!(has_auth(&gateway_seen[0].1), "the gateway gets the session");
        assert_eq!(store_seen.len(), 1);
        assert_eq!(store_seen[0].0, "/bucket/obj?X-Amz-Signature=abc");
        assert!(
            !has_auth(&store_seen[0].1),
            "the presigned hop must not carry the session"
        );
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn redirected_bytes_are_still_checked_against_the_pin() {
        let (result, _, _) = download("tampered", b"tampered", b"binary bytes").await;
        assert!(
            matches!(result, Err(FetchError::Mismatch(_))),
            "got {:?}",
            result.err()
        );
    }
}
