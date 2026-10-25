// A draft typed while a room is still opening survives the room's channel list
// arriving (the composer is fresh per channel, but the default channel resolving
// the first time is not another conversation: lib/composerKey.ts), and another
// channel still gets a fresh composer. The gateway is mocked in the browser.
//
// Run it like layout-regression.mjs (README): a web server on BNBLOC_E2E_URL
// (default http://localhost:5173), `playwright-core` from a scratch install.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const CHANNELS = [
  { channelId: "general", name: "general", isDefault: true, createdBy: "maya" },
  { channelId: "ops", name: "ops", isDefault: false, createdBy: "maya" },
];
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const p = new URL(route.request().url()).pathname;
  const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/rooms/r1/history") return json({ messages: [] });
  if (p === "/v1/rooms/r1/channels") { await new Promise((r) => setTimeout(r, 2500)); return json({ channels: CHANNELS }); }
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }] });
  if (p === "/v1/rooms/r1") return json({ roomId: "r1", name: "r1", description: "", visibility: "private", previewable: false, syncMode: "live", createdBy: "maya", createdAtMs: 1 });
  if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "owner", memberCount: 1, createdAtMs: 1 }] });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
await page.addInitScript(([t]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token]);
await page.goto(B + "/#/room/r1", { waitUntil: "load" });
const input = page.getByPlaceholder(/Message the room/);
await input.waitFor({ timeout: 15000 });
await input.fill("a draft typed while the room was still opening");
ok((await page.getByText("ops", { exact: true }).count()) === 0, "the channel list has not arrived yet");
await page.getByText("ops", { exact: true }).first().waitFor({ timeout: 10000 });
await page.waitForTimeout(800);
ok((await input.inputValue()) === "a draft typed while the room was still opening", "the draft is still there once the channels arrive");
await page.getByText("ops", { exact: true }).first().click();
await page.waitForTimeout(800);
ok((await page.getByPlaceholder(/Message the room/).inputValue()) === "", "another channel has its own, empty composer");
await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
