use super::*;
use std::io::{Read, Write};
use std::sync::atomic::{AtomicUsize, Ordering};

fn st(handed: Credential) -> State {
    State {
        handed,
        family: None,
        spent: None,
        session: None,
        session_tenant: String::new(),
        tenant: String::new(),
        switch_refused: None,
        renew_at: None,
        retry_at: None,
        stopped: false,
    }
}

fn refresh(t: &str) -> Credential {
    Credential::Refresh(t.into())
}

fn api(t: &str) -> Credential {
    Credential::ApiToken(t.into())
}

fn grant(session: &str, family: &str, tenant: &str) -> Grant {
    Grant {
        session_token: session.into(),
        refresh_token: family.into(),
        tenant: tenant.into(),
    }
}

const UNIX_NOW: u64 = 1_700_000_000;

const FAST: Timing = Timing {
    renew_every: Duration::from_secs(40 * 60),
    retry_min: Duration::from_secs(5),
    retry_max: Duration::from_secs(300),
};

fn fold(
    state: &mut State,
    step: &Step,
    outcome: Result<Grant, GatewayError>,
    now: Instant,
) -> Effects {
    let mut backoff = Backoff::new(FAST.retry_min, FAST.retry_max, Duration::MAX);
    apply(state, step, outcome, now, UNIX_NOW, &FAST, &mut backoff)
}

fn refused() -> GatewayError {
    GatewayError::Refused {
        status: 403,
        reason: String::new(),
    }
}

/// A refresh credential is forked once; every renewal after is the keeper's
/// own family, never the handed token (the web rotates that one, and a
/// rotated-away token presented later ends the whole family).
#[test]
fn a_refresh_credential_is_forked_once_then_the_own_family_renews() {
    let now = Instant::now();
    let mut s = st(refresh("web-1"));
    assert_eq!(next_step(&s, now), Step::Fork("web-1".into()));
    let fx = fold(
        &mut s,
        &Step::Fork("web-1".into()),
        Ok(grant("sess-1", "fam-1", "org-a")),
        now,
    );
    assert_eq!(fx.session.as_deref(), Some("sess-1"));
    assert_eq!(fx.family, Some(Some("fam-1".into())));
    assert!(!fx.signed_out);
    // Not due: nothing to do until the renewal time.
    assert_eq!(next_step(&s, now), Step::Wait(Some(now + FAST.renew_every)));
    // Due: the keeper's family renews, never a fork from the handed token.
    let later = now + FAST.renew_every;
    assert_eq!(next_step(&s, later), Step::Renew("fam-1".into()));
    let fx = fold(
        &mut s,
        &Step::Renew("fam-1".into()),
        Ok(grant("sess-2", "fam-2", "org-a")),
        later,
    );
    assert_eq!(
        fx.family,
        Some(Some("fam-2".into())),
        "the rotated family reaches the bridge"
    );
    assert_eq!(
        next_step(&s, later + FAST.renew_every),
        Step::Renew("fam-2".into())
    );
    // The web rotating its token changes nothing the keeper does.
    s.handed = refresh("web-2");
    assert_eq!(
        next_step(&s, later + FAST.renew_every),
        Step::Renew("fam-2".into())
    );
}

/// The credential the keeper forked from is spent: when its own family is
/// gone the keeper waits for the vault to hand another, instead of
/// presenting a token the web has long rotated away.
#[test]
fn a_lost_family_waits_for_a_new_credential_not_the_spent_one() {
    let now = Instant::now();
    let mut s = st(refresh("web-1"));
    fold(
        &mut s,
        &Step::Fork("web-1".into()),
        Ok(grant("sess-1", "fam-1", "org-a")),
        now,
    );
    let due = now + FAST.renew_every;
    let fx = fold(&mut s, &Step::Renew("fam-1".into()), Err(refused()), due);
    assert!(fx.signed_out);
    assert_eq!(fx.family, Some(None));
    assert!(s.session.is_none() && s.family.is_none());
    assert_eq!(
        next_step(&s, due),
        Step::Wait(None),
        "the spent token is not used again"
    );
    // The vault hands a different one: forked at once.
    s.handed = refresh("web-9");
    assert_eq!(next_step(&s, due), Step::Fork("web-9".into()));
}

