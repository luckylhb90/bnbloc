import assert from "node:assert/strict";
import test from "node:test";
import { uploadArtifact } from "../src/lib/artifactUpload.ts";
import { setActingTenant, setReauthHandler, setToken } from "../src/lib/session.ts";

function session({ token, actingTenant = null, reauth = null }) {
  setToken(token);
  setActingTenant(actingTenant);
  setReauthHandler(reauth);
}

test("artifact upload sends raw file bytes with authenticated gateway headers", async () => {
  const file = new File(["signed-archive"], "bnbloc.app.tar.gz");
  let request;
  session({ token: "session", actingTenant: "tenant-a" });
  const result = await uploadArtifact(file, {
    origin: "http://localhost",
    fetchImpl: async (url, init) => {
      request = { url, init };
      return Response.json({ key: `sha256/${"a".repeat(64)}`, sha256: "a".repeat(64), size: 14, });
    },
  });
  assert.deepEqual(result, { key: `sha256/${"a".repeat(64)}`, sha256: "a".repeat(64), size: 14, url: `http://localhost/v1/artifacts/sha256/${"a".repeat(64)}` });
  assert.equal(request.url, "/v1/platform/artifacts");
  assert.equal(request.init.method, "POST");
  assert.equal(request.init.body, file);
  assert.equal(new Headers(request.init.headers).get("Authorization"), "Bearer session");
  assert.equal(new Headers(request.init.headers).get("X-Bnbloc-Acting-Tenant"), "tenant-a");
});

test("artifact upload refreshes one expired session and retries with its new bearer", async () => {
  const file = new File(["signed-archive"], "bnbloc.app.tar.gz");
  let attempts = 0;
  let refreshes = 0;
  session({
    token: "old-session",
    reauth: async () => {
      refreshes++;
      setToken("new-session");
      return true;
    },
  });
  await uploadArtifact(file, {
    origin: "http://localhost",
    fetchImpl: async (_url, init) => {
      attempts++;
      const auth = new Headers(init.headers).get("Authorization");
      if (attempts === 1) {
        assert.equal(auth, "Bearer old-session");
        return new Response("expired", { status: 401 });
      }
      assert.equal(auth, "Bearer new-session");
      return Response.json({ key: `sha256/${"a".repeat(64)}`, sha256: "a".repeat(64), size: 14 });
    },
  });
  assert.equal(attempts, 2);
  assert.equal(refreshes, 1);
});

test("a 403 is an authorization answer and never triggers a session refresh", async () => {
  const file = new File(["signed-archive"], "bnbloc.app.tar.gz");
  let refreshes = 0;
  session({ token: "session", reauth: async () => { refreshes++; return true; } });
  await assert.rejects(
    uploadArtifact(file, { origin: "http://localhost", fetchImpl: async () => new Response("forbidden", { status: 403 }) }),
    /forbidden/,
  );
  assert.equal(refreshes, 0);
});

test("artifact upload exposes storage errors without attempting publication", async () => {
  const file = new File(["signed-archive"], "bnbloc.app.tar.gz");
  await assert.rejects(
    (session({ token: "session" }), uploadArtifact(file, {
      origin: "http://localhost",
      fetchImpl: async () => new Response("artifact store not configured", { status: 503 }),
    })),
    /artifact store not configured/i,
  );
});

test("artifact upload reports user cancellation distinctly from gateway failure", async () => {
  const file = new File(["signed-archive"], "bnbloc.app.tar.gz");
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(
    uploadArtifact(file, {
      origin: "http://localhost",
      signal: controller.signal,
      fetchImpl: async (_url, init) => {
        throw init.signal.reason;
      },
    }),
    /cancelled/i,
  );
});
