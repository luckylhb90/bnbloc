// A new room's channels (docs/prd/2026-10-28-channels-and-threads.md C25) in
// a real browser: Plaza's "New room" lists the room templates the member may
// use, shows the rail a picked one makes, and Create installs it with the
// dialog's id, name, description and visibility; "Plan with AI" proposes a
// layout, shown the same way, and Create makes it through the channel routes;
// nothing is made before Create, and an empty room makes nothing. The gateway
// is mocked in the browser. Run it like channels.mjs (README); CHROME names a
// local Chrome when playwright-core has no browser of its own.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const tok = (sub) => b64({ sub, tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };
const shots = process.env.SHOTS || ".";

const template = {
  id: "cc_team", kind: "room", name: "team-room", displayName: "Team room", description: "A team's everyday room.", scope: "platform", owner: "",
  status: "active", latestVersion: "1.0.0", version: "1.0.0", permissions: [], requires: {}, welcome: "Welcome.",
  room: {
    sections: [{ name: "Product", rules: "Decisions go to #decisions." }, { name: "Engineering" }],
    channels: [
      { name: "announcements", purpose: "Notices for everyone", settings: { whoPosts: "admins" } },
      { name: "requests", purpose: "New requests and feedback", section: "Product", settings: {} },
      { name: "decisions", purpose: "One decision a post", section: "Product", settings: {} },
      { name: "alerts", purpose: "Incidents and handovers", section: "Engineering", settings: { slowModeSeconds: 30 } },
    ],
    schedules: [], apps: [], appSnapshots: [], files: [], sources: [],
  },
};
const plan = {
  roomId: "launch-q3", name: "Q3 launch", description: "Prepare the Q3 launch.", expert: "", skills: [], firstMessage: "",
  sections: [{ name: "Launch", rules: "One task a thread." }],
  channels: [{ name: "checklist", purpose: "What is left", section: "Launch" }, { name: "general", purpose: "dropped" }, { name: "press", purpose: "Press and posts" }],
};

