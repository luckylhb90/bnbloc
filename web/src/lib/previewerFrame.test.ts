import {
  frameTicketStale,
  parsePreviewerCatalog,
  parsePreviewerEntry,
  previewerFrameUrl,
  previewerViewerId,
  readPreviewerMessage,
  themeFromBackground,
  validBundlePath,
  wantsThumbnail,
} from "./previewerFrame.ts";
import { STATE_LIMIT, THUMBNAIL_LIMIT } from "../../previewer-sdk/src/protocol.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}
function ok(cond: unknown, message: string): void {
  if (!cond) throw new Error(message);
}

const entry = {
  kind: "previewer",
  name: "sprite-sheet",
  version: "1.0.0",
  description: "Sprite sheets",
  installed: true,
  previewer: {
    entry: "index.html",
    match: { mimeTypes: ["image/png"], extensions: [".atlas.json"] },
    rank: 50,
    capabilities: ["thumbnail", "liveState", "toolbar"],
    toolbar: [{ id: "grid", icon: "grid", label: "Grid", kind: "toggle" }],
    bundleUrl: "https://gw.example.com/v1/artifacts/sha256/abc",
    bundleSha256: "abc",
    frameUrl: "/v1/previewers/sprite-sheet/1.0.0/TICKET.sig/",
    frameExpiresAtMs: 10_000_000,
  },
};

function withBlock(patch: Record<string, unknown>) {
  return { ...entry, previewer: { ...entry.previewer, ...patch } };
}

test("a well-formed catalog entry parses", () => {
  const d = parsePreviewerEntry(entry);
  ok(d, "parsed");
  eq(d?.name, "sprite-sheet", "name");
  eq(d?.toolbar.length, 1, "toolbar");
  eq(d?.capabilities, ["thumbnail", "liveState", "toolbar"], "capabilities");
  eq(previewerViewerId("sprite-sheet"), "component.previewer.sprite-sheet", "viewer id");
});

test("other kinds, opt-in-not-installed and malformed blocks are dropped", () => {
  eq(parsePreviewerEntry({ ...entry, kind: "plugin" }), null, "kind");
  eq(parsePreviewerEntry({ ...entry, installed: false }), null, "not installed");
  eq(parsePreviewerEntry({ ...entry, previewer: undefined }), null, "no block");
  eq(parsePreviewerEntry({ ...entry, name: "../x" }), null, "bad name");
  eq(parsePreviewerEntry(withBlock({ entry: "../index.html" })), null, "traversal entry");
  eq(parsePreviewerEntry(withBlock({ frameUrl: "https://evil.example/v1/previewers/a/b/c/" })), null, "absolute frame url");
  eq(parsePreviewerEntry(withBlock({ frameUrl: "/v1/other/a/b/c/" })), null, "foreign frame url");
  eq(parsePreviewerEntry(withBlock({ rank: 0 })), null, "rank");
  eq(parsePreviewerEntry(withBlock({ capabilities: ["network"] })), null, "capability");
  eq(parsePreviewerEntry(withBlock({ match: {} })), null, "empty match");
  eq(parsePreviewerEntry(withBlock({ toolbar: [{ id: "x", icon: "https://evil/icon.png", label: "X", kind: "button" }] })), null, "icon");
  eq(parsePreviewerCatalog({ components: [entry, { kind: "core", name: "c" }] }).length, 1, "catalog filter");
  eq(parsePreviewerCatalog(null), [], "garbage catalog");
});

test("bundle paths", () => {
  ok(validBundlePath("index.html") && validBundlePath("a/b.js"), "valid");
  for (const p of ["", "/a", "a//b", "../a", "a/./b", "a b", "a%2f"]) ok(!validBundlePath(p), `${p} rejected`);
});

