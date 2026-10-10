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
// The guest is written through document.write: exactly one script element.
eq(PDF_GUEST_HTML.split("</script>").length, 2, "single script block");
