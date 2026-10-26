import { artifactLinkUrl, parseHashRoute, routeHash, shareHash, shareTokenFromHash } from "./hashRoute.ts";

// Local runner (the web tsconfig has no node types); a failure throws.
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

test("views and rooms parse as before", () => {
  eq(parseHashRoute(""), { kind: "view", view: "plaza" }, "empty");
  eq(parseHashRoute("#sessionToken=x&subject=y"), { kind: "view", view: "plaza" }, "sso hash");
  eq(parseHashRoute("#/agents"), { kind: "view", view: "agents" }, "view");
  eq(parseHashRoute("#/board"), { kind: "view", view: "board" }, "the organization board");
  eq(parseHashRoute("#/room/r%201"), { kind: "room", roomId: "r 1" }, "room");
});

test("a message anchor parses into seq and channel", () => {
  eq(parseHashRoute("#/room/r1?seq=42"), { kind: "room", roomId: "r1", seq: 42 }, "seq");
  eq(
    parseHashRoute("#/room/r1?seq=42&channel=c%2F2"),
    { kind: "room", roomId: "r1", seq: 42, channelId: "c/2" },
    "seq + channel",
  );
  eq(parseHashRoute("#/room/r1?seq=abc&channel=c1"), { kind: "room", roomId: "r1" }, "bad seq ignored");
  eq(parseHashRoute("#/room/r1?seq=-3"), { kind: "room", roomId: "r1" }, "negative seq ignored");
  eq(parseHashRoute("#/room/%E0%A4%A"), { kind: "view", view: "plaza" }, "malformed escape");
});

test("routes serialize canonically and round-trip", () => {
  eq(routeHash({ kind: "room", roomId: "r 1" }), "#/room/r%201", "room");
  const anchored = { kind: "room" as const, roomId: "r1", seq: 7, channelId: "c 2" };
  eq(routeHash(anchored), "#/room/r1?seq=7&channel=c+2", "anchor");
  eq(parseHashRoute(routeHash(anchored)), anchored, "round trip");
  eq(routeHash({ kind: "view", view: "plaza" }), "#/", "plaza");
});

test("an artifact deep link parses, serializes and round-trips", () => {
  eq(parseHashRoute("#/room/r1?artifact=art_1"), { kind: "room", roomId: "r1", artifactId: "art_1" }, "artifact");
  eq(
    parseHashRoute("#/room/r1?artifact=art_1&v=3"),
    { kind: "room", roomId: "r1", artifactId: "art_1", artifactVersion: 3 },
    "artifact + version",
  );
  eq(parseHashRoute("#/room/r1?artifact=a%2Fb&v=2"), { kind: "room", roomId: "r1" }, "bad artifact id ignored");
  eq(parseHashRoute("#/room/r1?artifact=a1&v=0"), { kind: "room", roomId: "r1", artifactId: "a1" }, "bad version ignored");
  eq(parseHashRoute("#/room/r1?v=2"), { kind: "room", roomId: "r1" }, "version without artifact ignored");
  const linked = { kind: "room" as const, roomId: "r 1", artifactId: "a1", artifactVersion: 4 };
  eq(routeHash(linked), "#/room/r%201?artifact=a1&v=4", "serialize");
  eq(parseHashRoute(routeHash(linked)), linked, "round trip");
  const both = { kind: "room" as const, roomId: "r1", seq: 5, artifactId: "a1" };
  eq(parseHashRoute(routeHash(both)), both, "message anchor + artifact");
});

test("artifact links are absolute room-member URLs", () => {
  eq(artifactLinkUrl("https://gw.example.com", "r1", "a1"), "https://gw.example.com/#/room/r1?artifact=a1", "gateway base");
  eq(
    artifactLinkUrl("https://gw.example.com/app/#/room/r1?seq=3", "r1", "a1", 2),
    "https://gw.example.com/app/#/room/r1?artifact=a1&v=2",
    "page base drops its own hash",
  );
  eq(artifactLinkUrl("https://h/index.html?x=1", "r1", "a1"), "https://h/index.html#/room/r1?artifact=a1", "document base");
  eq(artifactLinkUrl("https://h/sub", "r1", "a1"), "https://h/sub/#/room/r1?artifact=a1", "directory base");
});

test("the public share route", () => {
  const token = "Ab3dEf6hIj9kLm2nOp5qRs"; // a link's token (22 base62)
  eq(shareHash(token), `#/share/${token}`, "the share page's hash");
  eq(shareTokenFromHash(shareHash(token)), token, "round trip");
  eq(shareTokenFromHash(`#/share/${token}`), token, "parses");
  // An older or unknown token still reaches the page, which the server
  // answers 404: "This link is no longer available".
  const old = "bsh_s1a2b3_Zm9vYmFyLWJhel9xdXV4"; // gitleaks:allow (made-up test token)
  eq(shareTokenFromHash(shareHash(old)), old, "an old link's token reaches the page");
  eq(shareTokenFromHash(`#/share/${token}/`), token, "trailing slash");
  for (const bad of ["", "#/share/", "#/share/short", "#/share/a%2Fb_cdefgh", "#/share/abc/def/ghi", `#/share/${token}?x=1`, "#/room/r1"]) {
    eq(shareTokenFromHash(bad), null, `rejects ${bad}`);
  }
});
