import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useInjectedAgent, useInjectedAttachments, useInjectedDraft, useInjectedFilePick, useInjectedReferences } from "@/views/composer/useInjectedDraft";
import {
  PaperPlaneRightIcon,
  HashIcon,
  PaperclipIcon,
  PlusIcon,
  XIcon,
  RobotIcon,
  BrainIcon,
  MicrophoneSlashIcon,
  MicrophoneIcon,
  SpinnerIcon,
} from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import type { LocalAgent } from "@/lib/useLocalAgent";
import { offeredChoice } from "@/lib/agentPrefs";
import { NO_ROOM_CATALOGS, resolveModelCatalog, type RoomModelCatalogs } from "@/lib/agentCatalogs";
import { PLATFORM_OFFLINE_MESSAGE, platformAddQuestion, routeSend, samePlatformAgent, type PlatformChoice } from "@/lib/platformAgents";
import { useAgentPrefs } from "@/views/composer/useAgentPrefs";
import type { ChannelAgent } from "@/lib/wire.gen";
import { isImeKey, isSubmitEnter } from "@/lib/keys";
import { useDismissOnOutside } from "@/ui/useDismissOnOutside";
import { Button } from "@/ui/Button";
import { ConfirmDialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";
import { AttachmentChips } from "@/views/AttachmentChips";
import { ATTACHMENT_ACCEPT, useComposerAttachments } from "@/views/useComposerAttachments";
import { pastedFiles } from "@/views/composer/pastedFiles";
import { AgentPicker } from "@/views/composer/AgentPicker";
import { ModelPicker } from "@/views/composer/ModelPicker";
import { ContextMeter } from "@/views/composer/ContextMeter";
import { useContextUsage } from "@/views/composer/useContextUsage";
import type { FeedItem } from "@/views/room/types";
import { SendModeToggle } from "@/views/composer/SendModeToggle";
import { StarterPrompts } from "@/views/composer/StarterPrompts";
import { fileSuggestions, type PromptChoice } from "@/lib/promptPacks";
import type { Referenceable, RoomAgent } from "@/views/composer/types";
import { agentHandle } from "@/lib/roomAgents";
import type { SendOptions } from "@/views/room/useRoomMessageActions";
import type { RoomPlatformAgents } from "@/views/room/useRoomPlatformAgents";
import { useDisplayNames } from "@/lib/useDisplayName";
import { BROADCAST_MENTIONS, mentionLabel, mentionMatches } from "@/views/composer/mentions";
import { contentLabel, type ExpertChoice } from "@/lib/contentComponents";
import { useComposerExpert } from "@/views/composer/useComposerExpert";
import { ExpertBanner, ExpertPermissionDialog, ExpertPicker } from "@/views/composer/ExpertPicker";
import { TeamPlanCard, useTeamPlan } from "@/views/composer/TeamPlanCard";
import { useComposerPrompts } from "@/views/composer/useComposerPrompts";
import { PromptPickerButton, PromptVariablesDialog, SlashMenu } from "@/views/composer/PromptPicker";
import { explicitRecipient, needsAgent, NO_AGENT_MESSAGE, resolveRecipient } from "@/views/composer/recipient";
import { useEnhancePrompt } from "@/views/composer/useEnhancePrompt";
import { useVoiceInput } from "@/views/composer/useVoiceInput";
import { useMediaCapability } from "@/lib/media";
import { AssistButton } from "@/views/AssistButton";

// Composer is the room message input with a bottom toolbar: the "To: Agent |
// People" segment, a recipient picker (my agents, or another member's agent to
// borrow), the model picker, an @-mention autocomplete, and a "#" reference
// picker. The toolbar is one row at every width: on a narrow composer the
// segment and the model picker are icon-only and attach + reference share one
// "+" menu. The "people" variant is the people chat's composer: no agent, no
// pickers — every message is people-only by construction.

import { mentionAt, toolButton, type ComposerFileContext } from "@/views/composer/composerShared";
export function Composer({
  roomId,
  channelId,
  agent: localAgent,
  onSend,
  onTyping,
  disabled,
  members = [],
  referenceable = [],
  roomAgents = [],
  subject,
  platformAgents,
  modelCatalogs = NO_ROOM_CATALOGS,
  showStarters = false,
  variant = "room",
  fileContext,
  feed,
  pick,
  channelDefault,
}: {
  roomId: string;
  channelId?: string | null;
  agent: LocalAgent;
  // Resolves true when the message is owned by the feed (the draft clears),
  // false when it was rejected outright (the draft is kept for editing).
  onSend: (text: string, opts: SendOptions) => Promise<boolean>;
  onTyping?: () => void;
  disabled?: boolean;
  members?: string[];
  referenceable?: Referenceable[];
  roomAgents?: RoomAgent[];
  subject?: string | null;
  // The picker's "Platform agents" group, its confirmed add, and host
  // routing of a send to a platform agent.
  platformAgents?: Pick<RoomPlatformAgents, "choices" | "add" | "route" | "commit">;
  // The room's owner-keyed model catalogs and borrow grants (model picker).
  modelCatalogs?: RoomModelCatalogs;
  // An empty room with an agent available: example prompts above the input.
  showStarters?: boolean;
  variant?: "room" | "people" | "file";
  // The file the conversation is about (a file panel's, or the room's
  // focused file): the starters become its suggestions.
  fileContext?: ComposerFileContext;
  // A prompt to insert (a suggestion handed over from the room); a new
  // nonce inserts it again.
  pick?: { readonly choice: PromptChoice; readonly nonce: number };
  // The channel's default agent (C8: proposed when the member made no
  // choice of their own here).
  channelDefault?: ChannelAgent | null;
  // The channel's rows: the context meter reads the addressed agent's newest
  // reply that reports how full its conversation is.
  feed?: readonly FeedItem[];
}) {
  const people = variant === "people";
  // Drafts, agents, references and attachments handed to "the composer"
  // (a used prompt, Make similar, Ask the agent) land in the room's main
  // composer only: never the people chat's or a file panel's.
  const main = variant === "room";
  const toast = useToast();
  const [text, setText] = useState("");
  const taRef = useRef<HTMLTextAreaElement>(null);
  // A prompt "Used" from the library, and references asked from the people
  // chat, land in the room's main composer only (never the people chat's).
  useInjectedDraft(roomId, main ? channelId : null, setText, taRef);
  const fileInputRef = useRef<HTMLInputElement>(null);
  // A find-a-way card's "Attach a file" opens the picker here.
  const openFilePicker = useCallback(() => fileInputRef.current?.click(), []);
  useInjectedFilePick(roomId, main ? channelId : null, openFilePicker);
  // The agent selection, restored from and saved to the room's prefs: the
  // picked recipient (another member's agent to borrow, a platform agent;
  // null = my own selected target), people-only (drives no agent), and the
  // model + reasoning tuning ("" = the provider default; changed only by the
  // member's own pick, see offeredChoice).
  const { agent, recipient, setRecipient, peopleOnly: peopleOnlyPref, setPeopleOnly, model, setModel, reasoning, setReasoning } =
    useAgentPrefs({ roomId, channelId, enabled: !people, agent: localAgent, roomAgents, subject: subject ?? null, offered: platformAgents?.choices, channelDefault });
  // A file panel always asks an agent (its sends name the file to change).
  const peopleOnly = variant === "file" ? false : peopleOnlyPref;
  // The welcome card's new room opens with its platform agent selected.
  const pickInjected = useCallback(
    (a: RoomAgent) => {
      setRecipient(a);
      setPeopleOnly(false);
    },
    [setRecipient, setPeopleOnly],
  );
  useInjectedAgent(roomId, main ? channelId : null, roomAgents, pickInjected);
  // Attached references (chips shown above the input), quoted into the message
  // on send so the agent receives the referenced content.
  const [refs, setRefs] = useState<Referenceable[]>([]);
  useInjectedReferences(roomId, channelId, main, setRefs, taRef);
  const [sending, setSending] = useState(false);
  // A send that would add a platform agent to the room asks first; its answer.
  const [addAsk, setAddAsk] = useState<{ choice: PlatformChoice; done: (ok: boolean) => void } | null>(null);
  const askAdd = (choice: PlatformChoice) => new Promise<boolean>((done) => setAddAsk({ choice, done }));
  // Bumped to open the agent picker (a send that has no agent to drive).
  const [pickAgent, setPickAgent] = useState(0);
  const [showRefPicker, setShowRefPicker] = useState(false);
  // The narrow toolbar's "+" menu (attach / reference).
  const [showMore, setShowMore] = useState(false);
  const moreRef = useRef<HTMLDivElement>(null);
  useDismissOnOutside(showMore, moreRef, () => setShowMore(false));
  const attachments = useComposerAttachments(roomId);
  // A "Make similar" draft's references arrive stored in the room.
  useInjectedAttachments(roomId, main ? channelId : null, attachments.addStored);
  // "Enhance prompt": the AI assist rewrites the text in place (Undo puts
  // it back); hidden without a model.
  const storedAttachmentIds = useMemo(() => attachments.files.flatMap((f) => (f.stored ? [f.stored.id] : [])), [attachments.files]);
  // @-mention autocomplete: when the caret is inside an "@partial" token, show a
  // filtered member list; selecting one inserts "@name ".
  const [mention, setMention] = useState<{ query: string; start: number } | null>(
    null,
  );
  // The list shows display names; picking inserts the canonical handle.
  const nameOf = useDisplayNames(members.map((m) => (m.includes("/") ? "" : m)).concat(roomAgents.map((a) => a.ownerId)));
  const labelOf = (m: string) => mentionLabel(m, roomAgents, nameOf);
  // A channel's composer offers @here and @channel after its people.
  const matches = mention ? mentionMatches(people ? members : [...members, ...BROADCAST_MENTIONS], mention.query, labelOf) : [];
  // Experts (the room's pinned ones and the member's own) answer @name too:
  // picking one sets the expert, which runs on the selected agent.
  const exp = useComposerExpert(roomId, !people);
  const enhance = useEnhancePrompt({
    roomId,
    channelId: people ? null : channelId,
    expertId: exp.expert?.component.id ?? null,
    text,
    setText,
    attachmentIds: storedAttachmentIds,
    taRef,
  });
  const enhancing = enhance.state !== "idle";
  // Voice input: press to record, press again to transcribe into the caret.
  // Hidden when the deployment has no transcription provider (or AI is off).
  const canVoice = useMediaCapability("transcribe");
  function applyTextForVoice(next: string) {
    setText(next);
    requestAnimationFrame(() => taRef.current?.focus());
  }
  const voice = useVoiceInput({ setText: applyTextForVoice, taRef });
  // Prompts of the member's packs and the room's: the picker, /commands
  // and expert quick prompts that point at a pack's prompt.
  const prompts = useComposerPrompts(roomId, !people, setText, taRef);
  const starters = useMemo(
    () => (fileContext ? fileSuggestions(prompts.choices, fileContext.file, fileContext.selectionKind) : prompts.starters),
    [fileContext, prompts.choices, prompts.starters],
  );
  useEffect(() => {
    if (pick) prompts.pick(pick.choice);
    // Only a new nonce inserts it again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pick?.nonce]);
  const slashListId = useId();
  const expertMatches = mention ? exp.mentionMatches(mention.query) : [];
  const optionCount = expertMatches.length + matches.length;
  // Keyboard-highlighted mention option (Arrow keys move it, Enter/Tab pick).
  const [activeMention, setActiveMention] = useState(0);
  const mentionListId = useId();
  const highlighted = optionCount > 0 ? Math.min(activeMention, optionCount - 1) : -1;

  // The catalog the model picker reflects, resolved with the same precedence
  // send() uses to choose the driving agent (an in-text @mention, else the
  // picked recipient, else my selected local target) and keyed by OWNER:
  // another member's or a platform agent's catalog comes only from the
  // room's catalogs (narrowed to a borrow grant's authorized models), never
  // from my own agents that happen to share its (core, provider).
  const effTarget = useMemo(
    () =>
      resolveModelCatalog({
        pick: explicitRecipient(text, roomAgents, recipient),
        subject: subject ?? null,
        localTargets: agent.targets,
        selectedTargetId: agent.target,
        room: modelCatalogs,
      }),
    [text, roomAgents, recipient, subject, agent.targets, agent.target, modelCatalogs],
  );
  const modelChoices = useMemo(() => effTarget?.models ?? [], [effTarget]);
  const contextUsage = useContextUsage({ feed, text, roomAgents, recipient, subject: subject ?? null, agent });
  // The stored model as used now (offeredChoice): as stored while the
  // catalog is unknown or empty, set aside only while a loaded catalog lacks
  // it. The stored pick itself changes only by the member's own pick, so a
  // catalog loading (re-entering a room, a core restarting) never erases it.
  // Nothing picked here: my own agent runs on the model I chose as its
  // default (Agents page), when there is one. Only my local targets carry it.
  const ownDefault = effTarget && agent.targets.includes(effTarget as (typeof agent.targets)[number]) ? agent.userDefaultModel(effTarget.core ?? "", effTarget.provider) : "";
  const offeredModel = offeredChoice(model, effTarget?.models) || ownDefault;
  // The model whose reasoning options apply: the explicit pick, else the
  // target's configured default, else the first catalog entry. Without this
  // last fallback a harness that has per-model reasoning but no default model
  // (e.g. claude-code/opencode) would show no reasoning picker until a model
  // was picked.
  const effModelForReasoning =
    offeredModel || effTarget?.defaultModel || modelChoices[0] || "";
  // Reasoning options follow that model (reasoningByModel), falling back to the
  // target-level ladder. Empty => no reasoning picker.
  const reasoningChoices = useMemo(() => {
    const byModel = effTarget?.reasoningByModel?.[effModelForReasoning];
    if (byModel?.length) return byModel;
    return effTarget?.reasoningEfforts ?? [];
  }, [effTarget, effModelForReasoning]);

  // The stored reasoning as used now, by the same rule.
  const offeredReasoning = offeredChoice(reasoning, reasoningChoices);

  // detectMention inspects the text before the caret for a trailing "@token"
  // (start-of-input or after whitespace) and opens/updates the picker. The token
  // allows the characters in an agent handle — "owner/provider", where a
  // provider may contain ':' (e.g. acp:hermes), '-' (claude-code), or '.' — so
  // autocomplete keeps matching after the user types the '/' separator.
  const detectMention = (value: string, caret: number) => {
    const found = mentionAt(value, caret);
    setMention(found);
    if (found) setActiveMention(0);
  };

  // Throttle typing pings to at most one every 2s while actively typing.
  const lastTypingRef = useRef(0);
  const applyText = (value: string, caret: number) => {
    setText(value);
    detectMention(value, caret);
    prompts.detect(value, caret);
    if (onTyping && value.trim()) {
      const now = Date.now();
      if (now - lastTypingRef.current > 2000) {
        lastTypingRef.current = now;
        onTyping();
      }
    }
  };

  const pickMention = (name: string) => {
    if (!mention) return;
    const before = text.slice(0, mention.start);
    const after = text.slice(mention.start + 1 + mention.query.length);
    const next = `${before}@${name} ${after}`;
    setText(next);
    setMention(null);
    requestAnimationFrame(() => taRef.current?.focus());
  };

  // The agent a picked expert runs on: the same precedence send() uses (an
  // in-text @agent, the picked recipient, my selected local target).
  const runsOnAgent = explicitRecipient(text, roomAgents, recipient);
  const myRunsOn = agent.targets.find((x) => x.id === agent.target);
  const runsOn = runsOnAgent
    ? mentionLabel(agentHandle(runsOnAgent), roomAgents, nameOf)
    : myRunsOn
      ? `your ${myRunsOn.provider}`
      : null;
  // A picked team is staffed on the agent that leads it and confirmed with
  // its cost estimate before the first send. The lead is resolved exactly
  // as send() will: a platform agent on the host routing picks now, so the
  // plan, its estimate and the confirmation follow that host.
  const leadAgent = runsOnAgent ? routeSend(runsOnAgent, platformAgents?.route, subject ?? null).recipient : null;
  const teamLead = leadAgent
    ? { ownerId: leadAgent.ownerId, core: leadAgent.core, provider: leadAgent.provider }
    : myRunsOn && subject
      ? { ownerId: subject, core: myRunsOn.core || undefined, provider: myRunsOn.provider }
      : null;
  const team = useTeamPlan(roomId, people || peopleOnly ? null : exp.expert, teamLead);

  // pickExpertMention removes the typed "@partial" and picks the expert.
  const pickExpertMention = (e: ExpertChoice) => {
    if (!mention) return;
    const before = text.slice(0, mention.start);
    const after = text.slice(mention.start + 1 + mention.query.length).replace(/^\s+/, "");
    setText(before + after);
    setMention(null);
    exp.pick(e);
    setPeopleOnly(false);
    requestAnimationFrame(() => taRef.current?.focus());
  };
  const pickOption = (index: number) => {
    if (index < expertMatches.length) pickExpertMention(expertMatches[index]);
    else pickMention(matches[index - expertMatches.length]);
  };

  const addRef = (r: Referenceable) => {
    setRefs((cur) => (cur.some((x) => x.id === r.id) ? cur : [...cur, r]));
    setShowRefPicker(false);
    requestAnimationFrame(() => taRef.current?.focus());
  };
  const removeRef = (id: number) => setRefs((cur) => cur.filter((x) => x.id !== id));

  const send = async () => {
    const t = text.trim();
    if (sending || enhancing || (!t && refs.length === 0 && attachments.files.length === 0)) return;
    setSending(true);
    // Prepend each reference as an attributed Markdown quote so the agent
    // receives the referenced content inline, then the user's own text.
    const quoted = refs
      .map((r) => {
        const who = r.author === "agent" ? "Agent" : nameOf(r.author);
        const body = r.text
          .split("\n")
          .map((l) => `> ${l}`)
          .join("\n");
        return `Referencing ${who}:\n${body}`;
      })
      .join("\n\n");
    const composed = quoted ? (t ? `${quoted}\n\n${t}` : quoted) : t;
    const picked = resolveRecipient({
      text: t,
      roomAgents,
      recipient,
      localTarget: agent.targets.find((x) => x.id === agent.target),
      subject: subject ?? null,
    });
    const toPeople = people || peopleOnly;
    // An expert runs on an agent: it is never sent to people only, and is
    // confirmed on first use when someone else's declares permissions.
    const expertSend = !toPeople ? exp.sendField() : undefined;
    if (exp.expert && !toPeople && !picked) {
      toast.error(`Pick an agent for ${contentLabel(exp.expert.component)} to run on`);
      setSending(false);
      return;
    }
    if (needsAgent(toPeople, picked)) {
      // Agent mode with nobody to drive: not sent as people-only without a word.
      // The picker opens (its platform agents included); the draft stays.
      toast.error(NO_AGENT_MESSAGE);
      setPickAgent((n) => n + 1);
      setSending(false);
      return;
    }
    if (expertSend && !(await exp.confirm())) {
      setSending(false);
      return;
    }
    if (expertSend && !team.confirmed) {
      toast.error("Check the team and its cost estimate, then choose Use this team");
      setSending(false);
      return;
    }
    // A platform agent goes to the host routing picks now (PRD v2 §4); with
    // every host offline nothing is sent (no dead job) and the draft stays.
    // The team plan resolved its lead through the same routeSend.
    const routed = routeSend(toPeople ? null : picked, platformAgents?.route, subject ?? null);
    if (routed.offline) {
      toast.error(PLATFORM_OFFLINE_MESSAGE);
      setSending(false);
      return;
    }
    // A platform agent the room does not have yet is added by the first send
    // that uses it, once the member agrees (the same question the picker asks).
    const toAdd = picked?.platform
      ? platformAgents?.choices.find((c) => !c.inRoom && samePlatformAgent({ core: c.agent.core ?? "", provider: c.agent.provider }, { core: picked.core ?? "", provider: picked.provider }))
      : undefined;
    if (toAdd && !(await askAdd(toAdd))) {
      setSending(false);
      return;
    }
    const host = routed.host;
    const platformRef = host && picked ? { core: picked.core ?? "", provider: picked.provider } : null;
    const eff: RoomAgent | null = toPeople ? picked : routed.recipient;
    // Attach the model+reasoning selection to the agent recipient when one is
    // present and the user picked either. Omitting both keeps agent defaults.
    const withOpts: RoomAgent | null =
      eff && !peopleOnly && (offeredModel || offeredReasoning)
        ? {
            ...eff,
            agentOptions: {
              ...(offeredModel ? { model: offeredModel } : {}),
              ...(offeredReasoning ? { reasoningEffort: offeredReasoning } : {}),
            },
          }
        : eff;
    try {
      const uploaded = attachments.files.length > 0 ? await attachments.uploadFiles() : [];
      if (uploaded === null) return;
      const accepted = await onSend(composed, {
        peopleOnly: toPeople,
        recipient: toPeople ? null : withOpts,
        attachmentIds: uploaded.map((attachment) => attachment.id),
        attachments: uploaded,
        ...(expertSend ? { expert: expertSend.expert, expertMarker: expertSend.marker } : {}),
      });
      // Clear only once the message is owned by the feed; a rejected send
      // (borrow not authorized, not a member…) keeps the draft for editing.
      if (!accepted) return;
      if (platformRef && host) platformAgents?.commit(platformRef, host.hostUserId);
      setText("");
      setRefs([]);
      attachments.clearFiles(attachments.files);
      setMention(null);
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="relative w-full">
      <SlashMenu
        matches={prompts.slashMatches}
        active={prompts.active}
        listId={slashListId}
        onPick={prompts.pickSlash}
        onHover={prompts.setActive}
      />
      {mention && optionCount > 0 && (
        <div className="absolute bottom-full left-0 z-20 mb-2 w-56 overflow-hidden rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]">
          <div className="border-b border-[var(--color-border)] px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Mention
          </div>
          <div id={mentionListId} role="listbox" aria-label="Mention suggestions">
          {expertMatches.map((e, index) => (
            <div
              key={`expert:${e.component.id}`}
              id={`${mentionListId}-${index}`}
              role="option"
              aria-selected={index === highlighted}
              onMouseDown={(ev) => ev.preventDefault()}
              onMouseEnter={() => setActiveMention(index)}
              onClick={() => pickExpertMention(e)}
              className={cn(
                "flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm",
                index === highlighted
                  ? "bg-[var(--color-surface-2)] text-[var(--color-fg)]"
                  : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
              )}
            >
              <span className="grid size-5 place-items-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
                <BrainIcon size={11} weight="fill" />
              </span>
              <span className="min-w-0 flex-1 truncate">{contentLabel(e.component)}</span>
              <span className="rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-1.5 text-[0.6rem] font-medium text-[var(--color-accent)]">
                Expert
              </span>
            </div>
          ))}
          {matches.map((m, i) => {
            const index = i + expertMatches.length;
            const isAgentHandle = m.includes("/");
            // A borrowable agent: an agent handle owned by someone other than me.
            // Resolve the owner from the roster (the handle may be core-qualified
            // as core/owner/provider), not by slicing the first path segment.
            const owner = roomAgents.find((a) => agentHandle(a) === m)?.ownerId;
            const borrow =
              isAgentHandle &&
              !!subject &&
              !!owner &&
              owner !== subject;
            return (
              <div
                key={m}
                id={`${mentionListId}-${index}`}
                role="option"
                aria-selected={index === highlighted}
                onMouseDown={(e) => e.preventDefault()}
                onMouseEnter={() => setActiveMention(index)}
                onClick={() => pickMention(m)}
                className={cn(
                  "flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm",
                  index === highlighted
                    ? "bg-[var(--color-surface-2)] text-[var(--color-fg)]"
                    : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
                )}
              >
                <span className="grid size-5 place-items-center rounded-full bg-[var(--color-accent-soft)] text-[0.6rem] font-semibold text-[var(--color-accent)]">
                  {isAgentHandle ? (
                    <RobotIcon size={11} weight="fill" />
                  ) : (
                    (labelOf(m)[0] || "?").toUpperCase()
                  )}
                </span>
                <span className="min-w-0 flex-1 truncate">{labelOf(m)}</span>
                {borrow && (
                  <span className="rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-1.5 text-[0.6rem] font-medium text-[var(--color-accent)]">
                    Borrow
                  </span>
                )}
              </div>
            );
          })}
          </div>
        </div>
      )}
      {showRefPicker && referenceable.length > 0 && (
        <div className="absolute bottom-full left-0 z-20 mb-2 max-h-64 w-80 overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]">
          <div className="sticky top-0 border-b border-[var(--color-border)] bg-[var(--color-elevated)] px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Reference a message
          </div>
          {referenceable
            .slice()
            .reverse()
            .map((r) => (
              <button
                key={r.id}
                onClick={() => addRef(r)}
                className="flex w-full flex-col gap-0.5 px-3 py-2 text-left hover:bg-[var(--color-surface-2)]"
              >
                <span className="text-xs font-medium text-[var(--color-accent)]">
                  {r.author === "agent" ? "Agent" : nameOf(r.author)}
                </span>
                <span className="line-clamp-2 text-sm text-[var(--color-fg-muted)]">
                  {r.text}
                </span>
              </button>
            ))}
        </div>
      )}
      {/* Flat: no card. The room footer draws the single top rule. */}
      <div
        onDragOver={(e) => {
          if (!sending && e.dataTransfer.types.includes("Files")) e.preventDefault();
        }}
        onDrop={(e) => {
          e.preventDefault();
          if (sending) return;
          attachments.addFiles(Array.from(e.dataTransfer.files));
          requestAnimationFrame(() => taRef.current?.focus());
        }}
      >
        <input
          ref={fileInputRef}
          type="file"
          multiple
          accept={ATTACHMENT_ACCEPT}
          disabled={disabled || sending}
          className="hidden"
          onChange={(e) => {
            attachments.addFiles(Array.from(e.currentTarget.files ?? []));
            requestAnimationFrame(() => taRef.current?.focus());
            e.currentTarget.value = "";
          }}
        />
        {refs.length > 0 && (
          <div className="flex flex-wrap gap-1.5 px-3 pt-2">
            {refs.map((r) => (
              <span
                key={r.id}
                className="flex max-w-[16rem] items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-surface-2)] py-1 pl-2.5 pr-1.5 text-xs"
              >
                <span className="font-medium text-[var(--color-accent)]">
                  {r.author === "agent" ? "Agent" : nameOf(r.author)}
                </span>
                <span className="truncate text-[var(--color-fg-muted)]">
                  {r.text}
                </span>
                <button
                  onClick={() => removeRef(r.id)}
                  className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
                  title="Remove reference"
                >
                  <XIcon size={12} />
                </button>
              </span>
            ))}
          </div>
        )}
        {showStarters && !people && (
          <StarterPrompts
            starters={starters}
            heading={fileContext ? `For ${fileContext.file.name}` : undefined}
            onPick={(c) => {
              if (fileContext?.onPick) return fileContext.onPick(c);
              setPeopleOnly(false);
              prompts.pick(c);
            }}
          />
        )}
        {!people && exp.expert && (
          <ExpertBanner
            expert={exp.expert}
            runsOn={peopleOnly ? null : runsOn}
            onClear={() => exp.pick(null)}
            onQuickPrompt={(q) => {
              setPeopleOnly(false);
              if (q.resolved) {
                prompts.pickRef(q.resolved);
                return;
              }
              setText(q.prompt);
              requestAnimationFrame(() => taRef.current?.focus());
            }}
          />
        )}
        {!people && !peopleOnly && <TeamPlanCard team={team} hasLead={!!teamLead} onCancel={() => exp.pick(null)} />}
        <AttachmentChips
          attachments={attachments.chipItems}
          onRemove={attachments.removeFile}
        />
        {/* The input row: mic left, the text field, send right. Voice and
            sending are what the text field is FOR, so they sit beside it,
            not in the tool tray below. Both are one compact size (size-8)
            centred on the first text line (mb-1 in the 2.5rem field), and
            the mic's column lines up with the tray's first icon (px-3). */}
        <div className="flex items-end gap-1 px-3">
        {canVoice && (
          <button
            type="button"
            onClick={() => void voice.toggle()}
            disabled={disabled || sending || enhancing || (voice.state !== "idle" && voice.state !== "recording")}
            className={cn(toolButton, "mb-1 size-8")}
            title={voice.state === "recording" ? "Stop and transcribe" : voice.state === "idle" ? "Speak to text" : "Transcribing…"}
            aria-label={voice.state === "recording" ? "Stop recording" : "Speak"}
          >
            {voice.state === "recording" ? (
              <MicrophoneSlashIcon size={18} className="animate-pulse text-[var(--color-danger)]" />
            ) : voice.state === "idle" ? (
              <MicrophoneIcon size={18} />
            ) : (
              <SpinnerIcon size={18} className="animate-spin" />
            )}
          </button>
        )}
        <textarea
          ref={taRef}
          // The people chat's placeholder states who can read it; two lines
          // keep it whole in a narrow panel.
          rows={people ? 2 : 1}
          value={text}
          disabled={disabled}
          readOnly={enhancing}
          placeholder={
            people
              ? "Message people in this room (agents can't see this)"
              : peopleOnly
                ? "Message people (the agent won't reply)"
                : variant === "file"
                  ? "Ask the agent to change this file…"
                  : "Message the room and its agent…"
          }
          onChange={(e) => applyText(e.target.value, e.target.selectionStart ?? e.target.value.length)}
          onPaste={(e) => {
            // Pasted files attach, whatever their type: a file copied in
            // the Finder arrives as a file, and WebKit (the desktop app's
            // webview) would otherwise also insert its name or path as text.
            const files = pastedFiles(e.clipboardData);
            if (!sending && files.length > 0) {
              e.preventDefault();
              attachments.addFiles(files);
              requestAnimationFrame(() => taRef.current?.focus());
            }
          }}
          role="combobox"
          aria-label="Message"
          aria-autocomplete="list"
          aria-expanded={!!mention && optionCount > 0}
          aria-controls={mention && optionCount > 0 ? mentionListId : undefined}
          aria-activedescendant={highlighted >= 0 && mention ? `${mentionListId}-${highlighted}` : undefined}
          onKeyDown={(e) => {
            // Keys during an IME composition (CJK input) are the IME's.
            if (isImeKey(e)) return;
            // Cmd/Ctrl+Z right after an enhance restores the member's text.
            if (enhance.onKeyDown(e)) return;
            // The /command menu, when open, takes the keys first.
            if (prompts.onKeyDown(e)) return;
            // While the mention list is open, arrows move the highlight and
            // Enter/Tab accept the highlighted option.
            if (mention && optionCount > 0) {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                const delta = e.key === "ArrowDown" ? 1 : -1;
                setActiveMention((highlighted + delta + optionCount) % optionCount);
                return;
              }
              if (e.key === "Enter" || e.key === "Tab") {
                e.preventDefault();
                pickOption(Math.max(0, highlighted));
                return;
              }
            }
            if (e.key === "Escape" && mention) {
              setMention(null);
              return;
            }
            if (isSubmitEnter(e)) {
              e.preventDefault();
              void send();
            }
          }}
          className="block max-h-40 min-h-[2.5rem] w-full flex-1 resize-none bg-transparent px-1 pb-1 pt-2.5 text-sm text-[var(--color-fg)] placeholder:text-[var(--color-fg-subtle)] focus:outline-none"
        />
        <Button
          variant="primary"
          size="icon"
          onClick={() => void send()}
          disabled={
            disabled ||
            sending ||
            enhancing ||
            attachments.isUploading ||
            (!text.trim() && refs.length === 0 && attachments.files.length === 0)
          }
          title="Send"
          className="mb-1 size-8 shrink-0"
        >
          <PaperPlaneRightIcon size={15} weight="fill" />
        </Button>
        </div>
        {/* Bottom toolbar, directly under the input row: ONE row at every
            width. Narrow: [+] [Agent|People icons] [agent] [model icon] …
            [send]; wide adds labels and splits attach / reference. */}
        <div className="flex items-center gap-1 px-2 pb-1.5">
          {referenceable.length > 0 ? (
            <>
              <div ref={moreRef} className="relative @md:hidden">
                <button
                  type="button"
                  onClick={() => setShowMore((v) => !v)}
                  disabled={disabled || sending}
                  className={toolButton}
                  title="Attach a file or reference a message"
                  aria-label="Attach a file or reference a message"
                  aria-haspopup="menu"
                  aria-expanded={showMore}
                >
                  <PlusIcon size={18} />
                </button>
                {showMore && (
                  <div role="menu" className="animate-in absolute bottom-full left-0 z-20 mb-2 w-52 overflow-hidden rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] py-1 shadow-[var(--shadow-pop)]">
                    <button
                      type="button"
                      role="menuitem"
                      onClick={() => {
                        setShowMore(false);
                        fileInputRef.current?.click();
                      }}
                      className="flex w-full items-center gap-2 px-3 py-2.5 text-left text-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
                    >
                      <PaperclipIcon size={16} /> Attach files
                    </button>
                    <button
                      type="button"
                      role="menuitem"
                      onClick={() => {
                        setShowMore(false);
                        setShowRefPicker(true);
                      }}
                      className="flex w-full items-center gap-2 px-3 py-2.5 text-left text-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
                    >
                      <HashIcon size={16} /> Reference a message
                    </button>
                  </div>
                )}
              </div>
              <button
                type="button"
                onClick={() => fileInputRef.current?.click()}
                disabled={disabled || sending}
                className={cn(toolButton, "hidden @md:grid")}
                title="Attach files"
                aria-label="Attach files"
              >
                <PaperclipIcon size={18} />
              </button>
              <button
                type="button"
                onClick={() => setShowRefPicker((v) => !v)}
                className={cn(toolButton, "hidden @md:grid")}
                title="Reference a message"
                aria-label="Reference a message"
              >
                <HashIcon size={18} />
              </button>
            </>
          ) : (
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={disabled || sending}
              className={toolButton}
              title="Attach files"
              aria-label="Attach files"
            >
              <PaperclipIcon size={18} />
            </button>
          )}
          {main && (
            <PromptPickerButton choices={prompts.choices} onPick={prompts.pick} disabled={disabled || sending} className={toolButton} />
          )}
          {main && (
            <SendModeToggle value={peopleOnly ? "people" : "agent"} onChange={(m) => setPeopleOnly(m === "people")} />
          )}
          <div className="flex min-w-0 flex-1 items-center gap-1">
            {!people && !peopleOnly && (
              <AgentPicker
                agent={agent}
                roomAgents={roomAgents}
                recipient={recipient}
                subject={subject ?? null}
                onPick={setRecipient}
                openNonce={pickAgent}
                platform={platformAgents?.choices}
                onAddPlatform={platformAgents?.add}
              />
            )}
            {!people && !peopleOnly && (exp.choices.length > 0 || exp.expert) && (
              <ExpertPicker choices={exp.choices} expert={exp.expert} onPick={exp.pick} />
            )}
            {!people && !peopleOnly && (modelChoices.length > 0 || reasoningChoices.length > 0) && (
              <ModelPicker
                models={modelChoices}
                modelNames={effTarget?.modelNames}
                reasonings={reasoningChoices}
                model={offeredModel}
                reasoning={offeredReasoning}
                defaultModel={effTarget?.defaultModel ?? ""}
                defaultReasoning={effTarget?.defaultReasoning ?? ""}
                onPickModel={setModel}
                onPickReasoning={setReasoning}
              />
            )}
            {!people && !peopleOnly && <ContextMeter usage={contextUsage} />}
            {!people && peopleOnly && (
              <span className="hidden truncate px-1 text-xs text-[var(--color-fg-subtle)] @md:inline">
                The agent reads this but won't reply
              </span>
            )}
          </div>
          {!people && !peopleOnly && enhance.canUndo && (
            <button type="button" onClick={enhance.undo} className="shrink-0 rounded-full px-2.5 py-1 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]" title="Put back what you wrote">
              Undo
            </button>
          )}
          {!people && !peopleOnly && enhance.task && (
            <AssistButton
              label="Enhance prompt"
              state={enhance.state}
              onRun={() => void enhance.run()}
              onStop={enhance.stop}
              disabled={disabled || sending || (!enhancing && !text.trim())}
            />
          )}
        </div>
      </div>
      <ExpertPermissionDialog expert={exp.confirming} onAnswer={exp.resolveConfirm} />
      <PromptVariablesDialog choice={prompts.filling} onCancel={prompts.cancelFill} onInsert={prompts.submitFill} />
      {/* Portalled like the picker's own: the chat column is a CSS container. */}
      {createPortal(
        <ConfirmDialog
          open={addAsk !== null}
          // Cancel (and the close after a confirm, which has answered already).
          onClose={() => {
            addAsk?.done(false);
            setAddAsk(null);
          }}
          onConfirm={() => {
            const ask = addAsk;
            if (!ask || !platformAgents) return;
            return platformAgents.add(ask.choice).then(ask.done);
          }}
          title="Add a platform agent?"
          description={addAsk ? platformAddQuestion(addAsk.choice, nameOf) : undefined}
          confirmLabel="Add to room and send"
        />,
        document.body,
      )}
    </div>
  );
}
