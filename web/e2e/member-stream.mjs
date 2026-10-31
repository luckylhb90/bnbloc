// The member stream (docs/prd/2026-10-30-replicas-without-owners.md §9) in
// a real browser: the tab holds ONE stream (GET /v1/my/events) and no room
// stream; it scopes it with the room it shows and the channel open there,
// never with a list of rooms (the server knows the member's rooms); another
// channel's activity moves its badge from the activity alone (no unread
// re-read); a room joined elsewhere (member_rooms) appears in the sidebar
// with its unread; opening another channel re-scopes it (POST
// /v1/my/events/scope) without reopening; a reconnect scopes the new
// stream with the channel then open. The gateway is mocked in the browser.
// Run it like channels.mjs (README); CHROME names a local Chrome.
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
// connection sends stream_opened; the first, once scoped to r1, another
// channel's activity, and it ends after a while, as a dropped stream does.
const open = new Map(); // streamId -> res
const streamServer = createServer((req, res) => {
  s.streams.push(req.url);
  const n = s.streams.length;
  const id = `ms_${n}`;
  open.set(id, res);
  res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
  res.write(sse("stream_opened", { roomId: "", payload: { streamId: id } }));
  // The server's own rooms of the member, their state on every connection.
  res.write(sse("unread_state", { roomId: "", payload: { unread: s.joinedR2 ? { r2: 2 } : {}, channels: {} } }));
  if (n === 1) setTimeout(() => res.end(), 4000);
});
const onScope = (body) => {
  s.scopes.push(body);
  const res = open.get(body.streamId);
  if (!res || body.streamId !== "ms_1" || body.open?.roomId !== "r1" || s.activitySent) return;
  s.activitySent = true;
  // The badge moves from the activity itself, before any unread read could
  // tell (the read would say 3 from now on, as the server would).
  setTimeout(() => (s.designUnread = 3) && s.readsAtActivity === undefined && (s.readsAtActivity = s.unreadReads ?? 0), 1000);
  setTimeout(() => res.write(sse("channel_activity", { channelId: "ch_design", payload: { channelId: "ch_design", lastSeq: 9, events: 3, messages: 3, mention: true } })), 1000);
};
// The stream must reach the page as it is written: a route the browser
// context intercepts is answered whole, so the stream is left to the
// network, where the dev server proxies /v1 to this port (vite.config.ts).
const STREAM_PORT = Number(process.env.STREAM_PORT || 7090);
await new Promise((r) => streamServer.listen(STREAM_PORT, "127.0.0.1", r));

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route((url) => url.pathname.startsWith("/v1/") && url.pathname !== "/v1/my/events", async (route) => {
  const req = route.request();
  const u = new URL(req.url());
  const p = u.pathname;
  const m = req.method();
  const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/my/events/scope" && m === "POST") {
    onScope(JSON.parse(req.postData() || "{}"));
    return route.fulfill({ status: 204 });
  }
  if (p.endsWith("/events")) {
    s.roomStreams = (s.roomStreams ?? 0) + 1;
    return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
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
  if (p === `/v1/users/${me}/rooms`) {
    s.roomListReads = (s.roomListReads ?? 0) + 1;
    const rooms = [{ roomId: "r1", name: "Team", description: "", visibility: "private", syncMode: "none", role: "owner", memberCount: 2, createdAtMs: 1 }];
    if (s.joinedR2) rooms.push({ roomId: "r2", name: "Launch", description: "", visibility: "private", syncMode: "none", role: "member", memberCount: 3, createdAtMs: 2 });
    return json({ rooms });
  }
  if (p === "/v1/assist") return json({ available: false, tasks: [] });
  if (["/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
if (process.env.DEBUG) page.on("console", (m) => console.log("console:", m.text()));
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
await page.addInitScript(([t, who]) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-1", apiToken: null, subject: who, tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })); }, [tok(me), me]);
await page.goto(B + "/#/room/r1", { waitUntil: "load" });
try {
  await page.getByRole("button", { name: /general/ }).first().waitFor({ timeout: 20000 });
  await page.waitForTimeout(800);
  const opened = s.scopes.find((b) => b.streamId === "ms_1" && b.open?.roomId);
  ok(s.streams.length === 1 && !s.roomStreams, `one member stream and no room stream: ${s.streams.length} member, ${s.roomStreams ?? 0} room`);
  ok(opened && opened.open.roomId === "r1" && JSON.stringify(opened.open.channels) === JSON.stringify(["ch_gen"]), `scoped to the room and its open channel: ${JSON.stringify(s.scopes)}`);
  ok(s.scopes.length > 0 && s.scopes.every((b) => !("rooms" in b)), `no scope names rooms (the server knows them): ${JSON.stringify(s.scopes)}`);
  // The activity moves #design's badge while #general stays open.
  const badge = page.locator("button", { hasText: "design" }).getByText("3");
  await badge.waitFor({ timeout: 8000 });
  ok((s.unreadReads ?? 0) === s.readsAtActivity, `another channel's activity moves its badge while one is open, without re-reading unread (${s.unreadReads} reads, ${s.readsAtActivity} before the activity)`);
  await page.screenshot({ path: `${shots}/member-stream-badge.png` });
  // Added to a room elsewhere: the stream says so with its unread, and the
  // sidebar reads its rooms again and shows it.
  const listReads = s.roomListReads ?? 0;
  s.joinedR2 = true;
  open.get("ms_1").write(sse("member_rooms", { roomId: "", payload: { joined: ["r2"] } }));
  open.get("ms_1").write(sse("unread_state", { roomId: "", payload: { unread: { r2: 2 }, channels: {} } }));
  const launch = page.getByRole("button", { name: /Launch/ }).first();
  await launch.waitFor({ timeout: 5000 }).catch(() => {});
  ok((s.roomListReads ?? 0) > listReads && (await launch.isVisible().catch(() => false)), `a room joined elsewhere appears in the sidebar (${listReads} → ${s.roomListReads} list reads)`);
  ok(await page.getByRole("button", { name: /Launch/ }).first().getByText("2").isVisible().catch(() => false), "with its unread from the stream's state");
  await page.screenshot({ path: `${shots}/member-stream-joined.png` });
  // Opening #design re-scopes the stream, no reopen.
  const before = s.streams.length;
  await page.getByRole("button", { name: /design/ }).first().click();
  await page.waitForTimeout(800);
  const last = s.scopes.at(-1);
  ok(last && last.streamId === "ms_1" && last.open && JSON.stringify(last.open.channels) === JSON.stringify(["ch_design"]), `opening a channel re-scopes the stream: ${JSON.stringify(last)}`);
  ok(s.streams.length === before, `no stream reopened by navigation (${before} → ${s.streams.length})`);
  // The first stream ends; the next is scoped with the channel now open.
  await page.waitForTimeout(5000);
  const again = s.scopes.find((b) => b.streamId === "ms_2" && b.open?.roomId);
  ok(s.streams.length >= 2 && again && JSON.stringify(again.open.channels) === JSON.stringify(["ch_design"]), `a reconnect scopes the new stream with the channel then open: ${JSON.stringify(again)}`);
} catch (e) {
  failures++;
  console.log("FAIL: threw", e.message);
  await page.screenshot({ path: `${shots}/fail-member-stream.png` });
}
ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
await browser.close();
streamServer.closeAllConnections();
streamServer.close();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
