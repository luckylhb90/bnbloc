import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { onboardingEvents } from "@/lib/onboardingEvents";
import { feedSeqRange } from "@/lib/feedSeqRange";
import { XIcon } from "@phosphor-icons/react";
import { api, type AgentJob } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { useCoarsePointer, useStage } from "@/lib/capability";
import { useDock } from "@/lib/useDock";
import { useRoomStream } from "@/lib/useRoomStream";
import { reportRoomGone, type RoomAccessEnd, roomGoneText } from "@/lib/roomGone";
import type { RoomEvent } from "@/lib/roomEvents";
import { LazyDeliverablesView } from "@/views/workbench/LazyWorkbench";
import { useWorkbench } from "@/views/workbench/useWorkbench";
import { applyAppChanged } from "@/views/workbench/AppDataLine";
import { desktop } from "@/lib/desktop";
import { inDesktopShell } from "@/lib/config";
import { liveRouteHash } from "@/lib/previewRoute";
import { useLocalAgent } from "@/lib/useLocalAgent";
import { useToast } from "@/ui/Toast";
import type { ChatRecipient, FeedItem } from "@/views/room/types";
import { DismissibleLayer, PreviewDock, RoomRail, type RailSurface } from "@/views/room/RoomLayout";
import { useRoomChannels } from "@/views/room/useRoomChannels";
import { MyMessagesNav } from "@/views/room/RoomNav";
import { RoomHeader, RoomSearchBar } from "@/views/room/RoomHeader";
import { MessageFeed } from "@/views/room/MessageFeed";
import { RoomComposer } from "@/views/room/RoomComposer";
import { PeopleChatDock, PeopleChatPanel } from "@/views/room/PeopleChatPanel";
import { usePeopleChat } from "@/views/room/usePeopleChat";
import { shareComposerRef } from "@/lib/team";
import { RoomDialogs } from "@/views/room/RoomDialogs";
import { ActivityRail } from "@/views/room/ActivityRail";
import { useRoomMessageActions } from "@/views/room/useRoomMessageActions";
import { QuotaRequestDialog } from "@/views/team/QuotaRequestDialog";
import { usePendingQuotaCount } from "@/views/team/QuotaRequestsPanel";
import { borrowKey } from "@/lib/roomAgents";
import { jobQueueAhead } from "@/lib/platformAgents";
import { useDesktopHandoff } from "@/views/room/useDesktopHandoff";
import { HandoffBanner } from "@/views/room/HandoffBanner";
import { useHandoffExport } from "@/views/room/useHandoffExport";
import { canInvite, mayChat, readOnlyInRoom } from "@/lib/roomRoles";
import { can } from "@/lib/permissions";
import { EventMemberJoined, EventMemberRemoved } from "@/lib/wire.gen";
import { DesktopConnectBanner } from "@/views/DesktopConnect";
import { useRoomData } from "@/views/room/useRoomData";
import { useRoomFeed } from "@/views/room/useRoomFeed";
import { useRoomActivity } from "@/views/room/useRoomActivity";
import { useFeedScroll } from "@/views/room/useFeedScroll";
import { useRoomTeam } from "@/views/room/useRoomTeam";
import { MessageExtrasProvider } from "@/views/room/messageExtras";
import { AttachmentOpenerProvider } from "@/views/room/MessageAttachments";
import { FileAskProvider } from "@/views/workbench/FileAskPanel";
import { AutomateProvider, automateTaskSeed, type AutomateApp } from "@/views/room/automate";
import { useRoomFileAsk } from "@/views/workbench/useRoomFileAsk";
import { JobLiveProvider, JobProgressProvider } from "@/views/room/JobProgressStrip";
import { waitingFor, type JobLive } from "@/lib/jobLive";
import { SaveTemplateDialog } from "@/views/room/SaveTemplateDialog";
import { TemplateStartCard } from "@/views/room/TemplateStartCard";
import { SchedulesDialog } from "@/views/room/SchedulesDialog";
import { GroupChatsDialog } from "@/views/room/GroupChatsDialog";
import { RunsDialog } from "@/views/room/RunsDialog";
import { CONTENT_SAVED, contentSaves, parseContentSaved } from "@/lib/contentSavedEvents";
import { CONTENT_USES_CHANGED, contentUsesChanged } from "@/lib/contentUsesEvents";
import { useWorkshopSaves } from "@/views/room/useWorkshopSaves";
import { errorText } from "@/lib/errors";
import { DeliverableOwner } from "@/views/workbench/deliverableOwner";

