import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { notificationsApi } from "@/lib/notificationsApi";
import { describeTestResult, type MemberNotifier } from "@/lib/notificationPrefs";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// NotifierDelivery lists the organization's notifier components (installed
// by the platform admin): an optional one can be turned on or off, and any
// can send the member a test notification. A member's own delivery (a
// personal bot) is a desktop plugin, configured in PersonalBots.
export function NotifierDelivery() {
  const [notifiers, setNotifiers] = useState<MemberNotifier[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setNotifiers((await notificationsApi.notifiers()).notifiers ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load notifiers"));
      setNotifiers([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold">Delivery</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          How your organization reaches you. When your desktop app runs a personal bot, notifications go there instead.
        </p>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      {notifiers && notifiers.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">
          No notifier is installed yet. Your platform admin installs one from Platform → Components.
        </p>
      )}
      {(notifiers ?? []).map((n) => (
        <NotifierCard key={n.name} notifier={n} reload={load} />
      ))}
    </section>
  );
}

function NotifierCard({ notifier, reload }: { notifier: MemberNotifier; reload: () => Promise<void> }) {
  const toast = useToast();
  const [busy, setBusy] = useState<"test" | "optin" | null>(null);

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<void>) => {
    setBusy(kind);
    try {
      await fn();
    } catch (e) {
      toast.error(errorText(e, "Something went wrong"));
    } finally {
      setBusy(null);
    }
  };

  const sendTest = () =>
    run("test", async () => {
      const r = describeTestResult(await notificationsApi.testNotifier(notifier.name));
      if (r.ok) toast.success(r.message);
      else toast.error(r.message);
    });

  const optIn = (install: boolean) =>
    run("optin", async () => {
      const ref = { name: notifier.name, kind: "notifier" as const };
      if (install) await api.installComponent(ref);
      else await api.uninstallComponent(ref);
      toast.success(install ? `${notifier.name} turned on for you` : `${notifier.name} turned off for you`);
      await reload();
    });

  return (
    <Card className="flex flex-col gap-3 px-4 py-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <span className="block text-sm font-medium">{notifier.name}</span>
          {notifier.description && <span className="block text-xs text-[var(--color-fg-muted)]">{notifier.description}</span>}
        </div>
        {notifier.optIn && (
          <Button size="sm" variant={notifier.installed ? "outline" : "primary"} disabled={busy !== null} onClick={() => void optIn(!notifier.installed)}>
            {notifier.installed ? "Turn off" : "Turn on"}
          </Button>
        )}
      </div>
      {notifier.optIn && !notifier.installed && (
        <p className="text-xs text-[var(--color-fg-subtle)]">This notifier is optional: turn it on to receive notifications through it.</p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => void sendTest()}>
          {busy === "test" ? "Sending…" : "Send test"}
        </Button>
      </div>
    </Card>
  );
}
