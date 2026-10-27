import { componentPageRouteFromHash, componentPageRouteHash } from "./previewRoute.ts";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(msg);
}

// A component page's tab route round-trips its component, page, query and room.
const full = { component: "connections/v1", page: "connect", query: "system=jira&a=b c", roomId: "r 1" };
const back = componentPageRouteFromHash(componentPageRouteHash(full));
assert(JSON.stringify(back) === JSON.stringify(full), `round trip: ${JSON.stringify(back)}`);

// Without query or room the hash carries neither.
const bare = componentPageRouteHash({ component: "connections", page: "connections" });
assert(bare === "#/open-page/connections/connections", bare);
assert(JSON.stringify(componentPageRouteFromHash(bare)) === JSON.stringify({ component: "connections", page: "connections" }), "bare");

// Other hashes are not this route.
assert(componentPageRouteFromHash("#/live/r/j") === null, "live is not a page");
assert(componentPageRouteFromHash("#/open-page/%E0/x") === null, "undecodable");
