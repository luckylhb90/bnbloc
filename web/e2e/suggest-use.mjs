// The composer recommends what fits the request being written, one click to turn it
// on (docs/prd/2026-10-25-suggest-what-to-use.md): an expert is picked, a skill pack
// is turned on for the room (or, when the room refuses, for the member's agents), a
// dismissal lasts, and a request nothing fits gets no row. The organization's
// decision model places the request when it runs (a few calls per draft, never one
// per keystroke), and the keyword matcher does when it does not. The gateway is
// mocked in the browser (no backend needed).
//
// Run it like layout-regression.mjs (README): a web server on BNBLOC_E2E_URL
// (default http://localhost:5173), `playwright-core` from a scratch install.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const token = b64({ sub: "maya", tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const comp = (kind, name, label, tags, description, requires = []) => ({
  id: `cc_${name}`, kind, name, displayName: label, description, tags, owner: "", scope: "platform", status: "active", latestVersion: "1.0.0", version: "1.0.0",
  permissions: [], requires: { skills: requires }, mine: false, removable: false,
});
const EXPERTS = [
  comp("expert", "app-builder", "App builder", ["apps", "pages", "ai", "polls", "forms"], "Builds web pages and small apps with you that people open from a link: a landing page, an assistant that answers with the organization's AI, a poll, survey, form or board.", ["interactive-apps@^1"]),
  comp("expert", "sql-helper", "SQL helper", ["sql", "data", "query"], "Turns a business question into a correct, readable SQL query."),
];
const SKILL = comp("skill", "interactive-apps", "Interactive apps", ["apps", "pages", "ai", "share", "polls", "forms"], "Builds any page people open from a link with the app SDK: bnbloc publishes and shares it and answers its AI calls with the organization's model.");
const PAGE_REQUEST = "build a landing page with an AI assistant that answers questions about our products";

const browser = await chromium.launch();
// decision: "off" (no decision model: the default of most runs), "app" (it chooses the
// App builder when offered), "none" (it chooses none), "limited" (429), "unavailable".
async function run(name, { catalog, roomRefuses = false, decision = "off", aiSetting = "" }, body) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const puts = [];
  const decides = [];
  await ctx.route("**/v1/**", async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
    if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
    if (p === "/v1/rooms/r1/history") return json({ messages: [] });
    if (p === "/v1/rooms/r1/channels") return json({ channels: [{ channelId: "general", name: "general", isDefault: true, createdBy: "maya" }] });
    if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }] });
    if (p === "/v1/rooms/r1") return json({ roomId: "r1", name: "r1", description: "", visibility: "private", previewable: false, syncMode: "live", createdBy: "maya", createdAtMs: 1 });
    if (p === "/v1/users/maya/rooms") return json({ rooms: [{ roomId: "r1", name: "r1", description: "", visibility: "private", syncMode: "live", role: "owner", memberCount: 1, createdAtMs: 1 }] });
    if (p === "/v1/content-components") return json({ components: catalog.filter((c) => !new URL(req.url()).searchParams.get("kind") || c.kind === new URL(req.url()).searchParams.get("kind")) });
    if (p === "/v1/components") return json({ components: [], contentComponents: catalog });
    if (p === "/v1/decisions" && req.method() === "GET") return json({ available: decision !== "off" && decision !== "unavailable", models: ["decision-1"] });
    if (p === "/v1/decisions" && req.method() === "POST") {
      const b = JSON.parse(req.postData() || "{}");
      decides.push(b);
      if (decision === "limited") return json({ message: "too many AI requests" }, 429);
      const choices = b.questions[0].choices.map((c) => c.value);
      const target = decision === "app" ? choices.find((v) => v.includes("app-builder")) ?? choices.find((v) => v.includes("interactive-apps")) : null;
      const probabilities = choices.map((v) => ({ value: v, probability: v === target ? 0.9 : v === "none" ? (target ? 0.05 : 0.95) : 0.05 / Math.max(1, choices.length) }));
      await new Promise((r) => setTimeout(r, 150));
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
  await ready(page);
  console.log(`-- ${name}`);
  await body({ page, input, puts, decides, ctx });
  await ctx.close();
}
const row = (page) => page.locator("[data-use-suggestion]");
// The room's composer is fresh per channel (RoomView keys it by the channel), so it is
// replaced when the channel list arrives: type only after that, or the draft is lost.
const ready = async (page) => {
  await page.getByText("general", { exact: true }).first().waitFor({ timeout: 15000 });
  await page.waitForTimeout(1200);
};

await run("an expert is suggested, picked in one click, and not suggested again", { catalog: [...EXPERTS, SKILL] }, async ({ page, input }) => {
  await input.fill("hi");
  await page.waitForTimeout(1200);
  ok((await row(page).count()) === 0, "a short draft gets no row");
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 5000 }).catch(() => {});
  ok((await row(page).count()) === 1, "a request for a page with an assistant gets a row");
  ok(/App builder fits this request/.test((await row(page).innerText().catch(() => "")) || ""), "the row names the App builder, not its skill pack");
  await row(page).getByRole("button", { name: "Use" }).click();
  await page.waitForTimeout(800);
  ok((await row(page).count()) === 0, "the row is gone once the expert is used");
  ok((await page.getByText("App builder").count()) > 0, "the expert shows in the composer");
});

