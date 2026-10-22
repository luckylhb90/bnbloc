import { renewAccount, type RenewIo } from "./renewal.ts";
import type { Grant, SessionRecord } from "./sessionGrant.ts";

const ok = (cond: boolean, msg: string) => {
  if (!cond) throw new Error(msg);
};

// A session token that expires at `exp` (seconds): two segments, the first the claims.
const token = (exp: number, tag = "a") =>
  `${Buffer.from(JSON.stringify({ sub: "maya", exp, tag })).toString("base64url")}.sig`;
const NOW = 1_000_000_000_000;
const sec = (ms: number) => Math.floor(ms / 1000);
const fresh = token(sec(NOW) + 3600);
const due = token(sec(NOW) + 60); // inside the 5 minute window: due

const rec = (t: string, over: Partial<SessionRecord> = {}): SessionRecord => ({
  token: t,
  refreshToken: "r1",
  apiToken: null,
  subject: "maya",
  tenant: "org-a",
  role: "member",
  ...over,
});

interface Fixture {
  io: RenewIo;
  vault: { record: SessionRecord | null; signedOut: boolean };
  minted: string[];
}

function fixture(record: SessionRecord | null, mint: (r: string) => Promise<Grant>): Fixture {
  const vault = { record, signedOut: false };
  const minted: string[] = [];
  const io: RenewIo = {
    read: () => vault.record,
    write: (r) => {
      vault.record = r;
      vault.signedOut = false;
    },
    markSignedOut: () => {
      vault.signedOut = true;
    },
    now: () => NOW,
    mintDeps: {
      refresh: (r) => {
        minted.push(r);
        return mint(r);
      },
      exchangeApiToken: (t) => {
        minted.push(t);
        return mint(t);
      },
    },
  };
  return { io, vault, minted };
}

const grant = (t: string, refreshToken = "r2"): Grant => ({ sessionToken: t, refreshToken, subject: "maya", tenant: "org-a" });
const refused = () => Promise.reject({ status: 401 });

// A refused bearer (a 401 the window holds) mints from the vault's credential,
// and the rotated record lands in the vault.
{
  const f = fixture(rec(due), async () => grant(fresh, "r2"));
  const r = await renewAccount(f.io, due);
  ok(r.outcome === "renewed" && f.minted.join() === "r1", "minted once from the record's refresh token");
  ok(f.vault.record?.token === fresh && f.vault.record.refreshToken === "r2", "the rotated record is kept");
}

// A sibling window already renewed it (the vault holds another bearer that is
// not due): adopted, nothing minted, so the refresh token is rotated once.
{
  const f = fixture(rec(fresh, { refreshToken: "r2" }), async () => grant("never"));
  const r = await renewAccount(f.io, due);
  ok(r.outcome === "fresh" && r.record.token === fresh && f.minted.length === 0, "a sibling's renewal is adopted");
}

// The vault's bearer is the refused one: it IS renewed (not adopted).
{
  const f = fixture(rec(fresh), async () => grant(token(sec(NOW) + 7200, "b")));
  const r = await renewAccount(f.io, fresh);
  ok(r.outcome === "renewed" && f.minted.length === 1, "a refused bearer is renewed even when it looks fresh");
}

// The keep-alive of an account no window shows renews only a due one.
{
  const idle = fixture(rec(fresh), async () => grant("never"));
  ok((await renewAccount(idle.io, null)).outcome === "fresh" && idle.minted.length === 0, "not due: left alone");
  const stale = fixture(rec(due), async () => grant(fresh));
  ok((await renewAccount(stale.io, null)).outcome === "renewed" && stale.minted.length === 1, "due: renewed");
  const opaque = fixture(rec("no-expiry-claims"), async () => grant(fresh));
  ok((await renewAccount(opaque.io, null)).outcome === "renewed", "a bearer with no expiry is renewed");
}

// A refusal marks the account signed out and keeps it; a network failure
// changes nothing and is retried later.
{
  const f = fixture(rec(due), refused);
  ok((await renewAccount(f.io, due)).outcome === "refused", "refused");
  ok(f.vault.signedOut && f.vault.record !== null, "kept, marked signed out");
  const down = fixture(rec(due), () => Promise.reject(new Error("offline")));
  ok((await renewAccount(down.io, due)).outcome === "failed" && !down.vault.signedOut, "a failure is not a sign-out");
  const forbidden = fixture(rec(due), () => Promise.reject({ status: 403 }));
  ok((await renewAccount(forbidden.io, due)).outcome === "refused" && forbidden.vault.signedOut, "403 too");
}

// A test account renews from its API token; no record or no credential is nothing to renew.
{
  const f = fixture(rec(due, { refreshToken: null, apiToken: "bnbloc_at_x" }), async () => grant(fresh, ""));
  const r = await renewAccount(f.io, due);
  ok(r.outcome === "renewed" && f.minted.join() === "bnbloc_at_x", "exchanged the API token");
  ok(f.vault.record?.apiToken === "bnbloc_at_x", "the API token stays on the record");
  ok((await renewAccount(fixture(null, refused).io, due)).outcome === "none", "no record");
  ok((await renewAccount(fixture(rec(due, { refreshToken: null }), refused).io, due)).outcome === "none", "no credential");
}
