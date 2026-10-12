import { useCallback, useEffect, useState } from "react";
import { browserPushStatus, disableBrowserPush, enableBrowserPush } from "@/lib/webPush";
import type { PushToggleState } from "@/lib/webPushModel";
import { displayEnv, iosInstallHint } from "@/lib/pwa";
import { Card } from "@/ui/Card";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

const HINTS: Record<Exclude<PushToggleState, "hidden">, string> = {
  on: "This browser shows a notification for every event you get notified about.",
  off: "Show a system notification in this browser for the events you chose above.",
  blocked: "Notifications are blocked for this site. Allow them in your browser's site settings, then reload.",
  unsupported: "This browser cannot receive push notifications (it needs a secure connection and push support).",
};

// iOS has web push only in the installed app (iOS 16.4+): a Safari tab says
// how to install instead of "this browser cannot".
const IOS_INSTALL_HINT =
  "On iPhone and iPad, notifications work in the installed app: tap Share, choose Add to Home Screen, then open bnbloc from your Home Screen and turn this on there.";

// BrowserPush is the "Browser notifications on this device" toggle (N4): it
// asks for permission, registers the push worker and stores this browser's
// subscription. Hidden in the desktop shell and when the deployment has not
// configured browser push.
export function BrowserPush() {
  const toast = useToast();
  const [state, setState] = useState<PushToggleState>("hidden");
  const [serverKey, setServerKey] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const s = await browserPushStatus();
      setState(s.state);
      setServerKey(s.serverKey);
    } catch {
      // Background best-effort: without the status the toggle stays hidden.
      setState("hidden");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (state === "hidden") return null;

  const toggle = async (on: boolean) => {
    setBusy(true);
    try {
      const next = on && serverKey ? await enableBrowserPush(serverKey) : await disableBrowserPush();
      setState(next);
      if (on && next === "blocked") toast.error("Notifications are blocked for this site");
    } catch (e) {
      toast.error(errorText(e, "Could not change browser notifications"));
      void load();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <label className="flex cursor-pointer items-center justify-between gap-4 px-4 py-3">
        <span className="min-w-0">
          <span className="block text-sm font-medium">Browser notifications on this device</span>
          <span className="block text-xs text-[var(--color-fg-muted)]">
            {state === "unsupported" && iosInstallHint(displayEnv()) ? IOS_INSTALL_HINT : HINTS[state]}
          </span>
        </span>
        <input
          type="checkbox"
          role="switch"
          className="size-4 accent-[var(--color-accent)]"
          checked={state === "on"}
          disabled={busy || state === "unsupported" || state === "blocked"}
          onChange={(e) => void toggle(e.target.checked)}
          aria-label="Browser notifications on this device"
        />
      </label>
    </Card>
  );
}
