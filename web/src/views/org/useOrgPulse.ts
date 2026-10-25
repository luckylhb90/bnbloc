import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { ApiError, errorText } from "@/lib/errors";
import { changesBetween, type OrgPulseSnapshot, type RateSample } from "@/lib/orgPulse";
import { readSse } from "@/lib/orgPulseStream";

// How long a rate sample is kept: the last minute is what the pump shows, and a
// little more keeps the window honest when a reconnect lands late.
const SAMPLE_KEEP_MS = 5 * 60_000;
const FEED_MAX = 40;
const RETRY_MS = 2000;

export type FeedItem = { t: number; text: string };

export type OrgPulseData = {
  snap: OrgPulseSnapshot | null;
  seenAt: number; // when this page last received a snapshot (0 = never)
  connected: boolean; // the stream is open
  forbidden: boolean; // the server refused this session (not an org admin or super admin)
  error: string | null;
  feed: FeedItem[];
  samples: RateSample[];
  peakWorking: number; // highest agents-working seen since the tab opened
};

// useOrgPulse holds the board's data for one mounted tab: a first snapshot, then the
// stream, reconnected with a short pause when it drops. The request is aborted
// when the tab closes.
export function useOrgPulse(): OrgPulseData {
  const [snap, setSnap] = useState<OrgPulseSnapshot | null>(null);
  const [seenAt, setSeenAt] = useState(0);
  const [connected, setConnected] = useState(false);
  const [forbidden, setForbidden] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [feed, setFeed] = useState<FeedItem[]>([]);
  const [peakWorking, setPeakWorking] = useState(0);
  const samples = useRef<RateSample[]>([]);
  const prev = useRef<OrgPulseSnapshot | null>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    const apply = (s: OrgPulseSnapshot) => {
      const now = Date.now();
      const before = prev.current;
      prev.current = s;
      if (before) {
        const lines = changesBetween(before, s);
        if (lines.length) setFeed((f) => [...lines.map((text) => ({ t: now, text })), ...f].slice(0, FEED_MAX));
      }
      samples.current = [...samples.current, { t: now, tokens: s.today.tokensIn + s.today.tokensOut }]
        .filter((x) => x.t >= now - SAMPLE_KEEP_MS);
      setSnap(s);
      setSeenAt(now);
      setError(null);
      setPeakWorking((p) => Math.max(p, s.live.agentsWorking));
    };
    const refused = (err: unknown) => err instanceof ApiError && err.status === 403;

    api.orgPulseSnapshot().then(apply).catch((err: unknown) => {
      if (ctrl.signal.aborted) return;
      if (refused(err)) setForbidden(true);
      else setError(errorText(err, "The board could not be loaded."));
    });

    const loop = async () => {
      while (!ctrl.signal.aborted) {
        try {
          const res = await api.orgPulseStream(ctrl.signal);
          if (!res.body) throw new Error("the board stream sent no body");
          setConnected(true);
          setForbidden(false);
          await readSse(
            res.body,
            (e) => {
              if (e.event === "snapshot") {
                try { apply(JSON.parse(e.data) as OrgPulseSnapshot); } catch { /* a bad frame is skipped */ }
              } else if (e.event === "error") {
                setConnected(false);
              }
            },
            ctrl.signal,
          );
        } catch (err) {
          if (ctrl.signal.aborted) return;
          if (refused(err)) { setForbidden(true); setConnected(false); return; }
          setError(errorText(err, "The board could not be loaded."));
        }
        setConnected(false);
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, RETRY_MS);
          ctrl.signal.addEventListener("abort", () => { clearTimeout(timer); resolve(); }, { once: true });
        });
      }
    };
    void loop();
    return () => ctrl.abort();
  }, []);

  return {
    snap, seenAt, connected, forbidden, error, feed,
    samples: samples.current,
    peakWorking,
  };
}

