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

// exactTime renders the full local timestamp for tooltips.
export function exactTime(ms: number | undefined): string {
  if (!ms) return "";
  return new Date(ms).toLocaleString();
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

// dedupeMembers folds members that differ only by case (a dev-identity artifact
// where the same person signs in as "maya" and "Maya") to one canonical entry,
// preferring the first-seen spelling. Keeps order stable.
export function dedupeMembers(members: string[]): string[] {
  const seen = new Map<string, string>();
  for (const m of members) {
    const key = m.toLowerCase();
    if (!seen.has(key)) seen.set(key, m);
  }
  return [...seen.values()];
}

// sameUser compares two user ids case-insensitively.
export function sameUser(a: string | null | undefined, b: string | null | undefined): boolean {
  if (!a || !b) return false;
  return a.toLowerCase() === b.toLowerCase();
}
