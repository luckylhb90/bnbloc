import { ApiError } from "./errors.ts";
import {
  allowedShareAccess,
  chosenShareAccess,
  DEFAULT_SHARE_ACCESS,
  expiresLabel,
  openShareLink,
  shareAccessChanged,
  shareAccessLabel,
  shareAccessOptions,
  shareBytesWanted,
  shareCopyState,
  shareCreatedHint,
  shareRequest,
  shareState,
  shareVersionLabel,
  viewsLabel,
  type PublicShare,
  type ShareOpenApi,
} from "./shares.ts";

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

const DAY = 86_400_000;
const HOUR = 3_600_000;

test("the dialog's choice becomes the create request", () => {
  eq(shareRequest({ latest: true, version: 4, days: 7, access: "org" }), { version: 0, expiresInHours: 168, access: "org" }, "always latest");
  eq(shareRequest({ latest: false, version: 4, days: 90, access: "anyone" }), { version: 4, expiresInHours: 2160, access: "anyone" }, "pinned, 90 days, anyone");
  eq(shareRequest({ latest: false, version: 2, days: 1, access: "org" }), { version: 2, expiresInHours: 24, access: "org" }, "1 day");
});

test("a link is for the organization unless its creator opts into a wider reach", () => {
  eq(DEFAULT_SHARE_ACCESS, "org", "the default access");
  const all = shareAccessOptions("Acme", ["org", "signed_in", "anyone"]);
  eq(all[0].value, DEFAULT_SHARE_ACCESS, "the default comes first");
  eq(
    all.map((o) => [o.value, o.label, o.disabled]),
    [
      ["org", "People in Acme (sign-in required)", false],
      ["signed_in", "Anyone signed in (any organization)", false],
      ["anyone", "Anyone with the link (no sign-in)", false],
    ],
    "the choices, narrowest reach first",
  );
  for (const o of all) {
    if (!o.hint) throw new Error(`${o.value} has no description`);
  }
  eq(shareAccessOptions("", ["org"])[0].label, "People in your organization (sign-in required)", "an organization without a name");

  // The organization's answer decides what is offered; the org choice is
  // always there.
  const noAnonymous = shareAccessOptions("Acme", ["org", "signed_in"]);
  eq(noAnonymous.map((o) => o.disabled), [false, false, true], "anonymous links off");
  eq(noAnonymous[2].hint, "Your organization has turned off links anyone can open.", "why anyone is off");
  const insideOnly = shareAccessOptions("Acme", ["org"]);
  eq(insideOnly.map((o) => o.disabled), [false, true, true], "outside links off");
  eq(insideOnly[1].hint, "Your organization has turned off links for people outside it.", "why signed_in is off");

  // Until the policy is read, only the default; a choice the organization
  // does not allow falls back to it.
  eq(allowedShareAccess(null), ["org"], "no policy yet");
  eq(allowedShareAccess({ shareAccess: ["org", "signed_in"] }), ["org", "signed_in"], "the policy's answer");
  eq(chosenShareAccess("signed_in", ["org", "signed_in"]), "signed_in", "an allowed choice");
  eq(chosenShareAccess("anyone", ["org", "signed_in"]), "org", "a disallowed choice");
  eq(chosenShareAccess("signed_in", allowedShareAccess(undefined)), "org", "a choice before the policy is read");

  eq(shareAccessLabel("org", "Acme"), "People in Acme", "org label");
  eq(shareAccessLabel("signed_in", "Acme"), "Anyone signed in", "signed_in label");
  eq(shareAccessLabel("anyone", "Acme"), "Anyone with the link", "anyone label");
  eq(shareAccessChanged("signed_in"), "Anyone signed in can open the link now", "the change toast");
});

test("expiry reads in days, then hours", () => {
  eq(expiresLabel(10 * DAY + HOUR, 0), "Expires in 10 days", "days");
  eq(expiresLabel(DAY + HOUR, 0), "Expires in 1 day", "one day");
  eq(expiresLabel(5 * HOUR - 1, 0), "Expires in 5 hours", "hours round up");
  eq(expiresLabel(30 * 60_000, 0), "Expires within the hour", "under an hour");
  eq(expiresLabel(0, 1), "Expired", "past");
});

test("link state: revoked wins over expired", () => {
  eq(shareState({ active: true, expiresAtMs: 10 }, 0), "active", "active");
  eq(shareState({ active: true, expiresAtMs: 10 }, 20), "expired", "clock passed expiry");
  eq(shareState({ active: false, expiresAtMs: 10 }, 0), "expired", "server says inactive");
  eq(shareState({ active: false, expiresAtMs: 10, revokedAtMs: 5 }, 20), "revoked", "revoked");
});