test("frame urls: desktop scheme vs gateway route, always with #host", () => {
  const d = parsePreviewerEntry(entry)!;
  const desktop = previewerFrameUrl(d, {
    // Tauri's convertFileSrc percent-encodes the whole path (slashes too).
    convertFileSrc: (path, protocol) => `${protocol}://localhost/${encodeURIComponent(path)}`,
    apiUrl: (p) => p,
    baseURI: "tauri://localhost/",
    hostOrigin: "tauri://localhost",
  });
  eq(desktop, "bnbloc-previewer://localhost/sprite-sheet/1.0.0/index.html#host=tauri%3A%2F%2Flocalhost", "desktop");
  const browser = previewerFrameUrl(d, { apiUrl: (p) => p, baseURI: "https://gw.example.com/#/room/r1", hostOrigin: "https://gw.example.com" });
  eq(browser, "https://gw.example.com/v1/previewers/sprite-sheet/1.0.0/TICKET.sig/index.html#host=https%3A%2F%2Fgw.example.com", "browser");
});

test("ticket staleness keeps a refresh margin", () => {
  ok(!frameTicketStale({ frameExpiresAtMs: 10_000_000 }, 1_000), "fresh");
  ok(frameTicketStale({ frameExpiresAtMs: 10_000_000 }, 10_000_000 - 60_000), "inside slack");
});

const frame = { id: "frame" };
const pngBytes = (size: number) => {
  const b = new Uint8Array(size);
  b.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  return b.buffer;
};

test("inbound messages: origin and source are pinned", () => {
  const ready = { type: "preview.ready" };
  eq(readPreviewerMessage({ origin: "null", source: frame, data: ready }, frame), ready, "accepted");
  eq(readPreviewerMessage({ origin: "https://gw.example.com", source: frame, data: ready }, frame), null, "non-opaque origin");
  eq(readPreviewerMessage({ origin: "null", source: { other: true }, data: ready }, frame), null, "other window");
  eq(readPreviewerMessage({ origin: "null", source: frame, data: ready }, null), null, "no frame");
  eq(readPreviewerMessage({ origin: "null", source: frame, data: { type: "artifact-load", html: "<b>" } }, frame), null, "foreign type");
});

test("inbound messages: size limits", () => {
  const big = "x".repeat(STATE_LIMIT);
  eq(readPreviewerMessage({ origin: "null", source: frame, data: { type: "preview.state.send", state: big } }, frame), null, "state > 16 KiB");
  ok(readPreviewerMessage({ origin: "null", source: frame, data: { type: "preview.state.send", state: { grid: true } } }, frame), "small state");
  ok(readPreviewerMessage({ origin: "null", source: frame, data: { type: "preview.thumbnail", png: pngBytes(1024) } }, frame), "png");
  eq(readPreviewerMessage({ origin: "null", source: frame, data: { type: "preview.thumbnail", png: pngBytes(THUMBNAIL_LIMIT + 1) } }, frame), null, "png > 512 KiB");
  eq(readPreviewerMessage({ origin: "null", source: frame, data: { type: "preview.thumbnail", png: new Uint8Array(64).buffer } }, frame), null, "not a png");
  const items = Array.from({ length: 21 }, (_, i) => ({ id: `b${i}`, icon: "grid", label: "B", kind: "button" }));
  eq(readPreviewerMessage({ origin: "null", source: frame, data: { type: "preview.toolbar.set", items } }, frame), null, "> 20 items");
  eq(readPreviewerMessage({ origin: "null", source: frame, data: { type: "preview.title", text: "t".repeat(121) } }, frame), null, "long title");
});

test("thumbnails are stored only when declared and missing", () => {
  const d = parsePreviewerEntry(entry)!;
  ok(wantsThumbnail(d, {}), "declared, none stored");
  ok(!wantsThumbnail(d, { thumbnail: { mimeType: "image/png" } }), "already stored");
  ok(!wantsThumbnail(d, undefined), "unknown version");
  ok(!wantsThumbnail({ capabilities: ["toolbar"] }, {}), "not declared");
});

test("theme follows the app background", () => {
  eq(themeFromBackground(" hsl(240 6% 7%)"), "dark", "dark hsl");
  eq(themeFromBackground("hsl(0, 0%, 98%)"), "light", "light hsl");
  eq(themeFromBackground("#101010"), "dark", "dark hex");
  eq(themeFromBackground(""), "light", "unknown");
});
