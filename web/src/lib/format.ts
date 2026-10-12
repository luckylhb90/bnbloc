// Small presentational helpers for the room feed: relative timestamps and
// case-insensitive member de-duplication (dev identities differ only by case).

// relativeTime renders a compact "just now / 3m / 2h / 5d" label, falling back
// to a locale date for anything older than a week.
export function relativeTime(ms: number | undefined, now: number = Date.now()): string {
  if (!ms) return "";
  const diff = Math.max(0, now - ms);
  const s = Math.floor(diff / 1000);
  if (s < 10) return "just now";
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d`;
  return new Date(ms).toLocaleDateString();
}

// exactTime renders the full local timestamp (tooltips, audit rows).
export function exactTime(ms: number | undefined): string {
  if (!ms) return "";
  return new Date(ms).toLocaleString();
}

// formatDate renders the local date of a timestamp ("—" when unset).
export function formatDate(ms: number | undefined): string {
  return ms ? new Date(ms).toLocaleDateString() : "—";
}

// formatDay renders a day as "Oct 12, 2026" (locale: the browser's when unset).
export function formatDay(ms: number, locale?: string): string {
  return new Date(ms).toLocaleDateString(locale, { year: "numeric", month: "short", day: "numeric" });
}

// formatClock renders the local time of day of a timestamp.
export function formatClock(ms: number): string {
  return new Date(ms).toLocaleTimeString();
}

// agoText phrases a past time as a sentence part: "just now", "3 minutes
// ago", "5 hours ago", or "on <date>" past two days.
export function agoText(ms: number, now: number = Date.now()): string {
  const min = Math.max(0, Math.round((now - ms) / 60000));
  if (min < 1) return "just now";
  if (min < 60) return `${min} ${min === 1 ? "minute" : "minutes"} ago`;
  const h = Math.round(min / 60);
  if (h < 48) return `${h} ${h === 1 ? "hour" : "hours"} ago`;
  return `on ${new Date(ms).toLocaleDateString()}`;
}

export function formatBytes(bytes: number): string {
  const units = ["bytes", "KB", "MB", "GB"] as const;
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  if (unitIndex === 0) return `${value} ${units[unitIndex]}`;
  return `${value.toFixed(1)} ${units[unitIndex]}`;
}

// formatUsd renders a dollar amount with two decimals, or two significant
// digits below a cent ($0.0042) so a small cost never reads $0.00. An
// unreported amount (undefined) is "—", never $0.00.
export function formatUsd(v: number | undefined): string {
  if (v === undefined) return "—";
  if (v > 0 && v < 0.01) return `$${v.toPrecision(2)}`;
  return `$${v.toFixed(2)}`;
}

// formatDuration renders a millisecond span compactly: 850ms, 4.2s, 12s,
// 3m 05s, 1h 02m.
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(Math.floor(s % 60)).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

export function compactNumber(value: number): string {
  if (value < 1000) return `${value}`;
  if (value < 1_000_000) return `${trimFixed(value / 1000)}k`;
  return `${trimFixed(value / 1_000_000)}m`;
}

function trimFixed(value: number): string {
  return value.toFixed(1).replace(/\.0$/, "");
}

// dedupeMembers drops repeated member ids, keeping the first of each. Ids
// compare exactly: subjects are case-sensitive, so "maya" and "Maya" are two
// members. Keeps order stable.
export function dedupeMembers(members: string[]): string[] {
  return [...new Set(members)];
}

// sameUser is the one same-subject check of the web client: user ids compare
// exactly (subjects are case-sensitive, as on the server).
export function sameUser(a: string | null | undefined, b: string | null | undefined): boolean {
  return !!a && !!b && a === b;
}
