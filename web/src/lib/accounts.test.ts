import {
  MAX_PARKED_ACCOUNTS,
  accountKind,
  accountsOn,
  keyOf,
  park,
  parseAccounts,
  sessionSubjectChanged,
  unpark,
} from "./accounts.ts";
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

// Parking replaces the account of the same gateway and subject and puts it
// first; the same subject on another gateway is another account.
let list = park([], "https://a", rec("maya"));
list = park(list, "https://a", rec("jack"));
list = park(list, "https://b", rec("maya"));
list = park(list, "https://a", rec("maya", { token: "fresh" }));
ok(list.length === 3, `3 accounts, got ${list.length}`);
ok(keyOf(list[0]!) === "https://a\nmaya" && list[0]!.record.token === "fresh", "the re-parked account is first and fresh");
ok(accountsOn(list, "https://a").length === 2 && accountsOn(list, "https://b").length === 1, "accountsOn filters by gateway");
ok(unpark(list, "https://a", "maya").length === 2 && unpark(list, "https://a", "nobody").length === 3, "unpark removes one account");

// A record that cannot renew (no credential, no subject) is never parked.
ok(park([], "g", rec("x", { refreshToken: null, apiToken: null })).length === 0, "no credential: not parked");
ok(park([], "g", rec("x", { subject: null })).length === 0, "no subject: not parked");

// The list is bounded.
let many: ReturnType<typeof park> = [];
for (let i = 0; i < MAX_PARKED_ACCOUNTS + 4; i++) many = park(many, "g", rec(`u${i}`));
ok(many.length === MAX_PARKED_ACCOUNTS, "the list is bounded");

// Storage round trip drops what cannot be read or renewed.
const raw = JSON.stringify([
  { gateway: "g", record: rec("maya") },
  { gateway: "g", record: rec("maya") }, // duplicate
  { gateway: "g", record: { token: "x", refreshToken: null, apiToken: null, subject: "dead" } }, // no credential
  { gateway: 5, record: rec("odd") }, // bad gateway
  null,
  "junk",
]);
const back = parseAccounts(raw);
ok(back.length === 1 && back[0]!.record.subject === "maya", `parse kept ${back.length}`);
ok(parseAccounts("not json").length === 0 && parseAccounts(null).length === 0 && parseAccounts("{}").length === 0, "garbage is an empty list");

// Another window changed the session: reload only when it is another account.
const a = JSON.stringify(rec("maya"));
const aRenewed = JSON.stringify(rec("maya", { token: "new" }));
const b = JSON.stringify(rec("jack"));
ok(!sessionSubjectChanged(a, aRenewed), "a renewal of the same account reloads nothing");
ok(sessionSubjectChanged(a, b), "another account reloads");
ok(sessionSubjectChanged(a, null) && sessionSubjectChanged(null, a), "signing out or in elsewhere reloads");
ok(!sessionSubjectChanged(null, ""), "nothing to nothing is no change");
