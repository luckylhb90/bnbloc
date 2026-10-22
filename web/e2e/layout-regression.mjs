// Layout regression suite — asserts ALL known-good geometry at once, so fixing
// one gap can't silently reopen another. Runs against the real gateway via the
// Vite dev proxy; enters a room by clicking its Plaza card (rooms are React
// state, not a URL route — the reason earlier probe attempts stalled).
import { chromium } from "playwright-core";
import fs from "node:fs";
import path from "node:path";
import os from "node:os";
function fc(){const b=path.join(os.homedir(),"Library/Caches/ms-playwright");for(const d of fs.readdirSync(b)){const p=path.join(b,d,"chrome-mac-arm64","Google Chrome for Testing.app","Contents","MacOS","Google Chrome for Testing");if(fs.existsSync(p))return p;}throw new Error("no chromium")}
const BASE = process.env.BNBLOC_E2E_URL || "http://localhost:5173/";

const CASES = [
  { name: "phone",   w: 375,  h: 667,  touch: true,  stage: "compact" },
  { name: "tablet",  w: 744,  h: 1133, touch: true,  stage: "medium"  },
  { name: "laptop",  w: 1200, h: 820,  touch: false, stage: "wide"    },
  { name: "desktop", w: 1440, h: 900,  touch: false, stage: "wide"    },
  { name: "wide",    w: 1920, h: 1080, touch: false, stage: "wide"    },
];

const fails = [];
const ck = (cond, label, detail) => { if (!cond) fails.push(`${label}: ${detail}`); };

async function enterRoom(pg, roomId) {
  // The Plaza opens on Works; the room cards live under the Rooms tab.
  await pg.getByRole("tab", { name: "Rooms" }).click().catch(() => {});
  // The Plaza renders room cards; click the card whose text contains roomId.
  // Poll for the room row rather than assuming the click landed immediately —
  // this is exactly what earlier attempts got wrong.
  for (let attempt = 0; attempt < 4; attempt++) {
    const clicked = await pg.evaluate((rid) => {
      const cards = [...document.querySelectorAll("*")].filter(
        (el) => el.getBoundingClientRect().width > 150 &&
          (el.textContent || "").includes(rid) &&
          el.querySelector("*") && // container, not a leaf
          getComputedStyle(el).cursor === "pointer");
      const card = cards.sort((a, b) => a.getBoundingClientRect().width - b.getBoundingClientRect().width)[0];
      if (card) { card.click(); return true; }
      // fallback: any clickable ancestor of a node containing the id
      const node = [...document.querySelectorAll("*")].find(
        (el) => (el.textContent || "").trim() === rid);
      if (node) { let n = node; for (let i = 0; i < 6 && n; i++, n = n.parentElement) { if (getComputedStyle(n).cursor === "pointer") { n.click(); return true; } } }
      return false;
    }, roomId);
    if (clicked) {
      const row = await pg.waitForSelector("[data-room-row]", { timeout: 6000 }).catch(() => null);
      if (row) return true;
    }
    await pg.waitForTimeout(800);
  }
  return false;
}

