// People's messages render Markdown (channels PRD C7) with the agents'
// renderer: a member's own bubble and another member's row both show bold,
// code and links, and an image is never fetched (it shows as a link). Every
// persisted message offers "Copy link", which copies the room's deep link to
// that message. The gateway is mocked in the browser.
//
// Run it like long-message.mjs (README): a web server on BNBLOC_E2E_URL.
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
const md = "**bold words** and `inline code`\n\n- one\n- two\n\n![tracker](https://tracker.example.com/pixel.png)";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, permissions: ["clipboard-read", "clipboard-write"] });
const imageRequests = [];
await ctx.route("https://tracker.example.com/**", (route) => { imageRequests.push(route.request().url()); return route.abort(); });
await ctx.route("**/v1/**", async (route) => {
  const p = new URL(route.request().url()).pathname;
  const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p === "/v1/rooms/r1/history")
    return json({
      messages: [
        { sequence: 1, kind: "chat_message", channelId: "general", payload: { userId: "maya", text: md, clientMsgId: "c1" }, atUnixMs: Date.now() - 2000 },
        { sequence: 2, kind: "chat_message", channelId: "general", payload: { userId: "jack", text: md, clientMsgId: "c2" }, atUnixMs: Date.now() - 1000 },
      ],
    });
  if (p === "/v1/rooms/r1/channels") return json({ channels: [channel()], sections: [], groups: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }, { userId: "jack", role: "member" }] });
  if (p === "/v1/rooms/r1") return json(room());
  if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "owner", memberCount: 2, createdAtMs: 1 }] });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
await page.addInitScript(([t]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token]);
await page.goto(B + "/#/room/r1", { waitUntil: "load" });
await page.locator("[data-seq='2'] strong").waitFor({ timeout: 10000 }).catch(() => {});

const rows = await page.evaluate(() =>
  ["1", "2"].map((seq) => document.querySelector(`[data-room-message-feed] [data-seq="${seq}"]`)).filter(Boolean).map((row) => ({
    strong: row.querySelector("strong")?.textContent ?? null,
    code: row.querySelector("code")?.textContent ?? null,
    items: row.querySelectorAll("li").length,
    imgs: row.querySelectorAll("img[src^='https://tracker']").length,
    imageLink: row.querySelector("[data-markdown-image]")?.textContent ?? null,
    stars: (row.textContent ?? "").includes("**"),
  })),
);
ok(rows.length === 2, `two message rows (${rows.length})`);
for (const [i, who] of [[0, "own bubble"], [1, "another member's row"]]) {
  const r = rows[i] ?? {};
  ok(r.strong === "bold words" && !r.stars, `${who}: bold renders (${JSON.stringify(r)})`);
  ok(r.code === "inline code", `${who}: inline code renders`);
  ok(r.items === 2, `${who}: the list renders`);
  ok(r.imgs === 0 && r.imageLink === "tracker", `${who}: the image is a link, never an <img>`);
}
ok(imageRequests.length === 0, `no remote image was fetched (${imageRequests.length})`);

const other = page.locator("[data-room-message-feed] [data-seq='2']");
await other.hover();
await other.locator("[data-copy-message-link]").click();
const copied = await page.evaluate(() => navigator.clipboard.readText()).catch(() => "");
ok(/#\/room\/r1\?seq=2(&channel=[^&]+)?$/.test(copied), `Copy link copies the message's deep link (${copied})`);

await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
