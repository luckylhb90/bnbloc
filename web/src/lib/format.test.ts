import { formatBytes } from "./format.ts";

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
