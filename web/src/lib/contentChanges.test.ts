import { readFileSync } from "node:fs";
import { announceContentChange, changing, contentChanges, touches, type ContentChange } from "./contentChanges.ts";
import { contentApi } from "./contentComponentsApi.ts";
import { contentSharingApi } from "./contentSharingApi.ts";
import { promptPacksApi } from "./promptPacksApi.ts";
import { roomComponentsApi } from "./roomComponentsApi.ts";
import { workGalleryApi } from "./workGalleryApi.ts";
import { emptyContentForm } from "./contentComponents.ts";
import { emptyPromptPackForm } from "./promptPacks.ts";
import type { ContentKind } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// heard collects the changes announced while run runs.
async function heard(run: () => Promise<unknown>): Promise<ContentChange[]> {
  const got: ContentChange[] = [];
  const stop = contentChanges.subscribe((c) => got.push(c));
  try {
    await run();
  } finally {
    stop();
  }
  return got;
}

// A change concerns the lists of its kind; one of unknown kind (an id-only
// call, a use) concerns every list.
{
  assert(touches({ kind: "assist" }, ["assist"]), "an assist change touches the assists");
  assert(!touches({ kind: "skill" }, ["assist", "expert"]), "a skill change leaves the assists and experts alone");
  assert(touches({ kind: null }, ["work"]), "a change of unknown kind touches every list");
}

// changing announces only once the server took the mutation, with the kind
// its answer names; a failed one announces nothing.
{
  eq(await heard(() => changing(Promise.resolve({ component: { kind: "expert" } }), (d) => d.component.kind as ContentKind)), [{ kind: "expert" }], "a save announces its kind");
  eq(await heard(() => changing(Promise.reject(new Error("409")), "skill").catch(() => {})), [], "a failed mutation announces nothing");
}

// A listener that throws is logged and the others still hear the change.
{
  const errors: unknown[] = [];
  const realError = console.error;
  console.error = (...a: unknown[]) => errors.push(a);
  const got: ContentChange[] = [];
  const stopBad = contentChanges.subscribe(() => {
    throw new Error("broken list");
  });
  const stopGood = contentChanges.subscribe((c) => got.push(c));
  try {
    announceContentChange("prompt");
  } finally {
    stopBad();
    stopGood();
    console.error = realError;
  }
  eq(got, [{ kind: "prompt" }], "the next listener heard it");
  assert(errors.length === 1, "the throwing listener was logged");
}

