import {
  DRAFT_PROMPT_LIMIT,
  DRAFT_REPLY_LIMIT,
  ERROR_LIMIT,
  PREVIEWER_CAPABILITIES,
  PREVIEWER_PROTOCOL,
  SAVE_LIMIT,
  SAVE_SUMMARY_LIMIT,
  STATE_LIMIT,
  THUMBNAIL_LIMIT,
  TITLE_LIMIT,
  TOOLBAR_LIMIT,
  SELECTION_DETAIL_LIMIT,
  SELECTION_LABEL_LIMIT,
  SELECTION_KEYS_LIMIT,
  UNITS_BYTES_LIMIT,
  UNITS_LIMIT,
  UNIT_TEXT_LIMIT,
  fitSelection,
  fitUnits,
  hostOriginFromHash,
  readFrameMessage,
  readHostMessage,
  readToolbar,
  readUnits,
  readToolbarItem,
  stateKey,
  validThumbnail,
  validToolbarValue,
} from "./protocol.ts";

// No node types in the web tsconfig: a local runner, same convention as
// web/src/lib/*.test.ts; a failure throws and fails `node --test`.
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

function png(size: number): ArrayBuffer {
  const bytes = new Uint8Array(size);
  bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  return bytes.buffer;
}

const zoom = { id: "zoom", icon: "zoom-in", label: "Zoom", kind: "zoom" };

test("toolbar items follow the code-owned vocabularies", () => {
  eq(readToolbarItem(zoom), zoom, "a plain zoom item");
  eq(readToolbarItem({ ...zoom, value: 2 }), { ...zoom, value: 2 }, "zoom carries a factor");
  eq(readToolbarItem({ ...zoom, icon: "rocket" }), null, "unknown icon");
  eq(readToolbarItem({ ...zoom, kind: "slider" }), null, "unknown kind");
  eq(readToolbarItem({ ...zoom, id: "Zoom" }), null, "upper-case id");
  eq(readToolbarItem({ ...zoom, id: "z".repeat(33) }), null, "id over 32 chars");
  eq(readToolbarItem({ ...zoom, label: " " }), null, "blank label");
  eq(readToolbarItem({ ...zoom, label: "x".repeat(41) }), null, "label over 40");
  eq(readToolbarItem({ ...zoom, value: 0 }), null, "zoom below ZOOM_MIN");
  eq(readToolbarItem({ ...zoom, value: 65 }), null, "zoom above ZOOM_MAX");
  eq(readToolbarItem({ ...zoom, active: true }), null, "active only on toggles");
  eq(readToolbarItem({ id: "grid", icon: "grid", label: "Grid", kind: "toggle", value: 2 }), null, "value only on zoom");
  eq(
    readToolbarItem({ id: "grid", icon: "grid", label: "Grid", kind: "toggle", active: true, disabled: false }),
    { id: "grid", icon: "grid", label: "Grid", kind: "toggle", active: true, disabled: false },
    "toggle state",
  );
});

test("toolbar lists: unique ids, at most TOOLBAR_LIMIT", () => {
  eq(readToolbar([zoom, zoom]), null, "duplicate ids");
  const many = Array.from({ length: TOOLBAR_LIMIT + 1 }, (_, i) => ({ ...zoom, id: `z${i}` }));
  eq(readToolbar(many), null, "over the limit");
  eq(readToolbar(many.slice(0, TOOLBAR_LIMIT))?.length, TOOLBAR_LIMIT, "exactly the limit");
  eq(readToolbar("zoom"), null, "not a list");
  eq(readToolbar([]), [], "empty list");
});

test("toolbar values match the item kind", () => {
  eq(validToolbarValue("button", null), true, "button: null");
  eq(validToolbarValue("button", true), false, "button: no boolean");
  eq(validToolbarValue("toggle", false), true, "toggle: boolean");
  eq(validToolbarValue("toggle", 1), false, "toggle: no number");
  eq(validToolbarValue("zoom", 1.5), true, "zoom: factor");
  eq(validToolbarValue("zoom", 100), false, "zoom: bounded");
  eq(validToolbarValue("zoom", Number.NaN), false, "zoom: finite");
});

