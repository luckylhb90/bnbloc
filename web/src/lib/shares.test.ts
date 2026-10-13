import {
  DEFAULT_SHARE_ACCESS,
  expiresLabel,
  shareAccessLabel,
  shareAccessOptions,
  shareRequest,
  shareState,
  shareVersionLabel,
  viewsLabel,
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

test("a link is for the organization unless its creator opts into anyone", () => {
  eq(DEFAULT_SHARE_ACCESS, "org", "the default access");
  const open = shareAccessOptions("Acme", false);
  eq(open[0].value, DEFAULT_SHARE_ACCESS, "the default comes first");
  eq(
    open.map((o) => [o.value, o.label, o.disabled]),
    [
      ["org", "People in Acme (sign-in required)", false],
      ["anyone", "Anyone with the link (no sign-in)", false],
    ],
    "the choices",
  );
  const off = shareAccessOptions("", true);
  eq(off[0].label, "People in your organization (sign-in required)", "an organization without a name");
  eq(off[1].disabled, true, "anyone links switched off by the organization");
  eq(shareAccessLabel("org", "Acme"), "People in Acme", "org label");
  eq(shareAccessLabel("anyone", "Acme"), "Anyone with the link", "anyone label");
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
