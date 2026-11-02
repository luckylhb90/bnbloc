// Catch me up and translate (channels PRD §4), against a mocked gateway and
// assist: a channel with unread messages offers "Catch me up"; the AI is
// asked with the unread messages only and its answer is shown to the member;
// a message's Translate shows the translation under the original. With no
// assist available, neither control is offered. Run it like
// long-message.mjs (README).
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };
const usage = { model: "m", promptTokens: 1, completionTokens: 1, latencyMs: 1 };

async function run(assistOn) {
  const asked = {};
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  await ctx.route("**/v1/**", async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (p === "/v1/assist")
      return json(assistOn
        ? { available: true, tasks: [{ name: "catch-up", componentId: "c1", output: "json" }, { name: "translate", componentId: "c2", output: "text" }] }
        : { available: false, tasks: [] });
    if (p === "/v1/assist/catch-up") {
      asked.catchUp = JSON.parse(req.postData() || "{}");
      return json({ fields: { summary: "They agreed to ship on Friday.", decided: ["Ship 3.2 on Friday"], open: ["Who writes the notes"], forYou: ["You were asked to review"] }, usage });
    }
    if (p === "/v1/assist/translate") {
      asked.translate = JSON.parse(req.postData() || "{}");
      const body = `event: delta\ndata: ${JSON.stringify({ text: "我们周五发布吗？" })}\n\nevent: done\ndata: ${JSON.stringify({ usage })}\n\n`;
      return route.fulfill({ status: 200, contentType: "text/event-stream", body });
    }
    if (p === "/v1/rooms-unread") return json({ unread: { r1: 2 }, channels: { r1: [{ channelId: "general", count: 2 }] } });
    if (p === "/v1/rooms/r1/marks") return json({ marks: [] });
    if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
    if (p === "/v1/rooms/r1/history")
      return json({
        messages: [
          { sequence: 1, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: "an old message", clientMsgId: "c1" }, atUnixMs: Date.now() - 9000 },
          { sequence: 2, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: "ship on friday?", clientMsgId: "c2" }, atUnixMs: Date.now() - 3000 },
          { sequence: 3, kind: "chat_message", channelId: "general", payload: { userId: "erin", text: "yes, maya reviews", clientMsgId: "c3" }, atUnixMs: Date.now() - 2000 },
        ],
      });
    if (p === "/v1/rooms/r1/channels") return json({ channels: [channel()], sections: [], groups: [] });
    if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "member" }, { userId: "jack", role: "owner" }, { userId: "erin", role: "member" }] });
    if (p === "/v1/rooms/r1") return json(room({ createdBy: "jack", memberCount: 3 }));
    if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "member", memberCount: 3, createdAtMs: 1 }] });
    if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
    if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
    return json({}, 404);
  });
  const page = await ctx.newPage();
  await page.addInitScript(([t]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token]);
  await page.goto(B + "/#/room/r1", { waitUntil: "load" });
  await page.locator("[data-room-message-feed] [data-seq='3']").waitFor({ timeout: 10000 }).catch(() => {});
  return { ctx, page, asked };
}

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
{
  const { ctx, page, asked } = await run(true);
  await page.locator("[data-catch-up]").waitFor({ timeout: 5000 }).catch(() => {});
  ok(((await page.locator("[data-catch-up]").textContent()) || "").includes("(2)"), "Catch me up counts the unread messages");
  await page.locator("[data-catch-up]").click();
  await page.locator("[data-catch-up-answer]").waitFor({ timeout: 5000 }).catch(() => {});
  const answer = (await page.locator("[data-catch-up-answer]").textContent()) || "";
  ok(answer.includes("ship on Friday") && answer.includes("You were asked to review") && answer.includes("Who writes the notes"), "the answer shows what happened, what is for me and what is open");
  const text = asked.catchUp?.text || "";
  ok(text.includes("ship on friday?") && text.includes("yes, maya reviews") && !text.includes("an old message"), "the AI is asked with the unread messages only");

  const row = page.locator("[data-room-message-feed] [data-seq='2']");
  await row.hover();
  await row.locator("[data-translate]").click();
  await row.locator("[data-translation]").filter({ hasText: "周五" }).waitFor({ timeout: 5000 }).catch(() => {});
  ok(((await row.locator("[data-translation]").textContent()) || "").includes("我们周五发布吗"), "the translation shows under the message");
  ok(((await row.textContent()) || "").includes("ship on friday?"), "the original stays");
  ok((asked.translate?.text || "").includes("ship on friday?"), "translate is asked with the message");
  await ctx.close();
}
{
  const { ctx, page } = await run(false);
  await page.waitForTimeout(800);
  ok((await page.locator("[data-catch-up]").count()) === 0, "with no assist, no Catch me up");
  const row = page.locator("[data-room-message-feed] [data-seq='2']");
  await row.hover();
  ok((await row.locator("[data-translate]").count()) === 0, "with no assist, no Translate");
  await ctx.close();
}
await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
