import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { ArrowClockwiseIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { inDesktopShell } from "@/lib/config";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { entryScriptFromHtml, runningEntryScript } from "@/lib/webBuild";

// How often a visible browser tab checks whether a newer web build is deployed.
const CHECK_MS = 5 * 60_000;

// A lazy import that failed is proof the page is out of date, faster than the
// poll below: whoever sees one (the viewer boundary, vite's preload error)
// reports it here, and the notice shows.
let reportedStale = false;
const reportListeners = new Set<() => void>();

export function reportStaleBuild(): void {
  if (reportedStale) return;
  reportedStale = true;
  for (const l of reportListeners) l();
}

const subscribeReports = (l: () => void): (() => void) => {
  reportListeners.add(l);
  return () => reportListeners.delete(l);
};

// useWebUpdate reports whether the gateway now serves a different web build
// than this tab is running, or a lazy import of this page already failed.
// The poll is browser only: the desktop shell embeds its bundle and has its
// own updater.
export function useWebUpdate(signedIn: boolean): boolean {
  const [stale, setStale] = useState(false);
  const reported = useSyncExternalStore(subscribeReports, () => reportedStale);
  const enabled = signedIn && !inDesktopShell() && !stale && !reported;
  const check = useCallback(async () => {
    const running = runningEntryScript(document);
    if (!running) return; // dev server: no hashed entry to compare
    try {
      const res = await fetch(new URL("./", window.location.href), { cache: "no-store" });
      if (!res.ok) return;
      const deployed = entryScriptFromHtml(await res.text());
      if (deployed && deployed !== running) setStale(true);
    } catch {
      // Offline or mid-deploy: try again on the next tick.
    }
  }, []);
  useVisiblePoll(() => void check(), CHECK_MS, enabled);
  useEffect(() => {
    window.addEventListener("vite:preloadError", reportStaleBuild);
    return () => window.removeEventListener("vite:preloadError", reportStaleBuild);
  }, []);
  return stale || reported;
}

// WebUpdateNotice is the quiet sidebar entry offering a reload; nothing
// reloads on its own, so an unsent draft is never lost.
export function WebUpdateNotice({ collapsed }: { readonly collapsed: boolean }) {
  const label = "New version — reload";
  return (
    <button
      type="button"
      onClick={() => window.location.reload()}
      title="A newer version of bnbloc is available. Reload to use it."
      aria-label={label}
      className={cn(
        "tactile relative flex items-center rounded-[var(--radius)] text-sm text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)]",
        collapsed ? "justify-center p-2" : "gap-2 px-3 py-2",
      )}
    >
      <ArrowClockwiseIcon size={18} weight="bold" className="shrink-0" />
      {collapsed ? (
        <span className="absolute right-1.5 top-1.5 size-2 rounded-full bg-[var(--color-accent)]" />
      ) : (
        <span className="min-w-0 flex-1 truncate text-left">{label}</span>
      )}
    </button>
  );
}
