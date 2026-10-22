use std::path::{Path, PathBuf};

use futures_util::StreamExt;
use sha2::{Digest, Sha256};
use tokio::io::AsyncWriteExt;

use crate::internal_ext::pin::Sha256Pin;
use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::{GatewayAccess, SidecarSpec};
use crate::internal_ext::sidecar_util::{
    artifact_download_url, cache_file_name, direct_download_url, download_message, hex_lower,
    set_executable,
};
use crate::internal_ext::Adapter;

impl Adapter {
    pub(super) async fn ensure_binary(&self, spec: &SidecarSpec) -> Result<PathBuf, FetchError> {
        let id = spec.id();
        let artifact = spec
            .artifact
            .as_ref()
            .ok_or_else(|| FetchError::Refused(format!("{} has no binary artifact", spec.name)))?;
        let cache_path = self
            .cache_path(&id, &artifact.sha256)
            .map_err(FetchError::Retry)?;
        let gateway = self.gateway.session();
        let downloaded = ensure_verified(
            &gateway,
            &artifact.url,
            &artifact.sha256,
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
        Ok(cache_path)
    }

    fn cache_path(&self, id: &str, sha256: &Sha256Pin) -> Result<PathBuf, String> {
        Ok(self
            .dirs()?
            .binaries()
            .join(cache_file_name(id, sha256.as_str())))
    }
}

/// The largest artifact the desktop downloads: the largest object the
/// gateway's blob store holds (services/room maxArtifactBlobBytes, the
/// platform artifact upload cap).
const MAX_ARTIFACT_BYTES: u64 = 256 << 20;

fn too_large() -> FetchError {
    FetchError::Refused(format!(
        "download: the artifact is larger than {} MiB",
        MAX_ARTIFACT_BYTES >> 20
    ))
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
    gateway: &GatewayAccess,
    url: &str,
    sha256: &Sha256Pin,
    dest: &Path,
    executable: bool,
    progress: impl FnMut(FetchProgress),
) -> Result<bool, FetchError> {
    if sha256.file_matches(dest).await {
        return Ok(false);
    }
    fetch_verified(gateway, url, sha256, dest, executable, progress).await?;
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
    gateway: &GatewayAccess,
    url: &str,
    sha256: &Sha256Pin,
    dest: &Path,
    executable: bool,
    mut progress: impl FnMut(FetchProgress),
) -> Result<(), FetchError> {
    let url = direct_download_url(
        &artifact_download_url(&gateway.base_url, url).map_err(FetchError::Refused)?,
    );
    let resp = gateway.send(reqwest::Method::GET, &url, |r| r).await?;
    let total = resp.content_length();
    if total.is_some_and(|total| total > MAX_ARTIFACT_BYTES) {
        return Err(too_large());
    }
    let dir = dest
        .parent()
        .ok_or(FetchError::Retry("download path has no parent".into()))?;
    tokio::fs::create_dir_all(dir)
        .await
        .map_err(|e| FetchError::Retry(format!("create cache dir: {e}")))?;
    // A temp of this download alone: two downloads of one file never write
    // into each other's bytes; the verified one is renamed into place.
    let tmp = crate::files::temp_path(dest, "part").map_err(FetchError::Retry)?;
    let mut hasher = Sha256::new();
    let streamed = async {
        let mut file = tokio::fs::File::create(&tmp)
            .await
            .map_err(|e| FetchError::Retry(format!("create temp: {e}")))?;
        let mut stream = resp.bytes_stream();
        let mut written: u64 = 0;
        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|e| FetchError::Retry(format!("download stream: {e}")))?;
            written += chunk.len() as u64;
            if written > MAX_ARTIFACT_BYTES {
                return Err(too_large());
            }
            hasher.update(&chunk);
            file.write_all(&chunk)
                .await
                .map_err(|e| FetchError::Retry(format!("write temp: {e}")))?;
            progress(FetchProgress::Downloading { written, total });
        }
        file.sync_all()
            .await
            .map_err(|e| FetchError::Retry(format!("sync temp: {e}")))
    }
    .await;
    if let Err(e) = streamed {
        let _ = tokio::fs::remove_file(&tmp).await;
        return Err(e);
    }
    let got = hex_lower(&hasher.finalize());
    progress(FetchProgress::Verifying);
    if got != sha256.as_str() {
        let _ = tokio::fs::remove_file(&tmp).await;
        return Err(FetchError::Mismatch(format!(
            "sha256 mismatch: got {got}, want {sha256} — refusing to run"
        )));
    }
    if executable {
        if let Err(e) = set_executable(&tmp) {
            let _ = tokio::fs::remove_file(&tmp).await;
            return Err(FetchError::Retry(e));
        }
    }
    if let Err(e) = tokio::fs::rename(&tmp, dest).await {
        let _ = tokio::fs::remove_file(&tmp).await;
        return Err(FetchError::Retry(format!("cache rename: {e}")));
    }
    sha256.verified(dest);
    Ok(())
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
        let sha = Sha256Pin::of(pin);
        let result = fetch_verified(
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
