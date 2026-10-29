import { useCallback, useState } from "react";
import type { Channel } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { applyPatch, capturePreset, presetName, undoPatch } from "@/lib/channelPresets";
import { emptyContentForm } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import type { ChannelPreset } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { ChannelPresetPicker } from "@/views/room/ChannelPresetPicker";

// The channel preset dialogs (docs/prd/2026-10-28-channels-and-threads.md
// §2.1c): apply one to a channel (one PATCH, undone from the toast) and save
// a channel as one in the member's Library.

/** useApplyPreset applies a preset to a channel with the channel route's
 *  PATCH and offers Undo: a second PATCH back to what the channel had. The
 *  one place applying and undoing happen (the settings dialog and New
 *  channel both use it). */
export function useApplyPreset(roomId: string, onChanged: () => void) {
  const toast = useToast();
  return useCallback(
    async (ch: Pick<Channel, "channelId" | "name" | "purpose" | "rules" | "settings">, preset: { readonly label: string; readonly channel: ChannelPreset }) => {
      const patch = applyPatch(ch, preset.channel);
      await channelsApi.patch(roomId, ch.channelId, patch);
      onChanged();
      const back = undoPatch(ch, patch);
      toast.success(`Applied ${preset.label} to #${ch.name}`, [
        {
          label: "Undo",
          onClick: () => {
            channelsApi.patch(roomId, ch.channelId, back).then(
              () => {
                onChanged();
                toast.success(`#${ch.name} is back as it was`);
              },
              (e: unknown) => toast.error(errorText(e, "Could not undo the preset")),
            );
          },
        },
      ]);
    },
    [roomId, onChanged, toast],
  );
}

export function ApplyPresetDialog({
  roomId,
  channel,
  onClose,
  onApplied,
}: {
  readonly roomId: string;
  readonly channel: Channel;
  readonly onClose: () => void;
  readonly onApplied: () => void;
}) {
  const [chosen, setChosen] = useState<{ readonly label: string; readonly channel: ChannelPreset } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const apply = useApplyPreset(roomId, onApplied);
  const run = async () => {
    if (!chosen || busy) return;
    setBusy(true);
    setErr("");
    try {
      await apply(channel, chosen);
      onClose();
    } catch (e) {
      setErr(errorText(e, "Could not apply the preset"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      title={`Apply a preset to #${channel.name}`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void run()} disabled={busy || !chosen}>
            {busy ? "Applying…" : "Apply"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <ChannelPresetPicker roomId={roomId} channel={channel} onPick={setChosen} />
        {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
      </div>
    </Dialog>
  );
}

export function SavePresetDialog({ channel, onClose }: { readonly channel: Channel; readonly onClose: () => void }) {
  const toast = useToast();
  const [label, setLabel] = useState(channel.name);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const captured = capturePreset(channel);
  const save = async () => {
    if (!label.trim() || busy) return;
    setBusy(true);
    setErr("");
    try {
      await contentApi.create({
        ...emptyContentForm("channel-preset"),
        name: presetName(label),
        displayName: label.trim(),
        description: channel.purpose,
        channel: captured,
      });
      toast.success(`Saved "${label.trim()}" to your Library as a channel preset`);
      onClose();
    } catch (e) {
      setErr(errorText(e, "Could not save the preset"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      title={`Save #${channel.name} as a preset`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || !label.trim()}>
            {busy ? "Saving…" : "Save preset"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <Field label="Name" hint="It keeps this channel's purpose, rules and settings; never its default agent (a person's).">
          <Input autoFocus value={label} onChange={(e) => setLabel(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void save()} />
        </Field>
        <ul data-preset-captured className="text-xs text-[var(--color-fg-muted)]">
          {captured.purpose && <li>purpose: {captured.purpose}</li>}
          {captured.rules && <li>rules: {captured.rules}</li>}
          {Object.entries((captured.settings ?? {}) as Record<string, unknown>).map(([k, v]) => (
            <li key={k}>
              {k}: {typeof v === "object" ? JSON.stringify(v) : String(v)}
            </li>
          ))}
        </ul>
        {err && <p role="alert" className="text-sm text-[var(--color-danger)]">{err}</p>}
      </div>
    </Dialog>
  );
}
