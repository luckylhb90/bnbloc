import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { subscribeRoom } from "@/lib/sse";
import type { RoomEvent } from "@/lib/roomEvents";
import { isRoomGoneStatus, reportRoomGone, roomAccessEnded, type RoomAccessEnd, roomGoneText } from "@/lib/roomGone";
import { ArtifactTabView } from "@/views/workbench/ArtifactTabView";
import { invalidateArtifacts } from "@/views/workbench/artifactData";

// StandalonePreview is the torn-out window for one artifact: the pop-out of
// a workbench tab (desktop shell). It hosts the same tab view at full
// viewport — the viewer and its controls, versions, compare, restore — with
// the same live collaboration wiring (artifact_state relay + remote state) so
// a game or canvas popped onto a second screen keeps syncing with the room.
//
// The route is #/preview/<roomId>/<artifactId>; the shell creates the window
// with that hash and injects the gateway base (same init script as main).

let liveStateNonce = 0;

export function StandalonePreview({ roomId, artifactId }: { roomId: string; artifactId: string }) {
  // The window shares the app's signed-in session (one origin, one
  // localStorage): own identity filters self-echoes and matches a removal.
  const { subject } = useAuth();
  const [version, setVersion] = useState<number | undefined>(undefined);
  // The room is gone for this member: the window says so and stops its
  // stream instead of reconnecting into 403s.
  const [gone, setGone] = useState<RoomAccessEnd | "missing" | null>(null);
  const [liveStates, setLiveStates] = useState<
    Record<string, { state: unknown; actor: string; nonce: number }>
  >({});

  // The artifact title for the native window title.
  useEffect(() => {
    let cancelled = false;
    api
      .artifactDetail(roomId, artifactId)
      .then((d) => {
        if (!cancelled && d.artifact?.title) document.title = `${d.artifact.title} · bnbloc`;
      })
      .catch((e: unknown) => {
        if (cancelled || !(e instanceof ApiError) || !isRoomGoneStatus(e.status)) return;
        // The artifact may be what is missing; only the room detail says
        // whether the room itself is gone.
        api.getRoom(roomId).catch((re: unknown) => {
          if (cancelled || !(re instanceof ApiError) || !isRoomGoneStatus(re.status)) return;
          reportRoomGone(roomId);
          setGone("missing");
        });
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, artifactId]);

  // Live room stream: version bumps refresh the panel; remote gestures flow
  // into the running artifact. Same contract as the in-room dock.
  useEffect(() => {
    if (gone) return;
    const off = subscribeRoom(roomId, (e: RoomEvent) => {
      const ended = roomAccessEnded(e, subject);
      if (ended) {
        off();
        reportRoomGone(roomId);
        setGone(ended);
        return;
      }
      if (e.kind === "artifact_updated") {
        const p = e.payload as { artifactId?: string } | undefined;
        if (!p?.artifactId || p.artifactId === artifactId) invalidateArtifacts(roomId, artifactId);
        return;
      }
      if (e.kind === "artifact_state") {
        const p = e.payload as { artifactId?: string; actor?: string; state?: unknown } | undefined;
        const id = p?.artifactId;
        if (id && p.actor !== subject) {
          setLiveStates((cur) => ({
            ...cur,
            [id]: { state: p.state, actor: p.actor ?? "?", nonce: ++liveStateNonce },
          }));
        }
      }
    });
    return off;
  }, [roomId, artifactId, subject, gone]);

  const relayArtifactState = useCallback(
    (id: string, state: unknown) => {
      void api.artifactState(roomId, id, state).catch(() => {});
    },
    [roomId],
  );

  return (
    <div className="flex h-[100dvh] flex-col bg-[var(--color-bg)]">
      {gone ? (
        <div className="grid flex-1 place-items-center p-6 text-sm text-[var(--color-fg-muted)]">
          {roomGoneText(gone)}
        </div>
      ) : (
        <div className="min-h-0 flex-1">
          <ArtifactTabView
            roomId={roomId}
            actor={subject ?? undefined}
            artifactId={artifactId}
            version={version}
            onVersion={setVersion}
            liveStates={liveStates}
            onLocalState={relayArtifactState}
            onOpenWindow={null}
          />
        </div>
      )}
    </div>
  );
}
