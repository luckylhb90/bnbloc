import { useEffect, useMemo, useState } from "react";
import type { Channel } from "@/lib/api";
import { AI_UNAVAILABLE, DRAFT_CHANNEL_PRESET } from "@/lib/channelAssists";
import { presetChanges, presetFromFields } from "@/lib/channelPresets";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import type { ChannelPreset } from "@/lib/wire.gen";
import { Field, Input } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { AssistButton } from "@/views/AssistButton";
import { useChannelAssist } from "@/views/room/useChannelAssist";

// ChannelPresetPicker chooses a channel preset (docs/prd/2026-10-28-channels-
// and-threads.md §2.1c) for a channel: one of the presets the member may use
// (their Library, the organization's, the platform's samples) or one the AI
// drafts from a sentence (draft-channel-preset), and shows what it changes on
// the channel before anything is applied. The caller applies the chosen one
// (lib/channelPresets applyPatch, one PATCH) and offers Undo.
export function ChannelPresetPicker({
  roomId,
  channel,
  onPick,
}: {
  readonly roomId: string;
  // The channel it is for: the preview compares with it (a new channel's
  // empty setup when there is none yet).
  readonly channel: Pick<Channel, "purpose" | "rules" | "settings"> & { readonly channelId?: string };
  // The chosen preset with its label, or null when none is chosen.
  readonly onPick: (preset: { readonly label: string; readonly channel: ChannelPreset } | null) => void;
}) {
  const [presets, setPresets] = useState<ContentComponent[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [picked, setPicked] = useState("");
  const [described, setDescribed] = useState("");
  const [drafted, setDrafted] = useState<ChannelPreset | null>(null);
  const draft = useChannelAssist(DRAFT_CHANNEL_PRESET, "draft a preset");

  useEffect(() => {
    let live = true;
    contentApi.list("channel-preset").then(
      (r) => live && setPresets(r.components.filter((c) => c.channel)),
      (e: unknown) => {
        if (!live) return;
        setPresets([]);
        setLoadErr(errorText(e, "Could not list the channel presets"));
      },
    );
    return () => {
      live = false;
    };
  }, []);

  const chosen = useMemo((): { label: string; channel: ChannelPreset } | null => {
    if (picked === "ai") return drafted ? { label: "The AI's draft", channel: drafted } : null;
    const c = presets?.find((p) => p.id === picked);
    return c?.channel ? { label: contentLabel(c), channel: c.channel } : null;
  }, [picked, drafted, presets]);
  useEffect(() => onPick(chosen), [chosen, onPick]);

  const runDraft = async () => {
    if (!described.trim()) return;
    const fields = await draft.run({ text: described.trim(), roomId, ...(channel.channelId ? { channelId: channel.channelId } : {}) });
    const p = presetFromFields(fields);
    setDrafted(p);
    if (p) setPicked("ai");
  };

  const changes = chosen ? presetChanges(channel, chosen.channel) : [];
  return (
    <div data-preset-picker className="flex flex-col gap-3">
      <Field label="Preset" hint={presets && presets.length === 0 && !loadErr ? "No channel presets yet: save a channel as one, or describe one for the AI." : "A kind of channel: its purpose, rules and settings."}>
        <Select aria-label="Channel preset" value={picked} onChange={(e) => setPicked(e.target.value)} disabled={presets === null}>
          <option value="">{presets === null ? "Loading…" : "None"}</option>
          {(presets ?? []).map((p) => (
            <option key={p.id} value={p.id}>
              {contentLabel(p)}
            </option>
          ))}
          {drafted && <option value="ai">The AI's draft</option>}
        </Select>
      </Field>
      {loadErr && <p role="alert" className="text-xs text-[var(--color-danger)]">{loadErr}</p>}
      <Field label="Or describe one" hint={draft.available ? "The AI drafts a preset you see before it is applied." : AI_UNAVAILABLE}>
        <div className="flex items-center gap-2">
          <Input
            aria-label="Describe a kind of channel"
            value={described}
            onChange={(e) => setDescribed(e.target.value)}
            placeholder="A help desk the agent answers first"
            disabled={!draft.available}
            onKeyDown={(e) => e.key === "Enter" && void runDraft()}
          />
          {draft.available && <AssistButton variant="text" label="Draft" state={draft.state} onRun={() => void runDraft()} disabled={!described.trim()} />}
        </div>
      </Field>
      {draft.error && <p role="alert" className="-mt-2 text-xs text-[var(--color-danger)]">{draft.error}</p>}
      {chosen && (
        <div data-preset-preview className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3 text-sm">
          <p className="mb-2 font-medium">{changes.length === 0 ? `${chosen.label} changes nothing here` : `${chosen.label} changes:`}</p>
          {changes.length > 0 && (
            <ul className="flex flex-col gap-1">
              {changes.map((c) => (
                <li key={c.field} data-preset-change={c.field} className="flex flex-wrap gap-x-2">
                  <span className="font-mono text-xs text-[var(--color-fg-muted)]">{c.field}</span>
                  <span className="text-[var(--color-fg-subtle)] line-through">{c.from}</span>
                  <span>→ {c.to}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