#[test]
fn a_refused_fork_or_exchange_waits_for_another_credential() {
    let now = Instant::now();
    for (cred, step) in [
        (refresh("bad"), Step::Fork("bad".into())),
        (api("svc-bad"), Step::Exchange("svc-bad".into())),
    ] {
        let mut s = st(cred);
        assert_eq!(next_step(&s, now), step);
        let fx = fold(&mut s, &step, Err(refused()), now);
        assert!(fx.signed_out && fx.session.is_none());
        assert_eq!(next_step(&s, now), Step::Wait(None));
    }
    // A signed-out gateway answer (401) is a refusal too.
    let mut s = st(refresh("old"));
    let fx = fold(
        &mut s,
        &Step::Fork("old".into()),
        Err(GatewayError::SessionExpired),
        now,
    );
    assert!(fx.signed_out);
}

/// A gateway that does not answer is retried with a growing delay, never
/// read as a sign-out, and the session the account has stays.
#[test]
fn an_unreachable_gateway_is_retried_not_a_sign_out() {
    let now = Instant::now();
    let mut s = st(refresh("web-1"));
    fold(
        &mut s,
        &Step::Fork("web-1".into()),
        Ok(grant("sess-1", "fam-1", "")),
        now,
    );
    let due = now + FAST.renew_every;
    let down = GatewayError::Failed {
        status: Some(503),
        reason: String::new(),
    };
    let fx = fold(&mut s, &Step::Renew("fam-1".into()), Err(down), due);
    assert_eq!(fx, Effects::default());
    assert_eq!(
        s.session.as_deref(),
        Some("sess-1"),
        "the live session stays"
    );
    assert_eq!(next_step(&s, due), Step::Wait(Some(due + FAST.retry_min)));
    assert_eq!(
        next_step(&s, due + FAST.retry_min),
        Step::Renew("fam-1".into()),
        "the same step again, not a fork"
    );
}

/// A test account exchanges its API token for each session and has no
/// family; the same token is used again, a refused one never.
#[test]
fn a_test_account_exchanges_its_api_token_again_and_again() {
    let now = Instant::now();
    let mut s = st(api("svc-1"));
    assert!(!s.handed.can_serve());
    let step = Step::Exchange("svc-1".into());
    assert_eq!(next_step(&s, now), step);
    let fx = fold(&mut s, &step, Ok(grant("sess-1", "", "org-a")), now);
    assert_eq!(fx.session.as_deref(), Some("sess-1"));
    assert_eq!(fx.family, None, "no family: nothing for a bridge");
    assert_eq!(
        next_step(&s, now + FAST.renew_every),
        step,
        "exchanged again, not spent"
    );
}

/// The vault's tenant is asked of the gateway once per value; a tenant the
/// account cannot act in keeps the session it has and is not retried.
#[test]
fn the_vault_tenant_is_switched_to_once() {
    let now = Instant::now();
    let mut s = st(refresh("web-1"));
    s.tenant = "org-b".into();
    fold(
        &mut s,
        &Step::Fork("web-1".into()),
        Ok(grant("sess-1", "fam-1", "org-a")),
        now,
    );
    assert_eq!(next_step(&s, now), Step::Switch("org-b".into()));
    let fx = fold(
        &mut s,
        &Step::Switch("org-b".into()),
        Ok(grant("sess-b", "", "org-b")),
        now,
    );
    assert_eq!(fx.session.as_deref(), Some("sess-b"));
    assert_eq!(s.session_tenant, "org-b");
    assert_eq!(next_step(&s, now), Step::Wait(Some(now + FAST.renew_every)));

    s.tenant = "org-c".into();
    assert_eq!(next_step(&s, now), Step::Switch("org-c".into()));
    let fx = fold(&mut s, &Step::Switch("org-c".into()), Err(refused()), now);
    assert_eq!(fx, Effects::default(), "a refused tenant is not a sign-out");
    assert_eq!(s.session.as_deref(), Some("sess-b"));
    assert_eq!(next_step(&s, now), Step::Wait(Some(now + FAST.renew_every)));
    s.tenant = "org-a".into();
    assert_eq!(next_step(&s, now), Step::Switch("org-a".into()));
    // No preference: never a switch.
    s.tenant.clear();
    assert_eq!(next_step(&s, now), Step::Wait(Some(now + FAST.renew_every)));
}

