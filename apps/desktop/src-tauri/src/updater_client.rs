use std::time::Duration;

use serde::Serialize;
use tauri::{Emitter, State};
use tauri_plugin_updater::UpdaterExt;

use crate::{child_process, settings::Settings, AppState};

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateCheckResult {
    available: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    notes: Option<String>,
    forced: bool,
}

#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "camelCase")]
struct UpdateProgress {
    downloaded: u64,
    total: Option<u64>,
}

// The gateway a fresh install points at before the user configured one. It
// is deployment data, so it is never hardcoded: a deployment's build sets
// BNBLOC_DEFAULT_GATEWAY (see docs/desktop-update-release-runbook.md). An
// unset build has no default — the UI asks for a gateway instead of silently
// talking to someone else's deployment.
const DEFAULT_GATEWAY_URL: Option<&str> = option_env!("BNBLOC_DEFAULT_GATEWAY");

/// The gateway base URL: the configured one, else the build's default, else
/// empty (not configured).
pub fn gateway_url(settings: &Settings) -> String {
    let configured = settings.gateway_url.trim();
    let url = if configured.is_empty() {
        DEFAULT_GATEWAY_URL.unwrap_or_default().trim()
    } else {
        configured
    };
    url.trim_end_matches('/').to_string()
}

// The updater talks to the gateway: connecting must be quick, and a stalled
// transfer must fail, but a large download over a slow link must not be cut
// off by a total-request deadline.
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const READ_IDLE_TIMEOUT: Duration = Duration::from_secs(30);

fn update_endpoint(settings: &Settings) -> Result<String, String> {
    let gateway = gateway_url(settings);
    if gateway.is_empty() {
        return Err("no gateway configured".into());
    }
    Ok(format!(
        "{gateway}/v1/app/update/{{{{target}}}}-{{{{arch}}}}/{{{{current_version}}}}"
    ))
}

fn bearer_header(session_token: &str) -> Result<String, String> {
    let token = session_token.trim();
    if token.is_empty() {
        return Err("session token is required".into());
    }
    Ok(format!("Bearer {token}"))
}

fn updater_for(
    app: &tauri::AppHandle,
    settings: &Settings,
    session_token: &str,
) -> Result<tauri_plugin_updater::Updater, String> {
    app.updater_builder()
        .endpoints(vec![update_endpoint(settings)?
            .parse()
            .map_err(|e| format!("invalid update endpoint: {e}"))?])
        .map_err(|e| e.to_string())?
        .header("Authorization", bearer_header(session_token)?)
        .map_err(|e| e.to_string())?
        .configure_client(|client| {
            client
                .connect_timeout(CONNECT_TIMEOUT)
                .read_timeout(READ_IDLE_TIMEOUT)
        })
        .build()
        .map_err(|e| e.to_string())
}

fn forced_from_raw(raw: &serde_json::Value) -> bool {
    raw.get("forced")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false)
}

#[tauri::command]
pub async fn check_update(
    app: tauri::AppHandle,
    state: State<'_, AppState>,
    session_token: String,
) -> Result<UpdateCheckResult, String> {
    let settings = state.settings.lock().clone();
    let updater = updater_for(&app, &settings, &session_token)?;
    match updater.check().await.map_err(|e| e.to_string())? {
        Some(update) => Ok(UpdateCheckResult {
            available: true,
            version: Some(update.version),
            notes: update.body,
            forced: forced_from_raw(&update.raw_json),
        }),
        None => Ok(UpdateCheckResult {
            available: false,
            version: None,
            notes: None,
            forced: false,
        }),
    }
}

#[tauri::command]
pub async fn install_update(
    app: tauri::AppHandle,
    state: State<'_, AppState>,
    session_token: String,
) -> Result<bool, String> {
    let settings = state.settings.lock().clone();
    let updater = updater_for(&app, &settings, &session_token)?;
    let Some(update) = updater.check().await.map_err(|e| e.to_string())? else {
        return Ok(false);
    };

    let progress_app = app.clone();
    let mut downloaded = 0_u64;
    let bytes = update
        .download(
            move |chunk_len, total| {
                downloaded = downloaded.saturating_add(chunk_len as u64);
                let _ =
                    progress_app.emit("updater://progress", UpdateProgress { downloaded, total });
            },
            || {},
        )
        .await
        .map_err(|e| e.to_string())?;

    // Stop the bridge + sidecars only now, right before the install (the
    // Windows installer cannot replace a running sidecar binary), and wait
    // for them to be gone. A failed install brings them back up — an update
    // problem must never leave the member without a bridge.
    child_process::terminate(crate::detach_children(&app), child_process::STOP_GRACE).await;
    let installed = tauri::async_runtime::spawn_blocking(move || update.install(bytes))
        .await
        .map_err(|e| e.to_string())
        .and_then(|result| result.map_err(|e| e.to_string()));
    match installed {
        Ok(()) => app.restart(),
        Err(e) => {
            crate::resume_children(&app);
            Err(format!("update install failed: {e}"))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn update_endpoint_uses_configured_gateway_without_trailing_slash() {
        let settings = Settings {
            gateway_url: "https://bnbloc.example.test/".into(),
            ..Default::default()
        };

        assert_eq!(
            update_endpoint(&settings).unwrap(),
            "https://bnbloc.example.test/v1/app/update/{{target}}-{{arch}}/{{current_version}}"
        );
    }

    #[test]
    fn unconfigured_gateway_follows_the_build_default_or_fails_closed() {
        let resolved = gateway_url(&Settings::default());
        match DEFAULT_GATEWAY_URL {
            Some(default) => assert_eq!(resolved, default.trim().trim_end_matches('/')),
            None => {
                assert_eq!(resolved, "");
                assert!(update_endpoint(&Settings::default()).is_err());
            }
        }
    }

    #[test]
    fn bearer_header_rejects_blank_tokens() {
        assert!(bearer_header("  ").is_err());
        assert_eq!(bearer_header(" session ").unwrap(), "Bearer session");
    }

    #[test]
    fn forced_is_read_only_when_boolean() {
        assert!(forced_from_raw(&json!({"forced": true})));
        assert!(!forced_from_raw(&json!({"forced": false})));
        assert!(!forced_from_raw(&json!({"forced": "true"})));
        assert!(!forced_from_raw(&json!({})));
    }
}