test("state is JSON within STATE_LIMIT bytes", () => {
  eq(stateKey({ a: 1 }), '{"a":1}', "small state");
  eq(stateKey("x".repeat(STATE_LIMIT)), null, "over the byte limit once quoted");
  eq(stateKey(1n), null, "bigint is not JSON");
  const cyclic: Record<string, unknown> = {};
  cyclic.self = cyclic;
  eq(stateKey(cyclic), null, "cyclic is not JSON");
  eq(stateKey("é".repeat(STATE_LIMIT / 2)), null, "limit counts UTF-8 bytes");
});

test("thumbnails are PNG within THUMBNAIL_LIMIT", () => {
  eq(validThumbnail(png(64)), true, "small png");
  eq(validThumbnail(png(THUMBNAIL_LIMIT)), true, "exactly the limit");
  eq(validThumbnail(png(THUMBNAIL_LIMIT + 1)), false, "over the limit");
  eq(validThumbnail(png(8)), false, "signature only");
  eq(validThumbnail(new Uint8Array(64).buffer), false, "no signature");
});

test("frame messages are validated by type", () => {
  eq(readFrameMessage({ type: "preview.ready" }), { type: "preview.ready" }, "ready");
  eq(readFrameMessage({ type: "preview.unknown" }), null, "unknown type");
  eq(readFrameMessage(null), null, "null");
  eq(readFrameMessage(["preview.ready"]), null, "array");
  eq(readFrameMessage({ type: "preview.toolbar.set", items: [zoom] }), { type: "preview.toolbar.set", items: [zoom] }, "toolbar set");
  eq(readFrameMessage({ type: "preview.toolbar.set", items: [zoom, zoom] }), null, "duplicate toolbar ids");
  eq(readFrameMessage({ type: "preview.state.send", state: { grid: true } })?.type, "preview.state.send", "state");
  eq(readFrameMessage({ type: "preview.state.send", state: "x".repeat(STATE_LIMIT) }), null, "oversized state");
  eq(readFrameMessage({ type: "preview.thumbnail", png: png(32) })?.type, "preview.thumbnail", "thumbnail");
  eq(readFrameMessage({ type: "preview.thumbnail", png: new Uint8Array(png(32)) }), null, "a view is not an ArrayBuffer");
  eq(readFrameMessage({ type: "preview.thumbnail", png: png(THUMBNAIL_LIMIT + 1) }), null, "oversized thumbnail");
  eq(readFrameMessage({ type: "preview.error", message: "bad" }), { type: "preview.error", message: "bad" }, "error");
  eq(readFrameMessage({ type: "preview.error", message: "" }), null, "empty error");
  eq(readFrameMessage({ type: "preview.error", message: "x".repeat(ERROR_LIMIT + 1) }), null, "error over limit");
  eq(readFrameMessage({ type: "preview.title", text: "" }), { type: "preview.title", text: "" }, "empty title clears");
  eq(readFrameMessage({ type: "preview.title", text: "x".repeat(TITLE_LIMIT + 1) }), null, "title over limit");
  eq(readFrameMessage({ type: "preview.title", text: 3 }), null, "title must be text");
});

