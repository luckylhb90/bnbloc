import {
  ArrowClockwiseIcon,
  ArrowSquareOutIcon,
  DownloadSimpleIcon,
  DesktopIcon,
} from "@phosphor-icons/react";
import type { ReleaseFile } from "@/lib/api";
import { isMacInstaller } from "@/lib/macInstall";
import { Button } from "@/ui/Button";
import { PlatformAgentsHint } from "@/views/PlatformAgentsHint";

export function DesktopHandoffCard({
  file,
  error,
  checking,
  downloading,
  openFailed = false,
  onDownload,
  onOpen,
  onCheck,
}: {
  readonly file: ReleaseFile | null;
  readonly error: string | null;
  readonly checking: boolean;
  readonly downloading: boolean;
  // The app was asked to open but never connected: most likely not installed.
  readonly openFailed?: boolean;
  readonly onDownload: () => void;
  readonly onOpen: () => void;
  readonly onCheck: () => void;
}) {
  return (
    <section className="w-full border-t border-[var(--color-border)] pt-4 text-left" aria-label="Connect your own agent">
      <div className="flex items-start gap-3">
        <span className="mt-0.5 text-[var(--color-accent)]" aria-hidden="true">
          <DesktopIcon size={20} />
        </span>
        <div className="min-w-0 flex-1">
          <p className="font-medium text-[var(--color-fg)]">Run your own agent on desktop</p>
          <p className="mt-1 text-sm leading-relaxed text-[var(--color-fg-muted)]">
            This browser has no connected agent. Desktop connects your local agent to this room. Chat and borrowing a teammate’s agent are available here without it.
          </p>
          <PlatformAgentsHint className="mt-2" />
          <div className="mt-3 flex flex-wrap items-center gap-2">
            {file ? (
              <Button size="sm" variant="primary" onClick={onDownload} disabled={downloading}>
                <DownloadSimpleIcon size={15} />
                {downloading ? "Preparing download…" : `Download ${isMacInstaller(file) ? "macOS" : "Windows"} app`}
              </Button>
            ) : (
              <span className="text-xs text-[var(--color-fg-subtle)]">
                Desktop download is not published for this platform yet. Ask your bnbloc administrator.
              </span>
            )}
            <Button size="sm" variant="outline" onClick={onOpen}>
              <ArrowSquareOutIcon size={15} />
              Open desktop app
            </Button>
            <Button size="sm" variant="ghost" onClick={onCheck} disabled={checking}>
              <ArrowClockwiseIcon size={15} />
              {checking ? "Checking…" : "Check connection"}
            </Button>
          </div>
          {openFailed && (
            <p className="mt-2 text-xs text-[var(--color-fg-muted)]" role="status">
              The desktop app didn't connect. If it isn't installed yet, download it, then open it and sign in.
            </p>
          )}
          {error && <p className="mt-2 text-xs text-[var(--color-danger)]" role="alert">{error}</p>}
        </div>
      </div>
    </section>
  );
}
