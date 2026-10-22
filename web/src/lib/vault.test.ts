import {
  MAX_ACCOUNTS,
  accountKind,
  accountKey,
  find,
  keyOf,
  markSignedOut,
  mostRecent,
  parseRev,
  parseVault,
  put,
  remove,
  serializeVault,
  touch,
  withRecord,
  withView,
} from "./vault.ts";
import type { SessionRecord } from "./sessionGrant.ts";

const rec = (subject: string, over: Partial<SessionRecord> = {}): SessionRecord => ({
  token: `t-${subject}`,
  refreshToken: `r-${subject}`,
  apiToken: null,
  subject,
  tenant: "org-a",
  role: "member",
  ...over,
});
const ok = (cond: boolean, msg: string) => {
  if (!cond) throw new Error(msg);
};

// The kind comes from the credential: an API token is a test account.
ok(accountKind(rec("maya")) === "member", "a refresh token is a person who signed in");
ok(accountKind(rec("svc:qa", { refreshToken: null, apiToken: "bnbloc_at_x" })) === "test", "an API token is a test account");

// Adding puts the account first and used now; the same gateway and subject
// replaces it (its last view stays); the same subject elsewhere is another.
let list = put([], "https://a", rec("maya"), 10);
list = put(list, "https://a", rec("jack"), 20);
list = put(list, "https://b", rec("maya"), 30);
list = withView(list, accountKey("https://a", "maya"), "#/room/r1");
list = put(list, "https://a", rec("maya", { token: "fresh" }), 40);
ok(list.length === 3, `3 accounts, got ${list.length}`);
ok(keyOf(list[0]!) === "https://a\nmaya" && list[0]!.record.token === "fresh" && list[0]!.lastUsed === 40, "re-added: first, fresh, used now");
ok(list[0]!.lastView === "#/room/r1", "the last view survives a sign-in again");
ok(find(list, "https://b\nmaya")?.record.subject === "maya" && find(list, "nope") === undefined && find(list, null) === undefined, "find by key");

// A record that cannot renew (no credential, no subject) is never held.
ok(put([], "g", rec("x", { refreshToken: null, apiToken: null }), 1).length === 0, "no credential: not held");
ok(put([], "g", rec("x", { subject: null }), 1).length === 0, "no subject: not held");

// Renewal, tenant switch and sign-in again keep the account where it is.
const key = accountKey("https://a", "jack");
let after = markSignedOut(list, key);
ok(find(after, key)!.signedOut && !find(list, key)!.signedOut, "marked signed out (a new list)");
ok(!find(withRecord(after, key, rec("jack", { token: "again" })), key)!.signedOut, "a renewal signs it in again");
ok(find(withRecord(list, key, rec("jack", { token: "n" })), key)!.record.token === "n", "a renewal replaces the record");
ok(find(withView(list, key, "#/plaza"), key)!.lastView === "#/plaza", "the view is kept");
ok(withView(list, key, "") === list || withView(list, key, "").length === list.length, "an unchanged view changes nothing");
ok(find(touch(list, key, 99), key)!.lastUsed === 99, "touch");
ok(remove(list, key).length === 2 && remove(list, "nope").length === 3, "remove one account");

// A new window binds to the account used last that can still sign in.
ok(keyOf(mostRecent(list)!) === "https://a\nmaya", "the latest used");
ok(keyOf(mostRecent(markSignedOut(list, "https://a\nmaya"))!) === "https://b\nmaya", "a signed-out account is not chosen");
ok(mostRecent([]) === undefined, "nobody");

// The list is bounded; the full vault drops a signed-out account first, then the
// least recently used, and never the account being added.
let many: ReturnType<typeof put> = [];
for (let i = 0; i < MAX_ACCOUNTS; i++) many = put(many, "g", rec(`u${i}`), 100 + i);
ok(many.length === MAX_ACCOUNTS, "full");
many = markSignedOut(many, accountKey("g", "u5"));
many = put(many, "g", rec("new"), 500);
ok(many.length === MAX_ACCOUNTS && !find(many, accountKey("g", "u5")) && !!find(many, accountKey("g", "u0")), "a signed-out account is dropped first");
many = put(many, "g", rec("newer"), 600);
ok(!find(many, accountKey("g", "u0")) && !!find(many, accountKey("g", "new")) && !!find(many, accountKey("g", "newer")), "then the least recently used");

// Storage round trip drops what cannot be read or renewed, and keeps what was kept.
const raw = serializeVault(list, 7);
const back = parseVault(raw);
ok(parseRev(raw) === 7, "the revision round trips");
ok(back.length === 3 && keyOf(back[0]!) === keyOf(list[0]!) && back[0]!.lastView === "#/room/r1", "round trip");
const junk = JSON.stringify({
  rev: 3,
  accounts: [
    { gateway: "g", record: rec("maya"), lastUsed: 5, signedOut: true },
    { gateway: "g", record: rec("maya") }, // duplicate
    { gateway: "g", record: { token: "x", refreshToken: null, apiToken: null, subject: "dead" } }, // no credential
    { gateway: 5, record: rec("odd") }, // bad gateway
    { gateway: "g", record: rec("plain") }, // no extras: defaults
    null,
    "junk",
  ],
});
const parsed = parseVault(junk);
ok(parsed.length === 2 && parsed[0]!.signedOut && parsed[0]!.lastUsed === 5, `parse kept ${parsed.length}`);
ok(parsed[1]!.lastUsed === 0 && parsed[1]!.lastView === "" && !parsed[1]!.signedOut, "defaults for the extras");
ok(parseVault("not json").length === 0 && parseVault(null).length === 0 && parseVault("{}").length === 0 && parseVault("[]").length === 0, "garbage is an empty vault");
ok(parseRev("not json") === 0 && parseRev(null) === 0 && parseRev(JSON.stringify({ rev: "x" })) === 0, "no revision is 0");
ok(serializeVault([], 0) === "", "a vault never written is no key");
// A vault emptied by a removal still counts the write: the shell must see it as newer.
ok(parseRev(serializeVault([], 5)) === 5, "an emptied vault keeps its revision");
