//! The on-disk layout of a sandbox component (`bnbloc.sandbox.package/v1`).
//!
//! A sandbox sidecar needs more than its executable: the package declares
//! extra per-platform files (a VMM helper, a firmware library, the
//! `bnbloc-vmexec` command router) published as `artifacts["<platform>/<name>"]`.
//! Every file is downloaded through the one verified download path
//! (`sidecar_cache::fetch_verified`, sha256-pinned, gateway origin only) and
//! laid out in ONE per-version directory with exactly the declared names:
//!
//! ```text
//! <app cache>/sandbox/<name>/<version>-<bundle id>/<name>[.exe]   the sidecar
//!                                                  /<file name>     each declared file
//! ```
//!
//! The bundle id hashes every (file, sha256) pair, so a republished helper
//! with an unchanged sidecar still gets a fresh directory. A bundle is built
//! in a hidden staging directory and renamed into place only once every file
//! verified; a running sidecar's directory is therefore never half-written.
//! Older bundle directories of the component are removed once the current
//! one is in place.
use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};

use crate::internal_ext::sidecar_cache::{fetch_verified, FetchProgress};
use crate::internal_ext::sidecar_fetch::FetchError;
use crate::internal_ext::sidecar_model::{GatewayAccess, SidecarSpec};
use crate::internal_ext::sidecar_util::{
    binary_ready, cache_stem, exe_suffix, hex_lower, set_executable,
};

/// The declared sidecar file that is the command router the cores use
/// (`BNBLOC_SANDBOX_VMEXEC`). A contract name (docs/prd/2026-10-09-agent-vm-
/// sandbox.md, "Phase 1 contracts"), not deployment data.
pub(super) const VMEXEC_FILE: &str = "bnbloc-vmexec";

/// One file of a bundle.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct BundleEntry {
    /// The file name inside the bundle directory.
    pub(super) rel: String,
    pub(super) url: String,
    pub(super) sha256: String,
    pub(super) executable: bool,
}

/// Where a sandbox spec's files live and what they must hash to.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct BundlePlan {
    /// `<app cache>/sandbox/<name>`: every bundle of this component.
    pub(super) root: PathBuf,
    /// The current bundle directory.
    pub(super) dir: PathBuf,
    /// Hidden staging directory the bundle is assembled in.
    pub(super) staging: PathBuf,
    /// The sidecar executable first, then the declared files.
    pub(super) entries: Vec<BundleEntry>,
}

impl BundlePlan {
    /// The sidecar executable inside the bundle.
    pub(super) fn exe(&self) -> PathBuf {
        self.dir.join(&self.entries[0].rel)
    }
}

/// The bundle plan of sandbox `spec` under `base` (`<app cache>/sandbox`).
pub(super) fn bundle_plan(base: &Path, spec: &SidecarSpec) -> Result<BundlePlan, String> {
    let mut entries = vec![BundleEntry {
        rel: format!("{}{}", spec.name, exe_suffix()),
        url: spec.url.clone(),
        sha256: spec.sha256.to_ascii_lowercase(),
        executable: true,
    }];
    for file in &spec.sandbox.files {
        // pkgmeta restricts names to [a-z0-9][a-z0-9._-]*; refuse anything
        // else rather than trusting a catalog entry with a path.
        if file.name.is_empty()
            || file.name.starts_with('.')
            || !file
                .name
                .chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || "._-".contains(c))
        {
            return Err(format!(
                "sidecar file name {:?} is not a plain file name",
                file.name
            ));
        }
        if entries.iter().any(|e| e.rel == file.name) {
            return Err(format!(
                "sidecar file {:?} collides with another file of {}",
                file.name, spec.name
            ));
        }
        entries.push(BundleEntry {
            rel: file.name.clone(),
            url: file.url.clone(),
            sha256: file.sha256.to_ascii_lowercase(),
            executable: file.executable,
        });
    }
    let mut hasher = Sha256::new();
    for entry in &entries {
        hasher.update(format!("{}={}\n", entry.rel, entry.sha256));
    }
    let id = hex_lower(&hasher.finalize());
    let version: String = spec
        .version
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '.' {
                c
            } else {
                '_'
            }
        })
        .collect();
    let dir_name = format!("{version}-{}", &id[..16]);
    let root = base.join(cache_stem(&spec.name));
    Ok(BundlePlan {
        dir: root.join(&dir_name),
        staging: root.join(format!(".{dir_name}.part")),
        root,
        entries,
    })
}