#[test]
fn credentials_never_print_their_token() {
    assert!(!format!("{:?}", refresh("super-secret")).contains("super-secret"));
    assert!(!format!("{:?}", api("svc-secret")).contains("svc-secret"));
    let parsed: Credential = serde_json::from_str(r#"{"kind":"apiToken","token":"t"}"#).unwrap();
    assert_eq!(parsed, api("t"));
    let parsed: Credential = serde_json::from_str(r#"{"kind":"refresh","token":"t"}"#).unwrap();
    assert_eq!(parsed, refresh("t"));
    assert!(serde_json::from_str::<Credential>(r#"{"kind":"password","token":"t"}"#).is_err());
}

// ------------------------------------------------------------ the real loop

type Seen = Arc<Mutex<Vec<(String, String, String)>>>;

/// A gateway on loopback: `answer(path, body, authorization)` is its reply.
fn gateway(
    answer: impl Fn(&str, &str, &str) -> (u16, String) + Send + Sync + 'static,
) -> (String, Seen) {
    let server = crate::internal_ext::loopback::bind_ephemeral().expect("bind");
    let base = crate::internal_ext::loopback::LoopbackHttp::of_addr(server.local_addr().unwrap());
    let seen: Seen = Arc::default();
    let log = Arc::clone(&seen);
    std::thread::spawn(move || {
        for stream in server.incoming() {
            let Ok(mut stream) = stream else { return };
            let mut raw = Vec::new();
            let mut buf = [0u8; 4096];
            let (head_end, length) = loop {
                let n = stream.read(&mut buf).unwrap_or(0);
                if n == 0 {
                    break (raw.len(), 0);
                }
                raw.extend_from_slice(&buf[..n]);
                if let Some(pos) = raw.windows(4).position(|w| w == b"\r\n\r\n") {
                    let head = String::from_utf8_lossy(&raw[..pos]).to_ascii_lowercase();
                    let length = head
                        .lines()
                        .find_map(|l| l.strip_prefix("content-length:"))
                        .and_then(|v| v.trim().parse::<usize>().ok())
                        .unwrap_or(0);
                    break (pos + 4, length);
                }
            };
            while raw.len() < head_end + length {
                let n = stream.read(&mut buf).unwrap_or(0);
                if n == 0 {
                    break;
                }
                raw.extend_from_slice(&buf[..n]);
            }
            let head = String::from_utf8_lossy(&raw[..head_end.min(raw.len())]).into_owned();
            let path = head
                .lines()
                .next()
                .and_then(|l| l.split_whitespace().nth(1))
                .unwrap_or("")
                .to_string();
            let auth = head
                .lines()
                .find_map(|l| {
                    let lower = l.to_ascii_lowercase();
                    lower
                        .starts_with("authorization:")
                        .then(|| l.split_once(':').map(|(_, v)| v.trim().to_string()))
                        .flatten()
                })
                .unwrap_or_default();
            let body = String::from_utf8_lossy(&raw[head_end.min(raw.len())..]).into_owned();
            let (status, reply) = answer(&path, &body, &auth);
            log.lock().push((path, body, auth));
            let _ = write!(
                stream,
                "HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{reply}",
                reply.len()
            );
        }
    });
    (base, seen)
}

#[derive(Default)]
struct Heard {
    sessions: Mutex<Vec<String>>,
    families: Mutex<Vec<Option<String>>>,
    signed_out: AtomicUsize,
}

impl KeeperSink for Heard {
    fn session(&self, token: String) {
        self.sessions.lock().push(token);
    }
    fn family(&self, token: Option<String>) {
        self.families.lock().push(token);
    }
    fn signed_out(&self) {
        self.signed_out.fetch_add(1, Ordering::SeqCst);
    }
}

const QUICK: Timing = Timing {
    renew_every: Duration::from_millis(150),
    retry_min: Duration::from_millis(20),
    retry_max: Duration::from_millis(80),
};

async fn until(what: &str, mut done: impl FnMut() -> bool) {
    for _ in 0..200 {
        if done() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(25)).await;
    }
    panic!("timed out waiting for {what}");
}

fn grant_json(session: &str, family: &str, tenant: &str) -> String {
    json!({"sessionToken": session, "refreshToken": family, "tenant": tenant}).to_string()
}

/// The whole life against a gateway: fork, rotate, re-scope, revoke. The
/// handed token is presented exactly once (the fork), however often the
/// vault hands the same or a rotated one, and the removal revokes the
/// keeper's own family.
#[tokio::test]
async fn the_keeper_forks_once_renews_its_own_family_and_revokes_it_at_the_end() {
    let renewals = Arc::new(AtomicUsize::new(0));
    let counter = Arc::clone(&renewals);
    let (base, seen) = gateway(move |path, body, _| match path {
        "/v1/auth/refresh/fork" => (200, grant_json("sess-0", "fam-0", "org-a")),
        "/v1/auth/refresh" => {
            let n = counter.fetch_add(1, Ordering::SeqCst) + 1;
            assert!(
                body.contains(&format!("fam-{}", n - 1)),
                "renews its own family: {body}"
            );
            (
                200,
                grant_json(&format!("sess-{n}"), &format!("fam-{n}"), "org-a"),
            )
        }
        "/v1/auth/switch-tenant" => (200, grant_json("sess-b", "", "org-b")),
        "/v1/auth/logout" => (204, String::new()),
        other => panic!("unexpected call {other}"),
    });
    let heard = Arc::new(Heard::default());
    let keeper = SessionKeeper::start(&base, "", refresh("web-1"), heard.clone(), QUICK);

    until("the fork", || heard.sessions.lock().first().is_some()).await;
    assert_eq!(heard.sessions.lock()[0], "sess-0");
    assert_eq!(heard.families.lock()[0].as_deref(), Some("fam-0"));
    until("two renewals", || renewals.load(Ordering::SeqCst) >= 2).await;
    keeper.hand(refresh("web-2"));
    keeper.hand(refresh("web-1"));
    keeper.rescope("org-b");
    until("the switch", || {
        heard.sessions.lock().iter().any(|s| s == "sess-b")
    })
    .await;
    assert_eq!(keeper.status(), KeeperStatus::Active);
    keeper.stop().await;
    let calls = seen.lock().clone();
    let forks: Vec<_> = calls
        .iter()
        .filter(|c| c.0 == "/v1/auth/refresh/fork")
        .collect();
    assert_eq!(
        forks.len(),
        1,
        "the handed token is presented once: {calls:?}"
    );
    assert!(forks[0].1.contains("web-1") && forks[0].1.contains("\"client\":\"desktop\""));
    assert!(
        !calls
            .iter()
            .any(|c| c.0 != "/v1/auth/refresh/fork" && c.1.contains("web-")),
        "the web's token is never used again"
    );
    let switch = calls
        .iter()
        .find(|c| c.0 == "/v1/auth/switch-tenant")
        .expect("switch");
    assert!(
        switch.2.starts_with("Bearer sess-"),
        "the switch is the account's own session: {switch:?}"
    );
    let logout = calls.last().expect("calls");
    assert_eq!(logout.0, "/v1/auth/logout");
    assert!(
        logout.1.contains("fam-"),
        "the keeper's own family is revoked: {logout:?}"
    );
    assert_eq!(heard.signed_out.load(Ordering::SeqCst), 0);
}

#[tokio::test]
async fn a_refused_credential_signs_out_once_and_a_new_one_signs_back_in() {
    let (base, _) = gateway(|path, body, _| match (path, body.contains("good")) {
        ("/v1/auth/refresh/fork", true) => (200, grant_json("sess-g", "fam-g", "org-a")),
        ("/v1/auth/refresh/fork", false) => (401, "{\"error\":\"refresh token rejected\"}".into()),
        other => panic!("unexpected call {other:?}"),
    });
    let heard = Arc::new(Heard::default());
    let keeper = SessionKeeper::start(&base, "", refresh("bad"), heard.clone(), QUICK);
    until("the sign-out", || {
        heard.signed_out.load(Ordering::SeqCst) == 1
    })
    .await;
    assert_eq!(keeper.status(), KeeperStatus::SignedOut);
    tokio::time::sleep(Duration::from_millis(300)).await;
    assert_eq!(
        heard.signed_out.load(Ordering::SeqCst),
        1,
        "told once, not on every tick"
    );
    assert!(heard.sessions.lock().is_empty());
    keeper.hand(refresh("good"));
    until("the session", || heard.sessions.lock().len() == 1).await;
    assert_eq!(keeper.status(), KeeperStatus::Active);
    keeper.stop().await;
}

#[tokio::test]
async fn a_test_account_has_a_session_and_nothing_to_revoke() {
    let (base, seen) = gateway(|path, body, _| match path {
        "/v1/auth/api-token" => {
            assert!(body.contains("svc-token"));
            (200, grant_json("sess-t", "", "org-a"))
        }
        other => panic!("unexpected call {other}"),
    });
    let heard = Arc::new(Heard::default());
    let keeper = SessionKeeper::start(&base, "org-a", api("svc-token"), heard.clone(), QUICK);
    until("two exchanges", || heard.sessions.lock().len() >= 2).await;
    keeper.stop().await;
    assert!(
        heard.families.lock().is_empty(),
        "an API token has no family for a bridge"
    );
    assert!(
        seen.lock().iter().all(|c| c.0 == "/v1/auth/api-token"),
        "stopping calls nothing: there is no family to log out"
    );
}

/// A session token that expires `left` seconds from `UNIX_NOW`.
fn token_expiring_in(left: u64) -> String {
    crate::internal_ext::member_dir::session_token(json!({"sub": "maya", "exp": UNIX_NOW + left}))
}

/// The gateway's session lifetime is configuration: a session that lasts
/// less than the longest wait is renewed ahead of its own expiry, a longer
/// one by the longest wait, never in a tight loop.
#[test]
fn a_session_is_renewed_ahead_of_its_own_expiry() {
    let t = FAST;
    let after = |left| renew_after(&t, &token_expiring_in(left), UNIX_NOW);
    assert_eq!(after(3600), t.renew_every, "an hour: the longest wait");
    assert_eq!(
        after(600),
        Duration::from_secs(300),
        "ten minutes: five before it ends"
    );
    assert_eq!(
        after(60),
        Duration::from_secs(30),
        "about to end: not sooner than the floor"
    );
    assert_eq!(
        after(0),
        Duration::from_secs(30),
        "already ended: the floor, not a busy loop"
    );
    assert_eq!(
        renew_after(&t, "sess-1", UNIX_NOW),
        t.renew_every,
        "no expiry named: the longest"
    );
    // The renewal time follows it.
    let now = Instant::now();
    let mut s = st(refresh("web-1"));
    let short = Grant {
        session_token: token_expiring_in(600),
        refresh_token: "fam-1".into(),
        tenant: String::new(),
    };
    fold(&mut s, &Step::Fork("web-1".into()), Ok(short), now);
    assert_eq!(s.renew_at, Some(now + Duration::from_secs(300)));
}

/// A refused API-token exchange while its last session still stands ends
/// that session too: the account is signed out, not shown as active until the
/// bearer expires.
#[test]
fn a_refused_exchange_ends_the_session_that_stood() {
    let now = Instant::now();
    let mut s = st(api("svc-1"));
    let step = Step::Exchange("svc-1".into());
    fold(&mut s, &step, Ok(grant("sess-1", "", "org-a")), now);
    assert!(s.session.is_some());
    let due = now + FAST.renew_every;
    let fx = fold(&mut s, &step, Err(refused()), due);
    assert!(fx.signed_out, "told once");
    assert!(
        s.session.is_none() && s.renew_at.is_none(),
        "no session stands"
    );
    assert_eq!(next_step(&s, due), Step::Wait(None));
}
