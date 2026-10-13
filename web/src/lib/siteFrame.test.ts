import { SITE_SANDBOX, SITE_TICKET_SLACK_MS, siteFrameUrl, siteGatewayUrl, siteTicketRefreshDelay } from "./siteFrame.ts";

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

// Tauri's own convertFileSrc (tauri scripts/core.js): the whole path is
// percent-encoded, so callers only take the scheme root from it.
const tauriConvert = (windows: boolean) => (path: string, protocol: string) =>
  windows ? `http://${protocol}.localhost/${encodeURIComponent(path)}` : `${protocol}://localhost/${encodeURIComponent(path)}`;
const sameOrigin = (p: string) => p;
const gateway = (p: string) => `https://gw.example.com${p}`;

test("browser frames load the gateway site URL", () => {
  const env = { apiUrl: sameOrigin, baseURI: "https://app.example.com/index.html" };
  eq(siteFrameUrl("/v1/sites/T.k-1/", env), "https://app.example.com/v1/sites/T.k-1/", "same origin");
  eq(
    siteFrameUrl("/v1/sites/T/", { apiUrl: gateway, baseURI: "https://app.example.com/" }),
    "https://gw.example.com/v1/sites/T/",
    "configured gateway",
  );
});

test("desktop frames go through the bnbloc-site scheme with the path verbatim", () => {
  const base = { apiUrl: gateway, baseURI: "tauri://localhost/" };
  eq(
    siteFrameUrl("/v1/sites/abc.def/", { ...base, convertFileSrc: tauriConvert(false) }),
    "bnbloc-site://localhost/v1/sites/abc.def/",
    "macOS/Linux",
  );
  eq(
    siteFrameUrl("/v1/sites/abc/", { ...base, convertFileSrc: tauriConvert(true) }),
    "http://bnbloc-site.localhost/v1/sites/abc/",
    "Windows",
  );
  // A public share site is not a member ticket path: straight to the gateway.
  eq(
    siteFrameUrl("/v1/public/shares/bsh_x/site/", { ...base, convertFileSrc: tauriConvert(false) }),
    "https://gw.example.com/v1/public/shares/bsh_x/site/",
    "public site",
  );
  eq(siteGatewayUrl("/v1/sites/abc/", base), "https://gw.example.com/v1/sites/abc/", "open in new tab");
});

test("a site URL on the sites origin is used as the gateway handed it out", () => {
  const sites = "https://sites.bnbloc.example.com/v1/sites/T/";
  eq(siteFrameUrl(sites, { apiUrl: sameOrigin, baseURI: "https://bnbloc.example.com/" }), sites, "browser");
  eq(siteGatewayUrl(sites, { apiUrl: gateway, baseURI: "tauri://localhost/" }), sites, "open in new tab");
  eq(
    siteFrameUrl(sites, { apiUrl: gateway, baseURI: "tauri://localhost/", convertFileSrc: tauriConvert(false) }),
    "bnbloc-site://localhost/v1/sites/T/",
    "desktop: the scheme, which forwards to the sites origin",
  );
  const pub = "https://sites.bnbloc.example.com/v1/public/shares/bsh_x/site/";
  eq(siteGatewayUrl(pub, { apiUrl: sameOrigin, baseURI: "https://bnbloc.example.com/s/x" }), pub, "a public link's site");
});

test("the sandbox never grants the app origin", () => {
  eq(SITE_SANDBOX.includes("allow-same-origin"), false, "no same origin");
  eq(SITE_SANDBOX.includes("allow-top-navigation"), false, "no top navigation");
});

test("tickets are re-minted before they expire", () => {
  eq(siteTicketRefreshDelay(undefined, 0), null, "no expiry");
  eq(siteTicketRefreshDelay(60 * 60_000, 0), 60 * 60_000 - SITE_TICKET_SLACK_MS, "an hour out");
  eq(siteTicketRefreshDelay(1000, 5000), 0, "already due");
});
