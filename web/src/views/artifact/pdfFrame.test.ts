import { PDF_GUEST_HTML, readPdfMessage } from "./pdfFrame.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

const frame = {};
const from = (data: unknown, origin = "null", source: unknown = frame) => readPdfMessage({ origin, source, data }, frame);
eq(from({ type: "artifact-runner-ready" }), { kind: "runner-ready" }, "runner hand-off");
eq(from({ type: "artifact-ready" }), { kind: "ready" }, "guest ready");
eq(from({ type: "pdf.status", page: 2, pages: 9, zoom: 1.5 }), { kind: "status", page: 2, pages: 9, zoom: 1.5 }, "status");
eq(from({ type: "pdf.status", page: 2, pages: 9, zoom: -1 }), null, "bad zoom");
eq(from({ type: "pdf.status", page: 1.5, pages: 9, zoom: 1 }), null, "bad page");
eq((from({ type: "pdf.error", message: "x".repeat(900) }) as { message: string }).message.length, 500, "error bounded");
eq(from({ type: "pdf.status", page: 1, pages: 1, zoom: 1 }, "https://evil.example"), null, "non-opaque origin");
eq(from({ type: "pdf.status", page: 1, pages: 1, zoom: 1 }, "null", {}), null, "other frame");
eq(from({ type: "pdf.load" }), null, "host-only type");
// The member's text selection, read as bounded text with where it shows.
const rect = { x: 10, y: 20, w: 100, h: 14 };
eq(from({ type: "pdf.selection", text: "Q3 revenue grew", page: 3, rect }), { kind: "selection", text: "Q3 revenue grew", page: 3, rect }, "a selection");
eq(from({ type: "pdf.selection", text: "" }), { kind: "selection", text: "", page: 0, rect: null }, "an empty text clears it");
eq((from({ type: "pdf.selection", text: "x".repeat(9000), page: 1, rect }) as { text: string }).text.length, 4000, "text bounded");
eq(from({ type: "pdf.selection", text: "hi", page: 0, rect }), null, "a page is 1 or more");
eq(from({ type: "pdf.selection", text: "hi", page: 1.5, rect }), null, "a page is a whole number");
eq((from({ type: "pdf.selection", text: "hi", page: 1, rect: { x: 1, y: 2, w: -3, h: 4 } }) as { rect: unknown }).rect, null, "a bad rect is dropped, the selection kept");
eq((from({ type: "pdf.selection", text: "hi", page: 1, rect: { x: 1e12, y: 2, w: 3, h: 4 } }) as { rect: unknown }).rect, null, "an absurd rect is dropped");
eq((from({ type: "pdf.selection", text: "hi", page: 1 }) as { rect: unknown }).rect, null, "no rect");
eq(from({ type: "pdf.selection", text: 3, page: 1, rect }), null, "text must be a string");
eq(from({ type: "pdf.selection", text: "hi", page: 1, rect }, "https://evil.example"), null, "selection from a non-opaque origin");
eq(from({ type: "pdf.selection", text: "hi", page: 1, rect }, "null", {}), null, "selection from another frame");

// A card cover: PNG bytes within the room's limit, from the opaque guest only.
const png = new Uint8Array([137, 80, 78, 71]).buffer;
eq(from({ type: "pdf.thumbnail", png }), { kind: "thumbnail", png }, "a thumbnail");
eq(from({ type: "pdf.thumbnail", png: new ArrayBuffer(0) }), null, "empty");
eq(from({ type: "pdf.thumbnail", png: new ArrayBuffer((512 << 10) + 1) }), null, "over the room's limit");
eq(from({ type: "pdf.thumbnail", png: "AAAA" }), null, "bytes, not text");
eq(from({ type: "pdf.thumbnail", png }, "https://evil.example"), null, "thumbnail from a non-opaque origin");

// The guest is written through document.write: exactly one script element.
eq(PDF_GUEST_HTML.split("</script>").length, 2, "single script block");
// The guest script is generated from a template literal: it must still
// parse (an escape lost there breaks the viewer and Compare alike).
{
  const src = PDF_GUEST_HTML.split("<script>")[1].split("</script>")[0];
  let parsed = true;
  try {
    new Function(src);
  } catch {
    parsed = false;
  }
  eq(parsed, true, "the guest script parses");
}
// Compare's answer: each page a unit, bounded like a previewer's.
eq(
  from({ type: "pdf.units", pages: ["Overview", "Revenue"], truncated: false }),
  { kind: "units", units: [{ label: "Page 1", text: "Overview", at: "page:1" }, { label: "Page 2", text: "Revenue", at: "page:2" }], truncated: false },
  "page units",
);
eq(from({ type: "pdf.units", pages: ["x".repeat(4001)] }), null, "an over-long page");
eq(from({ type: "pdf.units", pages: [1] }), null, "a non-text page");
