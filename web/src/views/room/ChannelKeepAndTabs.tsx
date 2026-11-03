import { useEffect, useState } from "react";
import { XIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import { MaxChannelRetentionDays, MaxChannelTabTitle, MaxChannelTabs, type Artifact, type ChannelSettings, type ChannelTab } from "@/lib/wire.gen";
import { Field, Input } from "@/ui/Input";
import { Select } from "@/ui/Select";

// ChannelKeepAndTabs is a channel's own message retention and pinned tabs
// in its settings (channels PRD C22, E1-E5): the retention only shortens the
// organization's (the server applies the shorter), and at most
// MaxChannelTabs of the room's deliverables are pinned beside the chat.
export function ChannelKeepAndTabs({
  roomId,
  settings,
  retention,
  onRetention,
  onTabs,
}: {
  readonly roomId: string;
  readonly settings: ChannelSettings;
  readonly retention: string;
  readonly onRetention: (v: string) => void;
  readonly onTabs: (tabs: ChannelTab[]) => void;
}) {
  const [files, setFiles] = useState<readonly Artifact[]>([]);
  useEffect(() => {
    let cancelled = false;
    api.artifacts(roomId).then(
      (l) => !cancelled && setFiles(l.artifacts ?? []),
      () => !cancelled && setFiles([]),
    );
    return () => {
      cancelled = true;
    };
  }, [roomId]);
  const tabs = settings.tabs ?? [];
  const pinned = new Set(tabs.map((t) => t.artifactId));
  const titleOf = (id: string) => files.find((f) => f.artifactId === id)?.title ?? id;
  return (
    <>
      <Field label="Keep messages (days)" hint="Messages older than this are deleted from the channel and its threads. 0 follows the organization's retention; a longer value than the organization's is not used.">
        <Input data-channel-retention type="number" min={0} max={MaxChannelRetentionDays} step={1} value={retention} onChange={(e) => onRetention(e.target.value)} className="max-w-40" />
      </Field>
      <Field label="Tabs" hint={`Deliverables pinned beside the chat, at most ${MaxChannelTabs}. Only people who can open a deliverable see its tab work.`}>
        <div className="flex flex-col gap-2">
          {tabs.map((t) => (
            <div key={t.artifactId} data-channel-tab-row={t.artifactId} className="flex items-center gap-2 text-sm">
              <span className="min-w-0 flex-1 truncate">{t.title || titleOf(t.artifactId)}</span>
              <button
                type="button"
                aria-label={`Unpin ${t.title || titleOf(t.artifactId)}`}
                onClick={() => onTabs(tabs.filter((x) => x.artifactId !== t.artifactId))}
                className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
              >
                <XIcon size={14} />
              </button>
            </div>
          ))}
          {tabs.length < MaxChannelTabs && (
            <Select
              data-channel-tab-add
              aria-label="Pin a deliverable"
              value=""
              onChange={(e) => {
                const id = e.target.value;
                if (id) onTabs([...tabs, { artifactId: id, title: titleOf(id).slice(0, MaxChannelTabTitle) }]);
              }}
            >
              <option value="">Pin a deliverable…</option>
              {files
                .filter((f) => !pinned.has(f.artifactId))
                .map((f) => (
                  <option key={f.artifactId} value={f.artifactId}>
                    {f.title}
                    {f.isApp ? " (app)" : ""}
                  </option>
                ))}
            </Select>
          )}
        </div>
      </Field>
    </>
  );
}

/** retentionFrom reads the days field: a whole number within the bound, or
 *  null (the caller says why). */
export function retentionFrom(v: string): number | null {
  const n = Number(v);
  return Number.isInteger(n) && n >= 0 && n <= MaxChannelRetentionDays ? n : null;
}
