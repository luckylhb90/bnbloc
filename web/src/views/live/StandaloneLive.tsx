import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { errorText } from "@/lib/errors";
import { useToast } from "@/ui/Toast";
import { applyLiveFrame, type JobLive } from "@/lib/jobLive";
import { subscribeRoom } from "@/lib/sse";
import type { JobLiveFrame } from "@/lib/wire.gen";
import { JobLiveView } from "./JobLiveView";

// StandaloneLive is a job's live view in its own window: the desktop
// shell's pop-out, or a browser tab on the web (#/live/<roomId>/<jobId>).
// It follows the room's stream for the job's frames; the first frame's
// facts come with the image itself, so the window shows the page at once.
export function StandaloneLive({ roomId, jobId }: { roomId: string; jobId: string }) {
  const { subject } = useAuth();
  const toast = useToast();
  const [live, setLive] = useState<Record<string, JobLive>>({});
  const apply = useCallback((f: JobLiveFrame) => setLive((cur) => applyLiveFrame(cur, f)), []);

  useEffect(
    () =>
      subscribeRoom(roomId, (e) => {
        if (e.kind !== "job_live_frame") return;
        const f = e.payload as JobLiveFrame | undefined;
        if (f?.jobId === jobId) apply(f);
      }),
    [roomId, jobId, apply],
  );
  const current = live[jobId];
  // Continue (a view that waited for its owner): the owner's message to the
  // same agent, in the view's channel; the window stays for the next step.
  const onContinue = useCallback(
    (l: JobLive) => {
      if (!l.provider) return;
      api
        .sendChat(roomId, { text: "Done, continue.", recipient: { ownerId: l.owner, provider: l.provider, ...(l.core ? { core: l.core } : {}) }, channelId: l.channelId ?? null, attachmentIds: [] }, crypto.randomUUID())
        .then(() => toast.success("Sent: the agent continues"))
        .catch((e: unknown) => toast.error(errorText(e, "Could not reach the agent")));
    },
    [roomId, toast],
  );
  useEffect(() => {
    document.title = `${current?.title || "Live page"} · bnbloc`;
  }, [current?.title]);

  return (
    <div className="flex h-dvh flex-col bg-[var(--color-bg)]">
      <JobLiveView roomId={roomId} live={current} subject={subject ?? null} bootstrap={{ jobId, onFacts: apply }} onContinue={onContinue} />
    </div>
  );
}
