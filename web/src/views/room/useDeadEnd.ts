import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { api, type AgentJob, type MemberComponent } from "@/lib/api";
import { ENHANCE_PROMPT, runGatedJSONAssist, runJSONAssist, runTextAssist, useAssistTask } from "@/lib/assist";
import {
  deadEndCards,
  deadEndMemo,
  fallbackFixes,
  FIND_A_WAY,
  findStep,
  lastReplyOf,
  ownSettledTurn,
  parseProposal,
  requestOf,
  withHostLine,
  type DeadEndCardState,
  type DeadEndScope,
} from "@/lib/deadEnd";
import { desktop } from "@/lib/desktop";
import { errorText } from "@/lib/errors";
import { mayAllowHosts, type SandboxStatus } from "@/lib/sandboxStatus";
import { agentHandle } from "@/lib/roomAgents";
import type { RoomEvent } from "@/lib/roomEvents";
import { composerAgentPicks, composerDrafts, composerFilePicks } from "@/lib/team";
import { SandboxedOwn, SandboxSettingPrivateHosts } from "@/lib/wire.gen";
import { routeHash } from "@/lib/hashRoute";
import { useToast } from "@/ui/Toast";
import type { RoomAgent } from "@/views/composer/types";
import type { SendOptions } from "@/views/room/useRoomMessageActions";
import type { FeedItem } from "@/views/room/types";

// useDeadEnd is a room's "never a dead end" (docs/prd/2026-10-20-never-a-
// dead-end.md §2.3): when the member's own turn settles live in their open
// room, the find-a-way assist runs gated on the reply, and a proposal shows
// as a card under it for this member only (local state, never posted). Any
// agent reply's "Didn't work? Find a way" runs it without the gate; with no
// assist model it offers the fixed fallback. Each fix runs an existing
// flow, and only on the member's click. The cards live in the session's
// store (lib/deadEnd.ts deadEndCards), not in this view: a find still
// running when the member leaves the room shows its card when they are
// back, and a failed one says so on its card.
/** What the card reaches (views/room/DeadEndCard.tsx). */
export interface DeadEndRoom {
  readonly cards: Readonly<Record<number, DeadEndCardState>>;
  readonly find: (reply: FeedItem) => void;
  readonly dismiss: (seq: number) => void;
  /** The component a fix names, as the member catalog lists it. */
  readonly component: (name: string) => MemberComponent | undefined;
  /** Installs it (the member confirmed); the component when it needs
   *  setting up, else null. */
  readonly install: (name: string) => Promise<MemberComponent | null>;
  readonly askAgent: (reply: FeedItem, handle: string) => void;
  readonly attach: () => void;
  readonly ownAgent: (reply: FeedItem) => void;
  readonly rephrase: (reply: FeedItem) => Promise<void>;
  /** Sends the member's reviewed note to the admins: whether it was
   *  queued for one. */
  readonly report: (reply: FeedItem, text: string) => Promise<boolean>;
  /** Lets the member's own sandboxed agents reach host (the member
   *  confirmed): adds it to their sandbox's private hosts on this computer. */
  readonly allowHost: (host: string) => Promise<void>;
  /** The sandbox's state on this computer, read again (the card waits for
   *  the host to open before offering to ask again). */
  readonly sandbox: () => Promise<SandboxStatus | null>;
  /** Sends the reply's request again, to the same agent. */
  readonly askAgain: (reply: FeedItem) => void;
}

/** Turns settled but whose reply is not in the feed yet wait at most for
 *  this many. */
const MAX_PENDING = 20;