/// Whether every file of the bundle is in place and hashes to its pin.
pub(super) async fn bundle_ready(plan: &BundlePlan) -> bool {
    for entry in &plan.entries {
        if !binary_ready(&plan.dir.join(&entry.rel), &entry.sha256).await {
            return false;
        }
    }
    true
}

/// Make the bundle runnable: download and verify every missing file into the
/// staging directory (files a previous attempt already verified are kept),
/// mark executables, then move the staging directory into place. Returns
/// whether anything was downloaded.
pub(super) async fn prepare_bundle(
    client: &reqwest::Client,
    gateway: &GatewayAccess,
    plan: &BundlePlan,
    mut progress: impl FnMut(&str, FetchProgress),
) -> Result<bool, FetchError> {
    if bundle_ready(plan).await {
        return Ok(false);
    }
    tokio::fs::create_dir_all(&plan.staging)
        .await
        .map_err(|e| FetchError::Retry(format!("create sandbox bundle dir: {e}")))?;
    for entry in &plan.entries {
        let dest = plan.staging.join(&entry.rel);
        if !binary_ready(&dest, &entry.sha256).await {
            fetch_verified(
                client,
                gateway,
                &entry.url,
                &entry.sha256,
                &dest,
                entry.executable,
                |p| progress(&entry.rel, p),
            )
            .await?;
        } else if entry.executable {
            set_executable(&dest).map_err(FetchError::Retry)?;
        }
    }
    let (staging, dir) = (plan.staging.clone(), plan.dir.clone());
    tauri::async_runtime::spawn_blocking(move || -> Result<(), String> {
        if dir.exists() {
            std::fs::remove_dir_all(&dir).map_err(|e| format!("replace sandbox bundle: {e}"))?;
        }
        std::fs::rename(&staging, &dir).map_err(|e| format!("install sandbox bundle: {e}"))
    })
    .await
    .map_err(|e| FetchError::Retry(format!("install sandbox bundle: {e}")))?
    .map_err(FetchError::Retry)?;
    Ok(true)
}

/// Remove every bundle directory of the component except `keep` (None: the
/// component was uninstalled — remove them all). Blocking; best-effort.
pub(super) fn remove_stale_bundles(root: &Path, keep: Option<&Path>) {
    let Ok(entries) = std::fs::read_dir(root) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if Some(path.as_path()) == keep {
            continue;
        }
        // The staging dir of the kept bundle may hold verified files of an
        // interrupted download; keep it too.
        if let Some(keep) = keep {
            let staging = keep
                .file_name()
                .map(|name| keep.with_file_name(format!(".{}.part", name.to_string_lossy())));
            if staging.as_deref() == Some(path.as_path()) {
                continue;
            }
        }
        let removed = if path.is_dir() {
            std::fs::remove_dir_all(&path)
        } else {
            std::fs::remove_file(&path)
        };
        if let Err(e) = removed {
            eprintln!("[sandbox] could not remove stale {}: {e}", path.display());
        }
    }
    if keep.is_none() {
        let _ = std::fs::remove_dir(root);
    }
}

/// The env a sandbox sidecar is launched with beyond the common sidecar
/// fields: the per-launch bearer in `tokenEnv`, the private data dir in
/// `dataDirEnv`, and each declared file's absolute path in its env.
pub(super) fn launch_env(
    spec: &SidecarSpec,
    exe: &Path,
    token: &str,
    data_dir: &Path,
) -> Vec<(String, String)> {
    let mut env = vec![(spec.sandbox.token_env.clone(), token.to_string())];
    if !spec.sandbox.data_dir_env.trim().is_empty() {
        env.push((
            spec.sandbox.data_dir_env.clone(),
            data_dir.to_string_lossy().into_owned(),
        ));
    }
    let dir = exe.parent().unwrap_or(exe);
    for file in &spec.sandbox.files {
        env.push((
            file.env.clone(),
            dir.join(&file.name).to_string_lossy().into_owned(),
        ));
    }
    env
}

