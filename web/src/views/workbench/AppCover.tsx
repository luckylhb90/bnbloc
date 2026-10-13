import { useEffect, useState } from "react";
import { AppWindowIcon } from "@phosphor-icons/react";
import { appCover } from "@/lib/appCover";
import { ApiError } from "@/lib/errors";
import { cn } from "@/lib/cn";
import { loadAppCover } from "./artifactData";

// AppCover fills its box (the card sets the 16:10 frame) with an app's
// cover: its site version's cover image when the site has one the gateway
// serves, else the generated cover (lib/appCover.ts: a gradient seeded by
// the app's id and the name's initial). A card is never blank, and a site
// without a cover is asked once per version.

// The versions whose site has no cover the gateway serves (404, 413, 415).
const noCover = new Set<string>();

export function AppCover({
  roomId,
  appId,
  version,
  name,
  enabled = true,
  className,
}: {
  readonly roomId: string;
  readonly appId: string;
  // The site version whose cover to show (0: none yet).
  readonly version: number;
  readonly name: string;
  // False defers the fetch (a card not yet scrolled into view).
  readonly enabled?: boolean;
  readonly className?: string;
}) {
  const key = `${roomId}\u0000${appId}\u0000${version}`;
  const [loaded, setLoaded] = useState<{ key: string; url: string } | null>(null);
  useEffect(() => {
    if (!enabled || version <= 0 || noCover.has(key)) return;
    let live = true;
    loadAppCover(roomId, appId, version)
      .then((url) => {
        if (live) setLoaded({ key, url });
      })
      .catch((e: unknown) => {
        if (e instanceof ApiError && (e.status === 404 || e.status === 413 || e.status === 415)) noCover.add(key);
      });
    return () => {
      live = false;
    };
  }, [roomId, appId, version, key, enabled]);
  const url = loaded?.key === key ? loaded.url : null;
  if (url) {
    return <img src={url} alt="" loading="lazy" decoding="async" className={cn("h-full w-full object-cover", className)} />;
  }
  const cover = appCover(appId, name);
  return (
    <span aria-hidden className={cn("grid h-full w-full place-items-center text-white/90", className)} style={{ background: cover.background }}>
      {cover.initial ? (
        <span className="text-4xl font-semibold leading-none drop-shadow-sm">{cover.initial}</span>
      ) : (
        <AppWindowIcon size={32} weight="duotone" />
      )}
    </span>
  );
}
