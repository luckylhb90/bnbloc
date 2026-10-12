import { useEffect, useRef, useState } from "react";
import { BrainIcon, CaretDownIcon, PushPinIcon, ShieldWarningIcon, XIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { contentLabel, permissionLabel, type ExpertChoice } from "@/lib/contentComponents";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { UserName } from "@/views/UserName";

// ExpertPicker is the composer's "Expert" dropdown: none, or one of the
// room's pinned experts or the member's own. The agent it runs on is the
// recipient picked beside it.
export function ExpertPicker({
  choices,
  expert,
  onPick,
}: {
  readonly choices: readonly ExpertChoice[];
  readonly expert: ExpertChoice | null;
  readonly onPick: (e: ExpertChoice | null) => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [open]);
  const label = expert ? contentLabel(expert.component) : "Expert";
  const pick = (e: ExpertChoice | null) => {
    onPick(e);
    setOpen(false);
  };
  const row = "flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]";
  return (
    <div className="relative min-w-0 shrink" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className={cn(
          "tactile flex h-10 max-w-full min-w-0 items-center gap-1.5 rounded-[var(--radius)] px-2 text-sm hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
          expert ? "text-[var(--color-accent)]" : "text-[var(--color-fg-muted)]",
        )}
        title={expert ? `Expert: ${label} — the agent wears this role` : "Pick an expert for the agent to wear"}
        aria-label={expert ? `Expert: ${label}` : "Expert: none"}
        aria-haspopup="true"
        aria-expanded={open}
      >
        <BrainIcon size={15} weight={expert ? "fill" : "regular"} />
        <span className="hidden min-w-0 truncate @md:inline @md:max-w-[9rem]">{label}</span>
        <CaretDownIcon size={12} className="shrink-0" />
      </button>
      {open && (
        <div className="animate-in absolute bottom-full left-0 z-20 mb-2 max-h-72 w-72 overflow-y-auto rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] py-1 shadow-[var(--shadow-pop)]">
          <div className="px-3 pb-1 pt-1.5 text-[0.65rem] font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Expert the agent wears</div>
          <button type="button" onClick={() => pick(null)} className={cn(row, !expert ? "font-medium text-[var(--color-fg)]" : "text-[var(--color-fg-muted)]")}>
            No expert
          </button>
          {choices.map((c) => (
            <button
              key={c.component.id}
              type="button"
              onClick={() => pick(c)}
              title={c.component.description || undefined}
              className={cn(row, expert?.component.id === c.component.id ? "font-medium text-[var(--color-fg)]" : "text-[var(--color-fg-muted)]")}
            >
              {c.source === "room" ? <PushPinIcon size={14} className="shrink-0 text-[var(--color-accent)]" aria-label="Pinned in this room" /> : <BrainIcon size={14} className="shrink-0" />}
              <span className="min-w-0 flex-1 truncate">{contentLabel(c.component)}</span>
              <span className="shrink-0 font-mono text-[0.65rem] text-[var(--color-fg-subtle)]">v{c.version || c.component.latestVersion}</span>
            </button>
          ))}
          {choices.length === 0 && <p className="px-3 py-2 text-xs text-[var(--color-fg-subtle)]">Create experts in the Library.</p>}
        </div>
      )}
    </div>
  );
}

// ExpertBanner sits above the input while an expert is picked: "expert X
// runs on agent Y", its welcome and its quick prompts (a chip fills the
// composer, it never sends).
export function ExpertBanner({
  expert,
  runsOn,
  onQuickPrompt,
  onClear,
}: {
  readonly expert: ExpertChoice;
  readonly runsOn: string | null;
  readonly onQuickPrompt: (prompt: string) => void;
  readonly onClear: () => void;
}) {
  const c = expert.component;
  return (
    <div className="px-3 pt-2" role="group" aria-label="Expert">
      <div className="flex items-center gap-1.5 px-1 text-xs text-[var(--color-fg-muted)]">
        <BrainIcon size={12} weight="fill" className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">
          <span className="font-medium text-[var(--color-fg)]">{contentLabel(c)}</span> v{expert.version || c.latestVersion}
          {runsOn ? <> runs on {runsOn}</> : <> — pick an agent for it to run on</>}
        </span>
        <button type="button" onClick={onClear} aria-label="Remove the expert" title="Remove the expert" className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]">
          <XIcon size={12} />
        </button>
      </div>
      {c.welcome && <p className="mt-1 line-clamp-3 whitespace-pre-wrap px-1 text-xs text-[var(--color-fg-subtle)]">{c.welcome}</p>}
      {(c.quickPrompts?.length ?? 0) > 0 && (
        <div className="-mx-1 mt-1.5 flex gap-1.5 overflow-x-auto px-1 pb-1 @md:flex-wrap @md:overflow-visible">
          {c.quickPrompts?.map((q) => (
            <button
              key={q.title}
              type="button"
              onClick={() => onQuickPrompt(q.prompt)}
              title={q.prompt}
              className="tactile max-w-[18rem] shrink-0 truncate rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-xs text-[var(--color-fg-muted)] hover:border-[var(--color-accent)]/40 hover:bg-[var(--color-accent-soft)] hover:text-[var(--color-fg)]"
            >
              {q.title}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ExpertPermissionDialog asks the member to confirm, on first use, what
// someone else's expert declares.
export function ExpertPermissionDialog({
  expert,
  onAnswer,
}: {
  readonly expert: ExpertChoice | null;
  readonly onAnswer: (ok: boolean) => void;
}) {
  const c = expert?.component;
  return (
    <Dialog
      open={!!expert}
      onClose={() => onAnswer(false)}
      size="sm"
      title={c ? `Use ${contentLabel(c)}?` : undefined}
      footer={
        <>
          <Button variant="ghost" onClick={() => onAnswer(false)}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => onAnswer(true)}>
            Use it
          </Button>
        </>
      }
    >
      {c && (
        <div className="flex flex-col gap-2 text-sm text-[var(--color-fg-muted)]">
          <p>
            This expert is by <UserName id={c.owner} /> and declares:
          </p>
          <ul className="flex flex-col gap-1">
            {c.permissions.map((p) => (
              <li key={p} className="flex items-center gap-1.5">
                <ShieldWarningIcon size={14} className="text-[var(--color-warning)]" />
                {permissionLabel(p)}
              </li>
            ))}
          </ul>
          {c.needsSandbox && <p>Turns that use it run in the agent sandbox.</p>}
        </div>
      )}
    </Dialog>
  );
}
