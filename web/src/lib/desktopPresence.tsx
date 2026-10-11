import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api, type ReleaseFile } from "@/lib/api";
import { inDesktopShell } from "@/lib/config";
import { openExternal } from "@/lib/desktop";
import { isPhoneBrowser } from "@/lib/desktopHandoff";
import { afterReleaseDownload } from "@/lib/macInstall";
import { useDesktopDownload } from "@/lib/useDesktopDownload";

// How often a browser re-checks whether the member's desktop bridge is online,
// and the re-checks after "Open desktop app" (the app needs a few seconds to
// start its bridge and heartbeat).
const POLL_MS = 60_000;
const OPEN_RECHECKS_MS = [3_000, 8_000, 15_000] as const;

export interface DesktopPresence {
  // True in a desktop-capable browser (not the desktop shell, not a phone)
  // with a signed-in member: the only place desktop guidance makes sense.
  readonly applicable: boolean;
  // Undefined until the first successful check.
  readonly ownAgents: readonly { online: boolean }[] | undefined;
  // The member's desktop app is running and connected (its bridge is online,
  // with or without an agent core).
  readonly connected: boolean;
  // A successful check found the desktop app not connected.
  readonly disconnected: boolean;
  // The desktop app connected at least once (so it is installed); undefined
  // until the first successful check.
  readonly installed: boolean | undefined;
  readonly checking: boolean;
  readonly error: string | null;
  // The app was asked to open and the follow-up checks still found it
  // offline: most likely not installed.
  readonly openFailed: boolean;
  readonly file: ReleaseFile | null;
  readonly downloading: boolean;
  readonly open: () => void;
  readonly download: () => void;
  readonly check: () => void;
}

const inactive: DesktopPresence = {
  applicable: false,
  ownAgents: undefined,
  connected: false,
  disconnected: false,
  installed: undefined,
  checking: false,
  error: null,
  openFailed: false,
  file: null,
  downloading: false,
  open: () => undefined,
  download: () => undefined,
  check: () => undefined,
};

const Ctx = createContext<DesktopPresence>(inactive);

export function useDesktopPresence(): DesktopPresence {
  return useContext(Ctx);
}

// DesktopPresenceProvider tracks, app-wide, whether a browser member's own
// desktop bridge is online (GET /v1/my/agents), so every surface — sidebar,
// room banner, empty room — offers to open or install the desktop app from
// one source of truth.
export function DesktopPresenceProvider({ subject, children }: { readonly subject: string | null; readonly children: ReactNode }) {
  const applicable =
    !!subject && !inDesktopShell() && !isPhoneBrowser(typeof navigator === "undefined" ? "" : navigator.userAgent);
  const [ownAgents, setOwnAgents] = useState<readonly { online: boolean }[] | undefined>();
  const [desktop, setDesktop] = useState<{ online: boolean; lastSeenAtMs: number } | undefined>();
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [openPending, setOpenPending] = useState(false);
  const [openFailed, setOpenFailed] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const desktopDownload = useDesktopDownload();
  const generation = useRef(0);
  const timers = useRef<number[]>([]);

  const check = useCallback(async (): Promise<boolean | undefined> => {
    const mine = ++generation.current;
    setChecking(true);
    try {
      const { agents, desktop: app } = await api.myAgents();
      if (mine !== generation.current) return undefined;
      setOwnAgents(agents ?? []);
      setDesktop(app ?? { online: false, lastSeenAtMs: 0 });
      setError(null);
      return !!app?.online;
    } catch (failure) {
      if (mine !== generation.current) return undefined;
      setError(failure instanceof Error ? failure.message : "Could not check the desktop connection");
      return undefined;
    } finally {
      if (mine === generation.current) setChecking(false);
    }
  }, []);

  const clearTimers = useCallback(() => {
    for (const t of timers.current) window.clearTimeout(t);
    timers.current = [];
  }, []);

  useEffect(() => {
    setOwnAgents(undefined);
    setDesktop(undefined);
    setOpenFailed(false);
    if (!applicable) return;
    void check();
    const poll = window.setInterval(() => void check(), POLL_MS);
    const onVisible = () => {
      if (document.visibilityState === "visible") void check();
    };
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("focus", onVisible);
    return () => {
      generation.current += 1;
      window.clearInterval(poll);
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("focus", onVisible);
      clearTimers();
    };
  }, [applicable, check, clearTimers, subject]);

  const open = useCallback(() => {
    void openExternal("bnbloc://open");
    clearTimers();
    setOpenPending(true);
    setOpenFailed(false);
    OPEN_RECHECKS_MS.forEach((delay, i) => {
      timers.current.push(
        window.setTimeout(() => {
          void check().then((online) => {
            if (online) {
              clearTimers();
              setOpenPending(false);
            } else if (i === OPEN_RECHECKS_MS.length - 1) {
              setOpenPending(false);
              setOpenFailed(true);
            }
          });
        }, delay),
      );
    });
  }, [check, clearTimers]);

  const download = useCallback(async () => {
    const file = desktopDownload.file;
    if (!file) return;
    setDownloading(true);
    setError(null);
    try {
      await api.downloadReleaseFile(file);
      afterReleaseDownload(file);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not download the desktop app");
    } finally {
      setDownloading(false);
    }
  }, [desktopDownload.file]);

  const connected = !!desktop?.online;
  const installed = desktop === undefined ? undefined : desktop.lastSeenAtMs > 0;
  const value = useMemo<DesktopPresence>(
    () =>
      applicable
        ? {
            applicable,
            ownAgents,
            connected,
            disconnected: desktop !== undefined && !connected,
            installed,
            checking: checking || openPending,
            error,
            openFailed: openFailed && !connected,
            file: desktopDownload.file,
            downloading,
            open,
            download: () => void download(),
            check: () => void check(),
          }
        : inactive,
    [applicable, ownAgents, connected, installed, checking, openPending, error, openFailed, desktopDownload.file, downloading, open, download, check],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