/// The `bnbloc-vmexec` helper of the bundle whose executable is `exe`, when
/// the package declares it.
pub(super) fn vmexec_path(spec: &SidecarSpec, exe: &Path) -> Option<PathBuf> {
    spec.sandbox
        .files
        .iter()
        .any(|f| f.name == VMEXEC_FILE)
        .then(|| exe.parent().unwrap_or(exe).join(VMEXEC_FILE))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::internal_ext::sidecar_model::{ComponentKind, SandboxLaunch, SidecarFileSpec};
    use crate::internal_ext::sidecar_orphan::SpawnRecord;

    fn sha(bytes: &[u8]) -> String {
        hex_lower(&Sha256::digest(bytes))
    }

    fn spec(base_url: &str, files: &[(&str, &[u8], bool)], main: &[u8]) -> SidecarSpec {
        SidecarSpec {
            kind: ComponentKind::Sandbox,
            name: "vmrt".into(),
            version: "0.1.0".into(),
            sha256: sha(main),
            url: format!("{base_url}/v1/artifacts/main"),
            manifest: None,
            local_url: "http://127.0.0.1:7311".into(),
            listen_env: "VMRT_LISTEN".into(),
            supervised_env: "VMRT_SUPERVISED".into(),
            args: Vec::new(),
            binary: true,
            plugin: Default::default(),
            sandbox: SandboxLaunch {
                token_env: "VMRT_TOKEN".into(),
                data_dir_env: "VMRT_DATA_DIR".into(),
                files: files
                    .iter()
                    .map(|(name, body, executable)| SidecarFileSpec {
                        name: name.to_string(),
                        env: format!(
                            "VMRT_{}_PATH",
                            name.to_ascii_uppercase().replace(['-', '.'], "_")
                        ),
                        executable: *executable,
                        url: format!("{base_url}/v1/artifacts/{name}"),
                        sha256: sha(body),
                    })
                    .collect(),
                guest: None,
            },
        }
    }

    /// A tiny artifact server: path → body; counts requests.
    fn serve(
        files: Vec<(&'static str, Vec<u8>)>,
    ) -> (String, std::sync::Arc<std::sync::atomic::AtomicUsize>) {
        use std::io::{BufRead, BufReader, Write};
        let server = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
        let base = format!("http://{}", server.local_addr().expect("addr"));
        let hits = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let counter = hits.clone();
        std::thread::spawn(move || {
            for stream in server.incoming() {
                let Ok(mut stream) = stream else { continue };
                let mut reader = BufReader::new(stream.try_clone().expect("clone"));
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() {
                    continue;
                }
                let target = line.split_whitespace().nth(1).unwrap_or("");
                let path = target.split('?').next().unwrap_or("").to_string();
                loop {
                    let mut header = String::new();
                    if reader.read_line(&mut header).is_err() || header.trim().is_empty() {
                        break;
                    }
                }
                counter.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                let body = files
                    .iter()
                    .find(|(p, _)| path == format!("/v1/artifacts/{p}"))
                    .map(|(_, b)| b.clone());
                let _ = match body {
                    Some(body) => {
                        let _ = write!(
                            stream,
                            "HTTP/1.1 200 OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                            body.len()
                        );
                        stream.write_all(&body)
                    }
                    None => write!(
                        stream,
                        "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                    ),
                };
            }
        });
        (base, hits)
    }

    fn temp_base(tag: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("bnbloc-sbx-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        dir
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn prepare_bundle_downloads_verifies_and_lays_out_declared_files() {
        let main = b"#!/bin/sh\necho sidecar\n".to_vec();
        let vmm = b"vmm helper".to_vec();
        let fw = b"firmware".to_vec();
        let vmexec = b"router".to_vec();
        let (base_url, hits) = serve(vec![
            ("main", main.clone()),
            ("vmm", vmm.clone()),
            ("firmware.lib", fw.clone()),
            ("bnbloc-vmexec", vmexec.clone()),
        ]);
        let spec = spec(
            &base_url,
            &[
                ("vmm", &vmm, true),
                ("firmware.lib", &fw, false),
                ("bnbloc-vmexec", &vmexec, true),
            ],
            &main,
        );
        let base = temp_base("layout");
        let plan = bundle_plan(&base, &spec).expect("plan");
        let gateway = GatewayAccess {
            base_url: base_url.clone(),
            token: "session".into(),
        };
        let client = reqwest::Client::new();

        let downloaded = prepare_bundle(&client, &gateway, &plan, |_, _| {})
            .await
            .expect("bundle prepared");

        assert!(downloaded);
        assert_eq!(hits.load(std::sync::atomic::Ordering::SeqCst), 4);
        assert!(plan.dir.starts_with(base.join("vmrt")));
        assert!(plan
            .dir
            .file_name()
            .unwrap()
            .to_string_lossy()
            .starts_with("0.1.0-"));
        assert_eq!(plan.exe(), plan.dir.join(format!("vmrt{}", exe_suffix())));
        assert_eq!(std::fs::read(plan.exe()).unwrap(), main);
        assert_eq!(std::fs::read(plan.dir.join("vmm")).unwrap(), vmm);
        assert_eq!(std::fs::read(plan.dir.join("firmware.lib")).unwrap(), fw);
        assert!(!plan.staging.exists(), "staging moved into place");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = |p: PathBuf| std::fs::metadata(p).unwrap().permissions().mode() & 0o111;
            assert_ne!(mode(plan.exe()), 0, "sidecar is executable");
            assert_ne!(
                mode(plan.dir.join("vmm")),
                0,
                "declared executable file is +x"
            );
            assert_ne!(mode(plan.dir.join("bnbloc-vmexec")), 0);
        }
        assert_eq!(
            vmexec_path(&spec, &plan.exe()),
            Some(plan.dir.join(VMEXEC_FILE))
        );

        // A verified bundle is reused without any download.
        let again = prepare_bundle(&client, &gateway, &plan, |_, _| {})
            .await
            .expect("reuse");
        assert!(!again);
        assert_eq!(hits.load(std::sync::atomic::Ordering::SeqCst), 4);

        // A republished helper (same sidecar) gets its own directory; the
        // old one is removed once the new one is in place.
        let vmm2 = b"vmm helper v2".to_vec();
        let (base_url2, _) = serve(vec![
            ("main", main.clone()),
            ("vmm", vmm2.clone()),
            ("firmware.lib", fw.clone()),
            ("bnbloc-vmexec", vmexec.clone()),
        ]);
        let spec2 = self::spec(
            &base_url2,
            &[
                ("vmm", &vmm2, true),
                ("firmware.lib", &fw, false),
                ("bnbloc-vmexec", &vmexec, true),
            ],
            &main,
        );
        let plan2 = bundle_plan(&base, &spec2).expect("plan2");
        assert_ne!(plan2.dir, plan.dir);
        let gateway2 = GatewayAccess {
            base_url: base_url2,
            token: "session".into(),
        };
        prepare_bundle(&client, &gateway2, &plan2, |_, _| {})
            .await
            .expect("v2");
        remove_stale_bundles(&plan2.root, Some(&plan2.dir));
        assert!(!plan.dir.exists());
        assert_eq!(std::fs::read(plan2.dir.join("vmm")).unwrap(), vmm2);

        // Uninstall removes every bundle.
        remove_stale_bundles(&plan2.root, None);
        assert!(!plan2.root.exists());
        let _ = std::fs::remove_dir_all(&base);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn prepare_bundle_refuses_a_file_whose_sha256_does_not_match() {
        let main = b"sidecar".to_vec();
        let (base_url, _) = serve(vec![("main", main.clone()), ("vmm", b"tampered".to_vec())]);
        let spec = spec(&base_url, &[("vmm", b"the pinned helper", true)], &main);
        let base = temp_base("mismatch");
        let plan = bundle_plan(&base, &spec).expect("plan");
        let gateway = GatewayAccess {
            base_url,
            token: "session".into(),
        };

        let err = prepare_bundle(&reqwest::Client::new(), &gateway, &plan, |_, _| {})
            .await
            .expect_err("a mismatching file must be refused");

        assert!(matches!(err, FetchError::Mismatch(_)), "{err:?}");
        assert!(
            !plan.dir.exists(),
            "no bundle directory with an unverified file"
        );
        assert!(!plan.staging.join("vmm").exists());
        let _ = std::fs::remove_dir_all(&base);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn prepare_bundle_refuses_artifacts_off_the_gateway_origin() {
        let main = b"sidecar".to_vec();
        let mut spec = spec("http://127.0.0.1:9", &[], &main);
        spec.url = "https://elsewhere.example/v1/artifacts/main".into();
        let base = temp_base("origin");
        let plan = bundle_plan(&base, &spec).expect("plan");
        let gateway = GatewayAccess {
            base_url: "http://127.0.0.1:9".into(),
            token: "session".into(),
        };
        let err = prepare_bundle(&reqwest::Client::new(), &gateway, &plan, |_, _| {})
            .await
            .expect_err("foreign origin");
        assert!(matches!(err, FetchError::Refused(_)), "{err:?}");
        let _ = std::fs::remove_dir_all(&base);
    }

    #[test]
    fn bundle_plan_refuses_paths_and_collisions() {
        let mut bad = spec("http://gw", &[("vmm", b"x", true)], b"y");
        bad.sandbox.files[0].name = "../vmm".into();
        assert!(bundle_plan(Path::new("/tmp/x"), &bad).is_err());
        let mut clash = spec("http://gw", &[("vmm", b"x", true)], b"y");
        clash.sandbox.files[0].name = format!("vmrt{}", exe_suffix());
        assert!(bundle_plan(Path::new("/tmp/x"), &clash).is_err());
    }

    #[test]
    fn launch_env_hands_token_data_dir_and_every_file_path() {
        let spec = spec(
            "http://gw",
            &[("vmm", b"x", true), ("firmware", b"y", false)],
            b"z",
        );
        let exe = Path::new("/cache/sandbox/vmrt/0.1.0-abc/vmrt");
        let env = launch_env(&spec, exe, "tok-123", Path::new("/data/sandbox/vmrt"));
        assert_eq!(
            env,
            vec![
                ("VMRT_TOKEN".to_string(), "tok-123".to_string()),
                (
                    "VMRT_DATA_DIR".to_string(),
                    "/data/sandbox/vmrt".to_string()
                ),
                (
                    "VMRT_VMM_PATH".to_string(),
                    "/cache/sandbox/vmrt/0.1.0-abc/vmm".to_string()
                ),
                (
                    "VMRT_FIRMWARE_PATH".to_string(),
                    "/cache/sandbox/vmrt/0.1.0-abc/firmware".to_string()
                ),
            ]
        );
        assert_eq!(vmexec_path(&spec, exe), None, "no declared vmexec helper");
    }

    /// The sandbox goes through the one orphan rule: its executable sits in a
    /// per-version bundle dir below the component's bundle root.
    #[test]
    fn a_sandbox_orphan_runs_from_its_bundle_root() {
        use crate::internal_ext::sidecar_orphan::owned_orphan;
        let root = temp_base("orphan");
        let bundle = root.join("0.1.0-abc");
        std::fs::create_dir_all(&bundle).unwrap();
        let exe = bundle.join("vmrt");
        let exe = exe.to_string_lossy().into_owned();
        let record = SpawnRecord {
            pid: 4242,
            sha256: "a".repeat(64),
            app_pid: 1,
        };
        assert_eq!(
            owned_orphan(4242, Some(&record), Some(&exe), &root, 2).map(|o| o.pid),
            Some(4242)
        );
        // Our own child (same app process) is never an orphan.
        assert!(owned_orphan(4242, Some(&record), Some(&exe), &root, 1).is_none());
        // A reused pid running something else is never signalled.
        assert!(owned_orphan(4242, Some(&record), Some("/usr/bin/vim"), &root, 2).is_none());
        let _ = std::fs::remove_dir_all(&root);
    }
}
