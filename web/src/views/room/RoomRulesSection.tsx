import { useEffect, useState } from "react";
import { api, type Channel, type RoomDetail } from "@/lib/api";
import { AI_UNAVAILABLE, DRAFT_ROOM_RULES, rulesAsk, rulesDraftOf } from "@/lib/channelAssists";
import { channelsApi } from "@/lib/channelsApi";
import { errorText } from "@/lib/errors";
import { ChannelCreationPolicyValues, MaxRoomRules, type ChannelCreationPolicy } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Field, Textarea } from "@/ui/Input";
import { Segment, Switch } from "@/ui/Segment";
import { AssistButton } from "@/views/AssistButton";
import { useChannelAssist } from "@/views/room/useChannelAssist";

const POLICY: Record<ChannelCreationPolicy, { readonly label: string; readonly hint: string }> = {
  anyone: { label: "Anyone", hint: "top level or their sections" },
  sections: { label: "In sections", hint: "top level: owner and admins" },
  admins: { label: "Owner and admins", hint: "" },
};

// RoomRulesSection is the room settings' rules and onboarding
// (docs/prd/2026-10-28-channels-and-threads.md §2.5a, §2.1b; the room's
// owner and admins): the rules (the AI drafts them with the welcome from
// the room's description and channels), whether a member agrees to them
// before posting (C16, off by default), the recommended channels a new
// member joins, and who creates channels (C15). A save's outcome is said on
// its button's line.
export function RoomRulesSection({ room, onChanged }: { readonly room: RoomDetail; readonly onChanged: (r: RoomDetail) => void }) {
  const [rules, setRules] = useState(room.rules);
  const [agree, setAgree] = useState(room.requireRulesAgreement);
  const [recommended, setRecommended] = useState<string[]>(room.recommendedChannels);
  const [policy, setPolicy] = useState<ChannelCreationPolicy>(room.channelCreation || "anyone");
  const [channels, setChannels] = useState<Channel[]>([]);
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState<{ readonly ok: boolean; readonly text: string } | null>(null);
  const [draftWelcome, setDraftWelcome] = useState("");
  const draft = useChannelAssist(DRAFT_ROOM_RULES, "draft the rules");
  useEffect(() => {
    let live = true;
    channelsApi
      .list(room.roomId)
      .then((l) => live && setChannels(l.channels))
      .catch(() => live && setChannels([]));
    return () => {
      live = false;
    };
  }, [room.roomId]);

  const save = async () => {
    setBusy(true);
    setSaid(null);
    try {
      const updated = await channelsApi.setup(room.roomId, { rules: rules.trim(), requireRulesAgreement: agree, recommendedChannels: recommended, channelCreation: policy });
      onChanged(updated);
      setSaid({ ok: true, text: updated.rulesVersion !== room.rulesVersion && agree ? "Saved: members agree to the new rules before their next post" : "Saved" });
    } catch (e) {
      setSaid({ ok: false, text: errorText(e, "Could not save") });
    } finally {
      setBusy(false);
    }
  };
  const suggest = async () => {
    const fields = await draft.run({ text: rulesAsk(room, channels), roomId: room.roomId });
    if (!fields) return;
    const d = rulesDraftOf(fields);
    if (d.rules) setRules(d.rules);
    setDraftWelcome(d.welcome);
    if (!d.rules && !d.welcome) setSaid({ ok: false, text: "The AI suggested nothing; write them by hand" });
  };
  const useWelcome = async () => {
    try {
      onChanged(await api.updateRoomWelcome(room.roomId, { welcome: draftWelcome }));
      setDraftWelcome("");
      setSaid({ ok: true, text: "The suggested welcome is saved" });
    } catch (e) {
      setSaid({ ok: false, text: errorText(e, "Could not save the welcome") });
    }
  };

  return (
    <div data-room-rules className="rounded-[var(--radius)] border border-[var(--color-border)] p-4">
      <p className="text-sm font-medium">Rules and channels</p>
      <p className="mb-3 text-xs text-[var(--color-fg-muted)]">Shown on the room's welcome page and in every channel's Rules sheet; agents read them too.</p>
      <div className="flex flex-col gap-3">
        <Field label="Rules" hint={draft.available ? undefined : AI_UNAVAILABLE}>
          <Textarea aria-label="Room rules" rows={5} maxLength={MaxRoomRules} value={rules} onChange={(e) => setRules(e.target.value)} placeholder="How this room works" />
        </Field>
        {draft.available && (
          <div className="flex items-center gap-2">
            <AssistButton variant="text" label="Draft rules and welcome" state={draft.state} onRun={() => void suggest()} />
            <span className="text-xs text-[var(--color-fg-subtle)]">From the room's description and channels; edit before saving.</span>
          </div>
        )}
        {draft.error && <p role="alert" className="text-xs text-[var(--color-danger)]">{draft.error}</p>}
        {draftWelcome && (
          <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3 text-sm">
            <p className="mb-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Suggested welcome</p>
            <p className="whitespace-pre-wrap">{draftWelcome}</p>
            <div className="mt-2 flex gap-2">
              <Button size="sm" variant="outline" onClick={() => void useWelcome()}>
                Use as the welcome
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setDraftWelcome("")}>
                Dismiss
              </Button>
            </div>
          </div>
        )}
        <div className="flex items-center justify-between gap-4">
          <div>
            <p className="text-sm">Members agree to the rules before they post</p>
            <p className="text-xs text-[var(--color-fg-muted)]">A change of the rules asks again. Visitors are never asked.</p>
          </div>
          <Switch checked={agree} ariaLabel="Require agreeing to the rules" onChange={setAgree} />
        </div>
        <Field label="Recommended channels" hint="A new member joins these; the welcome page lists them.">
          <div className="flex flex-wrap gap-1.5">
            {channels.map((c) => {
              const on = recommended.includes(c.channelId);
              return (
                <button
                  key={c.channelId}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setRecommended((r) => (on ? r.filter((x) => x !== c.channelId) : [...r, c.channelId]))}
                  className={on ? "tactile rounded-full bg-[var(--color-accent-soft)] px-2.5 py-1 text-xs text-[var(--color-fg)]" : "tactile rounded-full border border-[var(--color-border)] px-2.5 py-1 text-xs text-[var(--color-fg-muted)]"}
                >
                  #{c.name}
                </button>
              );
            })}
          </div>
        </Field>
        <Field label="Who creates channels">
          <Segment<ChannelCreationPolicy> value={policy} onChange={setPolicy} options={ChannelCreationPolicyValues.map((v) => ({ value: v, label: POLICY[v].label, hint: POLICY[v].hint || undefined }))} />
        </Field>
        <div className="flex items-center gap-3">
          {said && (
            <span role="status" className={said.ok ? "text-xs text-[var(--color-fg-muted)]" : "text-xs text-[var(--color-danger)]"}>
              {said.text}
            </span>
          )}
          <span className="flex-1" />
          <Button size="sm" variant="primary" onClick={() => void save()} disabled={busy}>
            {busy ? "Saving…" : "Save rules and channels"}
          </Button>
        </div>
      </div>
    </div>
  );
}
