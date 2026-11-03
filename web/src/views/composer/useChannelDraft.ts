import { useEffect, useRef, useState } from "react";
import { channelsApi } from "@/lib/channelsApi";
import { draftAction, restoreDraft } from "@/lib/channelDrafts";

// How long typing pauses before the draft is saved (decision: owner accepted 2026-11-02;
// the PRD names no interval).
const SAVE_AFTER_MS = 800;

// useChannelDraft keeps the composer's text of one channel or thread on the
// server (channels PRD C6), so another device or a reload shows it: read
// once when the composer opens, saved while typing pauses, removed when the
// text is sent or emptied. A change pending when the composer closes is
// saved then, never dropped. Best effort: a failed save never blocks typing
// or sending.
export function useChannelDraft(
  roomId: string,
  channelId: string | null | undefined,
  text: string,
  setText: (update: (current: string) => string) => void,
): void {
  const [loaded, setLoaded] = useState(false);
  const stored = useRef("");
  const pending = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!channelId) return;
    let cancelled = false;
    channelsApi.draft(roomId, channelId).then(
      (d) => {
        if (cancelled) return;
        stored.current = d.text;
        if (d.text) setText((current) => restoreDraft(current, d.text));
        setLoaded(true);
      },
      () => {
        if (!cancelled) setLoaded(true);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [roomId, channelId, setText]);

  useEffect(() => {
    if (!channelId) return;
    const action = draftAction(loaded, stored.current, text);
    if (action === "none") {
      pending.current = null;
      return;
    }
    const flush = () => {
      pending.current = null;
      const previous = stored.current;
      stored.current = action === "delete" ? "" : text;
      const call = action === "delete" ? channelsApi.clearDraft(roomId, channelId) : channelsApi.saveDraft(roomId, channelId, text);
      void call.catch(() => {
        if (stored.current === (action === "delete" ? "" : text)) stored.current = previous;
      });
    };
    pending.current = flush;
    const t = window.setTimeout(flush, SAVE_AFTER_MS);
    return () => window.clearTimeout(t);
  }, [loaded, text, roomId, channelId]);

  useEffect(() => () => pending.current?.(), []);
}
