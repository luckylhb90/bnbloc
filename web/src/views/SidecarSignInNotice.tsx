import { KeyIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { authPhase, signedOutCores } from "@/lib/sidecarState";
import { sidecarAgents, useSidecarAgents } from "@/lib/useSidecarAgents";

// SidecarSignInNotice is the quiet sidebar entry for an agent core that is
// signed out: its agents cannot take a turn until the member signs in, and the
// automatic sign-in is offered once per sign-out episode (it may have been
// closed unfinished, or the sign-in may have expired while the app was open).
// It stays until the core is signed in again. Desktop shell only; it keeps the
// shared sidecar poll running wherever the member is.
export function SidecarSignInNotice({ collapsed, subject }: { readonly collapsed: boolean; readonly subject: string | null }) {
  const snapshot = useSidecarAgents(!!subject);
  const waiting = signedOutCores(snapshot.entries);
  if (waiting.length === 0) return null;
  const entry = waiting[0]!;
  const name = entry.status.name;
  const opening = entry.signIn === "opening";
  const inBrowser = entry.signIn === "waiting" && authPhase(entry.auth) !== "signedIn";
  const label = opening ? "Opening sign-in…" : inBrowser ? `Finish signing in to ${name}` : `Sign in to ${name}`;
  return (
    <button
      type="button"
      disabled={opening}
      onClick={() => void sidecarAgents.signIn(name)}
      title={`${name} is signed out, so its agents cannot answer. Sign in to continue.`}
      aria-label={label}
      className={cn(
        "tactile relative flex items-center rounded-[var(--radius)] text-sm text-[var(--color-warning)] hover:bg-[var(--color-surface-2)] disabled:opacity-60",
        collapsed ? "justify-center p-2" : "gap-2 px-3 py-2",
      )}
    >
      <KeyIcon size={18} weight="bold" className="shrink-0" />
      {collapsed ? (
        <span className="absolute right-1.5 top-1.5 size-2 rounded-full bg-[var(--color-warning)]" />
      ) : (
        <span className="min-w-0 flex-1 truncate text-left">{label}</span>
      )}
    </button>
  );
}
