// After a request is sent to an agent, the platform says what fits it, drawn under that
// message, one click to turn it on and ask again (docs/prd/2026-10-25-suggest-what-to-use.md).
// The test does what a person does: types and presses Enter at once (no pause), then looks.
// The gateway is mocked in the browser (no backend needed).
//
// Run it like layout-regression.mjs (README): a web server on BNBLOC_E2E_URL
// (default http://localhost:5173), `playwright-core` from a scratch install.
import { chromium } from "playwright-core";
import { channel, room } from "./fixtures.mjs";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const comp = (kind, name, label, description, requires = []) => ({
  id: `cc_${name}`, kind, name, displayName: label, description, tags: [], owner: "", scope: "platform", status: "active", latestVersion: "1.0.0", version: "1.0.0",
  permissions: [], requires: { skills: requires }, mine: false, removable: false,
});
const EXPERTS = [
  comp("expert", "app-builder", "App builder", "Builds web pages and small apps with you that people open from a link.", ["interactive-apps@^1"]),
  comp("expert", "sql-helper", "SQL helper", "Turns a business question into a correct, readable SQL query."),
];
const SKILL = comp("skill", "interactive-apps", "Interactive apps", "Builds any page people open from a link with the app SDK.");
const REQUEST = "build a landing page with an AI assistant that answers questions about our products";

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
// decision: "app" (it picks App builder, else the skill pack), "none", "limited" (429), "unavailable".
const ONE_AGENT = [{ ownerUserId: "maya", provider: "codex", core: "acme" }];
// A room holds one member's agents of several cores, the same provider under two of them: the
// first of "mine" is not the one the request went to (a real room, 2026-10-26).
const SEVERAL_AGENTS = [
  { ownerUserId: "maya", provider: "claude", core: "acme" },
  { ownerUserId: "maya", provider: "codex", core: "acme" },
  { ownerUserId: "maya", provider: "claude-code", core: "nimbus" },
  { ownerUserId: "maya", provider: "claude", core: "nimbus" },
];
async function run(name, { catalog, decision = "app", roomRefuses = false, aiSetting = "", agents = ONE_AGENT }, body) {
  const ctx = await browser.newContext({ viewport: { width: Number(process.env.W || 1280), height: 900 } });
  const puts = [];
  const decides = [];
  const chats = [];
  const shortlists = [];
  const whole = [];
  await ctx.route("**/v1/**", async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
    if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
    if (p === "/v1/rooms/r1/history") return json({ messages: [] });
    if (p === "/v1/rooms/r1/channels") return json({ channels: [channel()], sections: [], groups: [] });
    if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }] });
    if (p === "/v1/rooms/r1/agents") return json({ agents });
    if (p === "/v1/rooms/r1/chat" && req.method() === "POST") { const b = JSON.parse(req.postData() || "{}"); chats.push(b); return json({ sequence: chats.length }); }
    if (p === "/v1/rooms/r1") return json(room());
    if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "owner", memberCount: 1, createdAtMs: 1 }] });
    if (p === "/v1/content-components") return json({ components: catalog.filter((c) => !new URL(req.url()).searchParams.get("kind") || c.kind === new URL(req.url()).searchParams.get("kind")) });
    // A whole-catalog read (no ?kind=) is what suggesting must not do; the viewers' narrow read is not.
    if (p === "/v1/components") { if (!new URL(req.url()).searchParams.get("kind")) whole.push(p); return json({ components: [], contentComponents: catalog }); }
    if (p === "/v1/library/candidates") {
      shortlists.push(new URL(req.url()).searchParams.get("q"));
      const wear = (c) => (c.requires?.skills ?? []).map((x) => x.split("@")[0]);
      return json({ total: catalog.length, candidates: catalog.map((c) => ({ id: c.id, kind: c.kind, name: c.name, label: c.displayName, description: c.description, tags: c.tags, wears: wear(c), version: c.version })) });
    }
    if (p === "/v1/decisions" && req.method() === "GET") return json({ available: decision !== "unavailable", models: ["decision-1"] });
    if (p === "/v1/decisions" && req.method() === "POST") {
      const b = JSON.parse(req.postData() || "{}");
      decides.push(b);
      if (decision === "limited") return json({ message: "too many AI requests" }, 429);
      if (decision === "unavailable") return json({ message: "no decision model" }, 409);
      const choices = b.questions[0].choices.map((c) => c.value);
      const target = decision === "app" ? choices.find((v) => v.includes("app-builder")) ?? choices.find((v) => v.includes("interactive-apps")) : null;
      const probabilities = choices.map((v) => ({ value: v, probability: v === target ? 0.9 : v === "none" ? (target ? 0.05 : 0.95) : 0.05 / Math.max(1, choices.length) }));
      await new Promise((r) => setTimeout(r, 200));
      return json({ model: "decision-1", usage: { input_tokens: 1, output_tokens: 1 }, answers: [{ type: "choice", name: "fit", choice: target ?? "none", confidence: 0.9, probabilities }] });
    }
    if (p === "/v1/content-uses" && req.method() === "GET") return json({ member: [], room: [] });
    if (p === "/v1/content-uses" && req.method() === "PUT") {
      const b = JSON.parse(req.postData() || "{}");
      puts.push(b);
      if (b.scope === "room" && roomRefuses) return json({ message: "only the room's owner or admins change its experts and skills" }, 403);
      return route.fulfill({ status: 204 });
    }
    if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
    if (["/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
    return json({}, 404);
  });
  const page = await ctx.newPage();
  if (aiSetting) await page.addInitScript(([v]) => localStorage.setItem("bnbloc.suggestWithAi:same-origin:maya", v), [aiSetting]);
  await page.addInitScript(([t]) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-maya-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })); }, [token]);
  await page.goto(B + "/#/room/r1", { waitUntil: "load" });
  const input = page.getByPlaceholder(/Message the room/);
  await input.waitFor({ timeout: 15000 });
  // The room's composer is fresh per channel until the channel list arrives: type after that.
  await page.getByText("general", { exact: true }).first().waitFor({ timeout: 15000 });
  await page.waitForTimeout(1200);
  console.log(`-- ${name}`);
  await body({ page, input, puts, decides, chats, shortlists, whole });
  await ctx.close();
}
const card = (page) => page.locator("[data-use-suggestion]");
// What a person does: write the request and send it at once.
const sendAtOnce = async (input, text = REQUEST) => { await input.fill(text); await input.press("Enter"); };

