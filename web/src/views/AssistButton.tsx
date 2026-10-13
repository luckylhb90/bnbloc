import { CircleNotchIcon, SparkleIcon, StopIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/Button";

// AssistButton is every AI assist entry's button (docs/prd/2026-10-13-ai-
// assist.md §5): the sparkle, a spinner while the reply is on its way and,
// when the run can be stopped (a streamed reply), a stop button. "icon" is
// the composer's round tool button with a tooltip; "text" a labelled
// button for a form ("Fill with AI").

/** Where a run is: idle, waiting for the reply, or streaming it. */
export type AssistRunState = "idle" | "waiting" | "streaming";

export function AssistButton({
  label,
  state,
  onRun,
  onStop,
  disabled,
  variant = "icon",
  className,
}: {
  readonly label: string;
  readonly state: AssistRunState;
  readonly onRun: () => void;
  // Set when a run can be stopped: the button stops it while it runs.
  readonly onStop?: () => void;
  readonly disabled?: boolean;
  readonly variant?: "icon" | "text";
  readonly className?: string;
}) {
  const running = state !== "idle";
  const stoppable = running && !!onStop;
  const title = stoppable ? "Stop" : running ? `${label}…` : label;
  const icon = stoppable ? (
    <StopIcon size={variant === "icon" ? 16 : 15} weight="fill" />
  ) : running ? (
    <CircleNotchIcon size={variant === "icon" ? 17 : 15} className="animate-spin text-[var(--color-accent)]" />
  ) : (
    <SparkleIcon size={variant === "icon" ? 18 : 15} />
  );
  const onClick = () => {
    if (stoppable) onStop?.();
    else if (!running) onRun();
  };
  if (variant === "text") {
    return (
      <Button variant="outline" size="sm" onClick={onClick} disabled={disabled || (running && !stoppable)} className={className} title={title}>
        {icon}
        {stoppable ? "Stop" : running ? `${label}…` : label}
      </Button>
    );
  }
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled || (running && !stoppable)}
      title={title}
      aria-label={title}
      aria-busy={running}
      className={cn(
        "tactile grid size-10 shrink-0 place-items-center rounded-full text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60 disabled:pointer-events-none disabled:opacity-50",
        running && "bg-[var(--color-surface-2)] text-[var(--color-fg)]",
        className,
      )}
    >
      {icon}
    </button>
  );
}
