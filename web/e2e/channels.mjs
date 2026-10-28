// Channels slice 1 (docs/prd/2026-10-28-channels-and-threads.md) in a real
// browser, the way a member uses it: the welcome page on first open, the rail
// by section, the Rules sheet, a channel's settings (its default agent by
// key), the directory, archiving, the rules agreement before a first post, an
// admins-only channel, slow mode, and a refusal said on the row that acted.
// The gateway is mocked in the browser by a small room that refuses what the
// room service refuses. Run it like suggest-use.mjs (README); CHROME names a
// local Chrome when playwright-core has no browser of its own.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const tok = (sub) => b64({ sub, tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };
const shots = process.env.SHOTS || ".";

function world(me) {
  const roles = { maya: "owner", jack: "member" };
  const s = {
    room: { roomId: "r1", name: "Team", description: "Ship the product", visibility: "private", previewable: false, syncMode: "none", createdBy: "maya", createdAtMs: 1, memberCount: 2,
      welcome: "Welcome to Team.", quickPrompts: [{ title: "Recap", prompt: "Recap the week" }], rules: "Be kind.", rulesVersion: 1, requireRulesAgreement: true, recommendedChannels: ["ch_rel"], channelCreation: "anyone" },
    channels: [
      { channelId: "ch_gen", name: "general", isDefault: true, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "maya", settings: {} },
      { channelId: "ch_rel", name: "releases", isDefault: false, createdBy: "maya", purpose: "Ship notes", rules: "Notes only.", sectionId: "sec_ops", position: 0, ownerId: "maya", settings: { whoPosts: "admins" } },
      { channelId: "ch_rand", name: "random", isDefault: false, createdBy: "jack", purpose: "Anything else", rules: "", sectionId: "", position: 1, ownerId: "jack", settings: {} },
      { channelId: "ch_slow", name: "busy", isDefault: false, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 2, ownerId: "maya", settings: { slowModeSeconds: 30 } },
    ],
    sections: [{ sectionId: "sec_ops", name: "Ops", position: 0, visibility: "open", rules: "Ops rules", owners: ["maya"], joinPolicy: "open", createdBy: "maya", createdAtMs: 1 }],
    // maya has no follow of #general (a room older than follows): every
    // member is in the default channel all the same.
    follows: { maya: { ch_rel: {}, ch_slow: {} }, jack: { ch_gen: {}, ch_rand: {} } },
    agreed: { maya: 1, jack: 0 },
    // Each member's own groups of their rail (C24).
    groups: { maya: [], jack: [] },
    chats: [], patches: [], lastPost: {},
  };
  const view = (c) => ({ ...c, joined: c.isDefault || !!s.follows[me][c.channelId], starred: !!s.follows[me][c.channelId]?.starred, hidden: !!s.follows[me][c.channelId]?.hidden, groupId: s.follows[me][c.channelId]?.groupId ?? "" });
  const role = roles[me];
  const admin = role === "owner" || role === "admin";
  const manage = (c) => admin || c.ownerId === me || (s.sections.find((x) => x.sectionId === c.sectionId)?.owners ?? []).includes(me);
  return { s, view, role, admin, manage };
}

