import { appLinkBadge, myAppsQuery } from "./myApps.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const NOW = 1_000_000_000;
const DAY = 86_400_000;

assert(appLinkBadge([], "Acme", NOW).label === "Not shared" && appLinkBadge([], "Acme", NOW).tone === "none", "no link");

const org = appLinkBadge([{ shareId: "s1", access: "org", expiresAtMs: NOW + 3 * DAY, version: 0 }], "Acme", NOW);
assert(org.label === "Org link" && org.tone === "org", `org: ${JSON.stringify(org)}`);
assert(org.title === "People in Acme · Expires in 3 days", `org title: ${org.title}`);

const mixed = appLinkBadge(
  [
    { shareId: "s1", access: "org", expiresAtMs: NOW + 3 * DAY, version: 0 },
    { shareId: "s2", access: "anyone", expiresAtMs: NOW + DAY + 1, version: 2 },
  ],
  "",
  NOW,
);
assert(mixed.label === "Public link +1" && mixed.tone === "anyone", `mixed: ${JSON.stringify(mixed)}`);
assert(mixed.title.split("\n").length === 2 && mixed.title.includes("Anyone with the link · Expires in 1 day"), `mixed title: ${mixed.title}`);
assert(mixed.title.includes("People in your organization"), "an unnamed organization");

const signedIn = appLinkBadge(
  [
    { shareId: "s1", access: "org", expiresAtMs: NOW + 3 * DAY, version: 0 },
    { shareId: "s3", access: "signed_in", expiresAtMs: NOW + 3 * DAY, version: 0 },
  ],
  "Acme",
  NOW,
);
assert(signedIn.label === "Signed-in link +1" && signedIn.tone === "signed_in", `signed_in: ${JSON.stringify(signedIn)}`);
assert(signedIn.title.includes("Anyone signed in · Expires in 3 days"), `signed_in title: ${signedIn.title}`);

assert(myAppsQuery() === "" && myAppsQuery("10:art_a", 24) === "?cursor=10%3Aart_a&limit=24", `query ${myAppsQuery("10:art_a", 24)}`);
