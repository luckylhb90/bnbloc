import { useCallback, useEffect, useRef, useState } from "react";
import { apiUrl } from "@/lib/config";
import { openExternal } from "@/lib/desktop";
import {
  SITE_SANDBOX,
  SITE_WIDTHS,
  siteFrameUrl,
  siteGatewayUrl,
  siteTicketRefreshDelay,
  type ConvertFileSrc,
  type SiteFrameEnv,
} from "@/lib/siteFrame";
import { DeviceFrame } from "./DeviceFrame";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";
import { errorText } from "@/lib/errors";

// SiteViewer runs a web app (`site` version: a static snapshot the gateway
// serves file by file) in a sandboxed iframe — opaque origin, no
// allow-same-origin, the gateway's own `sandbox` CSP on every file — with
// reload, device widths and open-in-new-tab. The host hands it a SiteSource
// (workbench: a member site ticket; public share page: the link's site URL),
// so the same viewer serves both. A new version remounts the viewer (the
// workbench keys viewers by version), which is how a tab that follows the
// latest version reloads after each agent turn.
//
// Member tickets last an hour: a fresh one is minted before expiry and used
// by the next load (reload, new version, open in new tab). A running page is
// not reloaded just to swap its ticket.

/** Never re-mint more often than this (a skewed clock must not spin). */
const MIN_REFRESH_MS = 30_000;

type SiteUrl = { readonly siteUrl: string; readonly expiresAtMs?: number };

function frameEnv(): SiteFrameEnv {
  const internals = window.__TAURI_INTERNALS__ as { convertFileSrc?: ConvertFileSrc } | undefined;
  return { convertFileSrc: internals?.convertFileSrc, apiUrl, baseURI: document.baseURI };
}

export default function SiteViewer({ site, file, toolbar }: ArtifactViewerProps) {
  const [width, setWidth] = useState("fill");
  const [frame, setFrame] = useState<{ src: string; nonce: number } | null>(null);
  const [error, setError] = useState("");
  const [fresh, setFresh] = useState<SiteUrl | null>(null);
  const freshRef = useRef<SiteUrl | null>(null);

  const mint = useCallback(async (): Promise<SiteUrl> => {
    if (!site) throw new Error("This web app has no address.");
    const next = await site.resolve();
    freshRef.current = next;
    setFresh(next);
    return next;
  }, [site]);

  // The newest URL unless its ticket is due for re-minting.
  const usable = useCallback(async (): Promise<SiteUrl> => {
    const current = freshRef.current;
    if (current && siteTicketRefreshDelay(current.expiresAtMs, Date.now()) !== 0) return current;
    return mint();
  }, [mint]);

  const load = useCallback(async () => {
    try {
      const url = await usable();
      setError("");
      setFrame((f) => ({ src: siteFrameUrl(url.siteUrl, frameEnv()), nonce: (f?.nonce ?? 0) + 1 }));
    } catch (e) {
      setError(errorText(e, "Could not open this web app."));
    }
  }, [usable]);

  useEffect(() => {
    void load();
  }, [load]);

  // Keep a fresh ticket ready before the current one expires.
  useEffect(() => {
    const delay = siteTicketRefreshDelay(fresh?.expiresAtMs, Date.now());
    if (delay === null) return;
    const timer = window.setTimeout(() => void mint().catch(() => {}), Math.max(delay, MIN_REFRESH_MS));
    return () => window.clearTimeout(timer);
  }, [fresh, mint]);

  const openInNewTab = useCallback(async () => {
    try {
      await openExternal(siteGatewayUrl((await usable()).siteUrl, frameEnv()));
    } catch {
      setError("Could not open this web app in a new tab.");
    }
  }, [usable]);

  useViewerToolbar(
    toolbar,
    [
      { id: "reload", kind: "button", icon: "reload", label: "Reload" },
      { id: "width", kind: "select", icon: "device", label: "Device width", options: SITE_WIDTHS, selected: width },
      { id: "open", kind: "button", icon: "open-external", label: "Open in new tab" },
    ],
    (id, value) => {
      if (id === "reload") void load();
      else if (id === "width" && typeof value === "string") setWidth(value);
      else if (id === "open") void openInNewTab();
    },
  );

  if (error && !frame) {
    return (
      <div className="grid h-full place-items-center p-6 text-center">
        <div className="flex max-w-sm flex-col items-center gap-2">
          <p className="text-sm text-[var(--color-fg-muted)]">{error}</p>
          <button
            type="button"
            onClick={() => void load()}
            className="tactile rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]"
          >
            Try again
          </button>
        </div>
      </div>
    );
  }
  if (!frame) return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Starting web app…</p>;
  return (
    <DeviceFrame width={width}>
      <iframe
        key={frame.nonce}
        src={frame.src}
        title={file.name}
        sandbox={SITE_SANDBOX}
        referrerPolicy="no-referrer"
        className="block h-full w-full border-0 bg-white"
      />
    </DeviceFrame>
  );
}
