import { useEffect, useState } from "react";
import { contentLabel, isTeam, type ContentUse } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { Field } from "@/ui/Input";
import { LoadFailed } from "@/ui/QueryState";
import { Select } from "@/ui/Select";

// A channel's expert (channels PRD C9, decision E6): one of the room's
// pinned experts, worn by every agent turn in the channel and its threads
// instead of the room's. What is chosen is saved with the channel's settings
// (saveChannelExpert); the note says before the save that, on a core that
// writes instructions into its workspace, the channel runs in a workspace of
// its own.
export function useChannelExpert(roomId: string, channelId: string) {
  const [experts, setExperts] = useState<readonly ContentUse[]>([]);
  const [saved, setSaved] = useState("");
  const [chosen, setChosen] = useState("");
  const [loaded, setLoaded] = useState(false);
  // A failed load is shown with a retry, never as a missing field.
  const [failed, setFailed] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let live = true;
    setFailed("");
    contentApi.uses(roomId).then(
      (r) => {
        if (!live) return;
        setExperts((r.room ?? []).filter((u) => u.kind === "expert" && !(u.component && isTeam(u.component))));
        const id = r.channelExperts?.[channelId] ?? "";
        setSaved(id);
        setChosen(id);
        setLoaded(true);
      },
      (e) => {
        if (!live) return;
        setLoaded(false);
        setFailed(errorText(e, "Could not load the room's experts"));
      },
    );
    return () => {
      live = false;
    };
  }, [roomId, channelId, attempt]);
  const retry = () => setAttempt((n) => n + 1);
  return { experts, saved, chosen, setChosen, loaded, failed, retry };
}

export type ChannelExpertState = ReturnType<typeof useChannelExpert>;

// saveChannelExpert applies a changed choice: the new expert, or removing
// the one saved. Nothing is sent when the choice did not change.
export async function saveChannelExpert(roomId: string, channelId: string, st: ChannelExpertState): Promise<void> {
  if (!st.loaded || st.chosen === st.saved) return;
  if (st.chosen) await contentApi.putUse("channel-expert", st.chosen, { roomId, channelId });
  else await contentApi.removeUse("channel-expert", st.saved, roomId, channelId);
}

export function ChannelExpertField({ st }: { readonly st: ChannelExpertState }) {
  if (st.failed) {
    return (
      <div className="flex flex-col gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Channel expert</span>
        <LoadFailed message={st.failed} onRetry={st.retry} />
      </div>
    );
  }
  if (!st.loaded) return null;
  const changed = st.chosen !== st.saved && st.chosen !== "";
  return (
    <Field
      label="Channel expert"
      hint={
        st.experts.length === 0
          ? "Pin an expert to the room first (room settings, Experts and skills)."
          : "Every agent turn in this channel and its threads wears it instead of the room's expert."
      }
    >
      <Select data-channel-expert value={st.chosen} disabled={st.experts.length === 0} onChange={(e) => st.setChosen(e.target.value)}>
        <option value="">None: the room's expert</option>
        {st.experts.map((u) => (
          <option key={u.componentId} value={u.componentId}>
            {u.component ? contentLabel(u.component) : u.componentId}
          </option>
        ))}
      </Select>
      {changed && (
        <p data-channel-expert-apart className="mt-1 text-xs text-[var(--color-fg-muted)]">
          An agent whose core keeps its instructions in its working files works on this channel in a workspace of its own: files it made for other channels are not there.
        </p>
      )}
    </Field>
  );
}
