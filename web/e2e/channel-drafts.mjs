// A channel's composer text is kept on the server (channels PRD C6): typed,
// it is saved; after a reload (another device) it is back in the composer;
// another channel's composer starts empty; sending removes it. The gateway
// is mocked in the browser and keeps the drafts as the room service does.
//
// Run it like long-message.mjs (README): a web server on BNBLOC_E2E_URL.
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const drafts = new Map();
let seq = 0;
const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const p = new URL(req.url()).pathname;
  const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  const draft = /^\/v1\/rooms\/r1\/channels\/([^/]+)\/draft$/.exec(p);
  if (draft) {
    const ch = draft[1];
    if (req.method() === "GET") return json(drafts.get(ch) ?? { text: "", updatedAtMs: 0 });
    if (req.method() === "DELETE") { drafts.delete(ch); return route.fulfill({ status: 204 }); }
    const { text } = JSON.parse(req.postData() || "{}");
    if (!text.trim()) { drafts.delete(ch); return json({ text: "", updatedAtMs: 0 }); }
    drafts.set(ch, { text, updatedAtMs: Date.now() });
    return json(drafts.get(ch));
  }
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/rooms/r1/chat" && req.method() === "POST") return json({ sequence: ++seq });
  if (p === "/v1/rooms/r1/history") return json({ messages: [] });
  if (p === "/v1/rooms/r1/channels")
    return json({ channels: [channel(), channel({ channelId: "design", name: "design", isDefault: false, position: 1 })], sections: [], groups: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }] });
  if (p === "/v1/rooms/r1") return json(room());
  if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "owner", memberCount: 1, createdAtMs: 1 }] });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
await page.addInitScript(([t]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token]);
const composer = () => page.locator("textarea").first();

await page.goto(B + "/#/room/r1", { waitUntil: "load" });
await composer().waitFor({ timeout: 10000 });
await page.waitForTimeout(500);
await composer().fill("a half-written thought");
await page.waitForTimeout(1500);
ok(drafts.get("general")?.text === "a half-written thought", `typing saves the draft (${JSON.stringify(drafts.get("general"))})`);

await page.reload({ waitUntil: "load" });
await composer().waitFor({ timeout: 10000 });
await page.waitForFunction(() => document.querySelector("textarea")?.value === "a half-written thought", null, { timeout: 5000 }).catch(() => {});
ok((await composer().inputValue()) === "a half-written thought", "after a reload the draft is back in the composer");

await page.getByRole("button", { name: /^design$/ }).first().click().catch(() => {});
await page.waitForTimeout(800);
ok((await composer().inputValue()) === "", "another channel's composer starts empty");
await page.getByRole("button", { name: /^general$/ }).first().click().catch(() => {});
await page.waitForFunction(() => document.querySelector("textarea")?.value === "a half-written thought", null, { timeout: 5000 }).catch(() => {});
ok((await composer().inputValue()) === "a half-written thought", "back in the channel, its draft is there");

await page.getByRole("radio", { name: /Send to people/ }).or(page.getByRole("button", { name: /Send to people/ })).first().click();
await composer().press("Enter");
await page.waitForTimeout(1500);
ok(!drafts.has("general"), `sending removes the draft (${JSON.stringify(drafts.get("general"))})`);

await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
