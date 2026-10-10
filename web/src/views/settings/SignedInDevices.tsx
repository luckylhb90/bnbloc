import { useCallback, useEffect, useState } from "react";
import { CpuIcon, DesktopIcon, GlobeIcon, TerminalWindowIcon } from "@phosphor-icons/react";
import { ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { clientLabel, groupDevices, otherSessionCount, type MySession } from "@/lib/devices";
import { devicesApi } from "@/lib/devicesApi";
import { exactTime, relativeTime } from "@/lib/format";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";

function ClientIcon({ client }: { readonly client: string }) {
  const cls = "shrink-0 text-[var(--color-fg-subtle)]";
  switch (client) {
    case "desktop":
      return <DesktopIcon size={18} className={cls} />;
    case "cli":
      return <TerminalWindowIcon size={18} className={cls} />;
    case "bridge":
      return <CpuIcon size={16} className={cls} />;
    default:
      return <GlobeIcon size={18} className={cls} />;
  }
}

// ago phrases relativeTime ("just now", "3m", or a date) as a past time.
function ago(ms: number): string {
  const r = relativeTime(ms);
  if (r === "just now") return r;
  return /^\d+[smhd]$/.test(r) ? `${r} ago` : `on ${r}`;
}

// What a pending confirmation will do.
type Pending =
  | { kind: "one"; session: MySession; agents: number }
  | { kind: "others"; count: number };

// SignedInDevices lists the member's active sign-ins (browser, desktop app,
// command line) with the desktop app's background agent nested under the
// device that runs it. Each can be revoked; revoking this device signs out
// here. "Sign out all other devices" keeps this device and its own agent.
export function SignedInDevices() {
  const { logout } = useAuth();
  const toast = useToast();
  const [sessions, setSessions] = useState<MySession[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setSessions((await devicesApi.list()).sessions ?? []);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Could not load your signed-in devices");
      setSessions([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const revokeOne = async (session: MySession) => {
    setBusy(true);
    try {
      if (session.current) {
        // Revoking this device is signing out here. The revoke also stops this
        // device's agent; the sign-out proceeds even if the revoke fails.
        await devicesApi.revoke(session.id).catch(() => {});
        logout();
        return;
      }
      await devicesApi.revoke(session.id);
      toast.success(`Signed out ${clientLabel(session.client)}`);
    } catch (e) {
      // 404: already gone (expired or revoked elsewhere) — the reload shows it.
      if (!(e instanceof ApiError && e.status === 404)) {
        toast.error(e instanceof ApiError ? `Could not sign out that device: ${e.message}` : "Could not sign out that device");
      }
    } finally {
      setBusy(false);
    }
    await load();
  };

  const revokeOthers = async () => {
    setBusy(true);
    try {
      const { revoked } = await devicesApi.revokeOthers();
      toast.success(revoked === 1 ? "Signed out 1 other session" : `Signed out ${revoked} other sessions`);
    } catch (e) {
      toast.error(e instanceof ApiError ? `Could not sign out other devices: ${e.message}` : "Could not sign out other devices");
    } finally {
      setBusy(false);
    }
    await load();
  };

  const groups = groupDevices(sessions ?? []);
  const others = otherSessionCount(sessions ?? []);

  const confirmTitle =
    pending?.kind === "others"
      ? "Sign out all other devices?"
      : pending?.session.current
        ? "Sign out of this device?"
        : `Sign out ${pending ? clientLabel(pending.session.client) : ""}?`;
  const confirmDescription =
    pending?.kind === "others"
      ? `${pending.count === 1 ? "1 other session" : `${pending.count} other sessions`} will be signed out, including their desktop agents. This device and its agent stay signed in.`
      : pending?.session.current
        ? "You will be signed out here and need to sign in again."
        : pending && pending.agents > 0
          ? "That device and its desktop agent will need to sign in again."
          : "That session will need to sign in again.";

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold">Signed-in devices</h2>
          <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
            Where your account is signed in. A revoked device is signed out within the hour.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={busy || others === 0}
          onClick={() => setPending({ kind: "others", count: others })}
        >
          Sign out all other devices
        </Button>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      {!sessions && <Skeleton className="h-24" />}
      {sessions && sessions.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">No active sign-ins.</p>
      )}
      {groups.length > 0 && (
        <Card className="divide-y divide-[var(--color-border)]">
          {groups.map((g) => (
            <div key={g.session.id}>
              <SessionRow
                session={g.session}
                busy={busy}
                onRevoke={() => setPending({ kind: "one", session: g.session, agents: g.agents.length })}
              />
              {g.agents.map((a) => (
                <SessionRow
                  key={a.id}
                  session={a}
                  nested
                  busy={busy}
                  onRevoke={() => setPending({ kind: "one", session: a, agents: 0 })}
                />
              ))}
            </div>
          ))}
        </Card>
      )}
      <ConfirmDialog
        open={pending !== null}
        onClose={() => setPending(null)}
        onConfirm={() => {
          if (!pending) return;
          if (pending.kind === "others") void revokeOthers();
          else void revokeOne(pending.session);
        }}
        title={confirmTitle}
        description={confirmDescription}
        confirmLabel={pending?.kind === "one" && pending.session.current ? "Sign out" : "Revoke"}
        danger
      />
    </section>
  );
}

function SessionRow({
  session,
  nested,
  busy,
  onRevoke,
}: {
  readonly session: MySession;
  readonly nested?: boolean;
  readonly busy: boolean;
  readonly onRevoke: () => void;
}) {
  return (
    <div className={`flex items-center justify-between gap-3 py-2.5 pr-4 ${nested ? "pl-11" : "pl-4"}`}>
      <span className="flex min-w-0 items-center gap-3">
        <ClientIcon client={session.client} />
        <span className="min-w-0">
          <span className="flex items-center gap-2 text-sm font-medium">
            <span className="truncate" title={session.userAgent || undefined}>
              {clientLabel(session.client)}
            </span>
            {session.current && <Badge tone="accent">This device</Badge>}
          </span>
          <span className="block text-xs text-[var(--color-fg-muted)]">
            <span title={exactTime(session.createdAtMs)}>Signed in {ago(session.createdAtMs)}</span>
            {" · "}
            <span title={exactTime(session.lastUsedAtMs)}>Last used {ago(session.lastUsedAtMs)}</span>
          </span>
        </span>
      </span>
      <Button variant="ghost" size="sm" disabled={busy} onClick={onRevoke}>
        {session.current ? "Sign out" : "Revoke"}
      </Button>
    </div>
  );
}