async function open(name, body) {
  const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const calls = [];
  await ctx.route("**/v1/**", async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = u.pathname;
    const m = req.method();
    const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
    const parsed = () => JSON.parse(req.postData() || "{}");
    if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
    if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
    if (p === "/v1/assist") return json({ available: true, tasks: [{ name: "plan-room", componentId: "cc_plan", output: "json" }] });
    if (p === "/v1/assist/plan-room" && m === "POST") return json({ fields: plan, usage: {} });
    if (p === "/v1/content-components" && m === "GET") {
      const kind = u.searchParams.get("kind");
      return json({ components: kind === "room" ? [template] : [] });
    }
    if (m !== "GET") {
      calls.push({ m, p, body: parsed() });
      if (p === "/v1/content-components/cc_team/create-room") return json({ roomId: parsed().roomId, name: parsed().name, componentId: "cc_team", from: "team-room@1.0.0", sections: ["Product", "Engineering"], channels: ["announcements", "requests", "decisions", "alerts"], schedules: [], waitingForAgent: [], enabled: [], apps: [], madeApps: [], files: [], skipped: [], sources: [], tools: [], toolsToAdd: [] }, 201);
      if (p === "/v1/rooms") return json({ ...parsed(), syncMode: "none", createdBy: "maya", createdAtMs: 1, memberCount: 1 }, 201);
      const sec = p.match(/^\/v1\/rooms\/[^/]+\/sections$/);
      if (sec) return json({ sectionId: "sec_" + parsed().name, name: parsed().name });
      const ch = p.match(/^\/v1\/rooms\/[^/]+\/channels$/);
      if (ch) return json({ channelId: "ch_" + parsed().name, name: parsed().name });
      if (/^\/v1\/rooms\/[^/]+\/channels\/[^/]+$/.test(p)) return json({});
      if (p.startsWith("/v1/auth/")) return json({});
      return json({});
    }
    if (p === "/v1/users/maya/rooms") return json({ rooms: [] });
    if (p === "/v1/rooms") return json({ rooms: [], total: 0 });
    if (p === "/v1/app/releases") return json({ project: { name: "bnbloc", tagline: "", intro: "" }, releases: [] });
    if (["/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
    if (p === "/v1/components") return json({ components: [] });
    if (p === "/v1/users/profiles") return json({ profiles: [] });
    return json({});
  });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.addInitScript((t) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-1", apiToken: null, subject: "maya", tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })); }, tok("maya"));
  await page.goto(B + "/#/plaza", { waitUntil: "load" });
  await page.getByRole("button", { name: "New room" }).first().click();
  const dialog = page.getByRole("dialog", { name: "New room" });
  await dialog.waitFor({ timeout: 20000 });
  console.log(`-- ${name}`);
  try {
    await body({ page, dialog, calls });
  } catch (e) {
    failures++;
    console.log("FAIL: threw", e.message);
    await page.screenshot({ path: `${shots}/fail-${name.replace(/\W+/g, "-")}.png` });
  }
  ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
  await browser.close();
}

const previewRows = (scope) =>
  scope.locator("[data-layout-preview] [data-preview-section]").evaluateAll((groups) =>
    groups.map((g) => [g.getAttribute("data-preview-section") || "top", [...g.querySelectorAll("[data-preview-channel]")].map((c) => c.getAttribute("data-preview-channel"))]),
  );

await open("a template: its rail before Create, then its install", async ({ page, dialog, calls }) => {
  const start = dialog.getByLabel("Start from");
  await start.waitFor({ timeout: 5000 });
  ok((await start.locator("option").allInnerTexts()).join("|") === "An empty room|Team room", "the picker lists an empty room and the template");
  await start.selectOption("cc_team");
  const setup = dialog.locator("[data-template-setup]");
  await setup.waitFor({ timeout: 5000 });
  const rows = await previewRows(setup);
  ok(
    JSON.stringify(rows) === JSON.stringify([["top", ["general", "announcements"]], ["Product", ["requests", "decisions"]], ["Engineering", ["alerts"]]]),
    `the preview is the rail it makes: ${JSON.stringify(rows)}`,
  );
  ok(/2 sections · 4 channels/.test(await setup.innerText()), "the summary counts its sections and channels");
  ok((await dialog.getByRole("button", { name: "Plan with AI" }).count()) === 0, "planning is off while a template is picked");
  ok(calls.length === 0, "nothing is made before Create");
  await page.screenshot({ path: `${shots}/templates-1-preview.png` });
  await dialog.getByPlaceholder("my-team-room").fill("team-alpha");
  await dialog.getByPlaceholder("Design reviews and agent pairing").fill("Alpha team's room");
  await dialog.getByRole("button", { name: "Create room" }).click();
  await page.waitForTimeout(800);
  const install = calls.find((c) => c.p === "/v1/content-components/cc_team/create-room");
  ok(!!install && install.body.roomId === "team-alpha" && install.body.description === "Alpha team's room" && install.body.visibility === "private", `Create installs the template with the dialog's fields: ${JSON.stringify(install?.body)}`);
  ok(!calls.some((c) => c.p === "/v1/rooms" || /\/channels$/.test(c.p)), "the dialog makes no room or channel itself: the install does");
});

await open("a plan: its layout before Create, made through the channel routes", async ({ page, dialog, calls }) => {
  await dialog.getByLabel("What the room is for").fill("Prepare the Q3 launch with product and press");
  await dialog.getByRole("button", { name: "Plan with AI" }).click();
  const preview = dialog.locator("[data-layout-preview]");
  await preview.waitFor({ timeout: 5000 });
  const rows = await previewRows(dialog);
  ok(JSON.stringify(rows) === JSON.stringify([["top", ["general", "press"]], ["Launch", ["checklist"]]]), `the planned rail, #general never twice: ${JSON.stringify(rows)}`);
  ok(calls.length === 0, "nothing is made before Create");
  await page.screenshot({ path: `${shots}/templates-2-plan.png` });
  await dialog.getByRole("button", { name: "Create room" }).click();
  await page.waitForTimeout(1000);
  const made = calls.map((c) => `${c.m} ${c.p.replace(/^\/v1\/rooms\/launch-q3/, "")} ${JSON.stringify(c.body)}`).filter((l) => !l.includes("/v1/auth"));
  const want = [
    'POST /v1/rooms {"roomId":"launch-q3","name":"Q3 launch","description":"Prepare the Q3 launch.","visibility":"private"}',
    'POST /sections {"name":"Launch","rules":"One task a thread.","position":0,"visibility":"open"}',
    'POST /channels {"name":"checklist","purpose":"What is left","sectionId":"sec_Launch"}',
    'PATCH /channels/ch_checklist {"position":0}',
    'POST /channels {"name":"press","purpose":"Press and posts","sectionId":""}',
    'PATCH /channels/ch_press {"position":1}',
  ];
  ok(JSON.stringify(made) === JSON.stringify(want), `the room, then its sections, then its channels: ${JSON.stringify(made, null, 1)}`);
});

await open("an empty room: the default channel alone", async ({ page, dialog, calls }) => {
  await dialog.getByPlaceholder("my-team-room").fill("plain");
  await dialog.getByRole("button", { name: "Create room" }).click();
  await page.waitForTimeout(800);
  const made = calls.filter((c) => !c.p.startsWith("/v1/auth")).map((c) => `${c.m} ${c.p}`);
  ok(JSON.stringify(made) === JSON.stringify(["POST /v1/rooms"]), `only the room is made: ${made}`);
});

console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
