import {
  UNREAD_FRESH_MS,
  accountsToAsk,
  anyUnread,
  isFresh,
  readUnread,
  unreadBadge,
  type UnreadReader,
} from "./accountUnread.ts";
import { put, markSignedOut, accountKey } from "./vault.ts";
import type { SessionRecord } from "./sessionGrant.ts";
import type { UnreadSummary } from "./wire.gen.ts";

const ok = (cond: boolean, msg: string) => {
  if (!cond) throw new Error(msg);
};
const rec = (subject: string): SessionRecord => ({ token: `t-${subject}`, refreshToken: `r-${subject}`, apiToken: null, subject, tenant: null, role: null });

// Who is asked: every account but this window's, none that cannot sign in.
let vault = put([], "g", rec("maya"), 1);
vault = put(vault, "g", rec("jack"), 2);
vault = put(vault, "g", rec("ada"), 3);
const maya = accountKey("g", "maya"), jack = accountKey("g", "jack"), ada = accountKey("g", "ada");
ok(accountsToAsk(vault, jack).sort().join() === [ada, maya].sort().join(), "the others are asked");
ok(accountsToAsk(markSignedOut(vault, ada), jack).join() === maya, "a signed-out account is not");
ok(accountsToAsk(put([], "g", rec("maya"), 1), maya).length === 0, "one account: nobody to ask");
ok(accountsToAsk(vault, null).length === 3, "a window showing nobody asks for all");

// The badge.
ok(unreadBadge(undefined) === "" && unreadBadge({ rooms: 0, more: false }) === "", "nothing: no badge");
ok(unreadBadge({ rooms: 3, more: false }) === "3", "a count");
ok(unreadBadge({ rooms: 100, more: true }) === "100+", "a floor");
ok(anyUnread(new Map([["a", { rooms: 0, more: false }], ["b", { rooms: 2, more: false }]])) && !anyUnread(new Map()), "any unread");
ok(isFresh(1000, 1000 + UNREAD_FRESH_MS - 1) && !isFresh(1000, 1000 + UNREAD_FRESH_MS), "fresh for a while");

// The read: renewed first when the bearer is about to end, renewed and retried
// once on a 401, and "unknown" (null) for anything else.
const NOW = 1_000_000_000_000;
const bearer = (secondsLeft: number) => `${Buffer.from(JSON.stringify({ exp: Math.floor(NOW / 1000) + secondsLeft })).toString("base64url")}.sig`;
interface Fx { io: UnreadReader; log: string[]; token: { v: string }; }
function fixture(opts: { token: string; answers: Array<UnreadSummary | { status: number } | Error>; renews?: boolean; present?: boolean }): Fx {
  const log: string[] = [];
  const token = { v: opts.token };
  const answers = [...opts.answers];
  const io: UnreadReader = {
    account: () => (opts.present === false ? null : { gateway: "https://gw", token: token.v }),
    summary: async (_g, t) => {
      log.push(`summary:${t === token.v ? "current" : "stale"}`);
      const a = answers.shift();
      if (a instanceof Error) throw a;
      if (a && "status" in a) throw a;
      return a as UnreadSummary;
    },
    renew: async () => {
      log.push("renew");
      if (opts.renews === false) return false;
      token.v = bearer(3600);
      return true;
    },
    now: () => NOW,
  };
  return { io, log, token };
}
const three: UnreadSummary = { rooms: 3, more: false };
{
  const f = fixture({ token: bearer(3600), answers: [three] });
  ok((await readUnread(f.io, "k"))?.rooms === 3 && f.log.join() === "summary:current", "a good bearer asks once");
}
{
  const f = fixture({ token: bearer(60), answers: [three] });
  ok((await readUnread(f.io, "k"))?.rooms === 3 && f.log.join() === "renew,summary:current", "a bearer about to end is renewed first");
}
{
  const f = fixture({ token: bearer(3600), answers: [{ status: 401 }, three] });
  ok((await readUnread(f.io, "k"))?.rooms === 3 && f.log.join() === "summary:current,renew,summary:current", "a 401 renews and asks once more");
}
{
  const f = fixture({ token: bearer(3600), answers: [{ status: 401 }, { status: 401 }] });
  ok((await readUnread(f.io, "k")) === null && f.log.filter((l) => l === "renew").length === 1, "a second 401 is unknown, renewed once");
  const refused = fixture({ token: bearer(3600), answers: [{ status: 401 }], renews: false });
  ok((await readUnread(refused.io, "k")) === null && refused.log.join() === "summary:current,renew", "a refused renewal is unknown");
  const down = fixture({ token: bearer(3600), answers: [new Error("offline")] });
  ok((await readUnread(down.io, "k")) === null && down.log.join() === "summary:current", "a network failure is unknown, not retried");
  const server = fixture({ token: bearer(3600), answers: [{ status: 503 }] });
  ok((await readUnread(server.io, "k")) === null && !server.log.includes("renew"), "a 5xx never renews");
  ok((await readUnread(fixture({ token: "x", answers: [], present: false }).io, "gone")) === null, "an account that left the vault");
  const dead = fixture({ token: bearer(0), answers: [three], renews: false });
  ok((await readUnread(dead.io, "k")) === null && dead.log.join() === "renew", "an expired bearer that cannot be renewed asks nothing");
}
