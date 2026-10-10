import { CopyIcon, CursorClickIcon, TerminalWindowIcon } from "@phosphor-icons/react";
import { MAC_APP_PATH, QUARANTINE_COMMAND } from "@/lib/macInstall";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";

const STEPS = [
  {
    title: "Drag bnbloc to Applications",
    body: "Open the downloaded .dmg and drag bnbloc onto the Applications folder.",
  },
  {
    title: "Open it once",
    body: "macOS blocks it (the app is not yet notarized). Close the warning; don't move the app to the Trash.",
  },
  {
    title: "System Settings → Privacy & Security → Open Anyway",
    body: "Scroll down to Security, click Open Anyway next to the bnbloc message, and confirm with your password. From then on bnbloc opens normally.",
  },
] as const;

// MacInstallGuideDialog explains how to open the not-yet-notarized desktop
// app on macOS: the same three steps as the DMG window, an illustration of
// the "Open Anyway" row, and the Terminal alternative.
export function MacInstallGuideDialog({ open, onClose }: { readonly open: boolean; readonly onClose: () => void }) {
  const toast = useToast();
  const copy = () => {
    void (navigator.clipboard ? navigator.clipboard.writeText(QUARANTINE_COMMAND) : Promise.reject(new Error("no clipboard"))).then(
      () => toast.success("Command copied"),
      () => toast.error("Could not copy the command"),
    );
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title="Install on macOS"
      description="bnbloc is not notarized by Apple yet, so macOS blocks its first launch. Here is how to open it."
      footer={
        // Focus lands on the pinned footer, so the steps open scrolled to the top.
        <Button variant="primary" onClick={onClose} autoFocus>
          Got it
        </Button>
      }
    >
      <ol className="flex flex-col gap-4">
        {STEPS.map((step, i) => (
          <li key={step.title} className="flex gap-3">
            <span className="grid size-6 shrink-0 place-items-center rounded-full bg-[var(--color-accent)] text-xs font-semibold text-[var(--color-accent-fg)]">
              {i + 1}
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium text-[var(--color-fg)]">{step.title}</p>
              <p className="mt-0.5 text-sm leading-relaxed text-[var(--color-fg-muted)]">{step.body}</p>
              {i === 2 && <OpenAnywayIllustration className="mt-3" />}
            </div>
          </li>
        ))}
      </ol>

      <section className="mt-5 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
        <p className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg)]">
          <TerminalWindowIcon size={16} className="text-[var(--color-fg-muted)]" aria-hidden="true" />
          Comfortable with Terminal?
        </p>
        <div className="mt-2 flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] py-1 pl-3 pr-1">
          <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap py-1 font-mono text-xs text-[var(--color-fg)]">
            {QUARANTINE_COMMAND}
          </code>
          <Button size="sm" variant="ghost" onClick={copy} aria-label="Copy the Terminal command">
            <CopyIcon size={14} /> Copy
          </Button>
        </div>
        <p className="mt-2 text-xs leading-relaxed text-[var(--color-fg-muted)]">
          After step 1, this removes the quarantine flag macOS put on the downloaded app ({MAC_APP_PATH}), so it
          opens without the warning.
        </p>
      </section>
    </Dialog>
  );
}

const SIDEBAR = ["Wi-Fi", "Bluetooth", "Network", "General", "Appearance", "Privacy & Security", "Desktop & Dock"] as const;

// OpenAnywayIllustration is a drawn mock of macOS System Settings at Privacy
// & Security (not a screenshot): the blocked-app row and its Open Anyway
// button. On a phone the sidebar folds away and the pane fills the frame.
function OpenAnywayIllustration({ className }: { readonly className?: string }) {
  return (
    <figure className={cn("m-0", className)}>
      <div
        role="img"
        aria-label='System Settings, Privacy & Security: under Security, the message "bnbloc" was blocked to protect your Mac, with an Open Anyway button.'
        className="overflow-hidden rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]"
      >
        <div className="flex items-center gap-1.5 border-b border-[var(--color-border)] px-3 py-2" aria-hidden="true">
          <span className="size-2.5 rounded-full bg-[hsl(4_70%_58%)]" />
          <span className="size-2.5 rounded-full bg-[hsl(40_80%_55%)]" />
          <span className="size-2.5 rounded-full bg-[hsl(130_45%_48%)]" />
          <span className="ml-2 text-[0.7rem] text-[var(--color-fg-subtle)]">System Settings</span>
        </div>
        <div className="flex" aria-hidden="true">
          <ul className="m-0 hidden w-40 shrink-0 list-none flex-col gap-0.5 border-r border-[var(--color-border)] p-2 sm:flex">
            {SIDEBAR.map((item) => (
              <li
                key={item}
                className={cn(
                  "rounded-md px-2 py-1 text-[0.7rem]",
                  item === "Privacy & Security"
                    ? "bg-[var(--color-accent)] font-medium text-[var(--color-accent-fg)]"
                    : "text-[var(--color-fg-muted)]",
                )}
              >
                {item}
              </li>
            ))}
          </ul>
          <div className="min-w-0 flex-1 p-3">
            <p className="text-xs font-semibold text-[var(--color-fg)]">Privacy &amp; Security</p>
            <div className="mt-2 flex flex-col gap-1.5">
              <div className="h-2 w-3/4 rounded bg-[var(--color-border)]" />
              <div className="h-2 w-1/2 rounded bg-[var(--color-border)]" />
            </div>
            <p className="mt-3 text-[0.7rem] font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Security</p>
            <div className="mt-1.5 flex flex-wrap items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 py-2">
              <span className="min-w-0 flex-1 text-[0.72rem] text-[var(--color-fg-muted)]">
                “bnbloc” was blocked to protect your Mac.
              </span>
              <span className="relative">
                <span className="inline-block rounded-md bg-[var(--color-fg)] px-2.5 py-1 text-[0.7rem] font-medium text-[var(--color-bg)] ring-2 ring-[var(--color-accent)] ring-offset-2 ring-offset-[var(--color-surface)]">
                  Open Anyway
                </span>
                <CursorClickIcon
                  size={18}
                  weight="fill"
                  className="absolute -bottom-3 -right-3 text-[var(--color-accent)] drop-shadow"
                />
              </span>
            </div>
          </div>
        </div>
      </div>
      <figcaption className="mt-1.5 text-xs text-[var(--color-fg-subtle)]">
        Illustration: the Open Anyway button appears for about an hour after the blocked launch.
      </figcaption>
    </figure>
  );
}
