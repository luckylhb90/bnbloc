import { useCallback, useEffect, useRef, useState } from "react";
import { api, type AgentJob } from "@/lib/api";
import type { RoomEvent } from "@/lib/sse";
import { onboardingEventFromRoomEvent, onboardingEvents } from "@/lib/onboardingEvents";
import { useScopedState } from "@/lib/useScopedState";
import type { RecentArtifactChange } from "@/views/room/ActivityRail";
import { invalidateArtifacts, loadArtifactDetail, type ArtifactDetail } from "@/views/workbench/artifactData";

export type LiveStates = Record<string, { state: unknown; actor: string; nonce: number }>;

// Monotonic nonce for live artifact state entries (Date.now() can collide
// within one millisecond across two artifacts).
let liveStateNonce = 0;

const WORKING_SAFETY_MS = 120_000;
const ANCHOR_WINDOW_MS = 30 * 60 * 1000;

type AnchorMap = Record<number, string[]>;

function anchor(cur: AnchorMap, seq: number, artifactId: string): AnchorMap {
  const ids = cur[seq] ?? [];
  return ids.includes(artifactId) ? cur : { ...cur, [seq]: [...ids, artifactId] };
}

/** The messages that produced an artifact's versions (explicit links). */
function sourceSeqs(detail: ArtifactDetail): number[] {
  const seqs = detail.versions.map((v) => v.sourceSeq ?? 0).filter((n) => Number.isInteger(n) && n > 0);
  return [...new Set(seqs)];
}

// FALLBACK ONLY — the time heuristic for a live version published without an
// explicit sourceSeq (a publisher that does not send one): anchor under the
// newest agent job's message when the artifact changed within
// ANCHOR_WINDOW_MS of that job. Reloads anchor by sourceSeq only.
// Explicit links always win; remove this once no such versions remain.
function heuristicAnchor(cur: AnchorMap, artifactId: string, jobs: Record<number, AgentJob[]>, changedAtMs: number): AnchorMap {
  for (const ids of Object.values(cur)) if (ids.includes(artifactId)) return cur;
  const newest = Math.max(0, ...Object.keys(jobs).map(Number));
  if (newest <= 0) return cur;
  const jobAt = jobs[newest]?.[0]?.updatedAtMs ?? 0;
  if (jobAt && Math.abs(changedAtMs - jobAt) > ANCHOR_WINDOW_MS) return cur;
  return anchor(cur, newest, artifactId);
}

