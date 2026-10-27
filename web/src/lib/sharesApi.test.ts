import { ApiError } from "./errors.ts";
import { publicShareApi } from "./sharesApi.ts";

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

interface Call {
  readonly url: string;
  readonly init: RequestInit;
}

/** Runs fn with fetch answering from a queue, and returns what was asked. */
async function withFetch(answers: Response[], fn: () => Promise<void>): Promise<Call[]> {
  const calls: Call[] = [];
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: unknown, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    const next = answers.shift();
    if (!next) throw new Error("unexpected request");
    return next;
  }) as typeof fetch;
  try {
    await fn();
  } finally {
    globalThis.fetch = original;
  }
  return calls;
}

await test("a share's previewers are read with no session, from the link's own route", async () => {
  const body = { components: [{ kind: "previewer", name: "x" }] };
  let got: unknown;
  const calls = await withFetch([Response.json(body)], async () => {
    got = await publicShareApi.previewers("tk/1 x");
  });
  eq(got, body, "the catalog body is returned as it came");
  eq(calls.length, 1, "one request");
  eq(calls[0].url.endsWith("/v1/public/shares/tk%2F1%20x/previewers"), true, "the credential is path-escaped");
  eq(calls[0].init.credentials, "omit", "no cookies, no session");
  eq(calls[0].init.referrerPolicy, "no-referrer", "no referrer");
  eq(calls[0].init.cache, "no-store", "no cache");
});

await test("a share's cover is read as a blob from the thumbnail route", async () => {
  let got: Blob | null = null;
  const calls = await withFetch([new Response(new Uint8Array([1, 2, 3]), { headers: { "Content-Type": "image/png" } })], async () => {
    got = await publicShareApi.cover("tk_1");
  });
  eq(calls[0].url.endsWith("/v1/public/shares/tk_1/thumbnail"), true, "the thumbnail route");
  eq(calls[0].init.credentials, "omit", "no session");
  eq(got !== null && (got as Blob).size, 3, "the bytes");
  eq(got !== null && (got as Blob).type, "image/png", "the type");
});

await test("a missing cover or a refused link is an ApiError with its status", async () => {
  for (const status of [404, 401, 403]) {
    let failure: unknown = null;
    await withFetch([new Response(JSON.stringify({ error: "no" }), { status, headers: { "Content-Type": "application/json" } })], async () => {
      await publicShareApi.cover("tk_1").catch((e: unknown) => {
        failure = e;
      });
    });
    eq(failure instanceof ApiError && failure.status, status, `${status} surfaces as its status`);
  }
});
