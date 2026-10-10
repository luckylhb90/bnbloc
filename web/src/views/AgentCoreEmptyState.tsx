import { ArrowClockwiseIcon } from "@phosphor-icons/react";
import type { ReleaseFile } from "@/lib/api";
import { Button } from "@/ui/Button";
import { ReleaseDownloadButton } from "@/views/ReleaseDownloadButton";

type CoreStatus = "ready" | "starting" | "offline";
type DesktopDownload = { readonly file: ReleaseFile | null; readonly loading: boolean };

// AgentCoreEmptyState is the guidance under a core that exposes no agents yet.
// It is generic and data-driven (the gateway's core record): no core name,
// companion app or setup recipe is baked into the client.
export function AgentCoreEmptyState({
  status,
  requiresDesktop,
  inDesktop,
  onRescan,
  desktopDownload,
  detail,
  error,
}: {
  readonly status: CoreStatus;
  // The core runs on the member's machine via the desktop app (a sidecar).
  readonly requiresDesktop: boolean;
  readonly inDesktop: boolean;
  readonly onRescan: () => void;
  readonly desktopDownload: DesktopDownload;
  // Precise state of a desktop-managed core (replaces the generic guidance),
  // and a quiet inline error while it retries.
  readonly detail?: string;
  readonly error?: string;
}) {
  const needsDesktopApp = requiresDesktop && !inDesktop;
  return (
    <div className="flex flex-col gap-2 px-3 py-2.5 text-xs text-[var(--color-fg-subtle)]">
      {detail ? (
        <span>{detail}</span>
      ) : (
        <span>
          {status === "offline"
            ? "This core has no reachable address yet."
            : "This core is reachable but reports no agents yet — its service may still be starting."}{" "}
          {needsDesktopApp
            ? "It runs on your machine through the bnbloc desktop app."
            : "Make sure its service is running, then re-scan. If it should be available to you, ask your admin."}
        </span>
      )}
      {error && <span className="text-[var(--color-warning)]">{error}</span>}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" onClick={onRescan}>
          <ArrowClockwiseIcon size={13} /> Re-scan
        </Button>
        {needsDesktopApp &&
          (desktopDownload.file ? (
            <ReleaseDownloadButton file={desktopDownload.file} label="Download the desktop app" variant="ghost" size="sm" />
          ) : (
            <span>{desktopDownload.loading ? "Finding download…" : "Download not configured — ask your admin."}</span>
          ))}
      </div>
    </div>
  );
}
