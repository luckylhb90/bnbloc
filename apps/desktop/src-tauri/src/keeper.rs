//! The session keeper of one account: the account's own sign-in on this
//! machine.
//!
//! The web hands the shell what it holds for an account (the vault's
//! credential); the keeper turns that into the account's own gateway session
//! and keeps it:
//!
//! - a **refresh** credential is forked once (`POST /v1/auth/refresh/fork`):
//!   a family of the keeper's own, nested under the web's. From then on the
//!   keeper renews only its own family and never presents the web's token
//!   again, because the web keeps rotating it and a rotated-away token that
//!   is presented later is a reuse, which ends the whole family and every
//!   fork under it. The bridge forks from the keeper's family, so a web
//!   rotation can never reach it either (docs/prd/2026-10-22-multi-account.md
//!   §2.3);
//! - an **API token** (a test account) is exchanged for a one-hour session
//!   again and again; it has no family, so no bridge.
//!
//! What the keeper makes (a session bearer, the family's refresh token) goes
//! to a `KeeperSink`; it holds no part of the account's runtime itself.
//! Removing the account ends the family (`POST /v1/auth/logout`, which also
//! ends the bridge's fork): the end of a sign-in is the end of everything it
//! opened.
//!
//! The next step is decided by one pure function (`next_step`) over the
//! state, and what a gateway answer means for the state by one more
//! (`apply`), so the rules above are tested without a gateway or a clock.
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::Mutex;
use serde::Deserialize;
use serde_json::json;

use crate::child_process::Backoff;
use crate::internal_ext::gateway_client::{post_json, GatewayError, API_TIMEOUT};

/// The client label of the keeper's family in the member's session list.
const CLIENT: &str = "desktop";

/// What the keeper takes for an account.
#[derive(Clone, PartialEq, Eq, serde::Deserialize)]
#[serde(tag = "kind", content = "token", rename_all = "camelCase")]
pub(crate) enum Credential {
    /// The account's refresh token from the vault (forked once).
    Refresh(String),
    /// A test account's API token (exchanged for each session).
    ApiToken(String),
}

impl Credential {
    /// Whether the account has a refresh family to serve agents from: a test
    /// account does not.
    pub(crate) fn can_serve(&self) -> bool {
        matches!(self, Credential::Refresh(_))
    }
}

impl std::fmt::Debug for Credential {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Credential::Refresh(_) => "Credential::Refresh(..)",
            Credential::ApiToken(_) => "Credential::ApiToken(..)",
        })
    }
}

/// What the keeper makes, for the account's runtime to apply.
pub(crate) trait KeeperSink: Send + Sync {
    /// A session bearer for the account (renewed, or re-scoped).
    fn session(&self, token: String);
    /// The keeper's own family token, which the bridge forks from (None once
    /// the family is gone).
    fn family(&self, token: Option<String>);
    /// The account can no longer renew: its credential was refused and no
    /// other has been handed. The runtime stops what acts as the account.
    fn signed_out(&self);
}

/// How long before a session expires the keeper renews it (the web's
/// `RENEW_AHEAD_MS`), and the shortest wait between two renewals.
const RENEW_AHEAD: Duration = Duration::from_secs(5 * 60);
const RENEW_MIN: Duration = Duration::from_secs(30);

/// How long the keeper waits between renewals and retries.
#[derive(Clone, Copy)]
pub(crate) struct Timing {
    /// The longest between renewals: the bridge keeps the session it minted
    /// for 30 minutes, so a session that lasts longer is still renewed well
    /// inside that. A shorter one is renewed ahead of its own expiry
    /// (`renew_after`).
    pub(crate) renew_every: Duration,
    pub(crate) retry_min: Duration,
    pub(crate) retry_max: Duration,
}

impl Timing {
    pub(crate) const LIVE: Timing = Timing {
        renew_every: Duration::from_secs(40 * 60),
        retry_min: Duration::from_secs(5),
        retry_max: Duration::from_secs(5 * 60),
    };
}