test("host messages are validated by type", () => {
  const init = {
    type: "preview.init",
    protocol: PREVIEWER_PROTOCOL,
    file: { name: "a.png", mimeType: "image/png", sizeBytes: 10, version: 2 },
    theme: "dark",
    viewport: { w: 640, h: 480 },
  };
  eq(readHostMessage(init), init, "init");
  eq(readHostMessage({ ...init, protocol: 2 }), null, "unknown protocol version");
  eq(readHostMessage({ ...init, theme: "sepia" }), null, "unknown theme");
  eq(readHostMessage({ ...init, file: { ...init.file, sizeBytes: -1 } }), null, "negative size");
  eq(readHostMessage({ ...init, viewport: { w: 1 } }), null, "viewport needs both sides");
  eq(readHostMessage({ type: "preview.file", bytes: png(16) })?.type, "preview.file", "file bytes");
  eq(readHostMessage({ type: "preview.file", bytes: "data" }), null, "file bytes must be an ArrayBuffer");
  eq(readHostMessage({ type: "preview.toolbar", id: "fit", value: null }), { type: "preview.toolbar", id: "fit", value: null }, "button gesture");
  eq(readHostMessage({ type: "preview.toolbar", id: "zoom", value: 2 })?.type, "preview.toolbar", "zoom gesture");
  eq(readHostMessage({ type: "preview.toolbar", id: "zoom", value: "2" }), null, "gesture value type");
  eq(readHostMessage({ type: "preview.toolbar", id: "../x", value: null }), null, "gesture id shape");
  eq(readHostMessage({ type: "preview.state", state: { a: 1 }, actor: "bob" })?.type, "preview.state", "state");
  eq(readHostMessage({ type: "preview.state", state: { a: 1 } }), null, "state needs an actor");
  eq(readHostMessage({ type: "preview.resize", w: 10, h: 20 }), { type: "preview.resize", w: 10, h: 20 }, "resize");
  eq(readHostMessage({ type: "preview.resize", w: Infinity, h: 20 }), null, "resize finite");
  eq(readHostMessage({ type: "preview.ready" }), null, "frame messages are not host messages");
});

test("the host origin comes from the fragment", () => {
  eq(hostOriginFromHash("#host=https%3A%2F%2Fapp.example"), "https://app.example", "encoded origin");
  eq(hostOriginFromHash("host=tauri%3A%2F%2Flocalhost"), "tauri://localhost", "without #");
  eq(hostOriginFromHash(""), "", "no fragment");
  eq(hostOriginFromHash("#other=1"), "", "no host key");
});

test("a selection is a bounded label and detail, or null to clear", () => {
  eq(readFrameMessage({ type: "preview.selection", selection: { label: "Slide 4", detail: "Q3 revenue" } }), { type: "preview.selection", selection: { label: "Slide 4", detail: "Q3 revenue" } }, "a selection");
  eq(readFrameMessage({ type: "preview.selection", selection: { label: "Revenue!B3:D5" } }), { type: "preview.selection", selection: { label: "Revenue!B3:D5", detail: "" } }, "no detail");
  eq(readFrameMessage({ type: "preview.selection", selection: null }), { type: "preview.selection", selection: null }, "cleared");
  eq(readFrameMessage({ type: "preview.selection", selection: { label: "" } }), null, "an empty label");
  eq(readFrameMessage({ type: "preview.selection", selection: { label: "x".repeat(SELECTION_LABEL_LIMIT + 1) } }), null, "a long label");
  eq(readFrameMessage({ type: "preview.selection", selection: { label: "x", detail: "y".repeat(SELECTION_DETAIL_LIMIT + 1) } }), null, "a long detail");
  const fit = fitSelection({ label: "x".repeat(500), detail: "y".repeat(5000) });
  eq([[...fit.label].length, [...fit.detail].length, fit.detail.endsWith("…")], [SELECTION_LABEL_LIMIT, SELECTION_DETAIL_LIMIT, true], "fitSelection cuts to the limits");
  eq(readFrameMessage({ type: "preview.selection", selection: fit }) !== null, true, "a fitted selection is accepted");
});

