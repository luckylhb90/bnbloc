// The live view's API calls (jobLive.ts has the pure helpers).

import { req } from "./api.ts";
import { roomPath } from "./roomId.ts";
import { LiveFrameHeader, type JobLiveFrame, type LiveControlReply, type LiveInput } from "./wire.gen.ts";

export const jobLiveApi = {
  /** The newest image and its facts; with watch, marks the caller as
   *  watching the large view (the tool sends frames faster). */
  frame: async (roomId: string, jobId: string, watch: boolean): Promise<{ image: Blob; facts: JobLiveFrame | null }> => {
    const res = await req<Response>(`${roomPath(roomId, "jobs", jobId, "live", "frame")}${watch ? "?watch=1" : ""}`, { as: "response" });
    return { image: await res.blob(), facts: decodeFacts(res.headers.get(LiveFrameHeader)) };
  },
  control: (roomId: string, jobId: string, take: boolean): Promise<LiveControlReply> =>
    req<LiveControlReply>(roomPath(roomId, "jobs", jobId, "live", "control"), { method: "POST", body: JSON.stringify({ take }) }),
  input: (roomId: string, jobId: string, events: readonly LiveInput[]): Promise<void> =>
    req<void>(roomPath(roomId, "jobs", jobId, "live", "input"), { method: "POST", body: JSON.stringify({ events }), as: "none" }),
};

// decodeFacts reads the frame answer's facts header (base64url JSON).
function decodeFacts(raw: string | null): JobLiveFrame | null {
  if (!raw) return null;
  try {
    const b64 = raw.replace(/-/g, "+").replace(/_/g, "/");
    const bytes = Uint8Array.from(atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4)), (c) => c.charCodeAt(0));
    const v = JSON.parse(new TextDecoder().decode(bytes)) as JobLiveFrame;
    return v && typeof v.jobId === "string" ? v : null;
  } catch {
    return null;
  }
}