async function open(name, me, body) {
  const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const w = world(me);
  const { s } = w;
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
    if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }, { userId: "jack", role: "member" }] });
    if (p === "/v1/rooms/r1/agents") return json({ agents: [{ ownerUserId: "jack", provider: "claude", core: "acme" }, { ownerUserId: "jack", provider: "claude", core: "nimbus" }] });
    if (p === "/v1/rooms/r1/shared-agents") return json([{ targetId: "shared-agent:nimbus:claude", ownerUserId: "jack", provider: "nimbus:claude", allowedModels: [], hasQuota: false }]);
    if (p === "/v1/rooms/r1/channels" && m === "GET") {
      const archived = u.searchParams.get("archived") === "1";
      return json({ channels: s.channels.filter((c) => !!c.archivedAtMs === archived).map(w.view), sections: s.sections, groups: s.groups[me] });
    }
    if (p === "/v1/rooms/r1/channel-groups" && m === "POST") {
      const b = body();
      const g = { groupId: "grp_" + (s.groups[me].length + 1), name: b.name.trim(), position: b.position ?? 0, createdAtMs: Date.now() };
      s.groups[me].push(g);
      return json(g);
    }
    const gm = p.match(/^\/v1\/rooms\/r1\/channel-groups\/([^/]+)$/);
    if (gm) {
      const g = s.groups[me].find((x) => x.groupId === gm[1]);
      if (m === "DELETE") {
        s.groups[me] = s.groups[me].filter((x) => x !== g);
        for (const f of Object.values(s.follows[me])) if (f.groupId === gm[1]) f.groupId = "";
        return route.fulfill({ status: 204 });
      }
      if (!g) return json({ error: "group not found" }, 404);
      Object.assign(g, body());
      return json(g);
    }
    if (p === "/v1/rooms/r1/channels" && m === "POST") {
      const b = body();
      const c = { channelId: "ch_" + b.name, name: b.name, isDefault: false, createdBy: me, purpose: b.purpose || "", rules: "", sectionId: b.sectionId || "", position: 9, ownerId: me, settings: {} };
      s.channels.push(c); s.follows[me][c.channelId] = {};
      return json(w.view(c));
    }
    if (p === "/v1/rooms/r1/channel-directory") return json({ channels: s.channels.filter((c) => !c.archivedAtMs).map((c) => ({ ...w.view(c), followers: 1 })), sections: s.sections });
    const cm = p.match(/^\/v1\/rooms\/r1\/channels\/([^/]+)(?:\/(\w+))?$/);
    if (cm) {
      const c = s.channels.find((x) => x.channelId === cm[1]);
      if (!c) return json({ error: "channel not found" }, 404);
      if (cm[2] === "follow") {
        if (m === "DELETE") delete s.follows[me][c.channelId];
        else s.follows[me][c.channelId] = { ...(s.follows[me][c.channelId] || {}), ...body() };
        return json(w.view(c));
      }
      if (cm[2] === "archive" || cm[2] === "restore" || m === "PATCH") {
        if (!w.manage(c)) return json({ error: "only the room's owner, an admin, the channel's owner or its section's owners change this channel" }, 403);
        if (cm[2] === "archive") c.archivedAtMs = Date.now();
        else if (cm[2] === "restore") delete c.archivedAtMs;
        else { const b = body(); s.patches.push({ id: c.channelId, ...b }); Object.assign(c, b); }
        return json(w.view(c));
      }
      if (m === "DELETE") {
        if (!w.admin) return json({ error: "only the owner or an admin may delete channels" }, 403);
        s.channels = s.channels.filter((x) => x !== c);
        return route.fulfill({ status: 200 });
      }
    }
    if (p === "/v1/rooms/r1/rules") return json({ rules: s.room.rules, version: s.room.rulesVersion, required: s.room.requireRulesAgreement, agreedVersion: s.agreed[me] });
    if (p === "/v1/rooms/r1/rules/agree") { s.agreed[me] = body().version; return json({ rules: s.room.rules, version: s.room.rulesVersion, required: true, agreedVersion: s.agreed[me] }); }
    if (p === "/v1/rooms/r1/chat" && m === "POST") {
      const b = body();
      const c = s.channels.find((x) => x.channelId === b.channelId);
      if (c?.archivedAtMs) return json({ error: "this channel is archived: it is read-only until an owner restores it", code: "channel_archived" }, 403);
      if (c?.settings?.whoPosts === "admins" && !w.admin) return json({ error: "only the room's owner and admins post in this channel", code: "channel_admins_only" }, 403);
      if (s.room.requireRulesAgreement && s.agreed[me] < s.room.rulesVersion) return json({ error: "this room asks you to agree to its rules (version 1) before you post", code: "rules_agreement_required" }, 403);
      const gap = (c?.settings?.slowModeSeconds ?? 0) * 1000;
      const last = s.lastPost[c?.channelId] ?? 0;
      if (gap && Date.now() - last < gap) return json({ error: "slow mode", code: "channel_slow_mode", retryAfterMs: gap - (Date.now() - last) }, 429);
      s.lastPost[c?.channelId] = Date.now();
      s.chats.push(b);
      return json({ sequence: s.chats.length });
    }
    if (p === "/v1/rooms/r1/agent-prefs" && m === "GET") return json({ found: false, channelId: "", ownerId: "", core: "", provider: "" });
    if (p === "/v1/rooms/r1/agent-prefs") return route.fulfill({ status: 204 });
    if (p === "/v1/rooms/r1" && m === "PATCH") { const b = body(); Object.assign(s.room, b); return json(s.room); }
    if (p === "/v1/rooms/r1") return json(s.room);
    if (p === `/v1/users/${me}/rooms`) return json({ rooms: [{ roomId: "r1", name: "Team", description: "", visibility: "private", syncMode: "none", role: w.role, memberCount: 2, createdAtMs: 1 }] });
    if (p === "/v1/assist") return json({ available: false, tasks: [] });
    if (["/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
    return json({}, 404);
  });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.addInitScript(([t, me]) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-1", apiToken: null, subject: me, tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })); }, [tok(me), me]);
  await page.goto(B + "/#/room/r1", { waitUntil: "load" });
  await page.locator("[data-channel-row]").first().waitFor({ timeout: 20000 });
  console.log(`-- ${name}`);
  try {
    await body({ page, s });
  } catch (e) {
    failures++;
    console.log("FAIL: threw", e.message);
    await page.screenshot({ path: `${shots}/fail-${name.replace(/\W+/g, "-")}.png` });
  }
  ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
  await browser.close();
}