await run("typed and sent at once: the card appears after the send, under that message", { catalog: [...EXPERTS, SKILL] }, async ({ page, input, decides, chats, shortlists, whole }) => {
  await sendAtOnce(input);
  ok(decides.length === 0 || chats.length === 1, "the message goes first");
  await card(page).waitFor({ timeout: 8000 }).catch(() => {});
  ok((await card(page).count()) === 1, "a card appears under the sent message with no pause from the member");
  ok(/App builder fits this request/.test((await card(page).innerText().catch(() => "")) || ""), "it names the App builder, not its skill pack");
  ok(chats.length === 1 && !chats[0].expert, "the member's own message was sent untouched, with no expert");
  ok(decides.length === 1 && decides[0].input === REQUEST, "one decision, about the message that was sent");
  ok(shortlists.length === 1 && shortlists[0] === REQUEST, "the server was asked for candidates for that request, once");
  ok(whole.length === 0, "the client never downloads the whole Library to suggest");
  ok(!JSON.stringify(decides[0].questions[0].instructions).includes("landing page"), "the request is the input, not part of the instructions");
});

await run("one click asks again with the expert, as a normal send", { catalog: [...EXPERTS, SKILL] }, async ({ page, input, chats }) => {
  await sendAtOnce(input);
  await card(page).waitFor({ timeout: 8000 }).catch(() => {});
  await card(page).getByRole("button", { name: "Ask again with it" }).click();
  await page.waitForTimeout(1200);
  ok(chats.length === 2, `the request is sent a second time (${chats.length})`);
  ok(chats[1]?.text === REQUEST && chats[1]?.expert?.id === "cc_app-builder", "the same text, wearing the expert");
  ok(chats[0]?.expert === undefined, "the first message was never changed");
  ok((await card(page).count()) === 0, "the card is gone once used");
});

await run("a request nothing fits gets no card", { catalog: [...EXPERTS, SKILL], decision: "none" }, async ({ page, input, decides }) => {
  await sendAtOnce(input, "what is the weather like in lisbon today");
  await page.waitForTimeout(2500);
  ok(decides.length === 1, "asked once");
  ok((await card(page).count()) === 0, "none means no card");
});

await run("a short reply and a people-only message are not asked about", { catalog: [...EXPERTS, SKILL] }, async ({ page, input, decides }) => {
  await sendAtOnce(input, "thanks!");
  await page.waitForTimeout(1500);
  ok(decides.length === 0, "a short message is not asked about");
});

