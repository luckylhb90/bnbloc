// Channel presets (docs/prd/2026-10-28-channels-and-threads.md §2.1c) in a
// real browser: "Apply a preset…" on a channel shows what the preset changes,
// applies it with one PATCH, and Undo on the toast puts the channel back;
// "Save as preset" saves the channel's purpose, rules and settings (never its
// default agent) as a channel preset in the Library. The gateway is mocked in
// the browser. Run it like channels.mjs (README); CHROME names a local Chrome.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const tok = (sub) => b64({ sub, tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };
const shots = process.env.SHOTS || ".";
const me = "maya";

const s = {
  room: { roomId: "r1", name: "Team", description: "", visibility: "private", previewable: false, syncMode: "none", createdBy: "maya", createdAtMs: 1, memberCount: 2, channelCreation: "anyone", welcome: "", quickPrompts: [], rules: "", rulesVersion: 0, requireRulesAgreement: false, recommendedChannels: [] },
  channels: [
    { channelId: "ch_gen", name: "general", isDefault: true, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "maya", settings: {} },
    { channelId: "ch_sup", name: "support", isDefault: false, createdBy: "maya", purpose: "Questions", rules: "", sectionId: "", position: 1, ownerId: "maya", settings: { slowModeSeconds: 5, defaultAgent: { ownerId: "maya", core: "acme", provider: "codex" } } },
  ],
  presets: [
    { id: "cc_desk", kind: "channel-preset", name: "help-desk", displayName: "Help desk", scope: "platform", latestVersion: "1.0.0", tags: [], permissions: [], requires: {}, mine: false, removable: false, createdAtMs: 1, updatedAtMs: 1,
      channel: { purpose: "Ask anything about the product", rules: "One question per thread.", settings: { agentParticipation: "questions", postingStyle: "threads" } } },
  ],
  patches: [],
  saves: [],
};
const view = (c) => ({ ...c, joined: true });

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const u = new URL(req.url());
  const p = u.pathname;
  const m = req.method();
  const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
  const body = () => JSON.parse(req.postData() || "{}");
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (p === "/v1/rooms/r1/history") return json({ messages: [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }] });
  if (p === "/v1/rooms/r1/agents") return json({ agents: [] });
  if (p === "/v1/rooms/r1/shared-agents") return json([]);
  if (p === "/v1/rooms/r1/channels" && m === "GET") return json({ channels: s.channels.map(view), sections: [], groups: [] });
  if (p === "/v1/rooms/r1/channel-threads") return json({ threads: [] });
  const cm = p.match(/^\/v1\/rooms\/r1\/channels\/([^/]+)$/);
  if (cm && m === "PATCH") {
    const c = s.channels.find((x) => x.channelId === cm[1]);
    const b = body();
    s.patches.push({ id: c.channelId, ...b });
    Object.assign(c, b);
    return json(view(c));
  }
  if (p === "/v1/content-components" && m === "GET") return json({ components: u.searchParams.get("kind") === "channel-preset" ? s.presets : [] });
  if (p === "/v1/content-components" && m === "POST") {
    const b = body();
    s.saves.push(b);
    return json({ component: { id: "cc_new", kind: b.form.kind, name: b.form.name, displayName: b.form.displayName, scope: "personal", latestVersion: "1.0.0", tags: [], permissions: [], requires: {}, mine: true, removable: true, createdAtMs: 2, updatedAtMs: 2 }, versions: [] });
  }
  if (p === "/v1/rooms/r1/agent-prefs" && m === "GET") return json({ found: false, channelId: "", ownerId: "", core: "", provider: "" });
  if (p === "/v1/rooms/r1/agent-prefs") return route.fulfill({ status: 204 });
  if (p === "/v1/rooms/r1/rules") return json({ rules: "", version: 0, required: false, agreedVersion: 0 });
  if (p === "/v1/rooms/r1") return json(s.room);
  if (p === `/v1/users/${me}/rooms`) return json({ rooms: [{ roomId: "r1", name: "Team", description: "", visibility: "private", syncMode: "none", role: "owner", memberCount: 1, createdAtMs: 1 }] });
  if (p === "/v1/assist") return json({ available: false, tasks: [] });
  if (["/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
await page.addInitScript(([t, who]) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-1", apiToken: null, subject: who, tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })); }, [tok(me), me]);
await page.goto(B + "/#/room/r1", { waitUntil: "load" });
const rail = page.locator("[data-room-rail]");
const openMenu = async () => {
  await rail.locator('[data-channel-row="support"]').hover();
  await page.getByRole("button", { name: "Channel options for #support" }).click();
};
try {
  await rail.locator('[data-channel-row="support"]').waitFor({ timeout: 20000 });
  await openMenu();
  await page.getByRole("menuitem", { name: "Apply a preset…" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "Channel preset" }).selectOption("cc_desk");
  const preview = dialog.locator("[data-preset-preview]");
  await preview.waitFor({ timeout: 5000 });
  const fields = await preview.locator("[data-preset-change]").evaluateAll((els) => els.map((e) => e.getAttribute("data-preset-change")));
  ok(JSON.stringify(fields) === JSON.stringify(["purpose", "rules", "agentParticipation", "postingStyle"]), `the preview lists what the preset changes: ${fields}`);
  ok(/AI suggestions are unavailable/.test(await dialog.innerText()), "with no model, describing a preset says the AI is unavailable");
  await page.screenshot({ path: `${shots}/preset-preview.png` });
  await dialog.getByRole("button", { name: "Apply", exact: true }).click();
  await page.getByText("Applied Help desk to #support").waitFor({ timeout: 5000 });
  const applied = s.patches.at(-1);
  ok(s.patches.length === 1 && applied.purpose === "Ask anything about the product" && applied.settings.agentParticipation === "questions" && applied.settings.slowModeSeconds === 5 && applied.settings.defaultAgent?.provider === "codex",
    `one PATCH applies it over the channel's own settings: ${JSON.stringify(applied)}`);
  await page.getByRole("button", { name: "Undo" }).click();
  await page.getByText("#support is back as it was").waitFor({ timeout: 5000 });
  const undone = s.patches.at(-1);
  ok(s.patches.length === 2 && undone.purpose === "Questions" && undone.rules === "" && JSON.stringify(undone.settings) === JSON.stringify({ slowModeSeconds: 5, defaultAgent: { ownerId: "maya", core: "acme", provider: "codex" } }),
    `Undo puts the channel back: ${JSON.stringify(undone)}`);

  await openMenu();
  await page.getByRole("menuitem", { name: "Save as preset" }).click();
  const save = page.getByRole("dialog");
  await save.getByRole("textbox", { name: "Name" }).fill("Support desk");
  await save.getByRole("button", { name: "Save preset" }).click();
  await page.getByText(/Saved "Support desk" to your Library/).waitFor({ timeout: 5000 });
  const form = s.saves.at(-1)?.form;
  ok(form?.kind === "channel-preset" && form.name === "support-desk-preset" && form.channel.purpose === "Questions" && form.channel.settings.slowModeSeconds === 5 && !("defaultAgent" in form.channel.settings),
    `Save as preset saves the channel's setup without its default agent: ${JSON.stringify(form)}`);
} catch (e) {
  failures++;
  console.log("FAIL: threw", e.message);
  await page.screenshot({ path: `${shots}/fail-presets.png` });
}
ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
await browser.close();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
