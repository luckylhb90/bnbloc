import { useEffect, useRef, useState } from "react";
import { ArrowClockwiseIcon, CopySimpleIcon, FlaskIcon, XIcon } from "@phosphor-icons/react";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { errorText } from "@/lib/errors";
import { SITE_SANDBOX, siteFrameUrl } from "@/lib/siteFrame";
import { Button } from "@/ui/Button";
import { ErrorBox } from "@/ui/ErrorBox";
import { useModalLayer } from "@/ui/useModalLayer";
import { siteFrameEnv } from "@/views/artifact/siteFrameEnv";

// AppDemo is an app's "Try demo" (docs/prd/2026-10-13-app-gallery.md §7):
// the app version's site, full-screen, in the sandboxed site frame (the
// site sandbox, never allow-same-origin; on the sites origin when the
// deployment has one; the desktop's site scheme) from a demo URL the
// gateway mints (POST …/versions/{v}/demo). There the SDK runs in demo
// mode: the app's data lives in the frame's memory only, nothing is saved
// or shared, and no request reaches a data space (the gateway refuses
// every data call under a demo URL). "Start over" reloads the frame,
// which starts empty again. This page answers the frame nothing.

export function AppDemo({ app, onClose, onUse }: { readonly app: ContentComponent | null; readonly onClose: () => void; readonly onUse: () => void }) {
  const surface = useRef<HTMLDivElement | null>(null);
  const [frame, setFrame] = useState<{ key: string; src: string; nonce: number } | null>(null);
  const [error, setError] = useState<{ key: string; message: string } | null>(null);
  const key = app ? `${app.id}@${app.version}` : "";
  useModalLayer(surface, { open: !!app, onEscape: onClose });

  useEffect(() => {
    if (!app) return;
    let live = true;
    setFrame(null);
    setError(null);
    workGalleryApi
      .demo(app)
      .then((d) => live && setFrame({ key, src: siteFrameUrl(d.siteUrl, siteFrameEnv()), nonce: 0 }))
      .catch((e: unknown) => live && setError({ key, message: errorText(e, "Could not start the demo") }));
    return () => {
      live = false;
    };
    // The version's identity is the key.
  }, [key]);

  if (!app) return null;
  const label = contentLabel(app);
  const shown = frame?.key === key ? frame : null;
  const failed = error?.key === key ? error.message : "";
  return (
    <div
      ref={surface}
      role="dialog"
      aria-modal="true"
      aria-label={`${label} demo`}
      tabIndex={-1}
      className="fixed inset-0 z-50 flex flex-col bg-[var(--color-bg)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)] pt-[env(safe-area-inset-top)] focus:outline-none"
    >
      <header className="flex shrink-0 flex-wrap items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 sm:px-4">
        <FlaskIcon size={18} className="shrink-0 text-[var(--color-accent)]" aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium text-[var(--color-fg)]">{label} · demo</p>
          <p className="truncate text-xs text-[var(--color-fg-subtle)]">Try it freely: what you enter stays in this window and is gone when you close it.</p>
        </div>
        <Button
          variant="ghost"
          size="sm"
          disabled={!shown}
          onClick={() => setFrame((f) => (f ? { ...f, nonce: f.nonce + 1 } : f))}
          title="Start over with no data"
        >
          <ArrowClockwiseIcon size={14} />
          <span className="max-sm:hidden">Start over</span>
        </Button>
        <Button variant="primary" size="sm" onClick={onUse}>
          <CopySimpleIcon size={14} />
          Use this app
        </Button>
        <button type="button" onClick={onClose} data-layer-close aria-label="Close the demo" className="tactile grid size-8 place-items-center rounded-full text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]">
          <XIcon size={18} />
        </button>
      </header>
      <div className="relative min-h-0 flex-1 bg-white">
        {failed ? (
          <div className="p-4">
            <ErrorBox>{failed}</ErrorBox>
          </div>
        ) : shown ? (
          <iframe
            key={shown.nonce}
            src={shown.src}
            title={`${label} demo`}
            sandbox={SITE_SANDBOX}
            referrerPolicy="no-referrer"
            className="block h-full w-full border-0"
          />
        ) : (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]" role="status">
            Starting the demo…
          </p>
        )}
      </div>
    </div>
  );
}