/// Bound on the logout of a family at removal or exit.
const REVOKE_TIMEOUT: Duration = Duration::from_secs(3);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum KeeperStatus {
    /// Has a session (renewing on schedule).
    Active,
    /// No session yet or a renewal failing for now (the gateway is down);
    /// retrying.
    Retrying,
    /// The credential was refused: waits for a new one from the vault.
    SignedOut,
}

struct State {
    /// The vault's current credential for the account.
    handed: Credential,
    /// The keeper's own refresh family (token), once forked.
    family: Option<String>,
    /// The handed token the keeper used and may not use again: a refresh
    /// token it forked from (or that was refused), an API token that was
    /// refused. A different handed token is usable again.
    spent: Option<String>,
    session: Option<String>,
    /// The tenant the session is scoped to.
    session_tenant: String,
    /// The tenant the vault wants ("" = no preference).
    tenant: String,
    /// The tenant a switch was refused for (not retried until it changes).
    switch_refused: Option<String>,
    renew_at: Option<Instant>,
    retry_at: Option<Instant>,
    stopped: bool,
}

/// How long after minting `session_token` it is renewed: ahead of its own
/// expiry (the gateway's session lifetime is configuration, not a constant of
/// the shell), never later than `timing.renew_every` and never sooner than
/// `RENEW_MIN`; a token that names no expiry waits the longest.
fn renew_after(timing: &Timing, session_token: &str, unix_now: u64) -> Duration {
    match crate::internal_ext::member_dir::token_exp(session_token) {
        Some(exp) => Duration::from_secs(exp.saturating_sub(unix_now))
            .saturating_sub(RENEW_AHEAD)
            .clamp(RENEW_MIN, timing.renew_every.max(RENEW_MIN)),
        None => timing.renew_every,
    }
}

fn unix_now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or_default()
}

/// What the keeper does next.
#[derive(Debug, PartialEq, Eq)]
enum Step {
    Fork(String),
    Renew(String),
    Exchange(String),
    Switch(String),
    /// Nothing until this time, or until something changes (None).
    Wait(Option<Instant>),
}

fn wants_switch(st: &State) -> Option<&str> {
    let want = st.tenant.trim();
    (!want.is_empty() && want != st.session_tenant && st.switch_refused.as_deref() != Some(want))
        .then_some(want)
}

/// The mint step for the credential, or a wait when it cannot be used.
fn mint_step(st: &State) -> Step {
    let usable = |token: &str| st.spent.as_deref() != Some(token);
    match (&st.handed, &st.family) {
        (Credential::ApiToken(t), _) if usable(t) => Step::Exchange(t.clone()),
        (Credential::Refresh(_), Some(family)) => Step::Renew(family.clone()),
        (Credential::Refresh(t), None) if usable(t) => Step::Fork(t.clone()),
        _ => Step::Wait(None),
    }
}

