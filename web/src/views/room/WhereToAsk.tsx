import { useState } from "react";
import { HashIcon } from "@phosphor-icons/react";
import { FIND_CHANNEL, findAnswerOf, findAsk, type ChannelSuggestion } from "@/lib/channelAssists";

const AI_WHERE_UNAVAILABLE = "Asking the AI where to ask is unavailable here; search the channels by name and purpose.";
import type { RoomChannel } from "@/lib/wire.gen";
import { Input } from "@/ui/Input";
import { AssistButton } from "@/views/AssistButton";
import { useChannelAssist } from "@/views/room/useChannelAssist";

// WhereToAsk answers "Where do I ask about X?" from the room's channels'
// purposes and rules (the find-channel assist, docs/prd/2026-10-28-
// channels-and-threads.md §4): a sentence and up to three of the listed
// channels, each opened in one click. Without a model it says so, and the
// directory's search is the way.
export function WhereToAsk({
  roomId,
  channels,
  onOpen,
}: {
  readonly roomId: string;
  readonly channels: readonly Pick<RoomChannel, "channelId" | "name" | "purpose" | "rules">[];
  readonly onOpen: (channelId: string) => void;
}) {
  const assist = useChannelAssist(FIND_CHANNEL, "find a channel");
  const [q, setQ] = useState("");
  const [answer, setAnswer] = useState<{ readonly answer: string; readonly channels: readonly ChannelSuggestion[] } | null>(null);
  if (!assist.available) return <p className="text-xs text-[var(--color-fg-subtle)]">{AI_WHERE_UNAVAILABLE}</p>;
  const ask = async () => {
    if (!q.trim() || channels.length === 0) return;
    const fields = await assist.run({ text: findAsk(q, channels), roomId });
    if (fields) setAnswer(findAnswerOf(fields, channels));
  };
  return (
    <div data-where-to-ask className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
      <div className="flex items-center gap-2">
        <Input value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void ask()} placeholder="Where do I ask about…" aria-label="Where do I ask about" className="h-9" />
        <AssistButton variant="text" label="Ask" state={assist.state} onRun={() => void ask()} disabled={!q.trim() || channels.length === 0} />
      </div>
      {assist.error && <p role="alert" className="text-xs text-[var(--color-danger)]">{assist.error}</p>}
      {answer && (
        <div role="status" className="flex flex-col gap-1 text-sm">
          {answer.answer && <p className="text-[var(--color-fg-muted)]">{answer.answer}</p>}
          {answer.channels.length === 0 && <p className="text-xs text-[var(--color-fg-subtle)]">No channel fits; ask in the default channel, or make one.</p>}
          {answer.channels.map((s) => {
            const c = channels.find((x) => x.channelId === s.channelId);
            return (
              <button key={s.channelId} type="button" onClick={() => onOpen(s.channelId)} className="tactile flex items-start gap-1.5 text-left hover:underline">
                <HashIcon size={14} className="mt-0.5 shrink-0" />
                <span>
                  <span className="font-medium">{c?.name}</span>
                  {s.why && <span className="text-[var(--color-fg-muted)]"> · {s.why}</span>}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
