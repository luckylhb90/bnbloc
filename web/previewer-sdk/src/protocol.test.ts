import {
  ERROR_LIMIT,
  PREVIEWER_PROTOCOL,
  STATE_LIMIT,
  THUMBNAIL_LIMIT,
  TITLE_LIMIT,
  TOOLBAR_LIMIT,
  SELECTION_DETAIL_LIMIT,
  SELECTION_LABEL_LIMIT,
  SUGGESTION_LABEL_LIMIT,
  SUGGESTION_LIMIT,
  SUGGESTION_PROMPT_LIMIT,
  fitSelection,
  hostOriginFromHash,
  readFrameMessage,
  readHostMessage,
  readSuggestions,
  readToolbar,
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
  const fit = fitSelection("x".repeat(500), "y".repeat(5000));
  eq([[...fit.label].length, [...fit.detail].length, fit.detail.endsWith("…")], [SELECTION_LABEL_LIMIT, SELECTION_DETAIL_LIMIT, true], "fitSelection cuts to the limits");
  eq(readFrameMessage({ type: "preview.selection", selection: fit }) !== null, true, "a fitted selection is accepted");
});

test("suggestions are bounded label/prompt pairs; absent means none", () => {
  eq(readSuggestions(undefined), [], "absent");
  eq(readSuggestions([{ label: "Summarize", prompt: "Summarize the key points." }]), [{ label: "Summarize", prompt: "Summarize the key points." }], "one");
  eq(readSuggestions([{ label: "", prompt: "x" }]), null, "an empty label");
  eq(readSuggestions([{ label: "x", prompt: " " }]), null, "a blank prompt");
  eq(readSuggestions([{ label: "x".repeat(SUGGESTION_LABEL_LIMIT + 1), prompt: "y" }]), null, "a long label");
  eq(readSuggestions([{ label: "x", prompt: "y".repeat(SUGGESTION_PROMPT_LIMIT + 1) }]), null, "a long prompt");
  eq(readSuggestions(Array.from({ length: SUGGESTION_LIMIT + 1 }, (_, i) => ({ label: `s${i}`, prompt: "p" }))), null, "too many");
  eq(readSuggestions("Summarize"), null, "not a list");
});
