import { RobotIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { AgentHandle, UserName } from "@/views/UserName";

export type ActorKind = "human" | "agent";

export type ActorParts = {
  readonly kind: ActorKind;
  readonly display: string;
  readonly owner?: string;
  readonly provider?: string;
};

// parseActor splits a canonical actor ("<ownerId>/<provider>" for an agent, a
// bare user id for a person). `display` is the canonical form; rendering goes
// through ActorLabel, which shows display names.
export function parseActor(actor: string): ActorParts {
  const trimmed = actor.trim();
  const slash = trimmed.indexOf("/");
  if (slash > 0 && slash < trimmed.length - 1) {
    const owner = trimmed.slice(0, slash);
    const provider = trimmed.slice(slash + 1);
    return { kind: "agent", display: `${owner}/${provider}`, owner, provider };
  }
  return { kind: "human", display: trimmed || "unknown" };
}

export function ActorLabel({
  actor,
  className,
}: {
  readonly actor: string;
  readonly className?: string;
}) {
  const parts = parseActor(actor);
  if (parts.kind === "agent" && parts.owner && parts.provider) {
    return (
      <span
        className={cn(
          "inline-flex min-w-0 items-center gap-1 rounded-full bg-[var(--color-accent-soft)] px-1.5 py-0.5 font-medium text-[var(--color-accent)]",
          className,
        )}
      >
        <RobotIcon size={12} weight="fill" className="shrink-0" />
        <AgentHandle owner={parts.owner} provider={parts.provider} className="tabular" />
      </span>
    );
  }
  return <UserName id={parts.display} className={cn("text-[var(--color-fg-muted)]", className)} />;
}
