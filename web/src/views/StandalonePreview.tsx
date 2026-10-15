import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { can } from "@/lib/permissions";
import { subscribeRoom } from "@/lib/sse";
import type { RoomEvent } from "@/lib/roomEvents";
import { isRoomGoneStatus, reportRoomGone, roomAccessEnded, type RoomAccessEnd, roomGoneText } from "@/lib/roomGone";
import { readOnlyInRoom } from "@/lib/roomRoles";
import { useLocalAgent } from "@/lib/useLocalAgent";
import { EventMemberJoined, EventMemberRemoved } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";
import { useRoomData } from "@/views/room/useRoomData";
import { useRoomFeed } from "@/views/room/useRoomFeed";
import { useRoomMessageActions } from "@/views/room/useRoomMessageActions";
import { ArtifactTabView } from "@/views/workbench/ArtifactTabView";
import { invalidateArtifacts } from "@/views/workbench/artifactData";
import { applyAppChanged } from "@/views/workbench/AppDataLine";
import { FileAskProvider } from "@/views/workbench/FileAskPanel";
import { useRoomFileAsk } from "@/views/workbench/useRoomFileAsk";

// StandalonePreview is the torn-out window for one artifact: the pop-out of
// a workbench tab (desktop shell). It hosts the same tab view at full
// viewport — the viewer and its controls, versions, compare, restore — with
// the same live collaboration wiring (artifact_state relay + remote state) so
// a game or canvas popped onto a second screen keeps syncing with the room.
//
// The route is #/preview/<roomId>/<artifactId>; the shell creates the window
// with that hash and injects the gateway base (same init script as main).
//
// The window has the Ask AI panel too (docs/prd/2026-10-14-select-edit-review.md):
// it is its own window with no room view, so it loads what the panel needs
// (members, agents, the default channel's feed) with the room's own hooks,
// and builds the panel's composer with the same hook as the room
// (useRoomFileAsk). Requests go to the room's default channel, and the
// window reads no cursor: opening a file here marks nothing read.

let liveStateNonce = 0;

// Nothing scrolls here, so there is nothing to anchor before older messages
// load. A module constant: useRoomFeed reloads when this changes, so a new
// function per render would reload the feed on every render.
const NO_ANCHOR = () => {};

export function StandalonePreview({ roomId, artifactId }: { roomId: string; artifactId: string }) {
  // The window shares the app's signed-in session (one origin, one
  // localStorage): own identity filters self-echoes and matches a removal.
  const { subject, reauth, role: orgRole } = useAuth();
  const toast = useToast();
  const agent = useLocalAgent(true);
  const [version, setVersion] = useState<number | undefined>(undefined);
  // The room is gone for this member: the window says so and stops its
  // stream instead of reconnecting into 403s.
  const [gone, setGone] = useState<RoomAccessEnd | "missing" | null>(null);
  const [liveStates, setLiveStates] = useState<
    Record<string, { state: unknown; actor: string; nonce: number }>
  >({});

  // The room's data for the Ask AI panel: members, agents, hosts.
  const data = useRoomData({ roomId, subject, reauth, onGone: () => setGone("missing") });
  const { setRoom, joinedGen } = data;
  // Requests go to the room's default channel (listed first).
  const [channelId, setChannelId] = useState<string | null>(null);
  const [channelsEpoch, setChannelsEpoch] = useState(0);
  useEffect(() => {
    let cancelled = false;
    api
      .channels(roomId)
      .then((r) => !cancelled && setChannelId((r.channels.find((c) => c.isDefault) ?? r.channels[0])?.channelId ?? null))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [roomId, channelsEpoch]);
  const onStaleChannel = useCallback(() => {
    setChannelId(null);
    setChannelsEpoch((n) => n + 1);
  }, []);
  const conversation = useRoomFeed({ roomId, channelId, subject, joinedGen, beforePrepend: NO_ANCHOR, onStaleChannel });
  const onQuotaExhausted = useCallback(
    // The run-limit request dialog lives in the room window.
    () => toast.info("This agent's run limit is used up. Ask the owner for more from the room."),
    [toast],
  );
  const { send } = useRoomMessageActions({
    roomId,
    channelId,
    subject,
    setFeed: conversation.setFeed,
    setRoom,
    onStaleChannel,
    onQuotaExhausted,
    onPlatformOffline: data.platform.refresh,
  });
  const readOnly = readOnlyInRoom(data.me ?? null, data.me !== undefined, can(orgRole, "drive_agent"));
  const { fileAsk } = useRoomFileAsk({ roomId, channelId, feed: conversation.feed, data, readOnly, agent, subject, online: [], send });

  // The stream's events reach the latest handlers without resubscribing.
  const dispatch = useRef<(e: RoomEvent) => void>(() => {});
  dispatch.current = (e) => {
    if (e.kind === "agent_roster") {
      if (e.rosterAgents) data.setRoster(e.rosterAgents);
      return;
    }
    if (e.kind === EventMemberJoined || e.kind === EventMemberRemoved) {
      data.reloadMembers();
      return;
    }
    conversation.applyEvent(e);
  };

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
      if (applyAppChanged(roomId, e)) return;
      dispatch.current(e);
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
          <FileAskProvider value={fileAsk}>
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
          </FileAskProvider>
        </div>
      )}
    </div>
  );
}
