import { useEffect, useRef } from "react";
import { createVisitorHost } from "@/lib/appVisitor";
import { apiUrl } from "@/lib/config";
import type { PublicShare } from "@/lib/shares";
import { publicShareApi } from "@/lib/sharesApi";
import { SITE_SANDBOX, siteGatewayUrl } from "@/lib/siteFrame";

// SharedApp is a public link to a web app (a `site`): the app itself,
// full-screen, as the people the link was sent to use it — usually on a
// phone. No file-viewer chrome (no header, version line or download): the
// app is the page. The frame keeps the site sandbox (opaque origin, never
// allow-same-origin) and sits inside the safe areas, so nothing hides under
// a notch or a home indicator, and the page itself never scrolls.
//
// The page holds the link's visitor token for the app (lib/appVisitor.ts):
// minted on the frame's first request, kept per share, renewed before it
// expires and handed only to this page's own app frame.

export function SharedApp({ token, share }: { readonly token: string; readonly share: PublicShare & { readonly siteUrl: string } }) {
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const title = share.artifact.title.trim() || "Web app";

  useEffect(() => {
    document.title = title;
  }, [title]);

  useEffect(() => {
    const host = createVisitorHost({
      shareToken: token,
      mint: (prev) => publicShareApi.visitor(token, prev),
      frame: () => frameRef.current?.contentWindow ?? null,
    });
    const onMessage = (ev: MessageEvent) => host.onMessage(ev);
    window.addEventListener("message", onMessage);
    return () => {
      window.removeEventListener("message", onMessage);
      host.stop();
    };
  }, [token]);

  return (
    <div className="fixed inset-0 overflow-hidden bg-[var(--color-bg)] pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)] pt-[env(safe-area-inset-top)]">
      <iframe
        ref={frameRef}
        src={siteGatewayUrl(share.siteUrl, { apiUrl, baseURI: document.baseURI })}
        title={title}
        sandbox={SITE_SANDBOX}
        referrerPolicy="no-referrer"
        className="block h-full w-full border-0 bg-white"
      />
    </div>
  );
}
