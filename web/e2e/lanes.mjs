// Delivery lanes (docs/prd/2026-10-28-channels-and-threads.md §2.4) in a
// real browser: the room view opens one lanes stream with the open channel;
// another channel's activity moves its badge while the first is open, from
// the activity alone (no unread re-read);
// opening another channel re-scopes the stream (POST .../events/scope) and
// a reconnect opens with the channel now open. The gateway is mocked in the
// browser. Run it like channels.mjs (README); CHROME names a local Chrome.
import { createServer } from "node:http";
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const tok = (sub) => b64({ sub, tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };
const shots = process.env.SHOTS || ".";
const me = "maya";

const s = {
  room: { roomId: "r1", name: "Team", description: "", visibility: "private", previewable: false, syncMode: "none", createdBy: "maya", createdAtMs: 1, memberCount: 2, channelCreation: "anyone", welcome: "", quickPrompts: [], rules: "", rulesVersion: 0, requireRulesAgreement: false, recommendedChannels: [] },
  channels: [
    { channelId: "ch_gen", name: "general", isDefault: true, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "maya", settings: {}, joined: true },
    { channelId: "ch_design", name: "design", isDefault: false, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 1, ownerId: "maya", settings: {}, joined: true },
  ],
  streams: [],
  scopes: [],
  designUnread: 0,
};
const sse = (kind, ev) => `event: ${kind}\ndata: ${JSON.stringify({ kind, roomId: "r1", ...ev })}\n\n`;

// A real event stream (a fulfilled route can only answer at once): each
// connection sends stream_opened, the first also another channel's activity,
// and the first ends after a while, as a dropped stream does.
const streamServer = createServer((req, res) => {
  const u = new URL(req.url, "http://x");
  s.streams.push(Object.fromEntries(u.searchParams));
  const n = s.streams.length;
  res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
  res.write(sse("stream_opened", { payload: { streamId: `st_${n}`, open: (u.searchParams.get("open") || "").split(",") } }));
  if (n === 1) {
    // The badge moves from the activity itself, before any unread read
    // could tell (the read would say 3 from now on, as the server would).
    setTimeout(() => (s.designUnread = 3) && s.readsAtActivity === undefined && (s.readsAtActivity = s.unreadReads ?? 0), 500);
    setTimeout(() => res.write(sse("channel_activity", { channelId: "ch_design", payload: { channelId: "ch_design", lastSeq: 9, events: 3, messages: 3, mention: true } })), 500);
    setTimeout(() => res.end(), 2000);
  }
});
await new Promise((r) => streamServer.listen(0, "127.0.0.1", r));
const streamURL = `http://127.0.0.1:${streamServer.address().port}`;

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const u = new URL(req.url());
  const p = u.pathname;
  const m = req.method();
  const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/rooms/r1/events") return route.continue({ url: streamURL + p + u.search });
  if (p === "/v1/rooms/r1/events/scope" && m === "POST") {
    s.scopes.push(JSON.parse(req.postData() || "{}"));
    return route.fulfill({ status: 204 });
  }
  if (p === "/v1/rooms-unread") s.unreadReads = (s.unreadReads ?? 0) + 1;
  if (p === "/v1/rooms-unread") return json({ unread: { r1: s.designUnread }, channels: { r1: [{ channelId: "ch_design", count: s.designUnread }] } });
  if (p === "/v1/rooms/r1/history") return json({ messages: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }, { userId: "jack", role: "member" }] });
  if (p === "/v1/rooms/r1/agents") return json({ agents: [] });
  if (p === "/v1/rooms/r1/shared-agents") return json([]);
  if (p === "/v1/rooms/r1/channels" && m === "GET") return json({ channels: s.channels, sections: [], groups: [] });
  if (p === "/v1/rooms/r1/channel-threads") return json({ threads: [] });
  if (p === "/v1/rooms/r1/agent-prefs" && m === "GET") return json({ found: false, channelId: "", ownerId: "", core: "", provider: "" });
  if (p === "/v1/rooms/r1/agent-prefs") return route.fulfill({ status: 204 });
  if (p === "/v1/rooms/r1/rules") return json({ rules: "", version: 0, required: false, agreedVersion: 0 });
  if (p === "/v1/rooms/r1") return json(s.room);
  if (p === `/v1/users/${me}/rooms`) return json({ rooms: [{ roomId: "r1", name: "Team", description: "", visibility: "private", syncMode: "none", role: "owner", memberCount: 2, createdAtMs: 1 }] });
  if (p === "/v1/assist") return json({ available: false, tasks: [] });
  if (["/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
await page.addInitScript(([t, who]) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-1", apiToken: null, subject: who, tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })); }, [tok(me), me]);
await page.goto(B + "/#/room/r1", { waitUntil: "load" });
try {
  await page.getByRole("button", { name: /general/ }).first().waitFor({ timeout: 20000 });
  await page.waitForTimeout(800);
  const first = s.streams[0] ?? {};
  ok(first.lanes === "1" && first.open === "ch_gen" && first.channelId === "ch_gen" && first.includePeople === "1" && !first.allChannels, `one lanes stream with the open channel: ${JSON.stringify(first)}`);
  // The activity moves #design's badge while #general stays open.
  const badge = page.locator("button", { hasText: "design" }).getByText("3");
  await badge.waitFor({ timeout: 8000 });
  ok((s.unreadReads ?? 0) === s.readsAtActivity, `another channel's activity moves its badge while one is open, without re-reading unread (${s.unreadReads} reads, ${s.readsAtActivity} before the activity)`);
  await page.screenshot({ path: `${shots}/lanes-badge.png` });
  // The stream reconnected (the server ended it); opening #design re-scopes it.
  await page.waitForTimeout(3500);
  const before = s.streams.length;
  await page.getByRole("button", { name: /design/ }).first().click();
  await page.waitForTimeout(800);
  const last = s.scopes.at(-1);
  ok(last && last.streamId && JSON.stringify(last.open) === JSON.stringify(["ch_design"]), `opening a channel re-scopes the stream: ${JSON.stringify(s.scopes)}`);
  ok(s.streams.length === before, `no stream reopened by navigation (${before} → ${s.streams.length})`);
  ok(s.streams.length >= 2 && s.streams[1].open === "ch_gen", "a reconnect opens with the channel then open");
} catch (e) {
  failures++;
  console.log("FAIL: threw", e.message);
  await page.screenshot({ path: `${shots}/fail-lanes.png` });
}
ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
await browser.close();
streamServer.closeAllConnections();
streamServer.close();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
