import { intentHash, parseIntent, resolveBinding } from "./windowBinding.ts";
import { accountKey, markSignedOut, put, type VaultAccount } from "./vault.ts";
import type { SessionRecord } from "./sessionGrant.ts";

const rec = (subject: string): SessionRecord => ({
  token: `t-${subject}`,
  refreshToken: `r-${subject}`,
  apiToken: null,
  subject,
  tenant: null,
  role: null,
});
const ok = (cond: boolean, msg: string) => {
  if (!cond) throw new Error(msg);
};

let vault: VaultAccount[] = put([], "https://gw", rec("maya"), 10);
vault = put(vault, "https://gw", rec("jack"), 20);
const maya = accountKey("https://gw", "maya");
const jack = accountKey("https://gw", "jack");

// The intent rides the hash behind "!" and never reads as a route.
ok(intentHash({ kind: "add" }) === "#!add-account", "add");
const open = intentHash({ kind: "account", key: jack });
ok(open.startsWith("#!account=") && !open.includes("\n"), "the key is encoded");
const parsed = parseIntent(open);
ok(parsed.intent?.kind === "account" && parsed.intent.key === jack && parsed.rest === "", "the account intent round trips");
ok(parseIntent("#!add-account").intent?.kind === "add", "add parses");
ok(parseIntent("#/room/r1").intent === null && parseIntent("#/room/r1").rest === "#/room/r1", "a route is not an intent and is kept");
ok(parseIntent("#!account=%E0%A4%A").intent === null, "an undecodable key is no intent");
ok(parseIntent("#!account=").intent === null, "an empty key is no intent");

// A new window binds to the account used last.
ok(resolveBinding({ stored: null, intent: null, vault }).key === jack, "the latest used");
// A reload keeps the window's own account, whatever was used since.
ok(resolveBinding({ stored: maya, intent: null, vault }).key === maya, "the binding survives a reload");
// An intent wins over the stored binding when it names a usable account.
ok(resolveBinding({ stored: maya, intent: { kind: "account", key: jack }, vault }).key === jack, "open this account");
// "Add an account" is a window with nobody bound, whatever else is stored.
const adding = resolveBinding({ stored: maya, intent: { kind: "add" }, vault });
ok(adding.key === null && adding.adding, "sign in another account");
// Never an account the vault does not have or that cannot sign in.
ok(resolveBinding({ stored: "https://gw\nghost", intent: null, vault }).key === jack, "a ghost binding falls back to the latest");
ok(resolveBinding({ stored: null, intent: { kind: "account", key: "https://gw\nghost" }, vault }).key === jack, "a ghost intent too");
const out = markSignedOut(vault, jack);
ok(resolveBinding({ stored: jack, intent: null, vault: out }).key === maya, "a signed-out binding falls back to one that can sign in");
ok(resolveBinding({ stored: null, intent: { kind: "account", key: jack }, vault: out }).key === maya, "and so does its intent");
ok(resolveBinding({ stored: null, intent: null, vault: [] }).key === null, "an empty vault shows the sign-in");
ok(!resolveBinding({ stored: null, intent: null, vault }).adding, "only an intent adds");
