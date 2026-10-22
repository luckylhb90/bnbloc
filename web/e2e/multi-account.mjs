// Several accounts in one app, in a real browser (docs/prd/2026-10-22-multi-
// account.md): the vault, the binding of a window to one account, a second
// window for another account, switching, removing an account (the window that
// showed it signs out), a refused credential (kept, marked, never bound), the
// per-account renewal lock (two windows renew one account once), the last view,
// and signing in. The gateway is mocked in the browser (no backend needed).
//
// Run it like layout-regression.mjs (README): a web server on
// BNBLOC_E2E_URL (default http://localhost:5173), `playwright-core` from a
// scratch install.
import { chromium } from "playwright-core";
const B = (process.env.BNBLOC_E2E_URL || "http://localhost:5173").replace(/\/+$/, "");
const b64 = (o) => Buffer.from(JSON.stringify(o)).toString("base64url");
const bearer = (sub, tag = "a", ttl = 3600) => b64({ sub, tenant: "default", role: "member", exp: Math.floor(Date.now() / 1000) + ttl, tag }) + ".sig";
const rec = (sub, over = {}) => ({ token: bearer(sub), refreshToken: `r-${sub}-1`, apiToken: null, subject: sub, tenant: "default", role: "member", ...over });
const KEY = (sub) => `\n${sub}`; // same-origin gateway "" + subject
let failures = 0;
const ok = (c, m) => { if (!c) { failures++; console.log("FAIL:", m); } else console.log("ok:", m); };

const calls = { refresh: [], logout: [] };
async function mockGateway(ctx, opts = {}) {
  await ctx.route("**/v1/**", async (route) => {
    const u = new URL(route.request().url());
    const p = u.pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (p === "/v1/auth/sso/config" || p.startsWith("/v1/auth/sso/config")) return json({ authProviderAvailable: false, directLoginAvailable: true });
    if (p === "/v1/auth/refresh") {
      const body = JSON.parse(route.request().postData() || "{}");
      calls.refresh.push(body.refreshToken);
      const sub = body.refreshToken.split("-")[1];
      if (opts.refuse?.has(sub)) return json({ error: "refresh token rejected" }, 401);
      const n = calls.refresh.length;
      return json({ sessionToken: bearer(sub, `r${n}`), refreshToken: `r-${sub}-${n + 1}`, subject: sub, tenant: "default", role: "member" });
    }
    if (p === "/v1/auth/logout") { calls.logout.push(JSON.parse(route.request().postData() || "{}").refreshToken); return route.fulfill({ status: 204 }); }
    if (p === "/v1/cores") return json({ cores: [] });
    if (p === "/v1/local-agent/targets") return json({ targets: [] });
    if (p === "/v1/my/preferences") return json({});
    if (p.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": connected\n\n" });
    if (["/v1/components", "/v1/my/agents", "/v1/platform-agents", "/v1/rooms", "/v1/my/rooms"].includes(p)) return json(p === "/v1/rooms" ? { rooms: [] } : []);
    return json({}, 404);
  });
}
const vault = (accounts, rev = 1) => JSON.stringify({ rev, accounts });
const entry = (sub, lastUsed, over = {}) => ({ gateway: "", record: rec(sub, over.record), lastUsed, lastView: over.lastView ?? "", signedOut: over.signedOut ?? false });
const who = async (page) => {
  const btn = page.getByRole("button", { name: /Account menu/ });
  await btn.first().waitFor({ timeout: 8000 });
  return (await btn.first().getAttribute("aria-label")) ?? "";
};

