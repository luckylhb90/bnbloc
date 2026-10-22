import { useState } from "react";
import { DesktopIcon, XIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { useDesktopPresence } from "@/lib/desktopPresence";
import { usePlatformAgents } from "@/lib/usePlatformAgents";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { DesktopHandoffCard } from "@/views/DesktopHandoffCard";
import { gatewayBase } from "@/lib/config";
import { StorageKey, memberKey, readStored, writeStored } from "@/lib/storageKeys";

// DesktopConnectNotice is the persistent sidebar entry shown in a browser while
// the member's own desktop app is not connected; it opens the open/install
// guidance on demand instead of interrupting.
export function DesktopConnectNotice({ collapsed }: { readonly collapsed: boolean }) {
  const presence = useDesktopPresence();
  const [open, setOpen] = useState(false);
  if (!presence.disconnected) return null;
  const label = "Desktop app not connected";
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        title={label}
        aria-label={label}
        className={cn(
          "tactile relative flex items-center rounded-[var(--radius)] text-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
          collapsed ? "justify-center p-2" : "gap-2 px-3 py-2",
        )}
      >
        <DesktopIcon size={18} className="shrink-0" />
        {collapsed ? (
          <span className="absolute right-1.5 top-1.5 size-2 rounded-full bg-[var(--color-warning,orange)]" />
        ) : (
          <span className="min-w-0 flex-1 truncate text-left">{label}</span>
        )}
      </button>
      <Dialog open={open} onClose={() => setOpen(false)} title="Connect the desktop app" size="md">
        <DesktopConnectBody />
      </Dialog>
    </>
  );
}

const dismissKey = (subject: string) => memberKey(StorageKey.desktopHintDismissed, { gateway: gatewayBase(), subject });

// DesktopConnectBanner is the in-room hint for a browser member whose desktop
// app is not connected. Dismissible per member; the sidebar entry stays. A room
// with a platform agent already has an agent to talk to, so the hint stays out
// of the way there (a new member's first room must not open with a warning).
export function DesktopConnectBanner({
  subject,
  hasPlatformAgent,
}: {
  readonly subject: string | null;
  readonly hasPlatformAgent: boolean;
}) {
  const presence = useDesktopPresence();
  const platform = usePlatformAgents();
  const [dismissed, setDismissed] = useState(() => !!subject && readStored(dismissKey(subject)) === "1");
  if (!presence.disconnected || dismissed || !subject || hasPlatformAgent) return null;
  const dismiss = () => {
    writeStored(dismissKey(subject), "1");
    setDismissed(true);
  };
  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-2 text-sm"
    >
      <DesktopIcon size={18} className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
      <span className="min-w-0 flex-1 text-[var(--color-fg-muted)]">
        {presence.openFailed
          ? "The desktop app didn't connect. If it isn't installed yet, download it first."
          : "Your own agent runs through the bnbloc desktop app, which isn't connected. Chat and borrowing a teammate's agent work here without it."}
        {platform.agents.length > 0 && " Or try a platform agent — no install needed: pick one under Platform agents in the agent picker."}
      </span>
      <div className="flex items-center gap-2">
        <Button size="sm" variant={presence.openFailed ? "outline" : "primary"} onClick={presence.open} disabled={presence.checking}>
          {presence.checking ? "Checking…" : "Open desktop app"}
        </Button>
        {presence.file && (
          <Button size="sm" variant={presence.openFailed ? "primary" : "outline"} onClick={presence.download} disabled={presence.downloading}>
            {presence.downloading ? "Preparing download…" : "Download"}
          </Button>
        )}
        <button
          type="button"
          onClick={dismiss}
          aria-label="Dismiss desktop app hint"
          title="Dismiss"
          className="tactile grid size-7 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
        >
          <XIcon size={14} />
        </button>
      </div>
    </div>
  );
}

// DesktopConnectBody renders the shared open/install/check guidance from the
// app-wide presence state.
export function DesktopConnectBody() {
  const presence = useDesktopPresence();
  return (
    <DesktopHandoffCard
      file={presence.file}
      error={presence.error}
      checking={presence.checking}
      downloading={presence.downloading}
      openFailed={presence.openFailed}
      onDownload={presence.download}
      onOpen={presence.open}
      onCheck={presence.check}
    />
  );
}
