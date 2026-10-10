import { useEffect, useState } from "react";
import { BellIcon, BellSlashIcon } from "@phosphor-icons/react";
import { notificationsApi } from "@/lib/notificationsApi";
import { isRoomMuted, withRoomMuted, type NotificationPrefs } from "@/lib/notificationPrefs";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";

// RoomMuteButton toggles "Mute notifications" for one room: the room joins
// (or leaves) the member's quiet rooms, which silences every notification
// type for it. Hidden until the member's prefs load (a read failure keeps it
// hidden rather than offering a toggle that cannot reflect the real state).
export function RoomMuteButton({ roomId }: { readonly roomId: string }) {
  const toast = useToast();
  const [prefs, setPrefs] = useState<NotificationPrefs | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    notificationsApi
      .prefs()
      .then((p) => {
        if (!cancelled) setPrefs(p);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  if (!prefs) return null;
  const muted = isRoomMuted(prefs, roomId);
  const label = muted ? "Unmute notifications" : "Mute notifications";

  const toggle = async () => {
    setBusy(true);
    try {
      // Re-read before writing so a change made elsewhere (another tab, the
      // Settings page) is not overwritten by this room's stale copy.
      const fresh = await notificationsApi.prefs();
      const saved = await notificationsApi.savePrefs(withRoomMuted(fresh, roomId, !muted));
      setPrefs(saved);
      toast.success(muted ? "Notifications unmuted for this room" : "Notifications muted for this room");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not change notification settings");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => void toggle()}
      disabled={busy}
      title={label}
      aria-label={label}
      aria-pressed={muted}
      className="tap"
    >
      {muted ? <BellSlashIcon size={18} /> : <BellIcon size={18} />}
    </Button>
  );
}