const browser = await chromium.launch();
// ---- 1. a new window binds to the account used last
{
  const ctx = await browser.newContext({ viewport: { width: 1300, height: 800 } });
  await mockGateway(ctx);
  await ctx.addInitScript((v) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", v); }, vault([entry("maya", 100), entry("jack", 200)]));
  const a = await ctx.newPage();
  await a.goto(B + "/");
  ok((await who(a)).includes("jack"), "1: a new window shows the account used last (jack)");
  const binding = await a.evaluate(() => sessionStorage.getItem("bnbloc.window.account"));
  ok(binding === KEY("jack"), `1: the window is bound to jack (${JSON.stringify(binding)})`);

  // ---- 2. a second window for the other account, side by side
  const b = await ctx.newPage();
  await b.goto(B + "/#!account=" + encodeURIComponent(KEY("maya")));
  ok((await who(b)).includes("maya"), "2: the second window shows maya");
  ok((await who(a)).includes("jack"), "2: the first window still shows jack");
  ok(!(await b.evaluate(() => location.hash)).includes("!account"), "2: the intent is gone from the URL");
  const vaultNow = JSON.parse(await a.evaluate(() => localStorage.getItem("bnbloc.vault")));
  ok(vaultNow.accounts.length === 2, "2: both accounts stay in the vault");

  // ---- 3. switching re-binds this window only
  await a.getByRole("button", { name: /Account menu/ }).first().click();
  await a.getByRole("menuitem", { name: /maya/ }).click();
  await a.waitForLoadState("domcontentloaded");
  await a.waitForTimeout(800);
  ok((await who(a)).includes("maya"), "3: switching shows maya in this window");
  ok((await who(b)).includes("maya"), "3: the other window is untouched (maya)");

  // ---- 4. removing an account signs out the window that shows it, only
  await a.getByRole("button", { name: /Account menu/ }).first().click();
  await a.getByRole("menuitem", { name: /Manage accounts|Add an account/ }).click();
  await a.getByRole("button", { name: /^Remove maya/ }).click();
  await a.getByRole("alertdialog").getByRole("button", { name: "Remove" }).click();
  await a.waitForTimeout(800);
  const left = JSON.parse(await a.evaluate(() => localStorage.getItem("bnbloc.vault")));
  ok(left.accounts.length === 1 && left.accounts[0].record.subject === "jack", "4: only jack is left in the vault");
  ok(calls.logout.includes("r-maya-1") || calls.logout.some((t) => t.startsWith("r-maya")), `4: maya's sign-in was revoked (${calls.logout})`);
  await b.waitForTimeout(800);
  const bText = await b.locator("body").innerText();
  ok(/Welcome back|Sign in/i.test(bText), "4: the window that showed maya is at the sign-in");
  await ctx.close();
}

// ---- 5. an account whose credential is refused is kept, marked, and its window goes to the sign-in
{
  calls.refresh.length = 0;
  const ctx = await browser.newContext({ viewport: { width: 1300, height: 800 } });
  await mockGateway(ctx, { refuse: new Set(["maya"]) });
  const dueMaya = entry("maya", 100, { record: { token: bearer("maya", "old", 30) } }); // about to end: due
  await ctx.addInitScript((v) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", v); }, vault([dueMaya, entry("jack", 200)]));
  const a = await ctx.newPage();
  await a.goto(B + "/");
  await a.waitForTimeout(2500); // the keep-alive renews maya (not shown here): refused
  const v = JSON.parse(await a.evaluate(() => localStorage.getItem("bnbloc.vault")));
  const maya = v.accounts.find((x) => x.record.subject === "maya");
  ok(maya && maya.signedOut === true, "5: a refused account stays in the vault, marked signed out");
  ok((await who(a)).includes("jack"), "5: the window of another account is unaffected");
  ok(calls.refresh.some((t) => t.startsWith("r-maya")), "5: the keep-alive tried maya's credential");
  // a new window never binds to a signed-out account
  const b = await ctx.newPage();
  await b.goto(B + "/#!account=" + encodeURIComponent(KEY("maya")));
  ok((await who(b)).includes("jack"), "5: an intent for a signed-out account falls back to one that can sign in");
  await ctx.close();
}

// ---- 6. add an account: a window with nobody bound, the others untouched
{
  const ctx = await browser.newContext({ viewport: { width: 1300, height: 800 } });
  await mockGateway(ctx);
  await ctx.addInitScript((v) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", v); }, vault([entry("jack", 200)]));
  const a = await ctx.newPage();
  await a.goto(B + "/");
  await who(a);
  const add = await ctx.newPage();
  await add.goto(B + "/#!add-account");
  await add.waitForTimeout(1200);
  const text = await add.locator("body").innerText();
  ok(/Welcome back|Sign in/i.test(text) && /Continue as jack/.test(text), "6: the add-account window shows the sign-in, offering the accounts already in: " + JSON.stringify(text.slice(0, 400)));
  ok((await who(a)).includes("jack"), "6: the window it was opened from is untouched");
  await ctx.close();
}
// ---- 7. two windows of one account renew it once (the lock, the sibling's renewal adopted)
{
  calls.refresh.length = 0;
  const ctx = await browser.newContext({ viewport: { width: 1300, height: 800 } });
  await mockGateway(ctx);
  const due = entry("jack", 200, { record: { token: bearer("jack", "due", 30) } });
  await ctx.addInitScript((v) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", v); }, vault([due]));
  const [a, b] = await Promise.all([ctx.newPage(), ctx.newPage()]);
  await Promise.all([a.goto(B + "/"), b.goto(B + "/")]);
  await Promise.all([who(a), who(b)]);
  await a.waitForTimeout(2500);
  ok(calls.refresh.filter((t) => t.startsWith("r-jack")).length === 1, `7: one renewal for two windows of one account (${calls.refresh})`);
  const v = JSON.parse(await a.evaluate(() => localStorage.getItem("bnbloc.vault")));
  ok(v.accounts[0].record.refreshToken === "r-jack-2", "7: the rotated refresh token is the one in the vault");
  await ctx.close();
}

// ---- 8. a window binding to an account goes where it last was
{
  const ctx = await browser.newContext({ viewport: { width: 1300, height: 800 } });
  await mockGateway(ctx);
  await ctx.addInitScript((v) => { if (!localStorage.getItem("bnbloc.vault")) localStorage.setItem("bnbloc.vault", v); }, vault([entry("jack", 200)]));
  const a = await ctx.newPage();
  await a.goto(B + "/");
  await who(a);
  await a.evaluate(() => { location.hash = "#/library"; });
  await a.waitForTimeout(900);
  const v = JSON.parse(await a.evaluate(() => localStorage.getItem("bnbloc.vault")));
  ok(v.accounts[0].lastView === "#/library", `8: the last view is kept (${v.accounts[0].lastView})`);
  const b = await ctx.newPage();
  await b.goto(B + "/");
  await who(b);
  ok((await b.evaluate(() => location.hash)) === "#/library", "8: a new window goes where the account last was");
  await ctx.close();
}

// ---- 9. signing in lands in the vault and binds the window
{
  const ctx = await browser.newContext({ viewport: { width: 1300, height: 800 } });
  await mockGateway(ctx);
  await ctx.route("**/v1/auth/login", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ sessionToken: bearer("ada"), refreshToken: "r-ada-1", subject: "ada", tenant: "default", role: "member" }) }));
  const a = await ctx.newPage();
  await a.goto(B + "/");
  await a.getByPlaceholder("you@company.com").fill("ada");
  await a.getByRole("button", { name: /Continue/ }).first().click();
  ok((await who(a)).includes("ada"), "9: after sign-in the window shows the account");
  const v = JSON.parse(await a.evaluate(() => localStorage.getItem("bnbloc.vault")));
  ok(v.accounts.length === 1 && v.accounts[0].record.subject === "ada" && v.rev >= 1, "9: it is in the vault");
  ok((await a.evaluate(() => sessionStorage.getItem("bnbloc.window.account"))) === KEY("ada"), "9: and bound");
  await ctx.close();
}

await browser.close();
console.log(failures ? `${failures} FAILED` : "all ok");
process.exit(failures ? 1 : 0);
