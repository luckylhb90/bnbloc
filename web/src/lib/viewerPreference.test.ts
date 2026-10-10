import { preferredViewerFor, rememberViewer, VIEWER_PREFERENCE_KEY, type PreferenceStorage } from "./viewerPreference.ts";

// Local runner (the web tsconfig has no node types); see the other *.test.ts.
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

function memory(): PreferenceStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
}

test("a choice is remembered per MIME type and extension", () => {
  const s = memory();
  rememberViewer({ mimeType: "image/png", extension: "png" }, "component.previewer.sprite-sheet", s);
  eq(preferredViewerFor({ mimeType: "image/png", extension: "" }, s), "component.previewer.sprite-sheet", "by mime");
  eq(preferredViewerFor({ mimeType: "", extension: ".PNG" }, s), "component.previewer.sprite-sheet", "by extension");
  eq(preferredViewerFor({ mimeType: "image/jpeg", extension: "jpg" }, s), undefined, "other type");
});

test("MIME wins over extension; octet-stream is not a type", () => {
  const s = memory();
  rememberViewer({ mimeType: "application/json", extension: "json" }, "core.json", s);
  rememberViewer({ mimeType: "", extension: "json" }, "core.code", s);
  eq(preferredViewerFor({ mimeType: "application/json", extension: "json" }, s), "core.json", "mime first");
  rememberViewer({ mimeType: "application/octet-stream", extension: "bin" }, "core.hex", s);
  eq(preferredViewerFor({ mimeType: "application/octet-stream", extension: "" }, s), undefined, "octet-stream ignored");
});

test("default forgets the choice; garbage storage is ignored", () => {
  const s = memory();
  rememberViewer({ mimeType: "image/png", extension: "png" }, "x", s);
  rememberViewer({ mimeType: "image/png", extension: "png" }, null, s);
  eq(preferredViewerFor({ mimeType: "image/png", extension: "png" }, s), undefined, "forgotten");
  s.data.set(VIEWER_PREFERENCE_KEY, "{not json");
  eq(preferredViewerFor({ mimeType: "image/png", extension: "png" }, s), undefined, "garbage");
  eq(preferredViewerFor({ mimeType: "image/png", extension: "png" }, null), undefined, "no storage");
});
