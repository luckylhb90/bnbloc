import type { ContentComponent } from "./contentComponents.ts";
import {
  canShare,
  isPublishedVersion,
  parseMembers,
  promoteCommands,
  publicationStatus,
  requestTitle,
  sharedPrompts,
  shareSummary,
  updateAvailable,
} from "./contentSharing.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function comp(over: Partial<ContentComponent> = {}): ContentComponent {
  return {
    id: "cc_1",
    kind: "expert",
    name: "reviewer",
    displayName: "Reviewer",
    owner: "maya",
    scope: "personal",
    status: "active",
    latestVersion: "1.1.0",
    version: "1.1.0",
    permissions: [],
    requires: {},
    mine: true,
    acked: true,
    uses: 0,
    createdAtMs: 1,
    updatedAtMs: 1,
    ...over,
  };
}

const req = (status: "pending" | "approved" | "declined", version: string, over = {}) => ({
  id: "cpr_1",
  componentId: "cc_1",
  kind: "expert" as const,
  name: "reviewer",
  version,
  requestedBy: "maya",
  status,
  createdAtMs: 1,
  ...over,
});

// Only the author shares their own component; a copy offers an update when
// its author has a newer version.
{
  assert(canShare(comp()), "author shares");
  assert(!canShare(comp({ mine: false, scope: "shared" })), "a copy is not re-shared");
  assert(!canShare(comp({ mine: false, scope: "builtin" })), "built-in");
  assert(updateAvailable(comp({ scope: "shared", mine: false, sourceLatestVersion: "1.2.0" })), "update available");
  assert(!updateAvailable(comp({ scope: "shared", mine: false })), "no newer version");
}

// Members typed in a field: any separator, each once.
eq(parseMembers(" jack, bob;\nmaya  jack\tBOB "), ["jack", "bob", "maya"], "parseMembers");
eq(parseMembers(""), [], "empty");

// The share toast names who got it, who had it and who was skipped.
eq(
  shareSummary({
    version: "1.1.0",
    shared: [
      { member: "jack", componentId: "a", version: "1.1.0", status: "created" },
      { member: "bob", componentId: "b", version: "1.1.0", status: "updated" },
      { member: "kim", componentId: "c", version: "1.2.0", status: "unchanged" },
    ],
    skipped: [{ member: "zed", reason: "not a member of your organization" }],
  }),
  "Shared version 1.1.0 with jack and bob; kim already has it; skipped zed (not a member of your organization).",
  "shareSummary",
);
eq(shareSummary({ version: "1.0.0", shared: [], skipped: [] }), "Nobody to share with.", "empty share");

// The author's publication line.
eq(publicationStatus(comp()), null, "never published");
eq(publicationStatus(comp({ publication: { request: req("pending", "1.0.0") } })), { text: "Version 1.0.0 waits for an admin's approval", tone: "warning" }, "first request");
eq(
  publicationStatus(comp({ publication: { orgVersion: "1.0.0", orgStatus: "active", request: req("pending", "1.1.0") } })),
  { text: "Version 1.1.0 waits for approval; your organization uses 1.0.0", tone: "warning" },
  "re-approval",
);
eq(
  publicationStatus(comp({ publication: { orgVersion: "1.0.0", orgStatus: "active", request: req("declined", "1.1.0", { decisionNote: "too broad" }) } })),
  { text: "Version 1.1.0 was not approved: too broad. Your organization uses 1.0.0", tone: "warning" },
  "declined",
);
eq(
  publicationStatus(comp({ publication: { orgVersion: "1.1.0", orgStatus: "active", request: req("approved", "1.1.0") } })),
  { text: "In your organization (version 1.1.0)", tone: "accent" },
  "approved",
);
eq(publicationStatus(comp({ publication: { orgVersion: "1.1.0", orgStatus: "retired" } })), { text: "Unpublished from your organization", tone: "muted" }, "retired");
assert(isPublishedVersion(comp({ publication: { orgVersion: "1.1.0", orgStatus: "active" } })), "latest is published");
assert(!isPublishedVersion(comp({ publication: { orgVersion: "1.0.0", orgStatus: "active" } })), "older published");

eq(requestTitle({ displayName: "", name: "reviewer", version: "1.2.0" }), "reviewer v1.2.0", "requestTitle");
const cmds = promoteCommands(comp());
assert(cmds[0] === "bnbloc content promote cc_1 --version 1.1.0 --publisher <your-publisher-id>", `promote: ${cmds[0]}`);
assert(cmds[3] === "bnbloc cores install --package reviewer-1.1.0.package.json --signature reviewer-1.1.0.package.json.minisig", `install: ${cmds[3]}`);

// The Plaza shows the organization's and the platform's prompts, newest
// packs first; never a member's own or someone's shared copy.
{
  const prompt = (id: string) => ({ id, title: `T ${id}`, body: `B ${id}`, uses: 0 });
  const packs = [
    comp({ id: "own", kind: "prompt", scope: "personal", prompts: [prompt("a")], updatedAtMs: 9 }),
    comp({ id: "org", kind: "prompt", scope: "org", owner: "jack", prompts: [prompt("b"), prompt("c")], updatedAtMs: 5 }),
    comp({ id: "plat", kind: "prompt", scope: "platform", owner: "", displayName: "House", prompts: [prompt("d")], updatedAtMs: 7 }),
    comp({ id: "copy", kind: "prompt", scope: "shared", prompts: [prompt("e")], updatedAtMs: 8 }),
  ];
  eq(
    sharedPrompts(packs, 10).map((p) => `${p.packId}/${p.id}`),
    ["plat/d", "org/b", "org/c"],
    "sharedPrompts",
  );
  eq(sharedPrompts(packs, 2).length, 2, "sharedPrompts limit");
  eq(sharedPrompts(packs, 1)[0].packName, "House", "pack name");
}
