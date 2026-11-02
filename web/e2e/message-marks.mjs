// Reactions, pins and saved messages (channels PRD §2.3), against a mocked
// gateway that keeps marks as realtime does: a reaction picked from a
// message's picker shows under it with its count, a second click takes it
// off; a channel manager pins a message and the pinned strip lists it; a
// save fills the bookmark; all of it is back after a reload.
//
// Run it like long-message.mjs (README): a web server on BNBLOC_E2E_URL.
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

let marks = [{ seq: 2, kind: "reaction", userId: "jack", value: "👍", by: "jack", createdAtMs: 1, channelId: "general" }];
const same = (a, b) => a.seq === b.seq && a.kind === b.kind && (a.userId || "") === (b.userId || "") && (a.value || "") === (b.value || "");
const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const p = new URL(req.url()).pathname;
  const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  const mark = /^\/v1\/rooms\/r1\/messages\/(\d+)\/marks$/.exec(p);
  if (mark) {
    const body = JSON.parse(req.postData() || "{}");
    const m = { seq: Number(mark[1]), kind: body.kind, ...(body.kind !== "pin" ? { userId: "maya" } : {}), ...(body.value ? { value: body.value } : {}), by: "maya", createdAtMs: Date.now(), channelId: "general" };
    if (body.kind === "pin" && req.method() === "PUT") return json({ error: "only those who manage the channel pin its messages" }, 403);
    marks = marks.filter((x) => !same(x, m));
    if (req.method() === "PUT") { marks.push(m); return json(m); }
    return route.fulfill({ status: 204 });
  }
  if (p === "/v1/rooms/r1/marks") return json({ marks });
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/rooms/r1/history")
    return json({
      messages: [
        { sequence: 1, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: "first message", clientMsgId: "c1" }, atUnixMs: Date.now() - 3000 },
        { sequence: 2, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: "ship on friday?", clientMsgId: "c2" }, atUnixMs: Date.now() - 2000 },
      ],
    });
  if (p === "/v1/rooms/r1/channels") return json({ channels: [channel({ ownerId: "jack" })], sections: [], groups: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "member" }, { userId: "jack", role: "owner" }] });
  if (p === "/v1/rooms/r1") return json(room({ createdBy: "jack", memberCount: 2 }));
  if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "member", memberCount: 2, createdAtMs: 1 }] });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
await page.addInitScript(([t]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token]);
const row = () => page.locator("[data-room-message-feed] [data-seq='2']");
const count = async (emoji) => (await row().locator(`[data-reaction="${emoji}"] [data-reaction-count]`).textContent().catch(() => null)) ?? null;

await page.goto(B + "/#/room/r1", { waitUntil: "load" });
await row().locator("[data-reaction='👍']").waitFor({ timeout: 10000 }).catch(() => {});
ok((await count("👍")) === "1", "another member's reaction shows with its count");

await row().hover();
await row().locator("[data-react]").click();
await page.locator("[data-reaction-choice='👍']").click();
await page.waitForTimeout(400);
ok((await count("👍")) === "2" && (await row().locator("[data-reaction='👍']").getAttribute("aria-pressed")) === "true", "picking 👍 adds mine");
await row().locator("[data-reaction='👍']").click();
await page.waitForTimeout(400);
ok((await count("👍")) === "1", "clicking it again takes mine off");

ok((await row().locator("[data-pin]").count()) === 0, "a member who does not manage the channel is offered no pin");
await row().hover();
await row().locator("[data-save]").click();
await page.waitForTimeout(400);
ok((await row().locator("[data-save]").getAttribute("aria-label")) === "Remove from saved", "saving fills the bookmark");
ok(marks.some((m) => m.kind === "saved" && m.seq === 2 && m.userId === "maya"), "the save reached the server");

marks.push({ seq: 1, kind: "pin", by: "jack", createdAtMs: 1, channelId: "general" });
await page.reload({ waitUntil: "load" });
await page.locator("[data-pinned-strip]").waitFor({ timeout: 10000 }).catch(() => {});
ok(((await page.locator("[data-pinned-strip]").textContent()) || "").includes("1 pinned"), "the pinned strip counts the channel's pins");
await page.locator("[data-pinned-strip]").click();
ok(((await page.locator("[data-pinned-row='1']").textContent()) || "").includes("first message"), "the pinned row shows its message");
ok((await row().locator("[data-save]").getAttribute("aria-label")) === "Remove from saved", "the save is back after a reload");

await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