test("labels", () => {
  eq(shareVersionLabel(0), "Latest version", "latest");
  eq(shareVersionLabel(3), "v3", "pinned");
  eq(viewsLabel(1), "1 view", "singular");
  eq(viewsLabel(1200), "1,200 views", "plural");
});

// The share page (PublicSharePage) of a link that needs sign-in: the
// anonymous request answers 401, so without a session it goes to sign in
// (and comes back through lib/loginReturn), with one it opens the link with
// a share ticket. A signed_in link never ends on the "only for members of"
// page: its ticket mint takes any session.
const share: PublicShare = {
  artifact: { kind: "site", title: "Poll" },
  version: { version: 1, mimeType: "application/vnd.bnbloc.site+tar", sizeBytes: 1, createdAtMs: 1 },
  sharedBy: { displayName: "maya" },
  expiresAtMs: 10,
  followsLatest: true,
  siteUrl: "/v1/public/shares/tk_1/site/",
};

function fakeApi(link: { needsSignIn: boolean; orgOnly?: boolean }, calls: string[]): ShareOpenApi {
  return {
    get: async (credential) => {
      calls.push(`get ${credential}`);
      if (credential.startsWith("bsh_") && link.needsSignIn) throw new ApiError(401, "sign in to open this link");
      return share;
    },
    ticket: async (token) => {
      calls.push(`ticket ${token}`);
      if (link.orgOnly) throw new ApiError(403, "This link is only for members of Acme");
      return { ticket: "tk_1" };
    },
  };
}

async function asyncTest(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}

await asyncTest("the share page of a signed_in link: sign in, then the ticket", async () => {
  const calls: string[] = [];
  eq(await openShareLink("bsh_a_b", false, fakeApi({ needsSignIn: true }, calls)), { kind: "signin" }, "no session: sign in");
  eq(calls, ["get bsh_a_b"], "no ticket is asked for without a session");

  calls.length = 0;
  eq(
    await openShareLink("bsh_a_b", true, fakeApi({ needsSignIn: true }, calls)),
    { kind: "ready", share, link: "tk_1", visitor: false },
    "signed in (any organization): opened with the ticket",
  );
  eq(calls, ["get bsh_a_b", "ticket bsh_a_b", "get tk_1"], "the requests");
});

await asyncTest("the share page of an anyone link and an org link outside it", async () => {
  const calls: string[] = [];
  eq(
    await openShareLink("bsh_a_b", false, fakeApi({ needsSignIn: false }, calls)),
    { kind: "ready", share, link: "bsh_a_b", visitor: true },
    "anyone: no sign-in",
  );
  eq(
    await openShareLink("bsh_a_b", true, fakeApi({ needsSignIn: true, orgOnly: true }, [])),
    { kind: "orgOnly", message: "This link is only for members of Acme" },
    "an org link's mint refuses someone outside the organization",
  );
  let thrown: unknown = null;
  await openShareLink("bsh_a_b", false, { ...fakeApi({ needsSignIn: false }, []), get: async () => Promise.reject(new ApiError(404, "share not found")) }).catch((e: unknown) => {
    thrown = e;
  });
  eq(thrown instanceof ApiError && thrown.status === 404, true, "a dead link is thrown (the page's gone state)");
});

test("a link row offers a copy only to its creator, and says when it cannot be", () => {
  eq(shareCopyState({ createdBy: "maya", copyable: true }, "active", "maya"), "copy", "own copyable link");
  eq(shareCopyState({ createdBy: "maya" }, "active", "maya"), "unavailable", "own link whose token was not stored");
  eq(shareCopyState({ createdBy: "maya" }, "active", "jack"), "none", "someone else's link says nothing");
  eq(shareCopyState({ createdBy: "maya", copyable: true }, "revoked", "maya"), "none", "a revoked link");
  eq(shareCopyState({ createdBy: "maya" }, "expired", "maya"), "none", "an expired link");
  eq(shareCopyState({ createdBy: "maya" }, "active", ""), "none", "no signed-in subject");
  eq(shareCreatedHint(true).includes("again"), true, "a copyable link says so");
  eq(shareCreatedHint(false).includes("only once"), true, "a link shown once says so");
});

test("the share page loads bytes beside the previewer lookup only when a built-in already claims the file", () => {
  const builtin = { input: "blob" };
  const site = { input: "site" };
  eq(shareBytesWanted(null, builtin), true, "a built-in claims it before the lookup answers: bytes load now");
  eq(shareBytesWanted(null, null), false, "nobody claims it yet: wait, it may be a large download-only file");
  eq(shareBytesWanted(null, site), false, "a site is the gateway's own frame: no bytes");
  eq(shareBytesWanted(builtin, builtin), true, "a chosen viewer that reads bytes");
  eq(shareBytesWanted({ input: "blob" }, null), true, "a previewer chosen after the lookup reads bytes");
  eq(shareBytesWanted(site, site), false, "a chosen site needs none");
});
