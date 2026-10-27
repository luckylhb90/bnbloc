import { createPreviewerClient, type InboundEvent, type PreviewerClient } from "./index.ts";
import { DRAFT_PROMPT_LIMIT, PREVIEWER_PROTOCOL, SAVE_LIMIT, SAVE_SUMMARY_LIMIT, THUMBNAIL_LIMIT } from "./protocol.ts";

// Local runner (no node types in the web tsconfig); same convention as
// web/src/lib/*.test.ts. Async cases are awaited at top level.
async function test(name: string, fn: () => void | Promise<void>): Promise<void> {
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
function throws(fn: () => void, message: string): void {
  try {
    fn();
  } catch {
    return;
  }
  throw new Error(`${message}: did not throw`);
}

const HOST = "https://app.example";

interface Posted {
  readonly message: { readonly type: string } & Record<string, unknown>;
  readonly targetOrigin: string;
  readonly transfer?: readonly unknown[];
}

function harness(hostOrigin = HOST, framed = true) {
  const posted: Posted[] = [];
  const parent = {
    postMessage(message: unknown, targetOrigin: string, transfer?: unknown[]) {
      posted.push({ message: message as Posted["message"], targetOrigin, transfer });
    },
  };
  let listener: ((ev: InboundEvent) => void) | null = null;
  const client: PreviewerClient = createPreviewerClient({
    parent: framed ? parent : null,
    hostOrigin,
    addMessageListener: (l) => {
      listener = l;
    },
  });
  const deliver = (data: unknown, opts: { origin?: string; source?: unknown } = {}) =>
    listener?.({ origin: opts.origin ?? HOST, source: "source" in opts ? opts.source : parent, data });
  return { client, posted, deliver, hasListener: () => listener !== null };
}

const init = {
  type: "preview.init",
  protocol: PREVIEWER_PROTOCOL,
  file: { name: "hero.png", mimeType: "image/png", sizeBytes: 3, version: 1 },
  theme: "light",
  viewport: { w: 800, h: 600 },
};

function png(size: number): ArrayBuffer {
  const bytes = new Uint8Array(size);
  bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  return bytes.buffer;
}

await test("ready is posted once, to the host origin", () => {
  const h = harness();
  h.client.announceReady();
  h.client.announceReady();
  eq(h.posted, [{ message: { type: "preview.ready" }, targetOrigin: "*" }], "one ready");
});

await test("preview.init tells a viewer whether it is for looking only", () => {
  const h = harness();
  const seen: (boolean | undefined)[] = [];
  h.client.onInit((i) => seen.push(i.viewOnly));
  h.deliver(init);
  h.deliver({ ...init, viewOnly: true });
  h.deliver({ ...init, viewOnly: "yes" });
  eq(seen, [undefined, true], "absent stays absent, true is carried, a wrong type is refused");
});

await test("without a host origin or a parent the client is inert", () => {
  for (const h of [harness(""), harness(HOST, false)]) {
    eq(h.client.active, false, "inactive");
    eq(h.hasListener(), false, "no listener");
    h.client.announceReady();
    h.client.setTitle("x");
    eq(h.posted.length, 0, "nothing posted");
  }
});

await test("messages from another origin or window are refused", () => {
  const h = harness();
  const seen: string[] = [];
  h.client.onInit((i) => seen.push(i.file.name));
  h.deliver(init, { origin: "https://evil.example" });
  h.deliver(init, { origin: "null" });
  h.deliver(init, { source: {} });
  h.deliver(init, { source: null });
  eq(seen, [], "nothing accepted");
  h.deliver(init);
  eq(seen, ["hero.png"], "the parent at the host origin is accepted");
});

await test("unknown or malformed host messages are ignored", () => {
  const h = harness();
  const seen: unknown[] = [];
  h.client.onToolbar((id, value) => seen.push([id, value]));
  h.deliver({ type: "preview.nope" });
  h.deliver({ type: "preview.toolbar", id: "Bad Id", value: null });
  h.deliver("preview.toolbar");
  h.deliver({ type: "preview.toolbar", id: "fit", value: null });
  eq(seen, [["fit", null]], "only the valid gesture");
});

await test("a file that arrives before onFile is buffered and delivered with its metadata", () => {
  const h = harness();
  h.deliver(init);
  h.deliver({ type: "preview.file", bytes: new Uint8Array([1, 2, 3]).buffer });
  const files: unknown[] = [];
  h.client.onFile((bytes, file) => files.push([new Uint8Array(bytes).length, file.name]));
  eq(files, [[3, "hero.png"]], "buffered file delivered on registration");
  h.client.onFile(() => files.push("late"));
  eq(files.length, 1, "a delivered file is not replayed");
});

await test("a file that arrives before init waits for it", () => {
  const h = harness();
  const files: string[] = [];
  h.client.onFile((_bytes, file) => files.push(file.mimeType));
  h.deliver({ type: "preview.file", bytes: new ArrayBuffer(1) });
  eq(files, [], "no metadata yet");
  h.deliver(init);
  eq(files, ["image/png"], "delivered once init arrives");
});

await test("late onInit handlers see the last init; resize updates the viewport", () => {
  const h = harness();
  h.deliver(init);
  h.deliver({ type: "preview.resize", w: 300, h: 200 });
  const seen: unknown[] = [];
  h.client.onInit((i) => seen.push(i.viewport));
  eq(seen, [{ w: 300, h: 200 }], "replayed with the resized viewport");
  const sizes: unknown[] = [];
  h.client.onResize((v) => sizes.push(v));
  h.deliver({ type: "preview.resize", w: 10, h: 20 });
  eq(sizes, [{ w: 10, h: 20 }], "resize handler");
});

await test("state flows both ways with the size limit enforced on send", () => {
  const h = harness();
  const remote: unknown[] = [];
  const off = h.client.onState((state, actor) => remote.push([state, actor]));
  h.deliver({ type: "preview.state", state: { grid: true }, actor: "bob" });
  off();
  h.deliver({ type: "preview.state", state: { grid: false }, actor: "bob" });
  eq(remote, [[{ grid: true }, "bob"]], "unsubscribed handlers stop receiving");
  h.client.sendState({ grid: true });
  eq(h.posted.at(-1)?.message, { type: "preview.state.send", state: { grid: true } }, "state sent");
  throws(() => h.client.sendState("x".repeat(20 * 1024)), "oversized state");
});

await test("setToolbar validates before posting", () => {
  const h = harness();
  const item = { id: "grid", icon: "grid", label: "Grid", kind: "toggle", active: true } as const;
  h.client.setToolbar([item]);
  eq(h.posted.at(-1)?.message, { type: "preview.toolbar.set", items: [item] }, "posted");
  throws(() => h.client.setToolbar([item, item]), "duplicate ids");
  throws(() => h.client.setToolbar([{ ...item, icon: "rocket" as never }]), "unknown icon");
  eq(h.posted.length, 1, "invalid toolbars are never posted");
});

await test("provideThumbnail requires a bounded PNG and transfers it", async () => {
  const h = harness();
  const ok = png(100);
  await h.client.provideThumbnail(ok);
  const last = h.posted.at(-1);
  eq(last?.message.type, "preview.thumbnail", "posted");
  eq(last?.transfer?.length, 1, "buffer transferred");
  let refused = 0;
  for (const bad of [new ArrayBuffer(100), png(THUMBNAIL_LIMIT + 1)]) {
    await h.client.provideThumbnail(bad).catch(() => refused++);
  }
  eq(refused, 2, "non-PNG and oversized refused");
  const blob = new Blob([new Uint8Array(png(50))], { type: "image/png" });
  await h.client.provideThumbnail(blob);
  eq(h.posted.at(-1)?.message.type, "preview.thumbnail", "a Blob is accepted");
});

await test("title is bounded; errors are clamped, never thrown", () => {
  const h = harness();
  h.client.setTitle("hero.png — 64×64");
  throws(() => h.client.setTitle("x".repeat(121)), "long title");
  h.client.reportError("x".repeat(900));
  const err = h.posted.at(-1)?.message as { message: string };
  eq(err.message.length, 500, "clamped to ERROR_LIMIT");
  h.client.reportError("   ");
  eq((h.posted.at(-1)?.message as { message: string }).message, "previewer error", "blank becomes a generic message");
});

await test("units: a request waits for its handler, the answer is fitted, a throw is reported", async () => {
  const h = harness();
  const req = (id: string) => ({ type: "preview.units.request", id, file: { name: "deck.pptx", mimeType: "x/y", sizeBytes: 3, version: 2 }, bytes: new ArrayBuffer(3) });
  h.deliver(req("r1"));
  eq(h.posted.length, 0, "no handler yet: nothing answered");
  h.client.onUnits((_bytes, file) => [{ key: "s1", label: `Slide 1 of ${file.name}`, text: "x".repeat(5000) }]);
  await new Promise((r) => setTimeout(r, 0));
  const answer = h.posted.at(-1)?.message as { type: string; id: string; units: { key: string; label: string; text: string }[]; truncated: boolean };
  eq([answer.type, answer.id, answer.units[0].key, answer.units[0].label, [...answer.units[0].text].length, answer.truncated], ["preview.units", "r1", "s1", "Slide 1 of deck.pptx", 4000, false], "fitted answer");
  h.client.onUnits(() => {
    throw new Error("not a deck");
  });
  h.deliver(req("r2"));
  await new Promise((r) => setTimeout(r, 0));
  eq(h.posted.at(-1)?.message, { type: "preview.units", id: "r2", error: "not a deck" }, "error reported");
  h.deliver({ ...req("bad id!") });
  await new Promise((r) => setTimeout(r, 0));
  eq((h.posted.at(-1)?.message as { id: string }).id, "r2", "a malformed request is ignored");
});

await test("draft: the prompt is trimmed and posted; a bad prompt throws; answers reach their handlers", () => {
  const h = harness();
  h.client.requestDraft("  a login flow with a reset branch  ");
  const sent = h.posted.at(-1);
  eq(sent?.message, { type: "preview.draft", prompt: "a login flow with a reset branch" }, "a draft request is posted, trimmed");
  throws(() => h.client.requestDraft("   "), "a blank prompt throws");
  throws(() => h.client.requestDraft("x".repeat(DRAFT_PROMPT_LIMIT + 1)), "a prompt over DRAFT_PROMPT_LIMIT throws");
  const drafts: unknown[] = [];
  const failed: string[] = [];
  h.client.onDrafted((r) => drafts.push(r));
  h.client.onDraftFailed((m) => failed.push(m));
  h.deliver({ type: "preview.drafted", reply: { title: "Login", nodes: [], edges: [] } });
  h.deliver({ type: "preview.draft.failed", message: "Assists are off here." });
  eq([drafts, failed], [[{ title: "Login", nodes: [], edges: [] }], ["Assists are off here."]], "answers reach their handlers");
});

await test("save: the bytes are transferred with the summary cut; a bad save throws; answers reach their handlers", () => {
  const h = harness();
  const bytes = new ArrayBuffer(12);
  h.client.savePreview(bytes, "x".repeat(SAVE_SUMMARY_LIMIT + 5));
  const sent = h.posted.at(-1);
  eq(sent?.message.type, "preview.save", "a save is posted");
  eq(sent?.targetOrigin, "*", "outbound messages use the same target as the other messages");
  eq(sent?.transfer?.[0] === bytes, true, "the bytes are transferred, not copied");
  eq([...((sent?.message.summary as string) ?? "")].length, SAVE_SUMMARY_LIMIT, "the summary is cut to its limit");
  h.client.savePreview(new ArrayBuffer(3));
  eq((h.posted.at(-1)?.message as { summary?: string }).summary, undefined, "a save without a summary has none");
  throws(() => h.client.savePreview(new ArrayBuffer(0)), "an empty save throws");
  throws(() => h.client.savePreview(new ArrayBuffer(SAVE_LIMIT + 1)), "a save over SAVE_LIMIT throws");
  const saved: number[] = [];
  const failed: string[] = [];
  h.client.onSaved((v) => saved.push(v));
  h.client.onSaveFailed((m) => failed.push(m));
  h.deliver({ type: "preview.saved", version: 5 });
  h.deliver({ type: "preview.save.failed", message: "changed meanwhile" });
  eq([saved, failed], [[5], ["changed meanwhile"]], "answers reach their handlers");
});

