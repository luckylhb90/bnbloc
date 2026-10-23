// A member's own message with very long unbroken text (a pasted token) stays
// inside its column, wraps, and folds behind "Show more" like everyone
// else's long text. The gateway is mocked in the browser (no backend needed).
//
// Run it like layout-regression.mjs (README): a web server on BNBLOC_E2E_URL
// (default http://localhost:5173), `playwright-core` from a scratch install.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
const word = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + "a".repeat(300);
const text = Array(7).fill(word).join("\n\n");
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const browser = await chromium.launch();
for (const width of [1440, 390]) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 } });
  await ctx.route("**/v1/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
    if (p === "/v1/rooms/r1/history") return json({ messages: [{ sequence: 1, kind: "chat_message", payload: { userId: "maya", text }, atUnixMs: Date.now() }] });
    if (p === "/v1/rooms/r1/channels") return json({ channels: [{ channelId: "general", name: "general", isDefault: true, createdBy: "maya" }] });
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
  const more = page.getByRole("button", { name: "Show more" });
  await more.waitFor({ timeout: 10000 }).catch(() => {});
  const m = await page.evaluate(() => {
    const row = document.querySelector("[id^=msg-]");
    const bubble = row?.firstElementChild?.firstElementChild;
    const b = bubble?.getBoundingClientRect(), r = row?.getBoundingClientRect();
    return { bubble: b && { l: b.left, r: b.right, h: b.height }, row: r && { l: r.left, r: r.right } };
  });
  ok(m.bubble && m.bubble.l >= m.row.l - 1 && m.bubble.r <= m.row.r + 1, `${width}px: the bubble stays inside its column (${JSON.stringify(m)})`);
  ok(m.bubble && m.bubble.h < 700, `${width}px: a long message is folded (${Math.round(m.bubble?.h ?? 0)}px)`);
  ok(await more.isVisible().catch(() => false), `${width}px: "Show more" is reachable`);
  await more.click().catch(() => {});
  ok(await page.getByRole("button", { name: "Show less" }).isVisible().catch(() => false), `${width}px: it unfolds`);
  await ctx.close();
}
await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
