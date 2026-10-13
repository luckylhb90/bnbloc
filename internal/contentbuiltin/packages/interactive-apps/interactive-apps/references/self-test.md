# Testing an app before you finish

Run this before you say an app is done, every time you build or change one.
It opens the page in a headless browser with the SDK in demo mode: every
call is answered in the page's own memory and nothing is sent, so the test
never touches the app's real data.

## Set up once

Work in a scratch directory outside your working tree, so nothing of the
test is published:

```sh
mkdir -p /tmp/app-test && cd /tmp/app-test
npm init -y >/dev/null && npm install playwright@1 >/dev/null
npx playwright install chromium
```

Save the harness below as `/tmp/app-test/check.mjs`. It serves the app's
folder, answers `~app/sdk.js` with the demo-mode SDK from bnbloc's room
records in your workspace (`app-sdk/sdk.js` there), opens the page, does
the page's main action, and prints what it found.

```js
// node check.mjs <app folder> <demo sdk.js> [cover.png]
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { chromium } from "playwright";

const [dir, sdk, cover] = process.argv.slice(2);
const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css",
  ".json": "application/json", ".png": "image/png", ".jpg": "image/jpeg", ".webp": "image/webp" };
const server = createServer(async (req, res) => {
  const path = decodeURIComponent(new URL(req.url, "http://localhost").pathname);
  const file = path.endsWith("/~app/sdk.js") ? sdk
    : join(dir, normalize(path.endsWith("/") ? path + "index.html" : path));
  try {
    const body = await readFile(file);
    res.writeHead(200, { "content-type": types[extname(file)] ?? "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end();
  }
}).listen(0, "127.0.0.1");
await new Promise((ok) => server.once("listening", ok));

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
const errors = [];
page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
page.on("pageerror", (e) => errors.push(String(e)));
await page.goto(`http://127.0.0.1:${server.address().port}/`, { waitUntil: "networkidle" });

// 2. Visible content: innerText leaves out what is hidden.
const visibleText = (await page.evaluate(() => document.body.innerText)).trim();

// 3. The page's main action. Replace these steps with yours: fill the
// form, tap the option, submit.
await page.getByRole("button").first().click();
await page.waitForTimeout(500);
const shownAfter = (await page.evaluate(() => document.body.innerText)).trim();
// What the action wrote, read back through the SDK (your collection).
const written = await page.evaluate(async () => (await app.collection("votes").find({})).docs.length);

if (cover) await page.screenshot({ path: cover });
await browser.close();
server.close();

const report = { errors, visibleText: visibleText.slice(0, 200), written, changedOnScreen: shownAfter !== visibleText };
console.log(JSON.stringify(report, null, 2));
process.exit(errors.length || !visibleText || !written || !report.changedOnScreen ? 1 : 0);
```

## Run it

From your working directory, for an app in `poll/`:

```sh
node /tmp/app-test/check.mjs "$PWD/poll" "<the room records directory>/app-sdk/sdk.js" "$PWD/poll/cover.png"
```

Exit 0 means all three checks passed:

1. `errors` is empty: no console error, no uncaught exception;
2. `visibleText` is not empty: the main view renders, not a blank or
   hidden screen (a page with no script at all, whose views start hidden,
   fails here);
3. `written` is at least 1 and `changedOnScreen` is true: the main action
   wrote through the SDK and the page shows the result.

Anything else: read the report, fix the page, run it again. If you cannot
make it pass, or cannot run it (no browser can be installed), tell the
person exactly which check failed or why it did not run; never call the
app done.

A fresh run starts with empty data. To test a page that needs data first
(a report, a board with cards), add the documents in the harness before the
action, e.g. `await page.evaluate(() => app.collection("cards").insert({title: "x"}, {public: true}))`.
