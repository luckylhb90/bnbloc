import { useCallback, useEffect, useState } from "react";
import { BellSlashIcon } from "@phosphor-icons/react";
import { api, type Room } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { notificationsApi } from "@/lib/notificationsApi";
import { NOTIFICATION_TOGGLES, withRoomMuted, type NotificationPrefs } from "@/lib/notificationPrefs";
import { PageHeader } from "@/ui/Page";
import { Card } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { NotifierDelivery } from "@/views/settings/NotifierDelivery";
import { PersonalBots } from "@/views/settings/PersonalBots";
import { ToolSettings } from "@/views/settings/ToolSettings";
import { BrowserPush } from "@/views/settings/BrowserPush";
import { SignedInDevices } from "@/views/settings/SignedInDevices";
import { MyBudget } from "@/views/settings/MyBudget";
import { AgentSandbox } from "@/views/settings/AgentSandbox";
import { ProfileSettings } from "@/views/settings/ProfileSettings";


// Settings holds the member's personal settings. Notifications are delivered
// by the notifier components the platform admin installed; these switches
// decide which events reach this member, muted rooms silence every type for
// that room, and Delivery lets the member use their own bot per notifier.
export function Settings() {
  const { subject } = useAuth();
  const toast = useToast();
  const [prefs, setPrefs] = useState<NotificationPrefs | null>(null);
  const [rooms, setRooms] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      setPrefs(await notificationsApi.prefs());
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load notification settings");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Room names for the muted list (ids remain the fallback).
  useEffect(() => {
    if (!subject) return;
    api
      .myRooms(subject)
      .then((r) => {
        const names: Record<string, string> = {};
        for (const room of (r.rooms ?? []) as Room[]) names[room.roomId] = room.name || room.roomId;
        setRooms(names);
      })
      .catch(() => {});
  }, [subject]);

  const save = async (next: NotificationPrefs) => {
    const prev = prefs;
    setPrefs(next);
    setSaving(true);
    try {
      setPrefs(await notificationsApi.savePrefs(next));
    } catch (e) {
      setPrefs(prev);
      toast.error(e instanceof Error ? e.message : "Could not save notification settings");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex min-h-full flex-col">
      <PageHeader title="Settings" subtitle="Your personal preferences" />
      <div className="flex max-w-2xl flex-col gap-6 px-6 py-6">
        {error && <ErrorBox>{error}</ErrorBox>}
        <ProfileSettings />
        <section className="flex flex-col gap-3">
          <div>
            <h2 className="text-sm font-semibold">Notifications</h2>
            <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
              Choose which events notify you. They are delivered through the notifiers below.
            </p>
          </div>
          <Card className="divide-y divide-[var(--color-border)]">
            {NOTIFICATION_TOGGLES.map((t) => (
              <label key={t.key} className="flex cursor-pointer items-center justify-between gap-4 px-4 py-3">
                <span className="min-w-0">
                  <span className="block text-sm font-medium">{t.label}</span>
                  <span className="block text-xs text-[var(--color-fg-muted)]">{t.hint}</span>
                </span>
                <input
                  type="checkbox"
                  role="switch"
                  className="size-4 accent-[var(--color-accent)]"
                  checked={!!prefs?.[t.key]}
                  disabled={!prefs || saving}
                  onChange={(e) => prefs && void save({ ...prefs, [t.key]: e.target.checked })}
                  aria-label={t.label}
                />
              </label>
            ))}
          </Card>
          <BrowserPush />
        </section>
        <NotifierDelivery />
        <PersonalBots />
        <section className="flex flex-col gap-3">
          <div>
            <h2 className="text-sm font-semibold">Muted rooms</h2>
            <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
              Mute a room from the bell button in its header. Muted rooms send you no notifications.
            </p>
          </div>
          {prefs && prefs.quietRooms.length === 0 ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">No muted rooms.</p>
          ) : (
            <Card className="divide-y divide-[var(--color-border)]">
              {(prefs?.quietRooms ?? []).map((id) => (
                <div key={id} className="flex items-center justify-between gap-3 px-4 py-2.5">
                  <span className="flex min-w-0 items-center gap-2 text-sm">
                    <BellSlashIcon size={16} className="shrink-0 text-[var(--color-fg-subtle)]" />
                    <span className="truncate">{rooms[id] ?? id}</span>
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={saving}
                    onClick={() => prefs && void save(withRoomMuted(prefs, id, false))}
                  >
                    Unmute
                  </Button>
                </div>
              ))}
            </Card>
          )}
        </section>
        <AgentSandbox />
        <ToolSettings />
        <MyBudget />
        <SignedInDevices />
      </div>
    </div>
  );
}
