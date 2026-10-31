// An agent's offer of a link (room_setup share, docs/prd/2026-10-25-agent-shares-from-
// conversation.md) is a card in the room only its requester sees; nothing is shared
// until they click Create link, which creates the dialog's default link (the
// organization's signed-in members, a week) as them and shows it with a Copy button.
// The gateway is mocked in the browser (no backend needed).
//
// Run it like layout-regression.mjs (README): a web server on BNBLOC_E2E_URL
// (default http://localhost:5173), `playwright-core` from a scratch install.
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const card = { roomId: "r1", artifactId: "art_1", title: "Landing page", kind: "site", for: "maya" };
const history = [
  { sequence: 1, kind: "chat_message", payload: { userId: "maya", text: "how do I share this page with my team?" }, atUnixMs: Date.now() - 5000 },
  { sequence: 2, kind: "agent_messages", atUnixMs: Date.now() - 4000, payload: { cursor: 0, messages: [{ seq: 1, createdAtUnixMs: Date.now() - 4000, payload: { role: "assistant", text: "Click Create link below.", ownerUserId: "jack", provider: "codex", jobId: "job_1", sourceSeq: 1 } }] } },
  { sequence: 3, kind: "agent_messages", atUnixMs: Date.now() - 3000, payload: { cursor: 0, messages: [{ seq: 2, createdAtUnixMs: Date.now() - 3000, payload: { role: "assistant", text: "maya's agent offers a link to Landing page", ownerUserId: "jack", provider: "codex", jobId: "job_1", sourceSeq: 1, notice: "share-proposal", shareProposal: card } }] } },
];
const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
for (const who of ["maya", "jack"]) {
  const token = b64({ sub: who, tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, permissions: ["clipboard-read", "clipboard-write"] });
  const creates = [];
  await ctx.route("**/v1/**", async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
    if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
    if (p === "/v1/rooms/r1/history") return json({ messages: history });
    if (p === "/v1/rooms/r1/channels") return json({ channels: [channel()], sections: [], groups: [] });
    if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: who, role: "member" }] });
    if (p === "/v1/rooms/r1") return json(room());
    if (p === `/v1/users/${who}/rooms`) return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "member", memberCount: 2, createdAtMs: 1 }] });
    if (p === "/v1/rooms/r1/artifacts/art_1/shares" && req.method() === "POST") {
      creates.push(JSON.parse(req.postData() || "{}"));
      return json({ share: { shareId: "sh_1", artifactId: "art_1", version: 0, createdBy: who, createdAtMs: 1, expiresAtMs: Date.now() + 7 * 86400000, viewCount: 0, access: "org", active: true, copyable: true }, token: "tok", url: "https://example.test/s/abc123" }, 201);
    }
    if (p === "/v1/rooms/r1/artifacts/art_1/shares") return json({ shares: [] });
    if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
    if (["/v1/components", "/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
    return json({}, 404);
  });
  const page = await ctx.newPage();
  await page.addInitScript(([t, w]) => localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: `r-${w}-1`, apiToken: null, subject: w, tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })), [token, who]);
  await page.goto(B + "/#/room/r1", { waitUntil: "load" });
  await page.getByText("general", { exact: true }).first().waitFor({ timeout: 15000 });
  await page.waitForTimeout(2000);
  console.log(`-- ${who}`);
  const cardEl = page.locator("[data-share-proposal]");
  if (who === "jack") {
    ok((await cardEl.count()) === 0, "someone the agent was not asked by never sees the card");
  } else {
    await cardEl.waitFor({ timeout: 8000 }).catch(() => {});
    ok((await cardEl.count()) === 1, "the requester sees the card");
    const text = await cardEl.innerText();
    ok(/the page "Landing page"/.test(text) && /nothing is shared until you create it/.test(text), "it says what is offered and that nothing is shared yet");
    ok(creates.length === 0, "nothing was created by the card appearing");
    await cardEl.getByRole("button", { name: "Create link" }).click();
    await cardEl.getByRole("button", { name: "Copy" }).waitFor({ timeout: 5000 }).catch(() => {});
    ok(creates.length === 1 && creates[0].access === "org" && creates[0].version === 0 && creates[0].expiresInHours === 168, `one link, the defaults (${JSON.stringify(creates)})`);
    ok((await cardEl.getByLabel("The link").inputValue().catch(() => "")) === "https://example.test/s/abc123", "the link shows on the card");
    ok((await page.evaluate(() => navigator.clipboard.readText()).catch(() => "")) === "https://example.test/s/abc123", "and was copied");
    await cardEl.getByRole("button", { name: "More options…" }).click();
    ok(await page.getByRole("dialog").first().isVisible().catch(() => false), "More options opens the Share dialog");
  }
  await ctx.close();
}
await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