const rail = (page) => page.locator("[data-room-rail]");

await open("owner: welcome, rail sections, rules header, settings dialog", "maya", async ({ page, s }) => {
  // First open: the welcome page.
  const welcome = page.locator("[data-room-welcome]");
  await welcome.waitFor({ timeout: 5000 });
  const wt = await welcome.innerText();
  ok(/Welcome to Team\./.test(wt) && /Be kind\./.test(wt) && /releases/.test(wt) && /Ship notes/.test(wt) && /Ops/.test(wt), "the welcome page shows the welcome, rules, a recommended channel with its purpose, and the sections");
  ok(/Asking the AI where to ask is unavailable/.test(wt), "with no model, where-to-ask says the AI is unavailable");
  await page.screenshot({ path: `${shots}/1-welcome.png` });
  await page.getByRole("button", { name: "Start", exact: true }).click();
  // The rail: the top level first, then Ops; random (not joined) is not there.
  const names = await rail(page).locator("[data-channel-row]").evaluateAll((els) => els.map((e) => e.getAttribute("data-channel-row")));
  ok(JSON.stringify(names) === JSON.stringify(["general", "busy", "releases"]), `the rail lists the joined channels by section: ${names}`);
  ok((await rail(page).locator('[data-rail-section="Ops"]').count()) === 1, "the Ops section is a group");
  // Open #releases: purpose and rules.
  await rail(page).locator('[data-channel-row="releases"] button').first().click();
  const header = page.locator("[data-channel-header]");
  await header.getByText("Ship notes").waitFor({ timeout: 5000 });
  await header.getByRole("button", { name: "Rules" }).click();
  const sheet = page.getByRole("dialog", { name: "Rules" });
  const st = await sheet.innerText();
  ok(/Be kind\./.test(st) && /Ops rules/.test(st) && /Notes only\./.test(st), "the Rules sheet has the room's, the section's and the channel's rules");
  await page.keyboard.press("Escape");
  // The default channel stays in the rail while another channel is open.
  ok((await rail(page).locator('[data-channel-row="general"]').count()) === 1, "#general stays in the rail when another channel is open");
  // A section folds while one of its channels is open: the open one stays,
  // its caret says folded, and unfolding brings the rest back.
  const ops = rail(page).getByRole("button", { name: "Ops", exact: true });
  await ops.click();
  ok((await ops.getAttribute("aria-expanded")) === "false" && (await rail(page).locator('[data-channel-row="releases"]').count()) === 1, "Ops folds while #releases is open, which stays shown");
  await ops.click();
  ok((await ops.getAttribute("aria-expanded")) === "true", "Ops unfolds");
  // The channel's settings: the default agent by key.
  await rail(page).locator('[data-channel-row="releases"]').hover();
  await page.getByRole("button", { name: "Channel options for #releases" }).click();
  // The menu is whole on screen: not clipped by the narrow rail, and the rail
  // does not scroll sideways (a regression seen on QA, 2026-10-28).
  {
    const fit = await page.evaluate(() => {
      const m = document.querySelector('[role="menu"]');
      const r = m.getBoundingClientRect();
      const rail = document.querySelector("[data-channel-rail]") || m.closest("body");
      const items = [...m.querySelectorAll('[role="menuitem"]')];
      const hidden = items.filter((i) => {
        const b = i.getBoundingClientRect();
        const at = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
        return !(at && i.contains(at));
      }).length;
      const railEl = [...document.querySelectorAll("div")].find((d) => d.querySelector('[data-channel-row="releases"]') && getComputedStyle(d).overflowY === "auto");
      return { inView: r.left >= 0 && r.top >= 0 && r.right <= innerWidth && r.bottom <= innerHeight, portalled: m.parentElement === document.body, hidden, railScrollsX: railEl ? railEl.scrollWidth > railEl.clientWidth : null };
    });
    ok(fit.inView && fit.hidden === 0, `the channel menu is whole on screen (${JSON.stringify(fit)})`);
    ok(fit.portalled, "the menu is drawn above the page, outside the rail");
    ok(fit.railScrollsX === false, "the rail does not scroll sideways");
    await page.screenshot({ path: `${shots}/channel-menu.png` });
  }
  await page.getByRole("menuitem", { name: "Settings, purpose and rules" }).click();
  const dlg = page.getByRole("dialog", { name: "#releases settings" });
  await dlg.waitFor();
  ok(/Stored for now: until a later version, the channel's agent answers when mentioned/.test(await dlg.innerText()), "agent participation says it is stored only");
  ok(/AI suggestions are unavailable here; fill it in by hand/.test(await dlg.innerText()), "the purpose's AI says it is unavailable (no model)");
  await dlg.getByLabel("Default agent").selectOption({ label: "nimbus/jack/claude (jack)" });
  await dlg.getByRole("spinbutton").fill("15");
  await page.screenshot({ path: `${shots}/2-settings.png` });
  await dlg.getByRole("button", { name: "Save" }).click();
  await dlg.waitFor({ state: "detached", timeout: 5000 });
  const last = s.patches.at(-1);
  ok(last?.settings?.defaultAgent?.core === "nimbus" && last?.settings?.defaultAgent?.ownerId === "jack" && last?.settings?.slowModeSeconds === 15 && last?.settings?.whoPosts === "admins",
    `the patch names the agent by its key and keeps the other settings: ${JSON.stringify(last?.settings)}`);
  // Browse: join #random.
  await rail(page).getByRole("button", { name: "Browse channels" }).click();
  const dir = page.getByRole("dialog", { name: "Browse channels" });
  await dir.getByLabel("Search channels").fill("anything");
  ok((await dir.locator("[data-directory-channel]").count()) === 1, "the directory's search finds by purpose");
  await dir.locator('[data-directory-channel="random"]').getByRole("button", { name: "Join" }).click();
  await dir.getByText("Joined: it is in your list").waitFor({ timeout: 5000 });
  ok(true, "a join says so on its row");
  await page.keyboard.press("Escape");
  await rail(page).locator('[data-channel-row="random"]').waitFor({ timeout: 5000 });
  ok(true, "the joined channel is in the rail");
  // Archive #busy, then it is listed under Archived.
  await rail(page).locator('[data-channel-row="busy"]').hover();
  await page.getByRole("button", { name: "Channel options for #busy" }).click();
  await page.getByRole("menuitem", { name: "Archive" }).click();
  await rail(page).getByRole("button", { name: "Archived (1)" }).waitFor({ timeout: 5000 });
  ok((await rail(page).locator('[data-channel-row="busy"]').count()) === 0, "an archived channel leaves the rail and is under Archived");
  await page.screenshot({ path: `${shots}/3-rail.png` });
  // A group of her own (C24): made from #releases' menu, it takes #releases
  // out of Ops on her rail only, folds, and stays after a reload.
  await rail(page).locator('[data-channel-row="releases"]').hover();
  await page.getByRole("button", { name: "Channel options for #releases" }).click();
  await page.getByRole("menuitem", { name: "New group…" }).click();
  const gd = page.getByRole("dialog", { name: "New group" });
  await gd.getByLabel("Name").fill("My work");
  await gd.getByRole("button", { name: "Make group" }).click();
  await gd.waitFor({ state: "detached", timeout: 5000 });
  const mine = rail(page).locator('[data-rail-group="My work"]');
  await mine.locator('[data-channel-row="releases"]').waitFor({ timeout: 5000 });
  ok((await rail(page).locator('[data-rail-section="Ops"] [data-channel-row="releases"]').count()) === 0, "#releases moved from Ops into her group");
  ok(s.follows.maya.ch_rel?.groupId === "grp_1", `the group is stored on her follow: ${JSON.stringify(s.follows.maya.ch_rel)}`);
  const order = await rail(page).locator("[data-rail-group], [data-rail-section]").evaluateAll((els) => els.map((e) => e.getAttribute("data-rail-group") || e.getAttribute("data-rail-section") || "top"));
  ok(order.indexOf("My work") >= 0 && order.indexOf("My work") < order.indexOf("Ops"), `her groups come before the room's sections: ${order}`);
  // Open another channel, then fold the group: #releases goes from view.
  await rail(page).locator('[data-channel-row="general"] button').first().click();
  const fold = mine.getByRole("button", { name: "My work", exact: true });
  await fold.click();
  ok((await fold.getAttribute("aria-expanded")) === "false" && (await mine.locator('[data-channel-row="releases"]').count()) === 0, "the folded group hides its channels");
  await fold.click();
  await page.reload({ waitUntil: "load" });
  await rail(page).locator('[data-rail-group="My work"] [data-channel-row="releases"]').waitFor({ timeout: 20000 });
  ok(true, "a reload keeps her group and its channel");
  // Out of the group: back in Ops.
  await rail(page).locator('[data-channel-row="releases"]').hover();
  await page.getByRole("button", { name: "Channel options for #releases" }).click();
  await page.getByRole("menuitem", { name: "Remove from my group" }).click();
  await rail(page).locator('[data-rail-section="Ops"] [data-channel-row="releases"]').waitFor({ timeout: 5000 });
  ok(true, "removed from her group, #releases is back in Ops");
  await page.screenshot({ path: `${shots}/4-groups.png` });
});

await open("member: rules agreement, admins-only, slow mode, a refusal on its row", "jack", async ({ page, s }) => {
  await page.locator("[data-room-welcome]").waitFor({ timeout: 5000 });
  await page.getByRole("button", { name: "Start", exact: true }).click();
  // Post in #general: the rules first; I agree sends it.
  const input = page.getByPlaceholder(/Message the room/);
  await input.waitFor();
  await page.waitForTimeout(800);
  await input.fill("hello all");
  await input.press("Enter");
  const agree = page.locator("[data-rules-agreement]");
  await agree.waitFor({ timeout: 5000 });
  ok(/Be kind\./.test(await agree.innerText()), "the composer shows the rules");
  await page.screenshot({ path: `${shots}/4-agree.png` });
  ok(s.chats.length === 0, "nothing was posted before agreeing");
  await agree.getByRole("button", { name: "I agree, send my message" }).click();
  await page.waitForTimeout(1200);
  ok(s.agreed.jack === 1 && s.chats.length === 1 && s.chats[0].text === "hello all", "agreeing records the version and sends the same message");
  ok((await input.inputValue()) === "", "the draft clears once sent");
  // #releases: admins only, the composer says why before typing.
  await page.getByRole("button", { name: "Browse channels" }).click();
  await page.getByRole("dialog", { name: "Browse channels" }).getByRole("button", { name: "releases" }).click();
  await page.locator("[data-composer-blocked]").waitFor({ timeout: 5000 });
  ok(/Only the room's owner and admins post in this channel/.test(await page.locator("[data-composer-blocked]").innerText()), "an admins-only channel disables the composer with the reason");
  // A member does not get the manager's items on someone else's channel.
  await rail(page).locator('[data-channel-row="releases"]').hover();
  await page.getByRole("button", { name: "Channel options for #releases" }).click();
  ok((await page.getByRole("menuitem", { name: "Settings, purpose and rules" }).count()) === 0, "no settings on a channel the member does not manage");
  ok((await page.getByRole("menuitem", { name: "Star" }).count()) === 1, "but starring it, for themselves");
  await page.keyboard.press("Escape");
  // Own channel #random: settings offered; delete not (owner/admin only).
  await rail(page).locator('[data-channel-row="random"]').hover();
  await page.getByRole("button", { name: "Channel options for #random" }).click();
  ok((await page.getByRole("menuitem", { name: "Settings, purpose and rules" }).count()) === 1, "settings on the member's own channel");
  ok((await page.getByRole("menuitem", { name: "Delete…" }).count()) === 0, "no delete for a member");
  await page.keyboard.press("Escape");
  // Slow mode: two posts in #busy.
  s.channels.find((c) => c.channelId === "ch_slow").settings.slowModeSeconds = 30;
  s.follows.jack.ch_slow = {};
  await page.getByRole("button", { name: "Browse channels" }).click();
  await page.getByRole("dialog", { name: "Browse channels" }).getByRole("button", { name: "busy" }).click();
  await page.waitForTimeout(800);
  await input.fill("one");
  await input.press("Enter");
  await page.waitForTimeout(800);
  await input.fill("two");
  await input.press("Enter");
  await page.locator("[data-slow-mode]").waitFor({ timeout: 5000 });
  ok(/you can post here again in \d+ s/.test(await page.locator("[data-slow-mode]").innerText()), "slow mode shows the wait on the composer");
  ok((await input.inputValue()) === "two", "the refused message stays in the box");
  await page.screenshot({ path: `${shots}/5-slow.png` });
  // A refused menu act says so on its row: the server refuses a stale menu.
  s.channels.find((c) => c.channelId === "ch_rand").ownerId = "maya";
  await rail(page).locator('[data-channel-row="random"]').hover();
  await page.getByRole("button", { name: "Channel options for #random" }).click();
  await page.getByRole("menuitem", { name: "Archive" }).click();
  await rail(page).locator('[data-channel-row="random"]').getByRole("status").waitFor({ timeout: 5000 });
  ok(/Could not archive it: only the room's owner/.test(await rail(page).locator('[data-channel-row="random"]').getByRole("status").innerText()), "a refusal is said on the row that acted, in the server's words");
});

console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