// useRoomActivity owns everything around the conversation that the room
// stream drives: durable agent-job rows and inline artifact cards (scoped to
// the active channel), live artifact collaboration state, the Files refresh
// signal/badge, recent artifact changes, and per-agent "working" indicators.
export function useRoomActivity({
  roomId,
  channelId,
  subject,
  liveReload,
  artifactsOpen,
  feedSeqs,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly subject: string | null;
  readonly liveReload: number;
  // The sequence range of the messages the feed has loaded (null while
  // empty): their deliverable cards are anchored from the server.
  readonly feedSeqs: { readonly min: number; readonly max: number } | null;
  // Whether the Files surface is the one open (no badge while it is).
  readonly artifactsOpen: boolean;
}) {
  const scope = channelId ?? "";
  const [agentJobs, setAgentJobs] = useScopedState<Record<number, AgentJob[]>>(scope, () => ({}));
  // Artifacts born from a chat message, keyed by that message's seq: the
  // cards render inline under the message whose turn produced the files. The
  // link is the version's explicit sourceSeq; see heuristicAnchor for the
  // fallback.
  const [artifactsBySeq, setArtifactsBySeq] = useScopedState<AnchorMap>(scope, () => ({}));
  const [liveStates, setLiveStates] = useState<LiveStates>({});
  const [filesSignal, setFilesSignal] = useState(0);
  const [filesBadge, setFilesBadge] = useState(0);
  const [recentArtifactChanges, setRecentArtifactChanges] = useState<RecentArtifactChange[]>([]);
  // Per-agent "working" indicators keyed by "<owner>/<provider>"; each carries
  // a label and a safety-expiry timer so a missed idle event never sticks.
  const [working, setWorking] = useState<Map<string, string>>(new Map());
  const workingTimers = useRef<Map<string, number>>(new Map());

  const agentJobsRef = useRef(agentJobs);
  agentJobsRef.current = agentJobs;
  const artifactsOpenRef = useRef(artifactsOpen);
  artifactsOpenRef.current = artifactsOpen;

  const clearWorking = useCallback((key: string) => {
    window.clearTimeout(workingTimers.current.get(key));
    workingTimers.current.delete(key);
    setWorking((w) => {
      if (!w.has(key)) return w;
      const next = new Map(w);
      next.delete(key);
      return next;
    });
  }, []);
  const armWorking = useCallback(
    (key: string, label: string) => {
      window.clearTimeout(workingTimers.current.get(key));
      workingTimers.current.set(key, window.setTimeout(() => clearWorking(key), WORKING_SAFETY_MS));
      setWorking((w) => (w.get(key) === label ? w : new Map(w).set(key, label)));
    },
    [clearWorking],
  );
  useEffect(() => {
    const timers = workingTimers.current;
    return () => timers.forEach((t) => window.clearTimeout(t));
  }, []);

  // Durable agent-job status for the channel, loaded on entry and on
  // reconnect catch-up; live transitions arrive as job events.
  useEffect(() => {
    if (!channelId) return;
    let cancelled = false;
    void (async () => {
      let bySeq: Record<number, AgentJob[]> = {};
      try {
        const res = await api.agentJobs(roomId, channelId);
        for (const j of res.jobs || []) (bySeq[j.messageSeq] ||= []).push(j);
      } catch {
        bySeq = {};
      }
      if (cancelled) return;
      setAgentJobs(bySeq);
      try {
        // The room already holds deliverables: the checklist can tell.
        const res = await api.artifacts(roomId, { limit: 1 });
        if (!cancelled && (res.artifacts ?? []).length > 0) onboardingEvents.emit({ kind: "deliverable", roomId, source: "history" });
      } catch {
        /* best-effort */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [roomId, channelId, liveReload, setAgentJobs]);

  // Deliverable cards of the loaded messages, by their explicit sourceSeq
  // (after a reload the live events that anchored them never arrive). Re-read
  // when older history pages in (min drops) and after a reconnect; new
  // messages are anchored by their live artifact events.
  const minSeq = feedSeqs?.min ?? 0;
  const maxSeqRef = useRef(0);
  maxSeqRef.current = feedSeqs?.max ?? 0;
  useEffect(() => {
    const max = maxSeqRef.current;
    if (minSeq <= 0 || max < minSeq) return;
    let cancelled = false;
    api
      .artifactAnchors(roomId, minSeq, max)
      .then((res) => {
        if (cancelled) return;
        setArtifactsBySeq((cur) => {
          let next = cur;
          for (const a of res.anchors ?? []) next = anchor(next, a.seq, a.artifactId);
          return next;
        });
      })
      .catch(() => {
        /* best-effort: live events still anchor */
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, channelId, minSeq, liveReload, setArtifactsBySeq]);

  // applyEvent handles the activity kinds of the room stream. Returns false
  // for kinds it does not own (the feed handles those).
  const applyEvent = useCallback(
    (e: RoomEvent): boolean => {
      if (e.kind === "agent_job_created" || e.kind === "agent_job_updated") {
        const j = e.payload as AgentJob | undefined;
        // Job events are room-wide; only this channel's rows sit under its feed.
        if (j && typeof j.messageSeq === "number" && (!j.channelId || j.channelId === channelId)) {
          setAgentJobs((cur) => ({ ...cur, [j.messageSeq]: [j] }));
        }
        return true;
      }
      if (e.kind === "artifact_state") {
        const p = e.payload as { artifactId?: string; actor?: string; state?: unknown } | undefined;
        if (p?.artifactId && p.actor !== subject) {
          const id = p.artifactId;
          const entry = { state: p.state, actor: p.actor ?? "?", nonce: ++liveStateNonce };
          setLiveStates((cur) => ({ ...cur, [id]: entry }));
        }
        return true;
      }
      if (e.kind === "artifact_updated") {
        onboardingEvents.emit(onboardingEventFromRoomEvent(e, subject, roomId, "stream"));
        setFilesSignal((n) => n + 1);
        if (!artifactsOpenRef.current) setFilesBadge((n) => n + 1);
        const p = e.payload;
        const artifactId = typeof p?.artifactId === "string" ? p.artifactId : "";
        if (!artifactId) return true;
        const title = typeof p?.title === "string" ? p.title : "";
        const currentVersion = typeof p?.currentVersion === "number" ? p.currentVersion : 0;
        if (title && currentVersion > 0) {
          const change: RecentArtifactChange = {
            artifactId,
            title,
            currentVersion,
            updatedBy: typeof p?.updatedBy === "string" ? p.updatedBy : "unknown",
            updatedAtMs: typeof p?.updatedAtMs === "number" ? p.updatedAtMs : e.atUnixMs ?? Date.now(),
          };
          setRecentArtifactChanges((cur) =>
            [
              change,
              ...cur.filter((c) => c.artifactId !== change.artifactId || c.currentVersion !== change.currentVersion),
            ].slice(0, 4),
          );
        }
        // The new version's explicit sourceSeq anchors its card under the
        // producing message (every message that produced a version shows
        // it); without one, the fallback heuristic anchors on first
        // sighting. The detail is refetched (cards and tabs share it).
        invalidateArtifacts(roomId, artifactId);
        const changedAt = typeof p?.updatedAtMs === "number" ? p.updatedAtMs : e.atUnixMs ?? Date.now();
        void loadArtifactDetail(roomId, artifactId)
          .then((detail) => {
            const produced = detail.versions.find((v) => v.version === detail.artifact.currentVersion)?.sourceSeq ?? 0;
            const seqs = produced > 0 ? [produced] : sourceSeqs(detail);
            setArtifactsBySeq((cur) => {
              if (seqs.length === 0) return heuristicAnchor(cur, artifactId, agentJobsRef.current, changedAt);
              let next = cur;
              for (const seq of seqs) next = anchor(next, seq, artifactId);
              return next;
            });
          })
          .catch(() => {
            /* deleted or inaccessible: no card */
          });
        return true;
      }
      if (e.kind === "agent_session_state") {
        const key = e.agentKey || "agent";
        if (e.agentPhase === "working") {
          const fresh = !e.atUnixMs || Date.now() - e.atUnixMs < 15_000;
          if (fresh) armWorking(key, e.agentLabel || "agent");
        } else {
          clearWorking(key);
        }
        return true;
      }
      // An assistant reply means that agent's turn produced output: clear its
      // working indicator, then let the feed append the reply.
      if (e.kind === "agent_messages" && e.agentKey) clearWorking(e.agentKey);
      return false;
    },
    [roomId, channelId, subject, armWorking, clearWorking, setAgentJobs, setArtifactsBySeq],
  );

  // Reconnect catch-up: anything may have changed while the stream was down.
  const bumpFiles = useCallback(() => {
    invalidateArtifacts(roomId);
    setFilesSignal((n) => n + 1);
  }, [roomId]);
  const clearFilesBadge = useCallback(() => setFilesBadge(0), []);

  return {
    agentJobs,
    artifactsBySeq,
    liveStates,
    filesSignal,
    filesBadge,
    recentArtifactChanges,
    working,
    applyEvent,
    bumpFiles,
    clearFilesBadge,
  };
}