type Panel = "chat" | "artifacts";
type RoomDialog = "settings" | "members" | "borrow" | "leave" | null;

// RoomView is one room. App keys it by room id, so EVERY piece of state here
// — channel, feed, jobs, artifact cards, dock, search, panels, dialogs,
// composer — starts fresh on a room switch; nothing is reset by hand.
// Channel-scoped state (feed, history, jobs, inline artifact cards) lives in
// scope-keyed hooks that restart when the channel changes and ignore late
// writes from the previous channel.

export function RoomView({
  roomId,
  onBack,
  onOpenRoom,
}: {
  roomId: string;
  onBack: () => void;
  onOpenRoom: (id: string) => void;
}) {
  const { subject, reauth, role: orgRole } = useAuth();
  const toast = useToast();
  // The active channel scopes the feed, the stream, sends and read cursors.
  // Null until the rail selects the room's default channel.
  const [channelId, setChannelId] = useState<string | null>(null);
  // Bumped when the active channel turned out stale (404): the rail reloads
  // the channel list and re-selects the room's default channel.
  const [channelsEpoch, setChannelsEpoch] = useState(0);
  const onStaleChannel = useCallback(() => {
    setChannelId(null);
    setChannelsEpoch((n) => n + 1);
  }, []);
  const [panel, setPanel] = useState<Panel>("chat");
  const [dialog, setDialog] = useState<RoomDialog>(null);
  const [confirmDelete, setConfirmDelete] = useState<FeedItem | null>(null);
  const [showSearch, setShowSearch] = useState(false);
  const [savingTemplate, setSavingTemplate] = useState(false);
  const [showSchedules, setShowSchedules] = useState(false);
  const [showGroups, setShowGroups] = useState(false);
  // An app's "Automate…" opens the schedules on a new AI task about it.
  const [automating, setAutomating] = useState<string | undefined>();
  const [showRuns, setShowRuns] = useState(false);
  const [search, setSearch] = useState("");
  const [starting, setStarting] = useState(false);
  const agent = useLocalAgent(true);
  // The room is gone for this member (detail 404/403, or the stream ended
  // access): say so once, drop it from every list that remembers it, and
  // leave — the route no longer points at a room that will never load.
  const goneRef = useRef(false);
  const leaveGoneRoom = useCallback(
    (reason: RoomAccessEnd | "missing") => {
      if (goneRef.current) return;
      goneRef.current = true;
      reportRoomGone(roomId);
      toast.info(roomGoneText(reason));
      onBack();
    },
    [roomId, toast, onBack],
  );
  const data = useRoomData({ roomId, subject, reauth, onGone: () => leaveGoneRoom("missing") });
  const { room, setRoom, roomAgents, joinedGen } = data;
  const handoff = useDesktopHandoff(roomId, subject, data.setRoster);
  // Owner/admin act on the room as a whole: export its handoff package, and
  // add the suggested members of an imported room.
  const me = data.me ?? null;
  const canManageRoom = !!me && canInvite(me);
  // Visitors follow the room but may not post (D1), and neither may a
  // read-only (viewer) organization role (the gateway refuses its chat);
  // unknown until the member list answers, so the composer never flashes
  // read-only.
  const readOnly = readOnlyInRoom(me, data.me !== undefined, can(orgRole, "drive_agent"));
  const handoffExport = useHandoffExport(roomId);

  // Layout: container stage drives the rail (inline/drawer/collapsed) and the
  // live-preview dock presentation.
  const roomRowRef = useRef<HTMLDivElement | null>(null);
  const roomStage = useStage(roomRowRef);
  const isCoarsePointer = useCoarsePointer();
  const [railDrawerOpen, setRailDrawerOpen] = useState(false);
  const railSurface: RailSurface = roomStage === "compact" ? "drawer" : "inline";
  const railCollapsed = roomStage === "medium";
  // The preview workbench: tabs of open deliverables beside the chat.
  const workbench = useWorkbench(roomId);
  const dock = useDock(roomStage, roomRowRef, workbench.open);
  const { dockMode, dockIsInline, dockIsOverlay, draggingDock, dockStyle, startDockDrag } = dock;
  // The guided tour's start step moves on once a room is open.
  useEffect(() => {
    onboardingEvents.emit({ kind: "room_opened", roomId });
  }, [roomId]);
  useEffect(() => {
    if (railSurface === "inline") setRailDrawerOpen(false);
  }, [railSurface]);

  // Conversation: feed + history for the active channel, kept scrolled.
  const scroller = useFeedScroll();
  const conversation = useRoomFeed({
    roomId,
    channelId,
    subject,
    joinedGen,
    beforePrepend: scroller.beforePrepend,
    onStaleChannel,
  });
  const { feed, setFeed } = conversation;
  useLayoutEffect(scroller.settle, [feed, scroller.settle]);
  // What the room tools save for the member is announced as it lands
  // (content_saved).
  useWorkshopSaves(roomId, subject);

  // Stream events dispatch through the latest handlers (activity first, then
  // the roster, then the feed) without resubscribing.
  const dispatch = useRef<(e: RoomEvent) => void>(() => {});
  const activityRef = useRef<ReturnType<typeof useRoomActivity> | null>(null);
  const stream = useRoomStream({
    roomId,
    subject,
    joinedGen,
    channelId,
    feed,
    historyLoaded: conversation.history.loaded,
    onMessageEvent: (e) => dispatch.current(e),
    onReconnect: () => activityRef.current?.bumpFiles(),
    onRevoked: leaveGoneRoom,
    // The room view also takes the people chat's events: its header badge
    // counts them (they never enter this feed).
    scope: { includePeople: true },
  });
  const { connected, showReconnecting, online, typingUsers, unreadByChannel, unreadFromSeq, liveReload } = stream;
  const feedSeqs = useMemo(() => feedSeqRange(feed), [feed]);
  const activity = useRoomActivity({ roomId, channelId, subject, liveReload, artifactsOpen: panel === "artifacts", feedSeqs });
  activityRef.current = activity;
  const { clearFilesBadge } = activity;
  dispatch.current = (e) => {
    if (team.applyEvent(e)) return;
    if (activity.applyEvent(e)) return;
    if (e.kind === "agent_roster") {
      if (e.rosterAgents) data.setRoster(e.rosterAgents);
      return;
    }
    // Someone joined or was removed: the member list (mentions, people,
    // and my own role) refreshes.
    if (e.kind === EventMemberJoined || e.kind === EventMemberRemoved) {
      data.reloadMembers();
      return;
    }
    if (applyAppChanged(roomId, e)) return;
    if (e.kind === CONTENT_USES_CHANGED) {
      contentUsesChanged.emit(roomId);
      return;
    }
    if (e.kind === CONTENT_SAVED) {
      const saved = parseContentSaved(e.payload);
      if (saved) contentSaves.emit(roomId, saved);
      return;
    }
    conversation.applyEvent(e);
  };

  // Borrow quota (F3): pending requests awaiting me as an owner (badge on the
  // authorization icon), and the "Request more" dialog a borrower gets when an
  // owner's run limit is used up.
  const pendingQuota = usePendingQuotaCount(roomId);
  const [quotaAsk, setQuotaAsk] = useState<{ recipient: ChatRecipient; runQuota?: number } | null>(null);
  const onQuotaExhausted = useCallback(
    (recipient: ChatRecipient, runQuota?: number) => setQuotaAsk({ recipient, runQuota }),
    [],
  );

  const { send, retry, deleteOwnMessage, editOwnMessage } = useRoomMessageActions({
    roomId,
    channelId,
    subject,
    setFeed,
    setRoom,
    onStaleChannel,
    onQuotaExhausted,
    onPlatformOffline: data.platform.refresh,
  });
  // A queued platform job shows how many of its host's jobs are ahead.
  const platformInRoom = data.platform.inRoom;
  const queueAhead = useCallback((job: AgentJob) => jobQueueAhead(job, platformInRoom), [platformInRoom]);

  const { openArtifact: openWorkbenchTab, openDeliverables, openLive } = workbench;
  // A job's live view in its own window: the desktop's, or a browser tab
  // at its deep link on the web (it works after the room is closed).
  const popOutLive = useCallback(
    async (jobId: string): Promise<boolean> => {
      if (inDesktopShell()) return desktop.popOutLive(roomId, jobId);
      return window.open(`${window.location.pathname}${window.location.search}${liveRouteHash(roomId, jobId)}`, "_blank", "noopener") !== null;
    },
    [roomId],
  );
  // "Continue" on a view that waited for its owner (a sign-in, browser
  // component PRD §3.7): the owner's own message to the same agent, which
  // opens the turn that resumes the run.
  const continueLive = useCallback(
    (live: JobLive) => {
      const recipient = roomAgents.find((a) => a.ownerId === live.owner && a.provider === live.provider && (!live.core || !a.core || a.core === live.core));
      if (!recipient) {
        toast.info("Tell the agent in the room that you are done");
        return;
      }
      void send("Done, continue.", { peopleOnly: false, recipient });
    },
    [roomAgents, send, toast],
  );
  const liveRoom = useMemo(
    () => ({ roomId, live: activity.jobLive, open: openLive, subject, onContinue: continueLive }),
    [roomId, activity.jobLive, openLive, subject, continueLive],
  );
  // A live tab closes with its view (the turn ended, or the view waiting
  // for a person was ended by its tool).
  const { state: wbState, dispatch: wbDispatch } = workbench;
  useEffect(() => {
    for (const t of wbState.tabs) if (t.liveJobId && !activity.jobLive[t.liveJobId]) wbDispatch({ type: "close", key: t.key });
  }, [wbState.tabs, activity.jobLive, wbDispatch]);
  // A view waiting for this member (a sign-in on their machine) opens in
  // the sidebar once, so they can do it here without hunting for a window.
  const waitingLive = waitingFor(activity.jobLive, subject ?? null, channelId);
  const openedWaiting = useRef("");
  useEffect(() => {
    if (!waitingLive || openedWaiting.current === waitingLive.jobId) return;
    openedWaiting.current = waitingLive.jobId;
    openLive(waitingLive.jobId);
  }, [waitingLive, openLive]);
  const openArtifactInFiles = useCallback(
    (artifactId: string) => {
      clearFilesBadge();
      openWorkbenchTab(artifactId);
    },
    [openWorkbenchTab, clearFilesBadge],
  );
  const viewAllDeliverables = useCallback((seq: number) => openDeliverables({ sourceSeq: seq }), [openDeliverables]);
  // Local artifact gesture -> room stream (every other viewer and any
  // room-watching agent receives it as artifact_state).
  const relayArtifactState = useCallback(
    (artifactId: string, state: unknown) => {
      void api.artifactState(roomId, artifactId, state).catch(() => {});
    },
    [roomId],
  );
  // Cancelling is a room action authorized by membership, so it works even
  // when another member's bridge runs the turn. A failure means the job just
  // finished or was stopped; the next job event reconciles the row.
  const cancelJob = useCallback(
    (jobId: string) => {
      void api.cancelAgentJob(roomId, jobId).catch(() => {});
    },
    [roomId],
  );
  const requestDelete = useCallback((item: FeedItem) => {
    // An unsent failed row has nothing on the server: discard without asking.
    if (item.seq == null) void deleteOwnMessage(item);
    else setConfirmDelete(item);
  }, [deleteOwnMessage]);

  // Start an agent for the selected target: the local core hosts it in its own
  // workspace, adopted as a new room; navigate to it.
  const startAgent = async () => {
    if (!agent.target) {
      toast.info("Select a local agent first (composer dropdown)");
      return;
    }
    setStarting(true);
    try {
      const r = await api.runLocalAgent({
        prompt: "Hello",
        agentTargetId: agent.target,
        ...agent.devAddress,
      });
      toast.success("Agent session started");
      onOpenRoom(r.roomId);
    } catch (e) {
      toast.error(errorText(e, "Could not start the local agent"));
    } finally {
      setStarting(false);
    }
  };

  const leave = async () => {
    try {
      await api.leaveRoom(roomId);
      toast.success("Left room");
      onBack();
    } catch {
      toast.error("Could not leave room");
    }
  };

  // Client-side search over the loaded messages of this channel.
  const q = search.trim().toLowerCase();
  const visibleFeed = useMemo(() => (q ? feed.filter((m) => m.text.toLowerCase().includes(q)) : feed), [feed, q]);
  const closeSearch = () => {
    setSearch("");
    setShowSearch(false);
  };
  const selectPanel = useCallback(
    (next: Panel) => {
      setPanel(next);
      if (next === "artifacts") clearFilesBadge();
      if (railSurface === "drawer") setRailDrawerOpen(false);
    },
    [railSurface, clearFilesBadge],
  );
  const selectChannel = useCallback(
    (next: string) => {
      setChannelId(next);
      setSearch("");
      if (railSurface === "drawer") setRailDrawerOpen(false);
    },
    [railSurface],
  );
  // Channel list + default-channel selection belong to the room, not to the
  // rail that renders them (a narrow room's rail drawer is unmounted).
  const roomChannels = useRoomChannels(roomId, channelsEpoch, channelId, selectChannel);
  const closeDialog = useCallback(() => setDialog(null), []);
  // The people chat: a human-only side conversation agents never see. Docked
  // beside the timeline on a wide room, a full-screen sheet otherwise, its
  // own window in the desktop shell.
  const member = !!room && !("preview" in room);
  const automate = useMemo<AutomateApp | null>(
    () =>
      member
        ? {
            open: (app) => {
              setAutomating(automateTaskSeed(app));
              setShowSchedules(true);
            },
            groups: () => setShowGroups(true),
          }
        : null,
    [member],
  );
  const peopleChat = usePeopleChat({
    roomId,
    joinedGen,
    enabled: room ? member : null,
    unreadByChannel,
  });
  const peopleUnread = peopleChat.channelId ? unreadByChannel[peopleChat.channelId] ?? 0 : 0;
  const peopleDocked = roomStage === "wide";
  const { refreshUnread } = stream;
  const { closePanel: closePeoplePanel } = peopleChat;
  const closePeopleChat = useCallback(() => {
    closePeoplePanel();
    refreshUnread();
  }, [closePeoplePanel, refreshUnread]);
  const askAgent = useCallback(
    (item: FeedItem) => {
      shareComposerRef(roomId, { id: item.id, author: item.author, text: item.text });
      setPanel("chat");
      if (!peopleDocked) closePeoplePanel();
      toast.info("Added to the room composer — send it to ask the agent");
    },
    [roomId, peopleDocked, closePeoplePanel, toast],
  );
  // Team features: message tasks, "Save as prompt", jump-to-message.
  const team = useRoomTeam({
    roomId,
    channelId,
    joinedGen,
    feed,
    history: conversation.history,
    loadEarlier: conversation.loadEarlier,
    selectChannel,
    jumpReady: peopleChat.resolved,
    divertJump: peopleChat.divertJump,
  });

  const rail = (
    <RoomRail
      roomId={roomId}
      activeChannel={channelId}
      channels={roomChannels}
      onSelectChannel={selectChannel}
      unreadByChannel={unreadByChannel}
      panel={panel}
      filesBadge={activity.filesBadge}
      collapsed={railCollapsed}
      surface={railSurface}
      onSelectPanel={selectPanel}
    />
  );
  // A file's Ask AI panel (workbench) and the file in front of the room:
  // the composer its sends are built by, and the room composer's starters
  // that follow the focused file (workbench/useRoomFileAsk).
  const { fileAsk, roomFileContext } = useRoomFileAsk({ roomId, channelId, feed, data, readOnly, agent, subject, online, send });
  const dockPanel = workbench.open ? (
    <PreviewDock
      mode={dockMode}
      roomId={roomId}
      actor={subject ?? undefined}
      channelId={channelId}
      refreshSignal={activity.filesSignal}
      workbench={workbench}
      dockStyle={dockStyle}
      draggingDock={draggingDock}
      liveStates={activity.liveStates}
      onLocalState={relayArtifactState}
      onClose={workbench.closeWorkbench}
      onPopOut={inDesktopShell() ? (artifactId) => desktop.popOutPreview(roomId, artifactId) : undefined}
      jobLive={activity.jobLive}
      onPopOutLive={popOutLive}
      onContinueLive={continueLive}
      onStartDrag={startDockDrag}
    />
  ) : null;

  const peoplePanel = (peopleChannelId: string) => (
    <PeopleChatPanel
      roomId={roomId}
      channelId={peopleChannelId}
      subject={subject}
      joinedGen={joinedGen}
      agent={agent}
      members={data.members}
      online={online}
      jumpSeq={peopleChat.jumpSeq}
      onJumped={peopleChat.clearJump}
      onAskAgent={askAgent}
      onClose={closePeopleChat}
      onPopOut={
        inDesktopShell()
          ? () => {
              void desktop.popOutPeopleChat(roomId).then((ok) => {
                if (ok) closePeopleChat();
              });
            }
          : undefined
      }
      onGone={leaveGoneRoom}
    />
  );

  return (
    <DeliverableOwner.Provider value={!!me && mayChat(me)}>
    <div className="coarse-targets relative flex h-full min-h-0 flex-col" data-room-stage={roomStage} data-pointer={isCoarsePointer ? "coarse" : "fine"}>
      <RoomHeader
        room={room}
        roomId={roomId}
        me={me}
        roomStage={roomStage}
        railSurface={railSurface}
        connected={connected}
        showReconnecting={showReconnecting}
        online={online}
        onOpenRailDrawer={() => setRailDrawerOpen(true)}
        onBack={onBack}
        onToggleSearch={() => (showSearch ? closeSearch() : setShowSearch(true))}
        onShowBorrow={() => setDialog("borrow")}
        borrowBadge={pendingQuota.count}
        onShowMembers={() => setDialog("members")}
        onShowSettings={() => setDialog("settings")}
        onConfirmLeave={() => setDialog("leave")}
        onExportHandoff={canManageRoom ? () => void handoffExport.run() : undefined}
        exportingBytes={handoffExport.exporting ? handoffExport.progressBytes : null}
        onSaveTemplate={canManageRoom ? () => setSavingTemplate(true) : undefined}
        onShowSchedules={member ? () => setShowSchedules(true) : undefined}
        onShowRuns={member ? () => setShowRuns(true) : undefined}
        onTogglePeopleChat={
          member ? () => (peopleChat.open ? closePeopleChat() : peopleChat.openPanel()) : undefined
        }
        peopleChatOpen={peopleChat.open}
        peopleChatUnread={peopleUnread}
        platformAgents={platformInRoom}
      />
      <ActivityRail
        working={activity.working}
        agentJobs={activity.agentJobs}
        recentArtifactChanges={activity.recentArtifactChanges}
        onCancelJob={cancelJob}
        queueAhead={queueAhead}
      />
      {showSearch && (
        <RoomSearchBar search={search} query={q} matchCount={visibleFeed.length} onSearchChange={setSearch} onClose={closeSearch} />
      )}
      {/* The empty-room card already carries the same guidance. */}
      {!(handoff.show && feed.length === 0 && activity.working.size === 0) && <DesktopConnectBanner subject={subject} hasPlatformAgent={platformInRoom.length > 0} />}
      <HandoffBanner roomId={roomId} canInvite={canManageRoom} members={data.members} />
      <TemplateStartCard roomId={roomId} subject={subject} members={data.members} canInvite={canManageRoom} />

      {/* Channel rail + feed + composer share one row; the rail scopes all
          three to the selected channel. */}
      <AttachmentOpenerProvider value={workbench.openAttachment}>
      <AutomateProvider value={automate}>
      <FileAskProvider value={fileAsk}>
      <div ref={roomRowRef} data-room-row className="relative flex min-h-0 flex-1 overflow-hidden">
        {railSurface === "inline" && rail}
        {panel === "artifacts" ? (
          <div className="min-w-0 flex-1">
            <LazyDeliverablesView
              roomId={roomId}
              channelId={channelId}
              refreshSignal={activity.filesSignal}
              onOpen={openArtifactInFiles}
              allowCreate
            />
          </div>
        ) : (
          <div data-room-chat-column className="@container relative flex min-w-0 flex-1 basis-0 flex-col">
            {team.strip}
            <div
              ref={scroller.scrollRef}
              data-tour="feed"
              onScroll={scroller.onScroll}
              className="min-w-0 flex-1 overflow-y-auto px-4 py-4 @5xl:px-6 @5xl:py-6"
            >
              <div ref={scroller.contentRef}>
                <MessageExtrasProvider value={team.extras}>
                <JobProgressProvider value={activity.jobProgress}>
                <JobLiveProvider value={liveRoom}>
                <MessageFeed
                  loaded={!!room}
                  roomId={roomId}
                  feed={feed}
                  visibleFeed={visibleFeed}
                  working={activity.working}
                  query={q}
                  search={search}
                  agentTarget={agent.target}
                  agentIsRemoteGateway={agent.isRemoteGateway}
                  starting={starting}
                  hasMoreHistory={conversation.history.hasMore}
                  loadingHistory={conversation.history.loading}
                  unreadFromSeq={unreadFromSeq}
                  showDesktopHandoff={handoff.show}
                  agentJobs={activity.agentJobs}
                  artifactsBySeq={activity.artifactsBySeq}
                  onStartAgent={() => void startAgent()}
                  onLoadEarlier={conversation.loadEarlier}
                  onRetry={retry}
                  onDelete={requestDelete}
                  onEdit={editOwnMessage}
                  onCancelJob={cancelJob}
                  queueAhead={queueAhead}
                  onOpenArtifactInFiles={openArtifactInFiles}
                  onViewAllDeliverables={viewAllDeliverables}
                  agentReady={roomAgents.some((a) => !a.platform) || data.platform.choices.some((c) => c.inRoom && c.online)}
                />
                </JobLiveProvider>
                </JobProgressProvider>
                </MessageExtrasProvider>
              </div>
            </div>

            {/* Composer (fresh per channel: draft, recipient and its restored
                per-channel preference), or the join panel for a preview. */}
            <RoomComposer
              key={channelId ?? ""}
              roomPreview={!!room && "preview" in room}
              readOnly={readOnly}
              agent={agent}
              subject={subject}
              roomId={roomId}
              channelId={channelId}
              typingUsers={typingUsers}
              roomAgents={roomAgents}
              roomMembers={data.members}
              online={online}
              feed={feed}
              joining={data.joining}
              onSend={send}
              onJoin={() => void data.join()}
              platformAgents={data.platform}
              modelCatalogs={data.modelCatalogs}
              working={activity.working.size}
              fileContext={roomFileContext}
            />
          </div>
        )}
        {peopleChat.open && peopleChat.channelId && peopleDocked && (
          <PeopleChatDock>{peoplePanel(peopleChat.channelId)}</PeopleChatDock>
        )}
        {(dockIsInline || dockIsOverlay) && dockPanel}
        {peopleChat.open && peopleChat.channelId && !peopleDocked && (
          <DismissibleLayer
            label="People chat"
            closeLabel="Close the people chat"
            onClose={closePeopleChat}
            surfaceClassName="flex h-full w-full flex-col"
          >
            {peoplePanel(peopleChat.channelId)}
          </DismissibleLayer>
        )}
        {railSurface === "drawer" && railDrawerOpen && (
          <DismissibleLayer
            label="Channels and room documents"
            closeLabel="Close channels and documents"
            onClose={() => setRailDrawerOpen(false)}
            surfaceClassName="h-full border-r"
          >
            <div className="pad-safe pad-safe-t flex h-full flex-col [--pad-safe-y:0.75rem]">
              <div className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-3">
                <span className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Room surfaces</span>
                <button
                  type="button"
                  data-layer-close
                  onClick={() => setRailDrawerOpen(false)}
                  className="tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
                  aria-label="Close channels and documents"
                  title="Close"
                >
                  <XIcon size={18} />
                </button>
              </div>
              <div className="min-h-0 flex-1">{rail}</div>
            </div>
          </DismissibleLayer>
        )}
      </div>
      </FileAskProvider>
      </AutomateProvider>
      </AttachmentOpenerProvider>

      {room && feed.length > 0 && <MyMessagesNav items={feed} />}
      {team.dialogs}

      <RoomDialogs
        roomId={roomId}
        room={room}
        me={me}
        subject={subject}
        online={online}
        roomAgents={roomAgents}
        platformAgents={data.platform}
        dialog={dialog}
        confirmDelete={confirmDelete}
        onCloseDialog={closeDialog}
        onRoomChanged={setRoom}
        onRoomDeleted={onBack}
        onLeave={() => void leave()}
        onCloseConfirmDelete={() => setConfirmDelete(null)}
        onConfirmDelete={(it) => void deleteOwnMessage(it)}
        onQuotaChanged={pendingQuota.refresh}
      />
      <SaveTemplateDialog
        open={savingTemplate}
        roomId={roomId}
        roomName={room?.name || roomId}
        roomAgents={roomAgents}
        onClose={() => setSavingTemplate(false)}
      />
      {showSchedules && (
        <SchedulesDialog
          roomId={roomId}
          subject={subject}
          moderator={canManageRoom}
          roomAgents={roomAgents}
          localTargets={agent.targets}
          aiTask={automating}
          onClose={() => {
            setShowSchedules(false);
            setAutomating(undefined);
          }}
        />
      )}
      {showGroups && <GroupChatsDialog roomId={roomId} mayChange={member} onClose={() => setShowGroups(false)} />}
      {showRuns && <RunsDialog roomId={roomId} onClose={() => setShowRuns(false)} />}
      {quotaAsk && (
        <QuotaRequestDialog
          roomId={roomId}
          owner={quotaAsk.recipient.ownerId}
          provider={borrowKey(quotaAsk.recipient)}
          providerLabel={quotaAsk.recipient.provider}
          runQuota={quotaAsk.runQuota}
          onClose={() => setQuotaAsk(null)}
        />
      )}
    </div>
    </DeliverableOwner.Provider>
  );
}
