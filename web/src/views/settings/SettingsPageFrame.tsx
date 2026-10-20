import { useEffect, useMemo, useRef, useState } from "react";
import type { ComponentSettingScope } from "@/lib/componentSettings";
import { readSettingsPageMessage, settingsPageHost, settingsPageInit, settingsPageSrc, type SettingsPageScope, type SettingsPageSource } from "@/lib/settingsPageFrame";
import { Button } from "@/ui/Button";
import { frameEnv, frameTheme } from "@/views/artifact/siteFrameEnv";

// SettingsPageFrame is a component's own settings page for one scope
// (docs/architecture/settings-pages.md), shown where the one form would be.
// It runs in the previewer's frame: sandbox="allow-scripts" without
// allow-same-origin (an opaque origin, no cookies, no app DOM, no Tauri
// IPC), its files served under a CSP with no network. The chrome around it
// is the platform's: the component, the scope, that the page is the
// component's own, and Close, so a page cannot pose as the console. The
// page reads and saves its scope only through the host (settingsPageHost).
export function SettingsPageFrame({
  name,
  scope,
  scopeLabel,
  page,
  calls,
  onClose,
}: {
  readonly name: string;
  readonly scope: ComponentSettingScope;
  readonly scopeLabel: string;
  readonly page: SettingsPageSource;
  readonly calls: SettingsPageScope;
  readonly onClose: () => void;
}) {
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const [ready, setReady] = useState(false);
  // The latest calls and close for the handler registered once per frame.
  const latest = useRef({ calls, onClose });
  latest.current = { calls, onClose };
  const host = useMemo(
    () => settingsPageHost({ load: () => latest.current.calls.load(), save: (u) => latest.current.calls.save(u) }),
    [],
  );

  useEffect(() => {
    // A sandboxed frame's origin is opaque: "*" is the only target; the
    // iframe element pins the recipient window.
    const post = (msg: unknown) => frameRef.current?.contentWindow?.postMessage(msg, "*");
    const onMessage = (ev: MessageEvent) => {
      const msg = readSettingsPageMessage(ev, frameRef.current?.contentWindow);
      if (!msg) return;
      switch (msg.type) {
        case "settings.ready":
          setReady(true);
          post(settingsPageInit(frameTheme(), scope));
          return;
        case "settings.close":
          latest.current.onClose();
          return;
        default:
          void host.answer(msg).then((answer) => answer && post(answer));
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [host, scope]);

  // The frame keeps the src it opened with: a later view's fresh ticket
  // must not reload the page under the person (a new bundle remounts it).
  const [src] = useState(() => settingsPageSrc(page, frameEnv()));
  return (
    <section aria-label={`${name}: ${scopeLabel}`} className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] p-2">
      <div className="flex items-start justify-between gap-3 px-1">
        <div className="min-w-0">
          <span className="block text-sm font-medium">
            {name} <span className="text-xs font-normal text-[var(--color-fg-subtle)]">{scopeLabel}</span>
          </span>
          <span className="block text-xs text-[var(--color-fg-muted)]">This page is the component&apos;s own.</span>
        </div>
        <Button size="sm" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
      {src ? (
        <div className="relative h-[420px] w-full overflow-hidden rounded-[var(--radius)]">
          <iframe
            ref={frameRef}
            title={`${name} settings (${scopeLabel})`}
            sandbox="allow-scripts"
            referrerPolicy="no-referrer"
            src={src}
            className="absolute inset-0 block h-full w-full border-0"
          />
          {!ready && (
            <div className="absolute inset-0 grid place-items-center bg-[var(--color-bg)] text-sm text-[var(--color-fg-subtle)]" role="status">
              Loading the {name} settings page…
            </div>
          )}
        </div>
      ) : (
        <p className="px-1 text-sm text-[var(--color-fg-subtle)]">This settings page cannot be shown here.</p>
      )}
    </section>
  );
}
