import { useCallback, useEffect, useRef, useState } from "react";
import { ScrollIcon, TimerIcon } from "@phosphor-icons/react";
import type { Channel } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { composerBlock, postRefusalOf, waitText, type PostRefusal } from "@/lib/channelRules";
import { ApiError, errorText } from "@/lib/errors";
import type { RoomMembership } from "@/lib/roomRoles";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { ChannelArchived, ChannelAdminsOnly, ChannelSlowMode, RulesAgreementRequired, RulesChanged, type RoomRulesState } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";

export interface ChannelGate {
  /** refused reads a failed post: null when it was not the channel's gate;
   *  else the composer now says why, and the promise answers whether to
   *  send the message again: true once the member agreed to the rules,
   *  false otherwise (dismissed, slow mode, archived, admins only: the
   *  draft stays in the box). */
  readonly refused: (e: unknown) => Promise<boolean> | null;
  /** Why the composer cannot post here now (archived, admins only), before
   *  a send; null when nothing blocks. */
  readonly blocked: string | null;
  /** The strip the room's footer shows above the composer. */
  readonly strip: React.ReactNode;
}

// useChannelGate is what the composer says about the channel's gate
// (docs/prd/2026-10-28-channels-and-threads.md §2.5a, C16, C22): when the
// server refused a post because the room asks members to agree to its
// rules, the rules and an "I agree" button (POST rules/agree, then the held
// message is sent); a slow mode's wait, counted down; an archived or
// admins-only channel's reason, with the composer disabled. The server's
// gate (realtime admitPost) decides every post; this only says why, on the
// composer that sent it.
export function useChannelGate({ roomId, channel, me }: { readonly roomId: string; readonly channel: Channel | null; readonly me: RoomMembership | null | undefined }): ChannelGate {
  const [refusal, setRefusal] = useState<PostRefusal | null>(null);
  // The send waiting for the member's agreement: answered true (send it
  // again) or false (keep the draft).
  const held = useRef<((again: boolean) => void) | null>(null);
  const answer = useCallback((again: boolean) => {
    held.current?.(again);
    held.current = null;
  }, []);
  const [rules, setRules] = useState<RoomRulesState | null>(null);
  const [agreeing, setAgreeing] = useState(false);
  const [error, setError] = useState("");
  const [until, setUntil] = useState(0);
  const [, tick] = useState(0);
  const channelId = channel?.channelId ?? "";
  // A new channel starts clean: the refusal was about another.
  useEffect(() => {
    setRefusal(null);
    answer(false);
    setUntil(0);
    setError("");
  }, [channelId, answer]);
  useEffect(() => () => answer(false), [answer]);
  const waiting = until > Date.now();
  useVisiblePoll(() => tick((n) => n + 1), 1000, waiting);
  useEffect(() => {
    if (!waiting && refusal?.code === ChannelSlowMode && until > 0) setRefusal(null);
  }, [waiting, refusal, until]);

  const loadRules = useCallback(async () => {
    try {
      setRules(await channelsApi.rules(roomId));
    } catch (e) {
      setError(errorText(e, "Could not load the rules"));
    }
  }, [roomId]);

  const refused = useCallback(
    (e: unknown): Promise<boolean> | null => {
      const r = e instanceof ApiError ? postRefusalOf({ code: e.code, error: e.message, retryAfterMs: e.retryAfterMs }) : null;
      if (!r) return null;
      answer(false);
      setRefusal(r);
      setError("");
      if (r.code === ChannelSlowMode) setUntil(Date.now() + r.retryAfterMs);
      if (r.code !== RulesAgreementRequired && r.code !== RulesChanged) return Promise.resolve(false);
      void loadRules();
      return new Promise<boolean>((resolve) => {
        held.current = resolve;
      });
    },
    [loadRules, answer],
  );

  const dismiss = () => {
    setRefusal(null);
    answer(false);
  };
  const agree = async () => {
    if (!rules) return;
    setAgreeing(true);
    setError("");
    try {
      await channelsApi.agree(roomId, rules.version);
      setRefusal(null);
      answer(true);
    } catch (e) {
      // The rules changed since they were shown: show the new ones.
      if (e instanceof ApiError && e.code === RulesChanged) await loadRules();
      setError(errorText(e, "Could not record your agreement"));
    } finally {
      setAgreeing(false);
    }
  };

  const blocked = composerBlock(channel, me ?? null);
  let strip: React.ReactNode = null;
  if (refusal && (refusal.code === RulesAgreementRequired || refusal.code === RulesChanged)) {
    strip = (
      <div data-rules-agreement role="alert" className="flex flex-col gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-3 text-sm">
        <p className="flex items-center gap-2 font-medium">
          <ScrollIcon size={16} /> This room asks you to agree to its rules before you post
        </p>
        {rules ? <p className="max-h-48 overflow-y-auto whitespace-pre-wrap text-[var(--color-fg-muted)]">{rules.rules}</p> : <p className="text-[var(--color-fg-subtle)]">Loading the rules…</p>}
        {error && <p className="text-xs text-[var(--color-danger)]">{error}</p>}
        <div className="flex items-center gap-2">
          <Button size="sm" variant="primary" onClick={() => void agree()} disabled={!rules || agreeing}>
            {agreeing ? "Sending…" : "I agree, send my message"}
          </Button>
          <Button size="sm" variant="ghost" onClick={dismiss}>
            Not now
          </Button>
        </div>
      </div>
    );
  } else if (refusal?.code === ChannelSlowMode && waiting) {
    strip = (
      <p data-slow-mode role="status" className="flex items-center gap-2 border-b border-[var(--color-border)] px-4 py-2 text-xs text-[var(--color-fg-muted)]">
        <TimerIcon size={14} /> Slow mode: you can post here again in {waitText(until - Date.now())}. Your message is still in the box.
      </p>
    );
  } else if (refusal && (refusal.code === ChannelArchived || refusal.code === ChannelAdminsOnly) && !blocked) {
    strip = (
      <p role="alert" className="border-b border-[var(--color-border)] px-4 py-2 text-xs text-[var(--color-danger)]">
        {refusal.message}
      </p>
    );
  }
  return { refused, blocked, strip };
}
