// Forwarding (channels PRD §2.3), against a mocked gateway: a message's
// Forward opens a dialog that sends the source by reference (room, seq) with
// the member's note; a forward in the feed shows its source as the source
// room answers it; a source the reader may not open (404) shows that, never
// its text. Run it like long-message.mjs (README).
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const posts = [];
const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const p = new URL(req.url()).pathname;
  const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  if (p === "/v1/rooms/r1/messages/1") return json({ sequence: 1, channelId: "general", kind: "chat_message", payload: { userId: "jack", text: "the original words" }, atUnixMs: 1 });
  if (p === "/v1/rooms/r1/messages/7") return json({ error: "message not found" }, 404);
  if (p === "/v1/rooms/r1/chat" && req.method() === "POST") { posts.push(JSON.parse(req.postData() || "{}")); return json({ sequence: 99 }); }
  if (p === "/v1/rooms/r1/marks") return json({ marks: [] });
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/rooms/r1/history")
    return json({
      messages: [
        { sequence: 1, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: "the original words", clientMsgId: "c1" }, atUnixMs: Date.now() - 4000 },
        { sequence: 2, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: "", clientMsgId: "c2", forwardOf: { roomId: "r1", seq: 1, channelId: "general" } }, atUnixMs: Date.now() - 3000 },
        { sequence: 3, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: "", clientMsgId: "c3", forwardOf: { roomId: "r1", seq: 7, channelId: "secret" } }, atUnixMs: Date.now() - 2000 },
      ],
    });
  if (p === "/v1/rooms/r1/channels") return json({ channels: [channel(), channel({ channelId: "design", name: "design", isDefault: false, position: 1 })], sections: [], groups: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "member" }, { userId: "jack", role: "owner" }] });
  if (p === "/v1/rooms/r1") return json(room({ createdBy: "jack", memberCount: 2 }));
  if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "Team", description: "", visibility: "private", syncMode: "live", role: "member", memberCount: 2, createdAtMs: 1 }] });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
await page.addInitScript(([t]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token]);
const row = (seq) => page.locator(`[data-room-message-feed] [data-seq='${seq}']`);

await page.goto(B + "/#/room/r1", { waitUntil: "load" });
await row(2).locator("[data-forward-text]").filter({ hasText: "original" }).waitFor({ timeout: 10000 }).catch(() => {});
ok(((await row(2).locator("[data-forward-text]").textContent()) || "").includes("the original words"), "a forward shows its source's words, read from the source room");
await row(3).locator("[data-forward-text]").filter({ hasText: "cannot" }).waitFor({ timeout: 5000 }).catch(() => {});
const hidden = (await row(3).locator("[data-forward-card]").textContent()) || "";
ok(hidden.includes("cannot open") && !hidden.includes("secret"), `a source the reader may not open is said so, never shown (${hidden})`);

await row(1).hover();
await row(1).locator("[data-forward]").click();
await page.locator("[data-forward-channel]").waitFor({ timeout: 5000 });
await page.waitForFunction(() => document.querySelector("[data-forward-channel]")?.querySelectorAll("option").length === 2, null, { timeout: 5000 }).catch(() => {});
await page.locator("[data-forward-channel]").selectOption("design");
await page.locator("[data-forward-note]").fill("worth a look");
await page.locator("[data-forward-send]").click();
await page.waitForTimeout(500);
const sent = posts[0] ?? {};
ok(sent.channelId === "design" && sent.text === "worth a look" && sent.forwardOf?.roomId === "r1" && sent.forwardOf?.seq === 1 && !String(sent.text).includes("original"), `the forward names its source and carries only the note (${JSON.stringify(sent)})`);

await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