test("units are bounded on both sides", () => {
  const ok = { type: "preview.units", id: "r1", units: [{ key: "s1", label: "Slide 1", text: "Title" }, { label: "", text: "" }], truncated: false };
  eq(readFrameMessage(ok), ok, "a unit list");
  eq(readFrameMessage({ type: "preview.units", id: "r1", error: "bad file" }), { type: "preview.units", id: "r1", error: "bad file" }, "an error");
  eq(readFrameMessage({ ...ok, id: "../x" }), null, "a bad id");
  eq(readFrameMessage({ ...ok, units: [{ label: "x", text: "y".repeat(UNIT_TEXT_LIMIT + 1) }] }), null, "a long text");
  eq(readFrameMessage({ ...ok, units: Array.from({ length: UNITS_LIMIT + 1 }, () => ({ label: "", text: "" })) }), null, "too many");
  const fitted = fitUnits(Array.from({ length: UNITS_LIMIT + 2 }, (_, i) => ({ key: `k${i}`, label: "L", text: "t" })));
  eq([fitted.units.length, fitted.truncated], [UNITS_LIMIT, true], "fitUnits stops at the limit");
  eq(readUnits(fitted.units) !== null, true, "a fitted list is accepted");
  const big = fitUnits(Array.from({ length: 1000 }, () => ({ label: "x", text: "y".repeat(UNIT_TEXT_LIMIT) })));
  eq([big.truncated, JSON.stringify(big.units).length <= UNITS_BYTES_LIMIT], [true, true], "fitUnits stops at the byte limit");
});

test("a selection may say its kind, place and keys; units their place; reveal names one", () => {
  const sel = { label: "Slide 2", detail: "Highlights", kind: "slide", rect: { x: 10, y: 20, w: 300, h: 160 }, keys: ["slide-257"] };
  eq(readFrameMessage({ type: "preview.selection", selection: sel }), { type: "preview.selection", selection: sel }, "a full selection");
  eq(readFrameMessage({ type: "preview.selection", selection: { ...sel, kind: "galaxy" } }), null, "an unknown kind");
  eq(readFrameMessage({ type: "preview.selection", selection: { ...sel, rect: { x: 0, y: 0, w: -1, h: 1 } } }), null, "a negative rect");
  eq(readFrameMessage({ type: "preview.selection", selection: { ...sel, keys: Array.from({ length: SELECTION_KEYS_LIMIT + 1 }, (_, i) => `k${i}`) } }), null, "too many keys");
  const many = fitSelection({ label: "Revenue!A1:Z100", kind: "cells", keys: Array.from({ length: SELECTION_KEYS_LIMIT + 1 }, (_, i) => `k${i}`) });
  eq([many.kind, many.keys], ["cells", undefined], "a range too big for keys sends none");
  eq(readFrameMessage({ type: "preview.units", id: "r1", units: [{ key: "s1", label: "Slide 1", text: "x", at: "slide:1" }], truncated: false })?.type, "preview.units", "a unit's place");
  eq(readHostMessage({ type: "preview.reveal", at: "Revenue!C3" }), { type: "preview.reveal", at: "Revenue!C3" }, "reveal");
  eq(readHostMessage({ type: "preview.reveal", at: "" }), null, "an empty reveal");
});

test("save: the edit capability, and a save is a non-empty buffer within SAVE_LIMIT", () => {
  eq(PREVIEWER_CAPABILITIES.includes("edit"), true, "edit is a code-owned capability");
  eq(readFrameMessage({ type: "preview.save", bytes: png(16) }), { type: "preview.save", bytes: png(16) }, "a save without a summary");
  eq(readFrameMessage({ type: "preview.save", bytes: png(16), summary: "shorter title" })?.type, "preview.save", "a save with a summary");
  eq(readFrameMessage({ type: "preview.save", bytes: png(16), summary: "" })?.type, "preview.save", "an empty summary is allowed");
  eq(readFrameMessage({ type: "preview.save", bytes: new ArrayBuffer(0) }), null, "an empty save is refused");
  eq(readFrameMessage({ type: "preview.save", bytes: new Uint8Array(png(16)) }), null, "a view is not an ArrayBuffer");
  eq(readFrameMessage({ type: "preview.save" }), null, "a save needs bytes");
  eq(readFrameMessage({ type: "preview.save", bytes: png(16), summary: 3 }), null, "the summary must be text");
  eq(readFrameMessage({ type: "preview.save", bytes: png(16), summary: "x".repeat(SAVE_SUMMARY_LIMIT + 1) }), null, "a summary over its limit");
  eq(readFrameMessage({ type: "preview.save", bytes: new ArrayBuffer(SAVE_LIMIT + 1) }), null, "a save over SAVE_LIMIT");
  eq(readFrameMessage({ type: "preview.save", bytes: new ArrayBuffer(SAVE_LIMIT) })?.type, "preview.save", "a save at SAVE_LIMIT");
});

