import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api, type ReleaseFile } from "@/lib/api";
import { inDesktopShell } from "@/lib/config";
import { openExternal } from "@/lib/desktop";
import { isPhoneBrowser } from "@/lib/desktopHandoff";
import { afterReleaseDownload } from "@/lib/macInstall";
import { useDesktopDownload } from "@/lib/useDesktopDownload";
import { errorText } from "./errors.ts";
import { useMyAgents } from "./queries";
import { useVisiblePoll } from "./useVisiblePoll";

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
  // The one /v1/my/agents query (lib/queries.ts), polled while the page is
  // visible: the agents surfaces read the same answer.
  const my = useMyAgents(applicable);
  const { refetch } = my;
  useVisiblePoll(() => void refetch(), POLL_MS, applicable);
  const desktop = my.data?.desktop;
  const connected = !!desktop?.online;
  const [openPending, setOpenPending] = useState(false);
  // The last check after "Open desktop app" answered (judged against the
  // answer it brought, in the effect below).
  const [openWaitOver, setOpenWaitOver] = useState(false);
  const [openFailed, setOpenFailed] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const desktopDownload = useDesktopDownload();
  const timers = useRef<number[]>([]);

  const clearTimers = useCallback(() => {
    for (const t of timers.current) window.clearTimeout(t);
    timers.current = [];
  }, []);
  useEffect(() => {
    setOpenFailed(false);
    return clearTimers;
  }, [applicable, subject, clearTimers]);
  // The app came online: whatever "Open desktop app" was waiting for is done.
  // Still offline after the last check: most likely not installed.
  useEffect(() => {
    if (connected) {
      clearTimers();
      setOpenPending(false);
    } else if (openWaitOver) {
      setOpenPending(false);
      setOpenFailed(true);
    }
    setOpenWaitOver(false);
  }, [connected, openWaitOver, clearTimers]);

  const open = useCallback(() => {
    void openExternal("bnbloc://open");
    clearTimers();
    setOpenPending(true);
    setOpenFailed(false);
    OPEN_RECHECKS_MS.forEach((delay, i) => {
      timers.current.push(
        window.setTimeout(() => {
          void refetch().then(() => {
            if (i === OPEN_RECHECKS_MS.length - 1) setOpenWaitOver(true);
          });
        }, delay),
      );
    });
  }, [refetch, clearTimers]);

  const download = useCallback(async () => {
    const file = desktopDownload.file;
    if (!file) return;
    setDownloading(true);
    setDownloadError(null);
    try {
      await api.downloadReleaseFile(file);
      afterReleaseDownload(file);
    } catch (failure) {
      setDownloadError(errorText(failure, "Could not download the desktop app"));
    } finally {
      setDownloading(false);
    }
  }, [desktopDownload.file]);

  const installed = desktop === undefined ? undefined : desktop.lastSeenAtMs > 0;
  const error = downloadError ?? (my.error ? errorText(my.error, "Could not check the desktop connection") : null);
  const value = useMemo<DesktopPresence>(
    () =>
      applicable
        ? {
            applicable,
            ownAgents: my.data?.agents,
            connected,
            disconnected: desktop !== undefined && !connected,
            installed,
            checking: my.fetching || openPending,
            error,
            openFailed: openFailed && !connected,
            file: desktopDownload.file,
            downloading,
            open,
            download: () => void download(),
            check: () => void refetch(),
          }
        : inactive,
    [applicable, my.data, my.fetching, connected, installed, openPending, error, openFailed, desktopDownload.file, downloading, open, download, refetch],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
