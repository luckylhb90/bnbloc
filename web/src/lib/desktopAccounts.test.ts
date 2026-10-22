import { NOT_BOUND, isNotBound, snapshotOf, vaultAccount } from "./desktopAccounts.ts";
import type { SessionRecord } from "./sessionGrant.ts";

const rec = (subject: string | null, over: Partial<SessionRecord> = {}): SessionRecord => ({
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

// A person hands the shell the refresh token to fork; a test account its API
// token, whichever else the record carries.
const person = vaultAccount({ gateway: "https://gw.example.com/", record: rec("maya") });
ok(person?.gatewayUrl === "https://gw.example.com", "the gateway is an origin without a trailing slash");
ok(person?.credential.kind === "refresh" && person.credential.token === "r-maya", "a person's refresh token");
ok(person?.tenant === "org-a", "the tenant goes with it");
const tester = vaultAccount({
  gateway: "https://gw.example.com",
  record: rec("svc:qa", { refreshToken: "r-x", apiToken: "bnbloc_at_x" }),
});
ok(tester?.credential.kind === "apiToken" && tester.credential.token === "bnbloc_at_x", "an API token wins");

// Half accounts are never handed over.
ok(vaultAccount({ gateway: "https://gw", record: rec(null) }) === null, "no subject");
ok(vaultAccount({ gateway: " ", record: rec("maya") }) === null, "no gateway");
ok(vaultAccount({ gateway: "https://gw", record: rec("maya", { refreshToken: null }) }) === null, "no credential");

// The list holds each account once, in order.
const list = snapshotOf([
  { gateway: "https://a", record: rec("maya") },
  { gateway: "https://a", record: rec("jack") },
  { gateway: "https://b", record: rec("maya") },
  { gateway: "https://a", record: rec("maya", { refreshToken: "later" }) },
  { gateway: "https://a", record: rec(null) },
]);
ok(list.length === 3, `3 accounts, got ${list.length}`);
ok(list[0]!.credential.token === "r-maya", "the first of an account wins");
ok(snapshotOf([]).length === 0, "an empty vault is an empty list (the shell stops everyone)");

// The shell's refusal for a window that shows no account is recognized.
ok(isNotBound(NOT_BOUND) && isNotBound(new Error(`x: ${NOT_BOUND}`)), "the unbound answer");
ok(!isNotBound("a gateway outage"), "other errors are not it");
