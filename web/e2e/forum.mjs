// Forum channels (docs/prd/2026-10-28-channels-and-threads.md §2.1c, C20) in a
// real browser: a forum lists its threads (no message feed), a new post starts
// from the channel's template and opens its thread, a thread is tagged and
// moved on the board, and a reply is marked as the answer. The gateway is
// mocked in the browser. Run it like channels.mjs (README); CHROME names a
// local Chrome.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const tok = (sub) => b64({ sub, tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + 3600 }) + ".sig";
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };
const shots = process.env.SHOTS || ".";
const me = "maya";

const forum = {
  channelId: "ch_bugs", name: "bugs", isDefault: false, createdBy: "maya", purpose: "Bug reports", rules: "", sectionId: "", position: 1, ownerId: "maya",
  settings: {
    postingStyle: "threads",
    tags: [{ id: "ui", name: "UI" }, { id: "api", name: "API" }],
    statuses: [{ id: "open", name: "Open" }, { id: "done", name: "Done" }],
    postTemplate: "What happened:\n\nSteps:",
  },
};
const s = {
  room: { roomId: "r1", name: "Team", description: "", visibility: "private", previewable: false, syncMode: "none", createdBy: "maya", createdAtMs: 1, memberCount: 2, channelCreation: "anyone", welcome: "", quickPrompts: [], rules: "", rulesVersion: 0, requireRulesAgreement: false, recommendedChannels: [] },
  channels: [{ channelId: "ch_gen", name: "general", isDefault: true, createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "maya", settings: {} }, forum],
  threads: [
    { channelId: "th_old", name: "Crash on save", kind: "thread", isDefault: false, createdBy: "jack", purpose: "", rules: "", sectionId: "", position: 0, ownerId: "jack", settings: forum.settings, joined: true, thread: { parentId: "ch_bugs", rootSeq: 3, replies: 1, lastReplyAtMs: Date.now() - 3_600_000, participants: ["jack"], tags: ["api"], status: "open" } },
  ],
  history: { th_old: [{ sequence: 4, kind: "chat_message", atUnixMs: Date.now() - 3_600_000, channelId: "th_old", payload: { text: "Fixed in 1.2", userId: "jack", clientMsgId: "c4" } }] },
  posts: [], marks: [],
};
const view = (t) => ({ ...t, joined: true });

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
  if (p === "/v1/rooms/r1/history") return json({ messages: s.history[u.searchParams.get("channelId") || "ch_gen"] ?? [] });
  if (p === "/v1/rooms/r1/members") return json({ members: [{ userId: "maya", role: "owner" }, { userId: "jack", role: "member" }] });
  if (p === "/v1/rooms/r1/agents") return json({ agents: [] });
  if (p === "/v1/rooms/r1/shared-agents") return json([]);
  if (p === "/v1/rooms/r1/channels" && m === "GET") return json({ channels: s.channels.map((c) => ({ ...c, joined: true })), sections: [], groups: [] });
  if (p === "/v1/rooms/r1/channel-threads") {
    if (u.searchParams.get("followed") === "1") return json({ threads: s.threads.map(view) });
    return json({ threads: s.threads.filter((t) => t.thread.parentId === u.searchParams.get("parent")).map(view) });
  }
  const tm = p.match(/^\/v1\/rooms\/r1\/channel-threads\/([^/]+)$/);
  if (tm && m === "PATCH") {
    const t = s.threads.find((x) => x.channelId === tm[1]);
    const b = body();
    s.marks.push({ id: tm[1], ...b });
    if (b.tags) t.thread.tags = b.tags;
    if (b.status !== undefined) t.thread.status = b.status;
    if (b.answeredSeq !== undefined) t.thread.answeredSeq = b.answeredSeq;
    return json(view(t));
  }
  if (p === "/v1/rooms/r1/chat" && m === "POST") {
    const b = body();
    s.posts.push(b);
    if (b.channelId === "ch_bugs") {
      const id = "th_new";
      s.threads.push({ channelId: id, name: b.threadTitle || b.text.slice(0, 30), kind: "thread", isDefault: false, createdBy: me, purpose: "", rules: "", sectionId: "", position: 0, ownerId: me, settings: forum.settings, joined: true, thread: { parentId: "ch_bugs", rootSeq: 10, replies: 0, participants: [], tags: b.threadTags ?? [] } });
      s.history[id] = [];
      return json({ sequence: 10, threadId: id });
    }
    return json({ sequence: 20 });
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
  await page.locator('[data-channel-row="bugs"] button').first().click();
  const fv = page.locator("[data-forum-view]");
  await fv.waitFor({ timeout: 15000 });
  ok((await page.locator('[data-tour="feed"]').count()) === 0, "a forum shows its threads, not a message feed");
  ok((await fv.locator("[data-forum-thread]").count()) === 1, "the forum lists its thread");

  // A new post starts from the template and opens its thread.
  await fv.locator("[data-forum-new]").click();
  const box = fv.getByRole("textbox", { name: "Post", exact: true });
  ok((await box.inputValue()).startsWith("What happened:"), "a new post starts from the channel's template");
  await fv.getByRole("textbox", { name: "Post title" }).fill("Button misaligned");
  await box.fill("What happened: the save button overlaps");
  await fv.getByRole("button", { name: "UI", exact: true }).click();
  await fv.getByRole("button", { name: "Post", exact: true }).click();
  const panel = page.locator("[data-thread-panel]");
  await panel.waitFor({ timeout: 5000 });
  const post = s.posts.at(-1);
  ok(post?.channelId === "ch_bugs" && post?.threadTitle === "Button misaligned" && JSON.stringify(post?.threadTags) === '["ui"]', `the post carries its title and tags: ${JSON.stringify(post)}`);
  ok((await panel.getByRole("heading").innerText()).includes("Button misaligned"), "posting opens its thread in the panel");

  // Set its status from the panel.
  await panel.locator("[data-thread-marks]").getByRole("combobox", { name: "Status" }).selectOption("open");
  await page.waitForTimeout(300);
  ok(s.marks.some((x) => x.id === "th_new" && x.status === "open"), "a thread's status is set from its panel");
  await panel.getByRole("button", { name: "Close the thread panel" }).click();

  // The board moves a card to another status.
  await fv.locator('[data-forum-layout="board"]').click();
  const board = fv.locator("[data-forum-board]");
  await board.waitFor({ timeout: 5000 });
  ok((await board.locator('[data-board-column="Open"] [data-board-card]').count()) === 2, "the board's Open column holds both open threads");
  await board.getByRole("combobox", { name: "Move Crash on save" }).selectOption("done");
  await page.waitForTimeout(400);
  ok(s.marks.some((x) => x.id === "th_old" && x.status === "done"), "a card moves to Done from the board");
  await page.screenshot({ path: `${shots}/forum-board.png` });

  // Mark a reply as the answer.
  await fv.locator('[data-forum-layout="list"]').click();
  await fv.locator('[data-forum-thread="Crash on save"]').click();
  await panel.waitFor({ timeout: 5000 });
  await page.waitForTimeout(600);
  await panel.getByRole("combobox", { name: "Answer" }).selectOption("4");
  await page.waitForTimeout(300);
  ok(s.marks.some((x) => x.id === "th_old" && x.answeredSeq === 4), "a reply is marked as the answer");
  await page.screenshot({ path: `${shots}/forum-list.png` });
} catch (e) {
  failures++;
  console.log("FAIL: threw", e.message);
  await page.screenshot({ path: `${shots}/fail-forum.png` });
}
ok(errors.length === 0, `no page errors (${errors.join(" | ")})`);
await browser.close();
console.log(failures ? `${failures} FAILED` : "all passed");
process.exit(failures ? 1 : 0);
