import {
  parseSessionRecord,
  parseSsoFragment,
  renewDelayMs,
  renewSession,
  RENEW_AHEAD_MS,
  recordFromGrant,
  type Grant,
  type RenewDeps,
  type SessionRecord,
} from "./sessionGrant.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

class HttpError extends Error {
  status: number;
  constructor(status: number) {
    super(`http ${status}`);
    this.status = status;
  }
}

// token builds a bnbloc-style two-segment token: base64url(claims).sig
function token(claims: Record<string, unknown>): string {
  const b64 = btoa(JSON.stringify(claims)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${b64}.sig`;
}

const stored: SessionRecord = { token: "old", refreshToken: "rt-1", subject: "ann@corp", tenant: "t2", role: "member" };

// A harness wiring renewSession the way auth.tsx does: the minted grant is
// installed and persisted as the gateway answered it.
function harness(over: Partial<RenewDeps>) {
  const state = { token: "old" as string | null, persisted: null as SessionRecord | null, loggedOut: false, refreshedWith: [] as string[] };
  const deps: RenewDeps = {
    stored,
    subject: "ann@corp",
    directLoginAvailable: false,
    refresh: async (rt) => {
      state.refreshedWith.push(rt);
      // The gateway refreshes into the tenant the member switched to (the
      // refresh family's tenant hint): t2.
      return { sessionToken: "fresh", refreshToken: "rt-2", subject: "ann@corp", tenant: "t2", role: "member" };
    },
    login: async () => {
      throw new Error("direct login must not run");
    },
    establish: async (mint) => {
      const record = recordFromGrant(await mint());
      state.token = record.token;
      state.persisted = record;
    },
    logout: () => {
      state.loggedOut = true;
      state.token = null;
    },
    ...over,
  };
  return { state, deps };
}

async function testRefreshSuccessRotates(): Promise<void> {
  const { state, deps } = harness({});
  assert((await renewSession(deps)) === true, "a refresh renews the session");
  assert(state.refreshedWith.join() === "rt-1", "the stored refresh token is spent");
  assert(state.token === "fresh", "the refreshed bearer is installed as minted (no client re-switch)");
  assert(state.persisted?.refreshToken === "rt-2", "the ROTATED refresh token is stored");
  assert(state.persisted?.tenant === "t2", "the record is in the tenant the gateway minted it for");
  assert(!state.loggedOut, "no sign-out on success");
}

async function testRefreshRefusedSignsOut(): Promise<void> {
  for (const status of [401, 403]) {
    const { state, deps } = harness({
      refresh: async () => {
        throw new HttpError(status);
      },
    });
    assert((await renewSession(deps)) === false, `refresh ${status} fails`);
    assert(state.loggedOut, `refresh ${status} signs out`);
  }
}

async function testRefreshNetworkErrorKeepsSession(): Promise<void> {
  const { state, deps } = harness({
    refresh: async () => {
      throw new TypeError("Failed to fetch");
    },
  });
  assert((await renewSession(deps)) === false, "network failure reports no renewal");
  assert(!state.loggedOut && state.token === "old", "network failure keeps the session");
}

async function testNoRefreshTokenFallsBack(): Promise<void> {
  const noRt = { ...stored, refreshToken: null };
  const sso = harness({ stored: noRt });
  assert((await renewSession(sso.deps)) === false && sso.state.loggedOut, "SSO-only without refresh token signs out");

  let loggedInAs = "";
  const dev = harness({
    stored: noRt,
    directLoginAvailable: true,
    login: async (sub): Promise<Grant> => {
      loggedInAs = sub;
      return { sessionToken: "dev", refreshToken: "rt-dev", subject: sub, tenant: "t2" };
    },
  });
  assert((await renewSession(dev.deps)) === true, "dev gateway re-logs-in");
  assert(loggedInAs === "ann@corp" && dev.state.persisted?.refreshToken === "rt-dev", "direct login adopts its refresh token");
  assert(dev.state.refreshedWith.length === 0, "no refresh without a refresh token");
}

function testFragmentAdoption(): void {
  const t = token({ sub: "ann@corp", tenant: "t1", role: "super_admin", exp: 1 });
  const f = parseSsoFragment(`#sessionToken=${t}&refreshToken=rt-9&subject=ann%40corp`);
  assert(f.ssoCode === null, "no code in the browser flow");
  assert(f.grant?.sessionToken === t && f.grant.refreshToken === "rt-9", "fragment carries both tokens");
  assert(f.grant?.subject === "ann@corp" && f.grant.tenant === "t1" && f.grant.role === "super_admin", "claims restore scope");

  const legacy = parseSsoFragment(`#sessionToken=${t}`);
  assert(legacy.grant?.refreshToken === undefined && legacy.grant?.subject === "ann@corp", "subject falls back to claims");

  const code = parseSsoFragment("#ssoCode=abc");
  assert(code.ssoCode === "abc" && code.grant === null, "desktop flow yields only the code");
  assert(parseSsoFragment("").grant === null && parseSsoFragment("#/rooms/x").grant === null, "route hashes are not grants");
}

function testSessionRecordParsing(): void {
  const rec = parseSessionRecord(JSON.stringify({ token: "a", refreshToken: "r", subject: "s", tenant: null }));
  assert(rec?.refreshToken === "r" && rec.tenant === null && rec.role === null, "record round-trips");
  assert(parseSessionRecord(JSON.stringify({ token: "a" }))?.refreshToken === null, "pre-refresh record has none");
  assert(parseSessionRecord("{bad") === null && parseSessionRecord(null) === null, "corrupt/missing → null");
  assert(parseSessionRecord(JSON.stringify({ refreshToken: "r" })) === null, "no bearer → no session");
}

function testRenewDelay(): void {
  const now = 1_000_000_000_000;
  const exp = Math.floor(now / 1000) + 3600;
  assert(renewDelayMs(token({ exp }), now) === 3600_000 - RENEW_AHEAD_MS, "renews ahead of expiry");
  assert(renewDelayMs(token({ exp: 1 }), now) === 0, "an expired bearer renews now");
  assert(renewDelayMs(token({ sub: "x" }), now) === null && renewDelayMs("junk", now) === null, "no exp → no timer");
}

await testRefreshSuccessRotates();
await testRefreshRefusedSignsOut();
await testRefreshNetworkErrorKeepsSession();
await testNoRefreshTokenFallsBack();
testFragmentAdoption();
testSessionRecordParsing();
testRenewDelay();