await run("Not now keeps it quiet", { catalog: [...EXPERTS, SKILL] }, async ({ page, input, decides }) => {
  await sendAtOnce(input);
  await card(page).waitFor({ timeout: 8000 }).catch(() => {});
  await card(page).getByRole("button", { name: "Not now" }).click();
  ok((await card(page).count()) === 0, "dismissed");
  await sendAtOnce(input, REQUEST + " please");
  await page.waitForTimeout(2500);
  ok((await card(page).count()) === 0, "the same candidate is not offered again this month");
  ok(decides.length >= 1, "(the second send was asked about, and the answer was filtered)");
});

await run("a newer message takes the card down", { catalog: [...EXPERTS, SKILL] }, async ({ page, input }) => {
  await sendAtOnce(input);
  await card(page).waitFor({ timeout: 8000 }).catch(() => {});
  await sendAtOnce(input, "thanks!");
  await page.waitForTimeout(600);
  ok((await card(page).count()) === 0, "sending another message removes the old card");
});

await run("a skill pack is turned on for the room, then the request is sent again", { catalog: [SKILL] }, async ({ page, input, puts, chats }) => {
  await sendAtOnce(input);
  await card(page).waitFor({ timeout: 8000 }).catch(() => {});
  ok(/Interactive apps fits this request/.test((await card(page).innerText().catch(() => "")) || ""), "a skill pack no expert wears is offered");
  await card(page).getByRole("button", { name: "Turn on and ask again" }).click();
  await page.waitForTimeout(1200);
  ok(puts.length === 1 && puts[0].scope === "room" && puts[0].roomId === "r1" && puts[0].componentId === "cc_interactive-apps", `turned on for the room (${JSON.stringify(puts)})`);
  ok(chats.length === 2 && chats[1].text === REQUEST, "and the request was sent again");
});

await run("a refused room falls back to the member's agents", { catalog: [SKILL], roomRefuses: true }, async ({ page, input, puts }) => {
  await sendAtOnce(input);
  await card(page).waitFor({ timeout: 8000 }).catch(() => {});
  await card(page).getByRole("button", { name: "Turn on and ask again" }).click();
  await page.waitForTimeout(1200);
  ok(puts.length === 2 && puts[0].scope === "room" && puts[1].scope === "member", `room first, then the member (${JSON.stringify(puts)})`);
});

await run("a rate limit or no decision model shows nothing and breaks nothing", { catalog: [...EXPERTS, SKILL], decision: "limited" }, async ({ page, input, chats }) => {
  await sendAtOnce(input);
  await page.waitForTimeout(2500);
  ok(chats.length === 1 && (await card(page).count()) === 0, "the send went through, no card");
});

await run("a member who turned the AI off sends nothing to the model", { catalog: [...EXPERTS, SKILL], aiSetting: "off" }, async ({ page, input, decides, chats }) => {
  await sendAtOnce(input);
  await page.waitForTimeout(2500);
  ok(chats.length === 1 && decides.length === 0 && (await card(page).count()) === 0, "no decision call, no card");
});


await run("asking again goes to the agent the request went to, not the first of my agents", { catalog: [...EXPERTS, SKILL], agents: SEVERAL_AGENTS }, async ({ page, input, chats }) => {
  // Pick the third agent (nimbus's claude-code) in the composer, as a member does.
  await page.getByRole("button", { name: /^Agent:/ }).click();
  await page.getByRole("button", { name: /claude-code/ }).first().click().catch(async () => { await page.getByText("claude-code", { exact: true }).first().click(); });
  await page.waitForTimeout(400);
  await sendAtOnce(input);
  await card(page).waitFor({ timeout: 8000 }).catch(() => {});
  ok(chats.length === 1 && chats[0].recipient?.provider === "claude-code" && chats[0].recipient?.core === "nimbus", `the first send went to nimbus's claude-code (${JSON.stringify(chats[0]?.recipient)})`);
  await card(page).getByRole("button", { name: "Ask again with it" }).click();
  await page.waitForTimeout(1200);
  ok(chats.length === 2, `the request is sent a second time (${chats.length})`);
  const a = chats[0]?.recipient, b = chats[1]?.recipient;
  ok(!!a && !!b && a.ownerId === b.ownerId && a.provider === b.provider && a.core === b.core, `the second send goes to the same agent: ${JSON.stringify(a)} then ${JSON.stringify(b)}`);
  ok(chats[1]?.expert?.id === "cc_app-builder", "wearing the expert");
});

await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