(async () => {
  const br = await chromium.launch({ executablePath: fc() });

  // Boot context: sign in, create a fixture room + a long-token message.
  const boot = await br.newContext({ viewport: { width: 1280, height: 900 } });
  const bp = await boot.newPage();
  await bp.goto(BASE, { waitUntil: "load" });
  await bp.waitForSelector("#root", { timeout: 20000 });
  await bp.waitForTimeout(1200);
  if (await bp.evaluate(() => /Welcome back/.test(document.body.innerText))) {
    await bp.fill("input#who", "maya"); await bp.click("button[type=submit]"); await bp.waitForTimeout(3500);
  }
  const fx = await bp.evaluate(async () => {
    // The signed-in account's record, from the vault (localStorage).
    const s = JSON.parse(localStorage.getItem("bnbloc.vault") || "{}").accounts?.[0]?.record ?? {};
    const H = { "Content-Type": "application/json", Authorization: "Bearer " + s.token };
    const j = async (u, o) => { const r = await fetch(u, o); if (!r.ok) throw new Error(u + " " + r.status); return r.json(); };
    const rid = "regress-" + Date.now();
    await j("/v1/rooms", { method: "POST", headers: H, body: JSON.stringify({ roomId: rid, name: rid, visibility: "private" }) });
    await j(`/v1/rooms/${rid}/messages`, { method: "POST", headers: H, body: JSON.stringify({ text: "id 42211f79ce5843dca9ffee0248c0b011 " + "x".repeat(60), clientMsgId: "c" + Date.now() }) }).catch(() => {});
    return { rid, token: s.token, subject: s.subject, vault: localStorage.getItem("bnbloc.vault") };
  });
  console.log("fixture room:", fx.rid, "\n");
  await boot.close();

  console.log("| case | w | stage | shell gap | rail gap | feed right | hOverflow | <44px |");
  console.log("|---|---|---|---|---|---|---|---|");

  for (const c of CASES) {
    const ctx = await br.newContext({ viewport: { width: c.w, height: c.h }, hasTouch: c.touch, isMobile: c.touch });
    const pg = await ctx.newPage();
    await pg.goto(BASE, { waitUntil: "load" });
    await pg.waitForSelector("#root", { timeout: 20000 });
    await pg.evaluate((f) => localStorage.setItem("bnbloc.vault", f.vault), fx);
    await pg.reload({ waitUntil: "load" });
    await pg.waitForTimeout(2000);

    const entered = await enterRoom(pg, fx.rid);
    if (!entered) { console.log(`| ${c.name} | ${c.w} | ROOM NOT REACHED |`); ck(false, `${c.name} enter`, "could not open room"); await ctx.close(); continue; }
    await pg.waitForTimeout(1500);

    const m = await pg.evaluate(() => {
      const q = (s) => document.querySelector(s);
      const bx = (e) => e ? { l: Math.round(e.getBoundingClientRect().left), r: Math.round(e.getBoundingClientRect().right), w: Math.round(e.getBoundingClientRect().width) } : null;
      const de = document.documentElement;
      const stageEl = q("[data-room-stage]"), railEl = q("[data-room-rail-mode]");
      const side = q("aside"), main = q("main");
      const chat = q("[data-room-chat-column]"), feed = q("[data-room-message-feed]");
      const small = [...document.querySelectorAll("button,a,[role=button]")].filter((b) => {
        const r = b.getBoundingClientRect(); if (r.width === 0 || r.height === 0) return false;
        const cs = getComputedStyle(b); if (cs.visibility === "hidden" || cs.display === "none") return false;
        return r.height < 44 || r.width < 44;
      }).length;
      return {
        stage: stageEl && stageEl.getAttribute("data-room-stage"),
        side: bx(side), main: bx(main), rail: bx(railEl), chat: bx(chat), feed: bx(feed),
        sw: de.scrollWidth, cw: de.clientWidth, small,
      };
    });

    const shellGap = m.side && m.main ? m.main.l - m.side.r : 0;
    const railGap = m.rail && m.chat ? m.chat.l - m.rail.r : 0;
    const feedRightGap = m.feed && m.chat ? m.chat.r - m.feed.r : 0;
    const ov = m.sw > m.cw + 1;

    ck(m.stage === c.stage, `${c.name} stage`, `want ${c.stage} got ${m.stage}`);
    ck(shellGap <= 2, `${c.name} shell gap`, `${shellGap}px dead space left of content`);
    ck(railGap <= 2, `${c.name} rail gap`, `${railGap}px between rail and chat`);
    ck(!ov, `${c.name} overflow`, `scrollWidth ${m.sw} > clientWidth ${m.cw}`);
    ck(feedRightGap <= 80, `${c.name} feed right`, `${feedRightGap}px dead space right of messages`);
    if (c.touch) ck(m.small === 0, `${c.name} tap targets`, `${m.small} controls under 44px`);

    console.log(`| ${c.name} | ${c.w} | ${m.stage} | ${shellGap} | ${railGap} | ${feedRightGap}${feedRightGap > 80 ? " **BAD**" : ""} | ${ov ? "**YES**" : "no"} | ${c.touch ? m.small : "-"} |`);
    await ctx.close();
  }

  await br.close();
  console.log("");
  if (fails.length) { console.log(`FAIL (${fails.length}):`); fails.forEach((f) => console.log("  - " + f)); process.exit(1); }
  console.log("PASS — all layout invariants hold across 5 viewports");
})().catch((e) => { console.error("HARNESS FAIL:", e.message); process.exit(2); });
