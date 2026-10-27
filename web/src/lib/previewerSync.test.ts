import { FRAME_TICKET_SLACK_MS, type PreviewerDescriptor } from "./previewerFrame.ts";
import { createPreviewerSync, settledWithin, type PreviewerSource } from "./previewerSync.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
async function test(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

/** A catalog body listing the named previewers, each with a frame ticket that expires at expires. */
function body(names: readonly string[], expires = 10_000_000_000) {
  return {
    components: names.map((name) => ({
      kind: "previewer",
      name,
      version: "1.0.0",
      description: "",
      installed: true,
      previewer: {
        entry: "index.html",
        match: { mimeTypes: [], extensions: [".x"] },
        rank: 50,
        capabilities: ["toolbar"],
        toolbar: [],
        bundleUrl: "",
        bundleSha256: "abc",
        frameUrl: `/v1/previewers/${name}/1.0.0/T.sig/`,
        frameExpiresAtMs: expires,
      },
    })),
  };
}

/** A source answering from a queue of bodies (or failures), counting its reads. */
function source(...answers: (unknown | Error)[]): PreviewerSource & { reads: number } {
  const s = {
    reads: 0,
    read() {
      s.reads += 1;
      const next = answers.length > 1 ? answers.shift() : answers[0];
      return next instanceof Error ? Promise.reject(next) : Promise.resolve(next);
    },
  };
  return s;
}

function harness(initial: PreviewerSource) {
  const have = new Map<string, PreviewerDescriptor>();
  const sync = createPreviewerSync(
    initial,
    (descriptors) => {
      have.clear();
      for (const d of descriptors) have.set(d.name, d);
    },
    (name) => have.get(name),
  );
  return { sync, have };
}

await test("the member source fills the registry, and a share source replaces it for the share page", async () => {
  const member = source(body(["pptx", "docx"]));
  const share = source(body(["excalidraw"]));
  const h = harness(member);
  await h.sync.sync();
  eq([...h.have.keys()], ["pptx", "docx"], "the member catalog");
  const restore = h.sync.setSource(share);
  await h.sync.sync();
  eq([...h.have.keys()], ["excalidraw"], "the share's list replaces it");
  eq(member.reads, 1, "the member source is not read on the share page");
  restore();
  await h.sync.sync();
  eq([...h.have.keys()], ["pptx", "docx"], "restoring gives the member source back");
});

await test("a ticket near its expiry is renewed from the active source, not the member catalog", async () => {
  const member = source(body(["pptx"]));
  const share = source(body(["excalidraw"], 5_000), body(["excalidraw"], 99_000_000_000));
  const h = harness(member);
  h.sync.setSource(share);
  await h.sync.sync();
  eq(h.have.get("excalidraw")?.frameExpiresAtMs, 5_000, "the share's first list carries a short ticket");
  const fresh = await h.sync.fresh("excalidraw", 5_000 - FRAME_TICKET_SLACK_MS + 1);
  eq(fresh?.frameExpiresAtMs, 99_000_000_000, "renewal read the share source again");
  eq(share.reads, 2, "two reads of the share source");
  eq(member.reads, 0, "the member catalog was never asked");
  const again = await h.sync.fresh("excalidraw", 1_000);
  eq(again?.frameExpiresAtMs, 99_000_000_000, "a valid ticket is returned without a read");
  eq(share.reads, 2, "no extra read");
});

await test("a previewer the source no longer lists is gone after a renewal", async () => {
  const share = source(body(["excalidraw"], 1_000), body([]));
  const h = harness(share);
  await h.sync.sync();
  eq(await h.sync.fresh("excalidraw", 2_000), undefined, "the sharer stopped offering it");
});

await test("callers of one source share a read; a read for a replaced source is ignored", async () => {
  const slow: PreviewerSource & { release(): void; reads: number } = (() => {
    let release = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    const s = {
      reads: 0,
      release: () => release(),
      read() {
        s.reads += 1;
        return gate.then(() => body(["stale"]));
      },
    };
    return s;
  })();
  const fresh = source(body(["current"]));
  const h = harness(slow);
  const a = h.sync.sync();
  const b = h.sync.sync();
  eq(slow.reads, 1, "two callers, one read");
  h.sync.setSource(fresh);
  await h.sync.sync();
  slow.release();
  await Promise.all([a, b]);
  eq([...h.have.keys()], ["current"], "the answer of the replaced source did not overwrite the active one");
});

await test("a failed read rejects and applies nothing, and the next read tries again", async () => {
  const flaky = source(new Error("offline"), body(["pptx"]));
  const h = harness(flaky);
  let failed = false;
  await h.sync.sync().catch(() => {
    failed = true;
  });
  eq(failed, true, "the failure reaches the caller");
  eq([...h.have.keys()], [], "nothing was applied");
  await h.sync.sync();
  eq([...h.have.keys()], ["pptx"], "the next read applied");
});

await test("restoring a source that was already replaced changes nothing", async () => {
  const member = source(body(["pptx"]));
  const h = harness(member);
  const restoreFirst = h.sync.setSource(source(body(["a"])));
  h.sync.setSource(source(body(["b"])));
  restoreFirst();
  await h.sync.sync();
  eq([...h.have.keys()], ["b"], "the second source stays active");
});

await test("a lookup the page must not wait on forever settles at its limit, and early when it answers", async () => {
  const started = Date.now();
  await settledWithin(new Promise(() => {}), 30);
  eq(Date.now() - started >= 25, true, "a lookup that never answers settles at the limit");
  const quick = Date.now();
  await settledWithin(Promise.resolve(1), 5000);
  eq(Date.now() - quick < 1000, true, "an answered lookup settles at once");
  await settledWithin(Promise.reject(new Error("x")), 5000);
});
