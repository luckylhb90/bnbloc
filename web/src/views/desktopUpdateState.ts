import type { DesktopUpdateProgress } from "@/lib/desktop";

let updateInFlight = false;

export function enterUpdateWork(): (() => void) | null {
  if (updateInFlight) return null;
  updateInFlight = true;
  return () => {
    updateInFlight = false;
  };
}

export function errorMessage(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback;
}

export function progressLabel(progress: DesktopUpdateProgress | null): string | null {
  if (!progress) return null;
  if (progress.total && progress.total > 0) {
    const pct = Math.round((progress.downloaded / progress.total) * 100);
    return `${pct}% downloaded`;
  }
  return `${Math.round(progress.downloaded / 1024 / 1024)} MB downloaded`;
}