export function useDeadEnd({
  roomId,
  channelId,
  subject,
  feed,
  roomAgents,
  send,
  agentJobs,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly subject: string | null;
  readonly feed: readonly FeedItem[];
  readonly roomAgents: readonly RoomAgent[];
  readonly send: (text: string, opts: SendOptions) => Promise<boolean>;
  /** The channel's agent jobs by the request's seq (useRoomActivity). */
  readonly agentJobs: Readonly<Record<number, readonly AgentJob[]>>;
}): { readonly observe: (e: RoomEvent) => void; readonly room: DeadEndRoom } {
  const toast = useToast();
  const findTask = useAssistTask(FIND_A_WAY);
  const enhanceTask = useAssistTask(ENHANCE_PROMPT);
  const cards = useSyncExternalStore(deadEndCards.subscribe, () => deadEndCards.room(roomId));
  const [catalog, setCatalog] = useState<readonly MemberComponent[]>([]);
  const pending = useRef(new Set<string>());
  const [settled, setSettled] = useState(0);
  const feedRef = useRef(feed);
  feedRef.current = feed;
  const jobsRef = useRef(agentJobs);
  jobsRef.current = agentJobs;

  const hasOwnAgent = roomAgents.some((a) => a.self && !a.platform);
  const setCard = useCallback((seq: number, card: DeadEndCardState | null) => deadEndCards.set(roomId, seq, card), [roomId]);

  // The scope a reply's fixes are held to: what the catalog lists as
  // installable now, the room's agents and, when the reply's run was the
  // member's own agent in their agent sandbox on this computer, whether
  // they may allow it a private host there.
  const scopeOf = useCallback(
    async (reply: FeedItem): Promise<DeadEndScope> => {
      let list = catalog;
      try {
        list = (await api.components()).components ?? [];
        setCatalog(list);
      } catch {
        /* the last catalog read: an install then still asks */
      }
      const job = reply.jobId ? Object.values(jobsRef.current).flat().find((j) => j.jobId === reply.jobId) : undefined;
      const sandboxedRun = !!job && job.sandboxed === SandboxedOwn && job.ownerUserId === subject;
      let sandbox: SandboxStatus | null = null;
      if (sandboxedRun) sandbox = await desktop.sandboxStatus().catch(() => null);
      return {
        installable: list.filter((c) => !c.installed).map((c) => c.name),
        agents: roomAgents.map(agentHandle),
        replier: reply.agentLabel,
        sandboxedRun,
        mayAllowHosts: !!sandbox && mayAllowHosts(sandbox),
        openHosts: sandbox?.privateHosts.hosts ?? [],
      };
    },
    [catalog, roomAgents, subject],
  );

  // compute runs find-a-way on a reply, once: gated (on its own, after a
  // turn) it shows a card only for a proposal; asked from the menu it shows
  // the fallback when there is no proposal.
  const compute = useCallback(
    async (reply: FeedItem & { readonly seq: number }, gated: boolean) => {
      if (!gated) setCard(reply.seq, { status: "finding" });
      // Until the catalog answers: the room's agents, nothing installable.
      let scope: DeadEndScope = { installable: [], agents: roomAgents.map(agentHandle), replier: reply.agentLabel };
      const fallback = (note: string): DeadEndCardState => ({ status: "ready", proposal: null, fixes: fallbackFixes(scope), note });
      // Every way out leaves "finding": a failure is the card's note.
      try {
        scope = await scopeOf(reply);
        const body = {
          text: "",
          roomId,
          channelId: channelId ?? undefined,
          replySeq: reply.seq,
          hasOwnAgent,
          sandboxedRun: scope.sandboxedRun,
          mayAllowHosts: scope.mayAllowHosts,
        };
        const out = gated ? await runGatedJSONAssist(FIND_A_WAY, body) : await runJSONAssist(FIND_A_WAY, body);
        const proposal = out ? parseProposal(out.fields, scope) : null;
        deadEndMemo.set(roomId, reply.seq, proposal ? { kind: "proposal", proposal } : { kind: "skipped" });
        if (proposal) setCard(reply.seq, { status: "ready", proposal, fixes: proposal.fixes.length > 0 ? proposal.fixes : fallbackFixes(scope) });
        else if (!gated) setCard(reply.seq, fallback("Nothing specific to propose: these may help."));
      } catch (e) {
        if (!gated) setCard(reply.seq, fallback(errorText(e, "Could not find a way")));
      }
    },
    [roomId, channelId, hasOwnAgent, roomAgents, scopeOf, setCard],
  );

  // A settled turn of the member's own, live in this channel.
  const observe = useCallback(
    (e: RoomEvent) => {
      if (e.kind !== "agent_job_updated") return;
      const job = e.payload as AgentJob | undefined;
      if (!job || !ownSettledTurn(job, subject, channelId) || pending.current.size >= MAX_PENDING) return;
      pending.current.add(job.jobId);
      setSettled((n) => n + 1);
    },
    [subject, channelId],
  );
  useEffect(() => {
    // No assist model: nothing runs on its own.
    if (!findTask) return;
    for (const jobId of [...pending.current]) {
      const reply = lastReplyOf(feed, jobId);
      if (!reply) continue;
      pending.current.delete(jobId);
      if (!deadEndMemo.has(roomId, reply.seq)) void compute(reply, true);
    }
  }, [feed, settled, findTask, roomId, compute]);

  const find = useCallback(
    (reply: FeedItem) => {
      if (reply.seq == null) return;
      const seq = reply.seq;
      const known = deadEndMemo.get(roomId, seq);
      switch (findStep(deadEndCards.get(roomId, seq), known, !!findTask)) {
        case "wait":
          return;
        case "proposal":
          if (known?.kind !== "proposal") return;
          void scopeOf(reply).then((scope) =>
            setCard(seq, { status: "ready", proposal: known.proposal, fixes: known.proposal.fixes.length > 0 ? known.proposal.fixes : fallbackFixes(scope) }),
          );
          return;
        case "fallback":
          void scopeOf(reply).then((scope) =>
            setCard(seq, { status: "ready", proposal: null, fixes: fallbackFixes(scope), note: "AI assist is not available here: these may help." }),
          );
          return;
        case "run":
          void compute({ ...reply, seq }, false);
      }
    },
    [roomId, findTask, scopeOf, compute, setCard],
  );

  // The request the reply answers, or a toast when it is not loaded.
  const requestText = useCallback(
    (reply: FeedItem): FeedItem | null => {
      const req = requestOf(feedRef.current, reply);
      if (!req) toast.info("The request is not in the loaded messages: scroll up to it first");
      return req ?? null;
    },
    [toast],
  );

  const room = useMemo<DeadEndRoom>(
    () => ({
      cards,
      find,
      dismiss: (seq) => setCard(seq, null),
      component: (name) => catalog.find((c) => c.name === name),
      install: async (name) => {
        const c = catalog.find((x) => x.name === name);
        if (!c) throw new Error(`${name} is no longer listed`);
        await api.installComponent(c);
        const list = (await api.components()).components ?? [];
        setCatalog(list);
        const added = list.find((x) => x.kind === c.kind && x.name === c.name);
        return added && (added.settingsMissing ?? []).length > 0 ? added : null;
      },
      askAgent: (reply, handle) => {
        const req = requestText(reply);
        const recipient = roomAgents.find((a) => agentHandle(a) === handle);
        if (!req || !recipient) return;
        const attachments = req.attachments ?? [];
        void send(req.text, { peopleOnly: false, recipient, attachmentIds: attachments.map((a) => a.id), attachments });
      },
      attach: () => composerFilePicks.queue(roomId, true),
      ownAgent: (reply) => {
        const mine = roomAgents.find((a) => a.self && !a.platform); // which-one: fills the composer's empty pick only; the member sends, and the toast names the agent
        if (!mine) {
          // The desktop app and its local agents: the download in a
          // browser, the set-up in the desktop app.
          window.location.hash = routeHash({ kind: "view", view: "agents" });
          return;
        }
        composerAgentPicks.queue(roomId, { core: mine.core ?? "", provider: mine.provider, own: true });
        const req = requestText(reply);
        if (req) composerDrafts.queue(roomId, req.text);
        toast.info(`${agentHandle(mine)} is picked: send the request to it`);
      },
      rephrase: async (reply) => {
        const req = requestText(reply);
        if (!req) return;
        let text = req.text;
        if (enhanceTask) {
          try {
            text = (await runTextAssist(ENHANCE_PROMPT, { text, roomId, channelId: channelId ?? undefined }, { onDelta: () => {} })).text;
          } catch (e) {
            toast.error(errorText(e, "Could not rewrite the request: edit it yourself"));
          }
        }
        composerDrafts.queue(roomId, text);
      },
      report: async (reply, text) => (await api.reportRoom(roomId, { text, seq: reply.seq, channelId: channelId ?? undefined })).queued,
      allowHost: async (host) => {
        const sandbox = await desktop.sandboxStatus();
        const item = sandbox?.name ? (await desktop.componentSettings())?.find((i) => i.kind === "sandbox" && i.name === sandbox.name) : undefined;
        const field = item?.fields.find((f) => f.key === SandboxSettingPrivateHosts);
        if (!item || !field) throw new Error("Your agent sandbox takes no private hosts on this computer");
        await desktop.saveComponentSettings("sandbox", item.name, { values: { [SandboxSettingPrivateHosts]: withHostLine(field.value ?? "", host) } });
      },
      sandbox: () => desktop.sandboxStatus(),
      askAgain: (reply) => {
        const req = requestText(reply);
        if (!req) return;
        // The agent that gave this reply, and no other: its handle names owner, core and
        // provider. When it is not in the room any more there is nobody to ask again, and
        // the member is told (never "my first agent").
        const recipient = roomAgents.find((a) => agentHandle(a) === reply.agentLabel);
        if (!recipient) {
          toast.info("The agent that answered is no longer in the room");
          return;
        }
        const attachments = req.attachments ?? [];
        void send(req.text, { peopleOnly: false, recipient, attachmentIds: attachments.map((a) => a.id), attachments });
      },
    }),
    [cards, find, setCard, catalog, toast, requestText, roomAgents, send, roomId, channelId, enhanceTask],
  );
  return { observe, room };
}