// Every content mutation of the client announces itself (the reported case:
// an organization publication removed, then the personal copy deleted, and
// the Library's assists still listed it). Reads announce nothing.
{
  const realFetch = globalThis.fetch;
  const requests: string[] = [];
  globalThis.fetch = (async (url: string | URL | Request, init?: RequestInit) => {
    const u = String(url);
    requests.push(`${init?.method ?? "GET"} ${u}`);
    let body: unknown = {};
    if (u.includes("/content-requests/")) body = { request: { kind: "skill" } };
    else if (u.includes("/content-components/prompts")) body = { component: { kind: "prompt" }, promptId: "p1", created: true };
    else if (u.includes("/content-components") || u.includes("publish-work")) body = { component: { kind: "assist" }, versions: [] };
    if (init?.method === "DELETE" || u.endsWith("/favorite") || u.includes("/content-uses") || u.includes("/unpublish") || u.includes("/tenants?")) {
      return new Response(null, { status: 204 });
    }
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  const form = emptyContentForm("assist");
  const pack = emptyPromptPackForm();
  const tar = new Blob(["x"]);
  const file = new File(["x"], "skill.tar");
  const cases: readonly [string, () => Promise<unknown>, ContentKind | null][] = [
    ["create", () => contentApi.create(form), "assist"],
    ["new version", () => contentApi.newVersion("cc1", form, "1.0.0"), "assist"],
    ["import", () => contentApi.importArchive(file, { kind: "assist" }), "assist"],
    ["create from an archive", () => contentApi.createArchive(tar), "assist"],
    ["new version from an archive", () => contentApi.newVersionArchive("cc1", tar), "assist"],
    ["delete", () => contentApi.remove("cc1"), null],
    ["turn on", () => contentApi.putUse("member", "cc1"), null],
    ["turn off", () => contentApi.removeUse("member", "cc1"), null],
    ["share", () => contentSharingApi.share("cc1", { members: ["jack"] }), null],
    ["revoke a room share", () => contentSharingApi.revokeRoomShare("cc1", "r1"), null],
    ["update a copy", () => contentSharingApi.updateCopy("cc1"), "assist"],
    ["publish to the organization", () => contentSharingApi.publish("cc1", "org"), null],
    ["withdraw from the organization", () => contentSharingApi.publish("cc1", "personal"), null],
    ["approve", () => contentSharingApi.decide("req1", "approve", ""), "skill"],
    ["unpublish in the organization", () => contentSharingApi.setOrgStatus("cc1", "unpublish"), null],
    ["remove from the organization", () => contentSharingApi.removeOrg("cc1"), null],
    ["unpublish on the platform", () => contentSharingApi.setPlatformStatus("cc1", "unpublish"), null],
    ["remove from the platform", () => contentSharingApi.removePlatform("cc1"), null],
    ["set the platform content's organizations", () => contentSharingApi.setPlatformTenants("find-a-way", "assist", { allTenants: true }), null],
    ["create a prompt pack", () => promptPacksApi.create(pack), "prompt"],
    ["new prompt pack version", () => promptPacksApi.newVersion("cc2", pack, "1.0.0"), "prompt"],
    ["import prompts", () => promptPacksApi.importFile(new File(["# p"], "p.md"), {}), "prompt"],
    ["save as prompt", () => promptPacksApi.savePrompt({ body: "hi" }), "prompt"],
    ["favorite a work", () => workGalleryApi.favorite("cc3", true), "work"],
    ["publish a work", () => workGalleryApi.publish("r1", "art1", {} as never), "work"],
    ["store a work cover", () => workGalleryApi.putCover({ id: "cc3", version: "1" }, new Blob(["p"], { type: "image/png" })), "work"],
    ["draw a work cover", () => workGalleryApi.drawCover({ id: "cc3", version: "1" }), "work"],
    ["a room from a component", () => roomComponentsApi.createRoom("cc4", {} as never), "room"],
  ];
  try {
    for (const [what, run, kind] of cases) eq(await heard(run), [{ kind }], what);
    eq(await heard(() => contentApi.list("assist")), [], "a list read announces nothing");
    eq(await heard(() => contentSharingApi.orgContent()), [], "the organization's content read announces nothing");
    assert(requests.length === cases.length + 2, `every case reached the server once (${requests.length})`);
  } finally {
    globalThis.fetch = realFetch;
  }
}

// A mutation added to a content API module later announces itself too:
// every property of the API objects that writes (a POST, PUT or DELETE)
// goes through changing, unless it changes no content list (named here,
// with why).
{
  const quiet: Readonly<Record<string, string>> = {
    ack: "acknowledges a version for the composer's confirmation; no list shows it",
    recordUse: "counts an insertion, best-effort",
    demo: "starts a sandboxed demo site",
    use: "copies an app into a room (a site, not content)",
    makeSimilar: "drafts a recipe in a room",
    makeCover: "draws a cover for a deliverable not yet published",
  };
  const files = ["contentComponentsApi.ts", "contentSharingApi.ts", "promptPacksApi.ts", "workGalleryApi.ts", "roomComponentsApi.ts"];
  let writes = 0;
  for (const file of files) {
    const src = readFileSync(new URL(`./${file}`, import.meta.url), "utf8");
    const props = src.slice(src.indexOf("Api = {")).split(/\n {2}(?=\w+: )/).slice(1);
    for (const prop of props) {
      const name = prop.slice(0, prop.indexOf(":"));
      if (!/method: (?:"(?:POST|PUT|DELETE)"|on \?)|postBody\(|postArchive\(/.test(prop)) continue;
      writes++;
      assert(prop.includes("changing(") || name in quiet, `${file}: ${name} writes without announcing a content change (lib/contentChanges.ts)`);
    }
  }
  assert(writes >= 30, `the scan found the API objects' writes (${writes})`);
}
