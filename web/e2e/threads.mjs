// Threads (docs/prd/2026-10-28-channels-and-threads.md §2.2) in a real
// browser: "Reply in thread" on a message opens the side panel; a reply sent
// there goes to the thread's channel and answers the root; the root shows its
// thread's line; the rail lists the followed thread; following, renaming and
// closing it work from the panel's header. The gateway is mocked in the
// browser. Run it like channels.mjs (README); CHROME names a local Chrome.
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
  channels: [{ channelId: "ch_gen", name: "general", isDefault: true, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "maya", settings: {} }],
  history: { ch_gen: [{ sequence: 7, kind: "chat_message", atUnixMs: Date.now() - 60000, channelId: "ch_gen", payload: { text: "Should we ship on Friday?", userId: "jack", clientMsgId: "c7" } }] },
  threads: [],
  follows: {},
  chats: [],
};
const threadView = (t) => ({ ...t, joined: !!s.follows[t.channelId] });

const browser = await chromium.launch(process.env.CHROME ? { executablePath: process.env.CHROME } : {});
const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
await ctx.route("**/v1/**", async (route) => {
  const req = route.request();
  const u = new URL(req.url());
  const p = u.pathname;
  const m = req.method();
  const json = (b, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(b) });
  const body = () => JSON.parse(req.postData() || "{}");
  if (p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
  if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
  if (p === "/v1/rooms/r1/history") {
    const ch = u.searchParams.get("channelId") || "ch_gen";
    return json({ messages: s.history[ch] ?? [] });
  }
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }, { userId: "jack", role: "member" }] });
  if (p === "/v1/rooms/r1/agents") return json({ agents: [] });
  if (p === "/v1/rooms/r1/shared-agents") return json([]);
  if (p === "/v1/rooms/r1/channels" && m === "GET") return json({ channels: s.channels.map((c) => ({ ...c, joined: true })), sections: [], groups: [] });
  if (p === "/v1/rooms/r1/channel-threads") {
    if (u.searchParams.get("followed") === "1") return json({ threads: s.threads.filter((t) => s.follows[t.channelId]).map(threadView) });
    return json({ threads: s.threads.filter((t) => t.thread.parentId === u.searchParams.get("parent")).map(threadView) });
  }
  if (p === "/v1/rooms/r1/threads" && m === "POST") {
    const b = body();
    let t = s.threads.find((x) => x.thread.parentId === b.parentId && x.thread.rootSeq === b.rootSeq);
    if (!t) {
      t = { channelId: "th_1", name: "Should we ship on Friday?", kind: "thread", isDefault: false, createdBy: me, purpose: "", rules: "", sectionId: "", position: 0, ownerId: me, settings: {}, thread: { parentId: b.parentId, rootSeq: b.rootSeq, replies: 0, participants: [] } };
      s.threads.push(t);
      s.history.th_1 = [];
      s.follows.th_1 = true;
      s.opened = (s.opened ?? 0) + 1;
    }
    return json(threadView(t));
  }
  const cm = p.match(/^\/v1\/rooms\/r1\/channels\/([^/]+)(?:\/(\w+))?$/);
  if (cm) {
    const t = s.threads.find((x) => x.channelId === cm[1]);
    if (!t) return json({ error: "channel not found" }, 404);
    if (cm[2] === "follow") {
      if (m === "DELETE") delete s.follows[t.channelId];
      else s.follows[t.channelId] = true;
      return json(threadView(t));
    }
    if (cm[2] === "archive") { t.archivedAtMs = Date.now(); return json(threadView(t)); }
    if (m === "PATCH") { Object.assign(t, body()); return json(threadView(t)); }
  }
  if (p === "/v1/rooms/r1/chat" && m === "POST") {
    const b = body();
    s.chats.push(b);
    const t = s.threads.find((x) => x.channelId === b.channelId);
    const seq = 100 + s.chats.length;
    if (t) {
      t.thread.replies += 1;
      t.thread.participants = [...new Set([...t.thread.participants, me])];
      (s.history[t.channelId] ||= []).push({ sequence: seq, kind: "chat_message", atUnixMs: Date.now(), channelId: t.channelId, payload: { text: b.text, userId: me, clientMsgId: b.clientMsgId, replyTo: b.replyTo } });
    }
    return json({ sequence: seq });
  }
  if (p === "/v1/rooms/r1/agent-prefs" && m === "GET") return json({ found: false, channelId: "", ownerId: "", core: "", provider: "" });
  if (p === "/v1/rooms/r1/agent-prefs") return route.fulfill({ status: 204 });
  if (p === "/v1/rooms/r1/rules") return json({ rules: "", version: 0, required: false, agreedVersion: 0 });
  if (p === "/v1/rooms/r1") return json(s.room);
  if (p === `/v1/users/${me}/rooms`) return json({ rooms: [{ roomId: "r1", name: "Team", description: "", visibility: "private", syncMode: "none", role: "owner", memberCount: 2, createdAtMs: 1 }] });
  if (p === "/v1/assist") return json({ available: false, tasks: [] });
  if (["/v1/my/agents", "/v1/platform-agents"].includes(p)) return json([]);
  return json({}, 404);
});
const page = await ctx.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
await page.addInitScript(([t, who]) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", JSON.stringify({ rev: 1, accounts: [{ gateway: "", record: { token: t, refreshToken: "r-1", apiToken: null, subject: who, tenant: "default", role: "member" }, lastUsed: Date.now(), lastView: "", signedOut: false }] })); }, [tok(me), me]);
await page.goto(B + "/#/room/r1", { waitUntil: "load" });
try {
  const root = page.getByText("Should we ship on Friday?").first();
  await root.waitFor({ timeout: 20000 });
  await root.hover();
  await page.locator("[data-reply-in-thread]").first().click();
  const panel = page.locator("[data-thread-panel]");
  await panel.waitFor({ timeout: 5000 });
  ok(s.opened === 1, "Reply in thread opened (made) the root's thread");
  ok((await panel.getByRole("heading").innerText()).includes("Should we ship on Friday?"), "the panel is titled from the root");
  ok((await page.locator("[data-rail-threads] [data-thread-row]").count()) === 1, "the rail lists the followed thread");
  ok((await panel.locator("[data-thread-root]").innerText()).includes("Should we ship on Friday?"), "the panel shows the root above the replies");

  // No agent in this room: the reply goes to people (the composer's toggle).
  await panel.getByRole("radio", { name: /Send to people/ }).click();
  const box = panel.getByPlaceholder(/^Message/);
  await box.waitFor();
  await page.waitForTimeout(500);
  await box.fill("Friday works if QA passes");
  await box.press("Enter");
  await page.waitForTimeout(800);
  const sent = s.chats.at(-1);
  ok(sent?.channelId === "th_1" && sent?.text === "Friday works if QA passes", `a reply sent in the panel goes to the thread: ${JSON.stringify(sent)}`);

  await panel.locator("[data-thread-follow]").click();
  await page.waitForTimeout(300);
  ok(!s.follows.th_1, "unfollow from the panel's header");
  await panel.getByRole("button", { name: "Rename the thread" }).click();
  const title = panel.getByRole("textbox", { name: "Thread title" });
  await title.fill("Ship date");
  await title.press("Enter");
  await page.waitForTimeout(300);
  ok(s.threads[0].name === "Ship date", "rename from the panel");
  await panel.locator("[data-thread-archive]").click();
  await page.getByRole("button", { name: "Close thread" }).click();
  await page.waitForTimeout(300);
  ok(!!s.threads[0].archivedAtMs, "close (archive) the thread after confirming");
  await page.screenshot({ path: `${shots}/threads.png` });
} catch (e) {
  failures++;
  console.log("FAIL: threw", e.message);
  await page.screenshot({ path: `${shots}/fail-threads.png` });
}
ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
await browser.close();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
