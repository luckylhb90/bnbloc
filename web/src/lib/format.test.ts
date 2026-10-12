import { formatBytes, formatDuration, formatUsd } from "./format.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

eq(formatBytes(0), "0 bytes", "zero");
eq(formatBytes(1023), "1023 bytes", "below a KB");
eq(formatBytes(1024), "1.0 KB", "one KB");
eq(formatBytes(1536), "1.5 KB", "fractional");
eq(formatBytes(5 * 1024 * 1024), "5.0 MB", "MB");
eq(formatBytes(3 * 1024 ** 3), "3.0 GB", "GB");
eq(formatBytes(4096 * 1024 ** 3), "4096.0 GB", "caps at GB");

// One money rule: an unreported cost is never $0.00, a small one keeps two
// significant digits.
eq(formatUsd(undefined), "—", "unreported");
eq(formatUsd(0), "$0.00", "zero");
eq(formatUsd(1.234), "$1.23", "two decimals");
eq(formatUsd(0.42), "$0.42", "cents");
eq(formatUsd(0.0042), "$0.0042", "below a cent");
eq(formatUsd(0.0012), "$0.0012", "below a cent, rounded");

eq(formatDuration(850), "850ms", "ms");
eq(formatDuration(4_200), "4.2s", "seconds with a decimal");
eq(formatDuration(12_400), "12s", "seconds");
eq(formatDuration(65_000), "1m 05s", "a minute");
eq(formatDuration(185_000), "3m 05s", "minutes");
eq(formatDuration(3_720_000), "1h 02m", "hours");
eq(formatDuration(-1), "—", "invalid");