test("save answers: a saved version is a positive integer; a failure is bounded text", () => {
  eq(readHostMessage({ type: "preview.saved", version: 4 }), { type: "preview.saved", version: 4 }, "saved");
  eq(readHostMessage({ type: "preview.saved", version: 0 }), null, "version 0 is not a version");
  eq(readHostMessage({ type: "preview.saved", version: 1.5 }), null, "a version is an integer");
  eq(readHostMessage({ type: "preview.saved", version: "4" }), null, "a version is a number");
  eq(readHostMessage({ type: "preview.save.failed", message: "changed meanwhile" }), { type: "preview.save.failed", message: "changed meanwhile" }, "failed");
  eq(readHostMessage({ type: "preview.save.failed", message: "" }), null, "a failure needs a message");
  eq(readHostMessage({ type: "preview.save.failed", message: "x".repeat(ERROR_LIMIT + 1) }), null, "a failure over ERROR_LIMIT");
  eq(readFrameMessage({ type: "preview.saved", version: 4 }), null, "saved is a host message, not a frame one");
});

test("draft: the draft capability, a trimmed prompt within DRAFT_PROMPT_LIMIT", () => {
  eq(PREVIEWER_CAPABILITIES.includes("draft"), true, "draft is a known capability");
  eq(readFrameMessage({ type: "preview.draft", prompt: "  a login flow  " }), { type: "preview.draft", prompt: "a login flow" }, "a prompt is trimmed");
  eq(readFrameMessage({ type: "preview.draft", prompt: "   " }), null, "a blank prompt is refused");
  eq(readFrameMessage({ type: "preview.draft" }), null, "a draft needs a prompt");
  eq(readFrameMessage({ type: "preview.draft", prompt: 3 }), null, "the prompt must be text");
  eq(readFrameMessage({ type: "preview.draft", prompt: "x".repeat(DRAFT_PROMPT_LIMIT + 1) }), null, "a prompt over its limit");
  eq(readFrameMessage({ type: "preview.draft", prompt: "x".repeat(DRAFT_PROMPT_LIMIT) })?.type, "preview.draft", "a prompt at its limit");
  eq(readHostMessage({ type: "preview.draft", prompt: "a" }), null, "a draft request is a frame message, not a host one");
});

test("draft answers: a reply is a JSON object within DRAFT_REPLY_LIMIT; a failure is bounded text", () => {
  const reply = { title: "Login", nodes: [{ id: "a", label: "Sign in" }], edges: [] };
  eq(readHostMessage({ type: "preview.drafted", reply }), { type: "preview.drafted", reply }, "a drafted reply");
  eq(readHostMessage({ type: "preview.drafted", reply: [1, 2] }), null, "a reply is an object, not a list");
  eq(readHostMessage({ type: "preview.drafted", reply: "text" }), null, "a reply is not text");
  eq(readHostMessage({ type: "preview.drafted" }), null, "a reply is required");
  eq(readHostMessage({ type: "preview.drafted", reply: { big: "x".repeat(DRAFT_REPLY_LIMIT) } }), null, "a reply over DRAFT_REPLY_LIMIT");
  eq(readHostMessage({ type: "preview.draft.failed", message: "Assists are off here." }), { type: "preview.draft.failed", message: "Assists are off here." }, "a failure");
  eq(readHostMessage({ type: "preview.draft.failed", message: "" }), null, "a failure needs a message");
  eq(readHostMessage({ type: "preview.draft.failed", message: "x".repeat(ERROR_LIMIT + 1) }), null, "a failure over ERROR_LIMIT");
  eq(readFrameMessage({ type: "preview.drafted", reply }), null, "drafted is a host message, not a frame one");
});

