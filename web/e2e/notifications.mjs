// Notification levels, @here/@channel and quiet hours
// (docs/prd/2026-10-28-channels-and-threads.md §2.5, C4, C5) in a real
// browser: a channel's menu sets the member's level (the default named by
// what it means now) and the rail marks a muted channel; the composer offers
// @here and @channel; Settings has the channel-messages switch and quiet
// hours. The gateway is mocked in the browser. Run it like channels.mjs
// (README); CHROME names a local Chrome.
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
    { channelId: "ch_gen", name: "general", isDefault: true, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "maya", settings: {}, joined: true, notifyEffective: "all" },
    { channelId: "ch_design", name: "design", isDefault: false, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 1, ownerId: "maya", settings: {}, joined: true, notifyEffective: "mentions" },
  ],
  follows: [],
  prefs: { mentions: true, channelMessages: true, agentFinished: true, agentFailed: true, borrowRequests: true, contentShared: true, contentReview: true, appNotices: true, roomReports: true, quietRooms: [] },
  prefPuts: [],
};

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const u = new URL(req.url());
  const p = u.pathname;
  const m = req.method();
  const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
  const body = () => JSON.parse(req.postData() || "{}");
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (p === "/v1/rooms/r1/history") return json({ messages: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }, { userId: "jack", role: "member" }] });
  if (p === "/v1/rooms/r1/agents") return json({ agents: [] });
  if (p === "/v1/rooms/r1/shared-agents") return json([]);
  if (p === "/v1/rooms/r1/channels" && m === "GET") return json({ channels: s.channels, sections: [], groups: [] });
  if (p === "/v1/rooms/r1/channel-threads") return json({ threads: [] });
  const fm = p.match(/^\/v1\/rooms\/r1\/channels\/([^/]+)\/follow$/);
  if (fm && m === "PUT") {
    const b = body();
    s.follows.push({ channelId: fm[1], ...b });
    const c = s.channels.find((x) => x.channelId === fm[1]);
    if ("level" in b) {
      c.notifyLevel = b.level;
      c.notifyEffective = b.level || "mentions";
    }
    return json(c);
  }
  if (p === "/v1/my/notification-prefs") {
    if (m === "PUT") {
      s.prefs = body();
      s.prefPuts.push(s.prefs);
    }
    return json(s.prefs);
  }
  if (p === "/v1/my/notifiers") return json({ notifiers: [] });
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
  // The channel's menu: the default named by what it means now, then mute.
  await page.getByRole("button", { name: "Channel options for #general" }).click({ force: true });
  ok(await page.getByRole("menuitem", { name: "Default (all messages)" }).isVisible(), "the default level is named by its meaning (all, where I posted)");
  await page.getByRole("menuitem", { name: "Muted" }).click();
  await page.waitForTimeout(400);
  ok(s.follows.at(-1)?.channelId === "ch_gen" && s.follows.at(-1)?.level === "muted", `muting sets the level on the follow: ${JSON.stringify(s.follows.at(-1))}`);
  ok(await page.getByLabel("Muted").first().isVisible(), "the rail marks the muted channel");
  await page.screenshot({ path: `${shots}/notify-levels.png` });

  // The composer offers @here and @channel.
  await page.getByRole("radio", { name: /Send to people/ }).click().catch(() => {});
  const box = page.getByPlaceholder(/^Message/).first();
  await box.waitFor();
  await box.fill("@he");
  const here = page.getByRole("option", { name: /here: people active in this channel/ });
  await here.waitFor({ timeout: 3000 });
  await here.click();
  ok((await box.inputValue()).startsWith("@here "), `picking inserts @here: ${await box.inputValue()}`);

  // Settings: channel messages and quiet hours.
  await page.goto(B + "/#/settings", { waitUntil: "load" });
  const channelMessages = page.getByRole("switch", { name: "Channel messages" });
  await channelMessages.waitFor({ timeout: 10000 });
  ok(await channelMessages.isChecked(), "channel messages are on by default");
  await page.getByRole("switch", { name: "Quiet hours" }).check();
  await page.waitForTimeout(400);
  const q = s.prefPuts.at(-1)?.quietHours;
  ok(q?.start === "22:00" && q?.end === "08:00" && !!q?.tz && q?.allowMentions === true, `quiet hours turn on with the night in my zone: ${JSON.stringify(q)}`);
  await page.getByLabel("Quiet until").fill("07:30");
  await page.waitForTimeout(400);
  ok(s.prefPuts.at(-1)?.quietHours?.end === "07:30", "the window's end is saved");
  await page.screenshot({ path: `${shots}/notify-quiet-hours.png` });
  await page.getByRole("switch", { name: "Quiet hours" }).uncheck();
  await page.waitForTimeout(400);
  ok(!s.prefPuts.at(-1)?.quietHours, "turning quiet hours off clears them");
} catch (e) {
  failures++;
  console.log("FAIL: threw", e.message);
  await page.screenshot({ path: `${shots}/fail-notifications.png` });
}
ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
await browser.close();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
