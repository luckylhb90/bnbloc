import { useEffect, useRef, useState } from "react";
import { AppWindowIcon } from "@phosphor-icons/react";
import { appCover, coverRefused } from "@/lib/appCover";
import { cn } from "@/lib/cn";
import { loadAppCover, loadComponentCover } from "./artifactData";

// AppCover fills its box (the card sets the 16:10 frame) with an app's
// or a work's cover: its own cover image when the gateway serves one (a
// site version's in a room, a work version's in the gallery), else the
// generated cover (lib/appCover.ts: a gradient seeded by the app's id and
// the name's initial). A card is never blank, and a cover the gateway
// refuses (coverRefused: none, too large, not an image) is asked once.

/** Where an app's cover image comes from: a cache key and its loader (an
 *  object URL). */
export interface CoverSource {
  readonly key: string;
  readonly load: () => Promise<string>;
}

/** A site version's cover (an app deliverable; version 0: none yet). */
export function siteCover(roomId: string, appId: string, version: number): CoverSource | null {
  if (version <= 0) return null;
  return { key: `site\u0000${roomId}\u0000${appId}\u0000${version}`, load: () => loadAppCover(roomId, appId, version) };
}

/** A work version's cover (its listing's coverUrl; absent: none). */
export function componentCover(coverUrl: string | undefined): CoverSource | null {
  if (!coverUrl) return null;
  return { key: `component\u0000${coverUrl}`, load: () => loadComponentCover(coverUrl) };
}

// The covers the gateway refused.
const noCover = new Set<string>();

export function AppCover({
  seed,
  name,
  cover,
  enabled = true,
  className,
}: {
  // The app's id: the generated cover's seed.
  readonly seed: string;
  readonly name: string;
  readonly cover: CoverSource | null;
  // False defers the fetch (a card not yet scrolled into view).
  readonly enabled?: boolean;
  readonly className?: string;
}) {
  const key = cover?.key ?? "";
  const load = useRef(cover?.load);
  load.current = cover?.load;
  const [loaded, setLoaded] = useState<{ key: string; url: string } | null>(null);
  useEffect(() => {
    const fetchCover = load.current;
    if (!enabled || !key || !fetchCover || noCover.has(key)) return;
    let live = true;
    fetchCover()
      .then((url) => {
        if (live) setLoaded({ key, url });
      })
      .catch((e: unknown) => {
        if (coverRefused(e)) noCover.add(key);
      });
    return () => {
      live = false;
    };
  }, [key, enabled]);
  const url = loaded?.key === key ? loaded.url : null;
  if (url) {
    return <img src={url} alt="" loading="lazy" decoding="async" className={cn("h-full w-full object-cover", className)} />;
  }
  const generated = appCover(seed, name);
  return (
    <span aria-hidden className={cn("grid h-full w-full place-items-center text-white/90", className)} style={{ background: generated.background }}>
      {generated.initial ? (
        <span className="text-4xl font-semibold leading-none drop-shadow-sm">{generated.initial}</span>
      ) : (
        <AppWindowIcon size={32} weight="duotone" />
      )}
    </span>
  );
}
