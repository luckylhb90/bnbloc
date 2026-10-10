import { useEffect, useId, useRef, useState } from "react";
import { CloudIcon, ShieldCheckIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import type { RoomPlatformAgent } from "@/lib/platformAgents";
import { UserName } from "@/views/UserName";

// PlatformAgentsIndicator is the room header's persistent transparency note
// (PRD 2026-10-10 v2 §5): a room with platform agents says so to every
// member, including those who joined after the agent was added. Its popover
// names the agents and their hosts and says runs happen in the host's
// sandbox.
export function PlatformAgentsIndicator({ agents }: { readonly agents: readonly RoomPlatformAgent[] }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const panelId = useId();

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  if (agents.length === 0) return null;
  const label = agents.length === 1 ? "Platform agent" : `${agents.length} platform agents`;
  return (
    <div ref={ref} className="relative min-w-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-label={`${label} · can read this room`}
        title={`${label} can read this room — runs happen in its host's sandbox`}
        // A compact pill on touch screens too: .tap grows the hit area, the
        // inline size keeps the painted pill small (the coarse-pointer
        // minimum would otherwise apply to the button itself).
        style={{ minHeight: 0, minWidth: 0 }}
        className="tap tactile flex max-w-full shrink-0 items-center gap-1 rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-2 py-0.5 text-[11px] font-medium text-[var(--color-accent)] hover:border-[var(--color-accent)]/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
      >
        <CloudIcon size={12} weight="fill" className="shrink-0" aria-hidden="true" />
        <span className="truncate">
          {label}
          <span className="hidden sm:inline"> · can read this room</span>
        </span>
      </button>
      {open && (
        <div
          id={panelId}
          role="dialog"
          aria-label="Platform agents in this room"
          className="animate-in z-30 mt-2 max-sm:fixed max-sm:inset-x-4 sm:absolute sm:left-0 sm:top-full sm:w-80 rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] p-3 text-sm shadow-[var(--shadow-pop)]"
        >
          <p className="font-medium text-[var(--color-fg)]">Platform agents in this room</p>
          <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
            These agents read this room&apos;s conversation to answer. Runs happen in their host&apos;s sandbox, on the
            host&apos;s computer.
          </p>
          <ul className="mt-2 flex flex-col divide-y divide-[var(--color-border)]">
            {agents.map((a) => (
              <li key={`${a.core}/${a.provider}`} className="py-2">
                <div className="flex items-center gap-1.5">
                  <span className="font-medium text-[var(--color-fg)]">{a.provider}</span>
                  <span className="text-xs text-[var(--color-fg-subtle)]">{a.core || "default core"}</span>
                  <ShieldCheckIcon size={13} weight="fill" className="text-[var(--color-accent)]" aria-label="Sandboxed" />
                </div>
                <ul className="mt-1 flex flex-col gap-0.5">
                  {a.hosts.map((h) => (
                    <li key={h.hostUserId} className="flex items-center gap-1.5 text-xs text-[var(--color-fg-muted)]">
                      <span
                        aria-hidden="true"
                        className={cn(
                          "size-1.5 shrink-0 rounded-full",
                          h.online ? "bg-[var(--color-accent)]" : "bg-[var(--color-fg-subtle)]",
                        )}
                      />
                      <span className="truncate">
                        hosted by <UserName id={h.hostUserId} />
                      </span>
                      <span className="text-[var(--color-fg-subtle)]">
                        {h.online ? (h.queued > 0 ? `· ${h.queued} queued` : "· online") : "· offline"}
                      </span>
                    </li>
                  ))}
                </ul>
                {a.addedBy && <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">Added by <UserName id={a.addedBy} /></p>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
