// The desktop's managed sidecars (Agents page, desktop shell only): each
// supervised component's live state, and "Sign in" only for a core that
// positively reports signed out through the optional sidecar auth contract.
// Product-neutral: nothing here knows any sidecar by name.
import { authPhase, canSignIn, isCore, type SidecarEntry } from "@/lib/sidecarState";
import { sidecarAgents } from "@/lib/useSidecarAgents";
import { Button } from "@/ui/Button";
import { Card, Badge } from "@/ui/Card";
import { useEmailMask } from "@/lib/privacyMode";

function adapterTone(state: string): "accent" | "neutral" | "danger" | "warning" {
  if (state === "running" || state === "ready" || state === "cached") return "accent";
  if (state === "waiting" || state === "exited") return "warning";
  if (state === "failed") return "danger";
  if (state === "stopped") return "neutral";
  return "warning";
}

function authNote(e: SidecarEntry): string {
  if (!isCore(e.status) || e.status.state !== "running") return "";
  if (e.signIn === "opening") return "Opening sign-in…";
  if (e.signIn === "waiting" && authPhase(e.auth) !== "signedIn") return "Finish signing in in your browser…";
  if (e.signIn === "error") return `Sign-in could not start: ${e.signInError}`;
  switch (authPhase(e.auth)) {
    case "signedIn":
      return e.auth?.user ? `Signed in as ${e.auth.user}` : "Signed in";
    case "signedOut":
      return "Signed out";
    case "checking":
      return "Checking sign-in…";
    default:
      return "";
  }
}

export function ManagedSidecars({ entries }: { readonly entries: readonly SidecarEntry[] }) {
  // A harness account is often an address: masked in privacy mode.
  const mask = useEmailMask();
  if (entries.length === 0) return null;
  return (
    <Card className="p-4">
      <div className="flex flex-col gap-3">
        <div className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">
          Managed sidecars
        </div>
        {entries.map((e) => {
          const { status } = e;
          const note = mask(authNote(e));
          return (
            <div
              key={`${status.kind ?? "core"}:${status.name}`}
              className="flex items-center justify-between gap-3 border-t border-[var(--color-border)] pt-3 first:border-t-0 first:pt-0"
            >
              <div>
                <div className="text-sm font-medium text-[var(--color-fg)]">{status.name}</div>
                <div className="mt-1 text-sm text-[var(--color-fg-muted)]">{status.message}</div>
                {note && <div className="mt-1 text-xs text-[var(--color-fg-subtle)]">{note}</div>}
                {e.targets.error && (
                  <div className="mt-1 text-xs text-[var(--color-warning)]">
                    Couldn’t list agents: {e.targets.error} — retrying.
                  </div>
                )}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {canSignIn(e) && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={e.signIn === "opening"}
                    onClick={() => void sidecarAgents.signIn(status.name)}
                  >
                    Sign in
                  </Button>
                )}
                <Badge tone={adapterTone(status.state)}>{status.state}</Badge>
                {status.pid ? <Badge tone="neutral">pid {status.pid}</Badge> : null}
                {status.version ? <Badge tone="neutral">v{status.version}</Badge> : null}
              </div>
            </div>
          );
        })}
      </div>
    </Card>
  );
}
