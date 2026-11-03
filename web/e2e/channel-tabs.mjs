// A channel's own retention and pinned tabs (channels PRD C22, E1-E5),
// against a mocked room service that keeps the channel's settings: the
// settings dialog saves "Keep messages" and a pinned deliverable; the tab
// then shows in the channel's header and opens the deliverable's room link.
// Run it like long-message.mjs (README).
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const general = channel({ ownerId: "maya" });
const patches = [];
const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const p = new URL(req.url()).pathname;
  const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  if (p === "/v1/rooms/r1/channels/general" && req.method() === "PATCH") {
    const body = JSON.parse(req.postData() || "{}");
    patches.push(body);
    Object.assign(general, { settings: body.settings ?? general.settings });
    return json(general);
  }
  if (p === "/v1/rooms/r1/artifacts")
    return json({ artifacts: [{ artifactId: "app1", roomId: "r1", channelId: "general", kind: "site", title: "Release board", isApp: true, currentVersion: 1, createdBy: "maya", createdAtMs: 1, updatedAtMs: 1 }], maxBytes: 1 });
  if (p === "/v1/rooms/r1/marks") return json({ marks: [] });
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/rooms/r1/history") return json({ messages: [] });
  if (p === "/v1/rooms/r1/channels") return json({ channels: [general], sections: [], groups: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }] });
  if (p === "/v1/rooms/r1") return json(room());
  if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "owner", memberCount: 1, createdAtMs: 1 }] });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
await page.addInitScript(([t]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token]);
await page.goto(B + "/#/room/r1", { waitUntil: "load" });
await page.locator("[data-channel-header]").waitFor({ timeout: 10000 });

await page.getByRole("button", { name: /Channel options for #general/ }).first().click();
await page.getByRole("menuitem", { name: /Settings/ }).first().click();
await page.locator("[data-channel-retention]").waitFor({ timeout: 5000 });
await page.locator("[data-channel-retention]").fill("7");
await page.waitForFunction(() => document.querySelector("[data-channel-tab-add]")?.querySelectorAll("option").length === 2, null, { timeout: 5000 }).catch(() => {});
await page.locator("[data-channel-tab-add]").selectOption("app1");
ok(((await page.locator("[data-channel-tab-row='app1']").textContent()) || "").includes("Release board"), "the pinned deliverable is listed with its title");
await page.getByRole("button", { name: /^Save$/ }).click();
await page.waitForTimeout(500);
const saved = patches.at(-1)?.settings ?? {};
ok(saved.retentionDays === 7, `Keep messages is saved (${JSON.stringify(saved)})`);
ok(saved.tabs?.length === 1 && saved.tabs[0].artifactId === "app1" && saved.tabs[0].title === "Release board", "the tab is saved by the deliverable's id, with its title");

await page.reload({ waitUntil: "load" });
await page.locator("[data-channel-tab='app1']").waitFor({ timeout: 10000 }).catch(() => {});
const tab = page.locator("[data-channel-tab='app1']");
ok(((await tab.textContent()) || "").includes("Release board"), "the channel's header shows the tab");
ok(((await tab.getAttribute("href")) || "").includes("artifact=app1"), "the tab opens the deliverable's room link");

await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
