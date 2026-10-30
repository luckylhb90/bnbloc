import { defaultQuietHours, type NotificationPrefs, type QuietHours } from "@/lib/notificationPrefs";
import { Card } from "@/ui/Card";

// QuietHoursSetting is the member's daily window without notifications
// (channels PRD §2.5): held back, not delayed, the rooms keep them unread;
// a direct mention may come through.
export function QuietHoursSetting({
  prefs,
  saving,
  save,
}: {
  readonly prefs: NotificationPrefs | null;
  readonly saving: boolean;
  readonly save: (next: NotificationPrefs) => void;
}) {
  const q = prefs?.quietHours;
  const set = (patch: Partial<QuietHours> | null) => {
    if (!prefs) return;
    save({ ...prefs, quietHours: patch === null ? undefined : { ...(q ?? defaultQuietHours()), ...patch } });
  };
  const disabled = !prefs || saving;
  return (
    <Card className="divide-y divide-[var(--color-border)]">
      <label className="flex cursor-pointer items-center justify-between gap-4 px-4 py-3">
        <span className="min-w-0">
          <span className="block text-sm font-medium">Quiet hours</span>
          <span className="block text-xs text-[var(--color-fg-muted)]">
            No notifications in this window every day. Nothing is sent later: the rooms keep what you missed unread.
          </span>
        </span>
        <input
          type="checkbox"
          role="switch"
          className="size-4 accent-[var(--color-accent)]"
          checked={!!q}
          disabled={disabled}
          onChange={(e) => set(e.target.checked ? {} : null)}
          aria-label="Quiet hours"
        />
      </label>
      {q && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 text-sm">
          <label className="flex items-center gap-2">
            From
            <input
              type="time"
              className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1"
              value={q.start}
              disabled={disabled}
              onChange={(e) => e.target.value && e.target.value !== q.end && set({ start: e.target.value })}
              aria-label="Quiet from"
            />
          </label>
          <label className="flex items-center gap-2">
            to
            <input
              type="time"
              className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1"
              value={q.end}
              disabled={disabled}
              onChange={(e) => e.target.value && e.target.value !== q.start && set({ end: e.target.value })}
              aria-label="Quiet until"
            />
          </label>
          <span className="text-xs text-[var(--color-fg-subtle)]">{q.tz}</span>
          <label className="flex basis-full cursor-pointer items-center gap-2 text-xs text-[var(--color-fg-muted)]">
            <input
              type="checkbox"
              className="size-4 accent-[var(--color-accent)]"
              checked={!!q.allowMentions}
              disabled={disabled}
              onChange={(e) => set({ allowMentions: e.target.checked })}
            />
            Let direct mentions through (not @channel or @here)
          </label>
        </div>
      )}
    </Card>
  );
}
