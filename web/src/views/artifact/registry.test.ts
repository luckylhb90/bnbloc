import { selectViewer, selectViewers, type ArtifactFileInfo, type ArtifactViewer } from "./registry.ts";

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

const load = () => Promise.reject(new Error("not loaded in tests"));
function viewer(id: string, match: ArtifactViewer["match"], extra: Partial<ArtifactViewer> = {}): ArtifactViewer {
  return { id, label: id, match, rank: 10, input: "blob", safety: "safe", source: "builtin", load, ...extra };
}
function file(name: string, mimeType: string, kind = "file", sizeBytes?: number): ArtifactFileInfo {
  const ext = name.includes(".") ? name.split(".").pop()!.toLowerCase() : "";
  return { name, mimeType, extension: ext, kind, sizeBytes, version: 1 };
}

const builtins = [
  viewer("core.image", { mimeTypes: ["image/png", "image/svg+xml", "image/*"], extensions: ["png", "svg"], kinds: ["image"] }),
  viewer("core.code", { extensions: ["ts", "json", "svg", "html"], kinds: ["code", "html"] }),
  viewer("core.json", { mimeTypes: ["application/json"], extensions: ["json"] }),
  viewer("core.app", { mimeTypes: ["text/html"], extensions: ["html"], kinds: ["app"] }),
  viewer("core.pdf", { mimeTypes: ["application/pdf"], extensions: ["pdf"] }, { maxBytes: 100 }),
  viewer("core.text", { mimeTypes: ["text/plain", "text/*"] }, { rank: 20 }),
];
const ids = (list: { viewer: ArtifactViewer }[]) => list.map((c) => c.viewer.id);

test("selection orders candidates and honours choice and size", () => {
  eq(ids(selectViewers(file("a.json", "application/json"), builtins)), ["core.json", "core.code"], "json tree first, code offered");
  eq(ids(selectViewers(file("a.svg", "image/svg+xml"), builtins)), ["core.image", "core.code"], "svg renders as image");
  eq(ids(selectViewers(file("Page", "", "html"), builtins)), ["core.code"], "inline html is source");
  eq(ids(selectViewers(file("a.html", "text/html"), builtins)), ["core.app", "core.code", "core.text"], "html file runs");
  eq(selectViewer(file("a.json", "application/json"), "core.code", builtins)?.id, "core.code", "member choice");
  eq(selectViewer(file("a.json", "application/json"), "core.pdf", builtins)?.id, "core.json", "invalid choice ignored");
  eq(selectViewer(file("big.pdf", "application/pdf", "file", 101), undefined, builtins), null, "too large");
  eq(selectViewer(file("a.bin", "application/octet-stream"), undefined, builtins), null, "unknown type");
});

test("an installed component outranks a built-in by rank, then by source", () => {
  const atlas = viewer("component.atlas", { mimeTypes: ["image/png"], extensions: [".atlas.png"] }, { rank: 5, source: "component" });
  eq(selectViewer(file("hero.png", "image/png"), undefined, [...builtins, atlas])?.id, "component.atlas", "lower rank wins");
  const tie = viewer("component.tie", { mimeTypes: ["image/png"] }, { rank: 10, source: "component" });
  eq(selectViewer(file("hero.png", "image/png"), undefined, [...builtins, tie])?.id, "component.tie", "tie goes to the component");
  const weak = viewer("component.weak", { mimeTypes: ["image/*"] }, { rank: 1, source: "component" });
  eq(selectViewer(file("hero.png", "image/png"), undefined, [...builtins, weak])?.id, "core.image", "specificity before rank");
});

test("a web app (site) opens only in the site viewer", () => {
  const site = viewer("core.site", { mimeTypes: ["application/vnd.bnbloc.site+tar"], kinds: ["site"] }, { input: "site" });
  const all = [...builtins, site];
  eq(ids(selectViewers(file("Todo app", "application/vnd.bnbloc.site+tar", "site"), all)), ["core.site"], "by MIME");
  eq(selectViewer(file("dist", "", "site"), undefined, all)?.id, "core.site", "by kind");
  eq(selectViewer(file("Todo app", "application/vnd.bnbloc.site+tar", "site"), "core.code", all)?.id, "core.site", "no other choice");
});
