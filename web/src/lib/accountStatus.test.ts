import { describeStatus, gatewayOrigin, shellKey, statusKey } from "./accountStatus.ts";
import type { AccountStatus } from "./desktop.ts";

const ok = (cond: boolean, msg: string) => {
  if (!cond) throw new Error(msg);
};

const status = (over: Partial<AccountStatus> = {}): AccountStatus => ({
  gatewayUrl: "https://gw.example.com",
  subject: "maya",
  session: "active",
  canServe: true,
  serving: true,
  bridge: "running",
  runningTurns: 0,
  ...over,
});

ok(describeStatus(undefined) === "", "nothing known: nothing said");
ok(describeStatus(status()) === "agents online", "online");
ok(describeStatus(status({ runningTurns: 2 })) === "agents online · 2 running", "running turns");
ok(describeStatus(status({ bridge: "starting" })) === "agents starting…", "starting");
ok(describeStatus(status({ bridge: "failed" })) === "agents not starting", "failed");
ok(describeStatus(status({ bridge: "stopped" })) === "agents offline", "offline");
ok(describeStatus(status({ serving: false, bridge: "stopped" })) === "agents off on this computer", "the person turned it off");
ok(describeStatus(status({ canServe: false, serving: false, bridge: "stopped" })) === "", "a test account has no agents to talk about");
ok(describeStatus(status({ session: "signedOut" })) === "sign in again", "signed out outranks the bridge");
ok(describeStatus(status({ session: "retrying", bridge: "stopped" })) === "connecting…", "no session yet");

// The shell names a gateway by its origin; the vault keeps what the page used.
ok(gatewayOrigin("https://gw.example.com/") === "https://gw.example.com", "a trailing slash");
ok(gatewayOrigin("https://gw.example.com/app") === "https://gw.example.com", "a path");
ok(gatewayOrigin("") === "" && gatewayOrigin("not a url") === "not a url", "same-origin and junk stay");
ok(statusKey(status()) === shellKey("https://gw.example.com/", "maya"), "one key for one account");
ok(statusKey(status()) !== shellKey("https://other.example.com", "maya"), "another gateway is another account");
