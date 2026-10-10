import { expiresLabel, shareRequest, shareState, shareVersionLabel, viewsLabel } from "./shares.ts";

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
  eq(shareRequest({ latest: true, version: 4, days: 7 }), { version: 0, expiresInHours: 168 }, "always latest");
  eq(shareRequest({ latest: false, version: 4, days: 90 }), { version: 4, expiresInHours: 2160 }, "pinned, 90 days");
  eq(shareRequest({ latest: false, version: 2, days: 1 }), { version: 2, expiresInHours: 24 }, "1 day");
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
