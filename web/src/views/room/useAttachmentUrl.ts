import { useCallback, useEffect, useState, type MouseEvent } from "react";
import { api } from "@/lib/api";
import { createAttachmentTicketCache } from "@/lib/attachmentTickets";
import { apiUrl } from "@/lib/config";
import { getToken } from "@/lib/session";
import { StorageKey, readStored, writeStored } from "@/lib/storageKeys";

// One ticket cache for the app: every rendering of an attachment (the feed,
// the people chat, the lightbox) shares its tickets.
const tickets = createAttachmentTicketCache(
  async (roomId, attachmentId) => {
    const t = await api.attachmentTicket(roomId, attachmentId);
    return { url: apiUrl(t.url), expiresAtMs: t.expiresAtMs };
  },
  Date.now,
  {
    load: () => {
      try {
        return JSON.parse(readStored(StorageKey.attachmentTickets, "session") || "{}");
      } catch {
        return {};
      }
    },
    save: (all) => writeStored(StorageKey.attachmentTickets, Object.keys(all).length ? JSON.stringify(all) : "", "session"),
  },
);

const session = () => getToken() ?? "";

export interface AttachmentUrl {
  // The ticket URL to render (null until the first ticket is minted). An
  // <img> keeps it once loaded; it is not renewed in place.
  readonly url: string | null;
  // A valid ticket URL now, renewing an expired one.
  readonly ensure: () => Promise<string>;
  // onClick for an <a href={url} download>: lets a valid link navigate,
  // else renews the ticket and downloads through the new URL.
  readonly onDownload: (e: MouseEvent<HTMLAnchorElement>) => void;
}

export function useAttachmentUrl(roomId: string, attachmentId: string, filename: string): AttachmentUrl {
  const [url, setUrl] = useState(() => tickets.cached(session(), roomId, attachmentId));
  useEffect(() => {
    if (url) return;
    let live = true;
    tickets
      .url(session(), roomId, attachmentId)
      .then((u) => {
        if (live) setUrl(u);
      })
      .catch(() => {
        /* not reachable now: the link stays inert until it is opened */
      });
    return () => {
      live = false;
    };
  }, [roomId, attachmentId, url]);

  const ensure = useCallback(async () => {
    const u = await tickets.url(session(), roomId, attachmentId);
    setUrl(u);
    return u;
  }, [roomId, attachmentId]);

  const onDownload = useCallback(
    (e: MouseEvent<HTMLAnchorElement>) => {
      if (url && tickets.cached(session(), roomId, attachmentId) === url) return;
      e.preventDefault();
      void ensure()
        .then((u) => {
          const a = document.createElement("a");
          a.href = u;
          a.download = filename;
          a.click();
        })
        .catch(() => {});
    },
    [url, roomId, attachmentId, filename, ensure],
  );

  return { url, ensure, onDownload };
}

// An attachment's bytes never change: the last few opened stay cached so a
// workbench tab switching back does not download them again.
const MAX_BLOBS = 8;
const blobs = new Map<string, Promise<Blob>>();

/** An attachment's bytes, typed with its content type (the preview
 *  workbench hands them to a viewer). */
export function attachmentBlob(roomId: string, attachmentId: string, contentType: string): Promise<Blob> {
  const key = `${roomId}\n${attachmentId}`;
  let p = blobs.get(key);
  if (!p) {
    p = api.attachmentBlob(roomId, attachmentId).then((b) => (b.type === contentType ? b : new Blob([b], { type: contentType })));
    p.catch(() => blobs.delete(key));
    blobs.set(key, p);
    while (blobs.size > MAX_BLOBS) blobs.delete(blobs.keys().next().value!);
  }
  return p;
}