await run("a request nothing fits gets no row", { catalog: [...EXPERTS, SKILL] }, async ({ page, input }) => {
  await input.fill("what is the weather like in lisbon today");
  await page.waitForTimeout(1500);
  ok((await row(page).count()) === 0, "no row for an unrelated request");
});

await run("a dismissal lasts across a reload", { catalog: [...EXPERTS, SKILL] }, async ({ page, input }) => {
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 5000 }).catch(() => {});
  await page.getByRole("button", { name: /Don't suggest App builder again/ }).click();
  await page.waitForTimeout(500);
  console.log("stored:", await page.evaluate(() => Object.entries(localStorage).filter(([k]) => k.includes("useSuggestions")).map(([k, v]) => k + "=" + v).join("; ")));
  ok((await row(page).count()) === 0, "dismissed");
  await page.reload({ waitUntil: "load" });
  const again = page.getByPlaceholder(/Message the room/);
  await again.waitFor({ timeout: 15000 });
  await ready(page);
  await again.fill(PAGE_REQUEST);
  await page.waitForTimeout(1500);
  ok((await row(page).count()) === 0, "still quiet after a reload");
});

await run("a skill pack is turned on for the room", { catalog: [SKILL] }, async ({ page, input, puts }) => {
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 5000 }).catch(() => {});
  ok(/Interactive apps fits this request/.test((await row(page).innerText().catch(() => "")) || ""), "a skill pack no expert wears is offered");
  await row(page).getByRole("button", { name: "Turn on" }).click();
  await page.waitForTimeout(800);
  ok(puts.length === 1 && puts[0].scope === "room" && puts[0].roomId === "r1" && puts[0].componentId === "cc_interactive-apps", `turned on for the room (${JSON.stringify(puts)})`);
  ok((await row(page).count()) === 0, "the row is gone");
});

await run("a refused room falls back to the member's agents", { catalog: [SKILL], roomRefuses: true }, async ({ page, input, puts }) => {
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 5000 }).catch(() => {});
  await row(page).getByRole("button", { name: "Turn on" }).click();
  await page.waitForTimeout(1000);
  ok(puts.length === 2 && puts[0].scope === "room" && puts[1].scope === "member", `room first, then the member (${JSON.stringify(puts)})`);
});

await run("the decision model places the request, a few calls per draft", { catalog: [...EXPERTS, SKILL], decision: "app" }, async ({ page, input, decides }) => {
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 6000 }).catch(() => {});
  ok(/App builder fits this request/.test((await row(page).innerText().catch(() => "")) || ""), "the model's choice is suggested");
  ok(decides.length === 1, `one call for one settled draft (${decides.length})`);
  const q = decides[0]?.questions?.[0];
  ok(q?.choices?.length === 4 && q.choices.at(-1).value === "none", "every candidate and none are asked about");
  ok(!JSON.stringify(q?.instructions).includes("landing page"), "the draft is the input, not part of the instructions");
  // Typing on, a character at a time, pausing never long enough to settle: no more calls.
  await input.fill("");
  await input.pressSequentially("write a weekly report from these notes, step by step", { delay: 40 });
  await page.waitForTimeout(2500);
  ok(decides.length === 2, `typing is one call once the writer pauses (${decides.length})`);
  // A long paste is cut to its start and end.
  await input.fill("");
  await input.fill("w".repeat(4000));
  await page.waitForTimeout(2500);
  const last = decides.at(-1);
  ok(String(last?.input).length < 1300, `a long draft sends a short input (${String(last?.input).length})`);
});

await run("the decision model's silence stands", { catalog: [...EXPERTS, SKILL], decision: "none" }, async ({ page, input, decides }) => {
  await input.fill(PAGE_REQUEST);
  await page.waitForTimeout(2500);
  ok(decides.length === 1, "asked once");
  ok((await row(page).count()) === 0, "none means no row, not a keyword guess");
});

await run("without a decision model the keyword matcher places it", { catalog: [...EXPERTS, SKILL], decision: "unavailable" }, async ({ page, input, decides }) => {
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 6000 }).catch(() => {});
  ok((await row(page).count()) === 1, "the keyword row appears");
  ok(decides.length === 0, "no decision call was made");
});

await run("a rate limit falls back to the keyword matcher", { catalog: [...EXPERTS, SKILL], decision: "limited" }, async ({ page, input, decides }) => {
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 6000 }).catch(() => {});
  ok((await row(page).count()) === 1, "the keyword row appears");
  ok(decides.length === 1, "one failed call, then the model is left alone for a while");
});

await run("a member who turned the AI off sends nothing", { catalog: [...EXPERTS, SKILL], decision: "app", aiSetting: "off" }, async ({ page, input, decides }) => {
  await input.fill(PAGE_REQUEST);
  await row(page).waitFor({ timeout: 6000 }).catch(() => {});
  ok((await row(page).count()) === 1, "the keyword row appears");
  ok(decides.length === 0, "nothing was sent to the model");
});

await browser.close();
console.log(failures ? `${failures} failed` : "all ok");
process.exit(failures ? 1 : 0);
