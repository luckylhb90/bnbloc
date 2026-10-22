import {
  credentialMint,
  parseSessionRecord,
  parseSsoFragment,
  renewDelayMs,
  RENEW_AHEAD_MS,
  type Grant,
  type SessionRecord,
} from "./sessionGrant.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// token builds a bnbloc-style two-segment token: base64url(claims).sig
function token(claims: Record<string, unknown>): string {
  const b64 = btoa(JSON.stringify(claims)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${b64}.sig`;
}

// The credential a record mints from: the refresh token first, else the API
// token of a test account (kept on the grant); neither: nothing to mint.
async function testCredentialMint(): Promise<void> {
  const spent: string[] = [];
  const deps = {
    refresh: async (rt: string): Promise<Grant> => {
      spent.push(`refresh:${rt}`);
      return { sessionToken: "fresh", refreshToken: "rt-2", subject: "ann@corp" };
    },
    exchangeApiToken: async (t: string): Promise<Grant> => {
      spent.push(`api:${t}`);
      return { sessionToken: "fresh", subject: "svc:qa" };
    },
  };
  const person: SessionRecord = { token: "old", refreshToken: "rt-1", apiToken: null, subject: "ann@corp", tenant: "t2", role: "member" };
  const grant = await credentialMint(person, deps)!();
  assert(spent.join() === "refresh:rt-1" && grant.refreshToken === "rt-2", "a person's refresh token is spent");
  const tester: SessionRecord = { ...person, refreshToken: null, apiToken: "bnbloc_at_x", subject: "svc:qa" };
  const minted = await credentialMint(tester, deps)!();
  assert(spent.at(-1) === "api:bnbloc_at_x" && minted.apiToken === "bnbloc_at_x", "a test account exchanges its API token, which stays on the grant");
  const both = await credentialMint({ ...tester, refreshToken: "rt-1" }, deps)!();
  assert(spent.at(-1) === "refresh:rt-1" && both.apiToken === undefined, "a record holding both renews from the refresh token");
  assert(credentialMint({ ...person, refreshToken: null }, deps) === null && credentialMint(null, deps) === null, "no credential: nothing to mint");
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
  assert(rec?.refreshToken === "r" && rec.tenant === null && rec.role === null && rec.apiToken === null, "record round-trips");
  assert(parseSessionRecord(JSON.stringify({ token: "a", apiToken: "bnbloc_at_y", subject: "svc:q" }))?.apiToken === "bnbloc_at_y", "an API token round-trips");
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

await testCredentialMint();
testFragmentAdoption();
testSessionRecordParsing();
testRenewDelay();
