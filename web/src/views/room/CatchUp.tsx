import { useCallback, useContext, useState } from "react";
import { SparkleIcon, TranslateIcon, XIcon } from "@phosphor-icons/react";
import { runTextAssist, useAssistTask } from "@/lib/assist";
import { CATCH_UP, catchUpAsk, catchUpLines, catchUpOf, TRANSLATE, translateAsk, type CatchUp } from "@/lib/catchUp";
import { errorText } from "@/lib/errors";
import { useChannelAssist } from "@/views/room/useChannelAssist";
import { MessageExtrasContext } from "@/views/room/messageExtras";
import type { FeedItem } from "@/views/room/types";

// CatchUpPill offers "Catch me up" while the open channel has messages the
// member has not read (channels PRD §4): the AI reads those messages and
// answers what happened, what was decided, what is open and what is for
// them. Only the member sees it; it spends their assist budget. Hidden when
// no model or no catch-up task is there for them.
export function CatchUpPill({
  roomId,
  channelId,
  channelName,
  me,
  feed,
  unreadFromSeq,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly channelName: string;
  readonly me: string | null;
  readonly feed: readonly FeedItem[];
  readonly unreadFromSeq: number | null;
}) {
  const assist = useChannelAssist(CATCH_UP, "catch you up");
  const [answer, setAnswer] = useState<CatchUp | null>(null);
  const lines = catchUpLines(feed, unreadFromSeq);
  if (!assist.available || (lines.length === 0 && !answer)) return null;
  const run = async () => {
    const fields = await assist.run({ text: catchUpAsk(me ?? "", channelName, lines), roomId, channelId: channelId ?? undefined });
    if (fields) setAnswer(catchUpOf(fields));
  };
  const list = (title: string, items: readonly string[]) =>
    items.length > 0 ? (
      <div>
        <div className="text-[0.7rem] font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">{title}</div>
        <ul className="ml-4 list-disc">
          {items.map((d) => (
            <li key={d}>{d}</li>
          ))}
        </ul>
      </div>
    ) : null;
  return (
    <div className="pointer-events-none absolute left-1/2 top-2 z-10 flex w-80 max-w-[calc(100%-2rem)] -translate-x-1/2 flex-col items-center">
      {!answer && (
        <button
          type="button"
          data-catch-up
          disabled={assist.state !== "idle"}
          onClick={() => void run()}
          className="tactile pointer-events-auto flex items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)]/90 px-2.5 py-1 text-xs text-[var(--color-fg-muted)] shadow-[var(--shadow-pop)] backdrop-blur hover:text-[var(--color-fg)]"
        >
          <SparkleIcon size={13} className="text-[var(--color-accent)]" />
          {assist.state === "idle" ? `Catch me up (${lines.length})` : "Reading…"}
        </button>
      )}
      {assist.error && <div className="pointer-events-auto mt-1 rounded bg-[var(--color-surface)] px-2 py-1 text-xs text-[var(--color-danger)]">{assist.error}</div>}
      {answer && (
        <div data-catch-up-answer className="pointer-events-auto flex w-full flex-col gap-2 rounded-[var(--radius-lg)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] p-3 text-sm shadow-[var(--shadow-diffuse)]">
          <div className="flex items-start gap-2">
            <p className="flex-1">{answer.summary || "Nothing much happened."}</p>
            <button type="button" aria-label="Close" onClick={() => setAnswer(null)} className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]">
              <XIcon size={14} />
            </button>
          </div>
          {list("For you", answer.forYou)}
          {list("Decided", answer.decided)}
          {list("Still open", answer.open)}
        </div>
      )}
    </div>
  );
}

export interface Translations {
  readonly bySeq: ReadonlyMap<number, string>;
  readonly busy: ReadonlySet<number>;
  readonly available: boolean;
  readonly translate: (seq: number, text: string) => void;
  readonly clear: (seq: number) => void;
}

// useTranslations keeps the member's translations of messages (§4): asked
// one message at a time, shown under the original, never sent to anyone.
// The language is the browser's (decision: owner accepted 2026-11-02).
export function useTranslations(roomId: string, channelId: string | null): Translations {
  const task = useAssistTask(TRANSLATE);
  const [bySeq, setBySeq] = useState<ReadonlyMap<number, string>>(new Map());
  const [busy, setBusy] = useState<ReadonlySet<number>>(new Set());
  const translate = useCallback(
    (seq: number, text: string) => {
      if (!task) return;
      setBusy((b) => new Set(b).add(seq));
      runTextAssist(task.name, { text: translateAsk(navigator.language, text), roomId, channelId: channelId ?? undefined }, { onDelta: () => {} })
        .then((r) => setBySeq((m) => new Map(m).set(seq, r.text.trim())))
        .catch((e) => setBySeq((m) => new Map(m).set(seq, errorText(e, "Could not translate the message"))))
        .finally(() => {
          setBusy((b) => {
            const n = new Set(b);
            n.delete(seq);
            return n;
          });
        });
    },
    [task, roomId, channelId],
  );
  const clear = useCallback((seq: number) => {
    setBySeq((m) => {
      const n = new Map(m);
      n.delete(seq);
      return n;
    });
  }, []);
  return { bySeq, busy, available: !!task, translate, clear };
}

// TranslateButton asks for a stored message in the member's language.
export function TranslateButton({ item }: { readonly item: FeedItem }) {
  const extras = useContext(MessageExtrasContext);
  const t = extras?.translations;
  const seq = item.seq;
  if (!t?.available || seq == null || !item.text.trim()) return null;
  const shown = t.bySeq.has(seq);
  return (
    <button
      type="button"
      data-translate
      disabled={t.busy.has(seq)}
      onClick={() => (shown ? t.clear(seq) : t.translate(seq, item.text))}
      title={shown ? "Hide translation" : "Translate"}
      aria-label={shown ? "Hide translation" : "Translate"}
      className="tactile flex items-center rounded-sm text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
    >
      <TranslateIcon size={12} />
    </button>
  );
}

// Translation is a message's translation under it, the original kept.
export function Translation({ item, className }: { readonly item: FeedItem; readonly className?: string }) {
  const extras = useContext(MessageExtrasContext);
  const seq = item.seq;
  const text = seq == null ? undefined : extras?.translations?.bySeq.get(seq);
  const busy = seq != null && extras?.translations?.busy.has(seq);
  if (!text && !busy) return null;
  return (
    <div data-translation className={`mt-1 border-l-2 border-[var(--color-border-strong)] pl-2 text-sm text-[var(--color-fg-muted)] whitespace-pre-wrap ${className ?? ""}`}>
      {busy ? "Translating…" : text}
    </div>
  );
}
