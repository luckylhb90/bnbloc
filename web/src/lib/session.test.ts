import { authedFetch, errorDetail, getToken, refreshSession, setActingTenant, setReauthHandler, setToken } from "./session.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function reply(status: number): Response {
  return new Response(status === 200 ? "{}" : "nope", { status });
}

async function testConcurrent401sShareOneRefresh(): Promise<void> {
  setToken("old");
  setActingTenant(null);
  let refreshes = 0;
  setReauthHandler(async () => {
    refreshes++;
    await new Promise((r) => setTimeout(r, 5));
    setToken("new");
    return true;
  });
  const seen: string[] = [];
  const fetchImpl = async (_url: string, init: RequestInit) => {
    const auth = new Headers(init.headers).get("Authorization") ?? "";
    seen.push(auth);
    return reply(auth === "Bearer new" ? 200 : 401);
  };
  const results = await Promise.all([
    authedFetch("/v1/a", {}, fetchImpl),
    authedFetch("/v1/b", {}, fetchImpl),
    authedFetch("/v1/c", {}, fetchImpl),
  ]);
  assert(results.every((r) => r.status === 200), "every request succeeds after the refresh");
  assert(refreshes === 1, `three concurrent 401s run ONE refresh (ran ${refreshes})`);
  assert(getToken() === "new", "the refreshed token is installed");
}

async function testForbiddenNeverRefreshes(): Promise<void> {
  setToken("tok");
  let refreshes = 0;
  setReauthHandler(async () => {
    refreshes++;
    return true;
  });
  const res = await authedFetch("/v1/rooms/x", {}, async () => reply(403));
  assert(res.status === 403 && refreshes === 0, "a 403 is an answer, not a stale token");
}

async function testAuthEndpointsNeverRefresh(): Promise<void> {
  let refreshes = 0;
  setReauthHandler(async () => {
    refreshes++;
    return true;
  });
  const res = await authedFetch("/v1/auth/switch-tenant", {}, async () => reply(401));
  assert(res.status === 401 && refreshes === 0, "a session-minting call cannot recurse into a refresh");
}

async function testHeaders(): Promise<void> {
  setToken("tok");
  setActingTenant("t2");
  let headers = new Headers();
  await authedFetch("/v1/x", { headers: { "Content-Type": "application/json" } }, async (_u, init) => {
    headers = new Headers(init.headers);
    return reply(200);
  });
  assert(headers.get("Authorization") === "Bearer tok", "bearer is attached");
  assert(headers.get("X-Bnbloc-Acting-Tenant") === "t2", "acting tenant is attached");
  assert(headers.get("Content-Type") === "application/json", "caller headers are kept");
  setActingTenant(null);
}

async function testNoHandlerMeansNoRefresh(): Promise<void> {
  setReauthHandler(null);
  assert((await refreshSession()) === false, "without a handler a refresh reports failure");
}

async function testErrorDetailNeverShowsARawStatusOrHtml(): Promise<void> {
  const detail = (body: string, status: number) => errorDetail(new Response(body, { status }));
  assert((await detail('{"error":"room not found"}', 404)) === "room not found", "the gateway's JSON error");
  assert((await detail('{"error":"tenant suspended","code":"x"}', 403)) === "tenant suspended", "the error body with a code");
  assert((await detail("404 page not found\n", 404)) === "Request failed (404)", "a router's plain-text body is not the error body");
  assert((await detail('{"message":"nope"}', 400)) === "Request failed (400)", "no other JSON shape is read");
  assert((await detail("", 502)).startsWith("The server is temporarily unavailable (502)"), "an empty 502");
  assert((await detail("<html><body>Bad Gateway</body></html>", 502)).startsWith("The server is temporarily unavailable"), "a proxy HTML page");
  assert((await detail("", 500)) === "Request failed (500)", "an empty 500");
}

await testErrorDetailNeverShowsARawStatusOrHtml();
await testConcurrent401sShareOneRefresh();
await testForbiddenNeverRefreshes();
await testAuthEndpointsNeverRefresh();
await testHeaders();
await testNoHandlerMeansNoRefresh();
