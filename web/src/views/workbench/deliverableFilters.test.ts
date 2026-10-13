import { deliverableFiltered, deliverableMatches, deliverableQuery, type DeliverableFilter } from "./deliverableFilters.ts";
import type { Artifact } from "../../lib/wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const NOW = 10 * 86_400_000;
const art = (over: Partial<Artifact>): Artifact => ({
  artifactId: "a",
  roomId: "r",
  channelId: "",
  kind: "markdown",
  title: "notes",
  currentVersion: 1,
  createdBy: "maya",
  createdAtMs: NOW,
  updatedAtMs: NOW,
  isApp: false,
  ...over,
});
const poll = art({ artifactId: "poll", kind: "site", title: "Friday poll", mimeType: "application/vnd.bnbloc.site+tar", isApp: true });
const page = art({ artifactId: "page", kind: "site", title: "Landing page", mimeType: "application/vnd.bnbloc.site+tar", isApp: false });
const doc = art({ artifactId: "doc", title: "notes.md" });
const all = [poll, page, doc];
const f = (over: Partial<DeliverableFilter>): DeliverableFilter => ({ type: "all", author: "", date: "any", q: "", ...over });
const ids = (filter: DeliverableFilter) => all.filter((a) => deliverableMatches(a, filter, NOW)).map((a) => a.artifactId).join(",");

// "Apps" is the server's isApp: a site that is no app is not one here, and
// the web never looks at the kind or the data to decide.
assert(ids(f({ type: "apps" })) === "poll", `apps: ${ids(f({ type: "apps" }))}`);
assert(deliverableMatches(art({ kind: "markdown", isApp: true }), f({ type: "apps" }), NOW), "isApp alone decides");
assert(ids(f({ type: "web" })) === "poll,page", `web: ${ids(f({ type: "web" }))}`);
assert(ids(f({})) === "poll,page,doc", "all");
assert(ids(f({ type: "apps", q: "landing" })) === "", "apps and a search");
assert(ids(f({ type: "apps", q: "FRIDAY" })) === "poll", "case-blind search");
assert(ids(f({ type: "apps", author: "jack" })) === "", "author");

// The server narrows to apps (so a page holds apps only), images by type.
assert(deliverableQuery(f({ type: "apps" })).apps === true, "the request asks for apps");
assert(deliverableQuery(f({})).apps === undefined, "no apps narrowing by default");
assert(deliverableQuery(f({ type: "images" })).mime === "image/*", "images");
assert(deliverableFiltered(f({ type: "apps" })) && !deliverableFiltered(f({})), "filtered");

// Dates count back from now.
const old = art({ artifactId: "old", updatedAtMs: NOW - 2 * 86_400_000, isApp: true, kind: "site" });
assert(!deliverableMatches(old, f({ type: "apps", date: "day" }), NOW), "older than a day");
assert(deliverableMatches(old, f({ type: "apps", date: "week" }), NOW), "within a week");
