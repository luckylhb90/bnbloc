import { useEffect, useMemo, useRef, useState } from "react";
import { desktop } from "@/lib/desktop";
import type { MemberComponent } from "@/lib/api";
import { panelHost, panelInit, panelSrc, readPanelFrameMessage } from "@/lib/panelFrame";
import { Dialog } from "@/ui/Dialog";
import { frameEnv, frameTheme } from "@/views/artifact/siteFrameEnv";

// PanelDialog is a plugin's own panel (docs/architecture/plugin-panels.md),
// opened from its card on the desktop. It runs in the previewer's frame:
// sandbox="allow-scripts" without allow-same-origin (an opaque origin, no
// cookies, no app DOM, no Tauri IPC), its files served under a CSP with no
// network. The chrome around it is the platform's: the component's name, that
// the page is the component's own, and the dialog's close, so a page cannot
// pose as the console. The page reaches its plugin only through the host
// (panelHost), which asks the desktop to make each call with the bearer the
// desktop keeps. The platform names nothing the panel is for.
export function PanelDialog({ component, onClose }: { readonly component: MemberComponent | null; readonly onClose: () => void }) {
  if (!component?.panel) return null;
  return (
    <Dialog open onClose={onClose} title={component.name} description="This page is the component's own." size="lg">
      <PanelFrame plugin={component.name} page={component.panel} onClose={onClose} />
    </Dialog>
  );
}

function PanelFrame({ plugin, page, onClose }: { readonly plugin: string; readonly page: NonNullable<MemberComponent["panel"]>; readonly onClose: () => void }) {
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const [ready, setReady] = useState(false);
  const latest = useRef(onClose);
  latest.current = onClose;
  const host = useMemo(() => panelHost({ call: (method, path, body) => desktop.panelCall(plugin, method, path, body) }), [plugin]);

  useEffect(() => {
    // A sandboxed frame's origin is opaque: "*" is the only target; the
    // iframe element pins the recipient window.
    const post = (msg: unknown) => frameRef.current?.contentWindow?.postMessage(msg, "*");
    const onMessage = (ev: MessageEvent) => {
      const msg = readPanelFrameMessage(ev, frameRef.current?.contentWindow);
      if (!msg) return;
      switch (msg.type) {
        case "panel.ready":
          setReady(true);
          post(panelInit(frameTheme()));
          return;
        case "panel.close":
          latest.current();
          return;
        default:
          void host.answer(msg).then((answer) => answer && post(answer));
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [host]);

  // The frame keeps the src it opened with (a new bundle remounts it).
  const [src] = useState(() => panelSrc(page, frameEnv()));
  if (!src) return <p className="text-sm text-[var(--color-fg-subtle)]">This page runs on your computer: open it in the bnbloc desktop app.</p>;
  return (
    <div className="relative h-[560px] w-full overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]">
      <iframe ref={frameRef} title={`${plugin} panel`} sandbox="allow-scripts" referrerPolicy="no-referrer" src={src} className="absolute inset-0 block h-full w-full border-0" />
      {!ready && (
        <div className="absolute inset-0 grid place-items-center bg-[var(--color-bg)] text-sm text-[var(--color-fg-subtle)]" role="status">
          Loading {plugin}…
        </div>
      )}
    </div>
  );
}
