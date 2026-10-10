import { schemeUrl } from "./schemeUrl.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (a !== b) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// Tauri's own convertFileSrc (tauri scripts/core.js) percent-encodes the path.
const tauriConvert = (windows: boolean) => (path: string, protocol: string) =>
  windows ? `http://${protocol}.localhost/${encodeURIComponent(path)}` : `${protocol}://localhost/${encodeURIComponent(path)}`;

eq(schemeUrl(tauriConvert(false), "bnbloc-previewer", "sprite-sheet/1.0.0/index.html"), "bnbloc-previewer://localhost/sprite-sheet/1.0.0/index.html", "macOS keeps slashes");
eq(schemeUrl(tauriConvert(true), "bnbloc-site", "/v1/sites/T/"), "http://bnbloc-site.localhost/v1/sites/T/", "Windows form, leading slash");