fn next_step(st: &State, now: Instant) -> Step {
    if let Some(at) = st.retry_at.filter(|at| *at > now) {
        return Step::Wait(Some(at));
    }
    match (&st.session, st.renew_at) {
        (None, _) => mint_step(st),
        (Some(_), Some(at)) if at <= now => mint_step(st),
        _ => match wants_switch(st) {
            Some(tenant) => Step::Switch(tenant.to_string()),
            None => Step::Wait(st.renew_at),
        },
    }
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Grant {
    session_token: String,
    #[serde(default)]
    refresh_token: String,
    #[serde(default)]
    tenant: String,
}

/// What a step's answer does to the state; what the sink should hear.
#[derive(Debug, Default, PartialEq, Eq)]
struct Effects {
    session: Option<String>,
    family: Option<Option<String>>,
    signed_out: bool,
}

/// Fold a step's outcome into the state.
fn apply(
    st: &mut State,
    step: &Step,
    outcome: Result<Grant, GatewayError>,
    now: Instant,
    unix_now: u64,
    timing: &Timing,
    backoff: &mut Backoff,
) -> Effects {
    let mut fx = Effects::default();
    match outcome {
        Ok(grant) => {
            backoff.reset();
            st.retry_at = None;
            match step {
                Step::Switch(tenant) => {
                    st.session_tenant = if grant.tenant.is_empty() {
                        tenant.clone()
                    } else {
                        grant.tenant
                    };
                }
                _ => {
                    st.renew_at = Some(now + renew_after(timing, &grant.session_token, unix_now));
                    st.session_tenant = grant.tenant;
                    if let Step::Fork(token) = step {
                        st.spent = Some(token.clone());
                    }
                    if !grant.refresh_token.is_empty() {
                        st.family = Some(grant.refresh_token.clone());
                        fx.family = Some(Some(grant.refresh_token));
                    }
                }
            }
            st.session = Some(grant.session_token.clone());
            fx.session = Some(grant.session_token);
        }
        Err(GatewayError::Failed { .. }) => {
            st.retry_at = Some(now + backoff.next(Duration::ZERO));
        }
        Err(GatewayError::SessionExpired | GatewayError::Refused { .. }) => match step {
            Step::Switch(tenant) => {
                // Not a tenant the account can act in: keep the session it
                // has, and ask again only for another tenant.
                st.switch_refused = Some(tenant.clone());
            }
            Step::Fork(token) | Step::Exchange(token) => {
                // Refused: no session of this credential stands (an API token
                // revoked while its last session is still unexpired is as
                // over as a refused first mint).
                st.spent = Some(token.clone());
                st.session = None;
                st.renew_at = None;
                fx.signed_out = true;
            }
            Step::Renew(_) => {
                // The family is gone (signed out elsewhere, expired, or its
                // tenant suspended); the handed token was forked from long
                // ago and may be rotated away, so it is spent.
                st.family = None;
                st.session = None;
                st.renew_at = None;
                if let Credential::Refresh(token) = &st.handed {
                    st.spent.get_or_insert_with(|| token.clone());
                }
                fx.family = Some(None);
                fx.signed_out = true;
            }
            Step::Wait(_) => {}
        },
    }
    fx
}

struct Shared {
    gateway: String,
    timing: Timing,
    sink: Arc<dyn KeeperSink>,
    state: Mutex<State>,
    wake: tokio::sync::Notify,
}

/// The keeper of one account (a handle; the loop runs until `stop`).
#[derive(Clone)]
pub(crate) struct SessionKeeper {
    shared: Arc<Shared>,
}

impl SessionKeeper {
    pub(crate) fn start(
        gateway: &str,
        tenant: &str,
        credential: Credential,
        sink: Arc<dyn KeeperSink>,
        timing: Timing,
    ) -> Self {
        let shared = Arc::new(Shared {
            gateway: crate::settings::normalize_gateway_url(gateway),
            timing,
            sink,
            state: Mutex::new(State {
                handed: credential,
                family: None,
                spent: None,
                session: None,
                session_tenant: String::new(),
                tenant: tenant.trim().to_string(),
                switch_refused: None,
                renew_at: None,
                retry_at: None,
                stopped: false,
            }),
            wake: tokio::sync::Notify::new(),
        });
        let keeper = Self { shared };
        let run = keeper.clone();
        tauri::async_runtime::spawn(async move { run.run().await });
        keeper
    }

    /// The vault's current credential (a snapshot of it arrived). The keeper
    /// keeps using its own family; the new one matters only when it has none
    /// (it was refused, or never forked).
    pub(crate) fn hand(&self, credential: Credential) {
        let changed = {
            let mut st = self.shared.state.lock();
            let changed = st.handed != credential;
            st.handed = credential;
            changed
        };
        if changed {
            self.shared.wake.notify_one();
        }
    }

    /// The tenant the vault wants the account in.
    pub(crate) fn rescope(&self, tenant: &str) {
        let tenant = tenant.trim().to_string();
        let changed = {
            let mut st = self.shared.state.lock();
            let changed = st.tenant != tenant;
            st.tenant = tenant;
            changed
        };
        if changed {
            self.shared.wake.notify_one();
        }
    }

    pub(crate) fn status(&self) -> KeeperStatus {
        let st = self.shared.state.lock();
        match (&st.session, mint_step(&st)) {
            (Some(_), _) => KeeperStatus::Active,
            (None, Step::Wait(None)) => KeeperStatus::SignedOut,
            (None, _) => KeeperStatus::Retrying,
        }
    }

    /// End the loop and the account's family: the end of a sign-in is the end
    /// of everything it opened (the bridge's fork goes with it). Best effort,
    /// bounded; a failed revoke leaves the family to expire.
    pub(crate) async fn stop(&self) {
        let family = {
            let mut st = self.shared.state.lock();
            st.stopped = true;
            st.session = None;
            st.family.take()
        };
        self.shared.wake.notify_one();
        let Some(family) = family else { return };
        let body = json!({ "refreshToken": family });
        let revoke = post_json(
            &self.shared.gateway,
            "/v1/auth/logout",
            None,
            &body,
            REVOKE_TIMEOUT,
        );
        if let Err(e) = revoke.await {
            eprintln!("[keeper] revoking the account's family failed: {e}");
        }
    }

    async fn run(&self) {
        let timing = self.shared.timing;
        let mut backoff = Backoff::new(timing.retry_min, timing.retry_max, Duration::MAX);
        let mut announced_out = false;
        loop {
            let now = Instant::now();
            let step = {
                let st = self.shared.state.lock();
                if st.stopped {
                    return;
                }
                next_step(&st, now)
            };
            if let Step::Wait(until) = step {
                match until {
                    Some(at) => {
                        let _ = tokio::time::timeout(
                            at.saturating_duration_since(now),
                            self.shared.wake.notified(),
                        )
                        .await;
                    }
                    None => self.shared.wake.notified().await,
                }
                continue;
            }
            let outcome = self.execute(&step).await;
            let (fx, signed_out_now) = {
                let mut st = self.shared.state.lock();
                if st.stopped {
                    return;
                }
                let fx = apply(
                    &mut st,
                    &step,
                    outcome,
                    Instant::now(),
                    unix_now(),
                    &timing,
                    &mut backoff,
                );
                (
                    fx,
                    matches!(mint_step(&st), Step::Wait(None)) && st.session.is_none(),
                )
            };
            if let Some(family) = fx.family {
                self.shared.sink.family(family);
            }
            if let Some(token) = fx.session {
                announced_out = false;
                self.shared.sink.session(token);
            }
            if fx.signed_out && signed_out_now && !announced_out {
                announced_out = true;
                eprintln!("[keeper] the account's credential was refused; waiting for a new one");
                self.shared.sink.signed_out();
            }
        }
    }

    async fn execute(&self, step: &Step) -> Result<Grant, GatewayError> {
        let (path, bearer, body) = match step {
            Step::Fork(token) => (
                "/v1/auth/refresh/fork",
                None,
                json!({ "refreshToken": token, "client": CLIENT }),
            ),
            Step::Renew(family) => ("/v1/auth/refresh", None, json!({ "refreshToken": family })),
            Step::Exchange(token) => ("/v1/auth/api-token", None, json!({ "token": token })),
            Step::Switch(tenant) => (
                "/v1/auth/switch-tenant",
                self.shared.state.lock().session.clone(),
                json!({ "tenantId": tenant }),
            ),
            Step::Wait(_) => return Err(GatewayError::SessionExpired),
        };
        post_grant(&self.shared.gateway, path, bearer.as_deref(), &body).await
    }
}

/// One of the gateway's session calls, its answer parsed as a grant.
async fn post_grant(
    gateway: &str,
    path: &str,
    bearer: Option<&str>,
    body: &serde_json::Value,
) -> Result<Grant, GatewayError> {
    let raw = post_json(gateway, path, bearer, body, API_TIMEOUT).await?;
    serde_json::from_slice::<Grant>(&raw)
        .ok()
        .filter(|grant| !grant.session_token.is_empty())
        .ok_or_else(|| GatewayError::Failed {
            status: None,
            reason: "the gateway's session answer was unreadable".into(),
        })
}

#[cfg(test)]
#[path = "keeper_tests.rs"]
mod keeper_tests;
