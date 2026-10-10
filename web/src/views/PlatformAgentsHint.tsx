import { CloudIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { usePlatformAgents } from "@/lib/usePlatformAgents";

// PlatformAgentsHint is the zero-core guidance's way out: when the platform
// offers agents to this organization, a member without a local core can use
// one in any room right away. Renders nothing when none are offered.
export function PlatformAgentsHint({ className }: { readonly className?: string }) {
  const { agents } = usePlatformAgents();
  if (agents.length === 0) return null;
  return (
    <p className={cn("flex items-start gap-2 text-sm text-[var(--color-fg-muted)]", className)}>
      <CloudIcon size={16} weight="fill" className="mt-0.5 shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
      <span>
        <span className="font-medium text-[var(--color-fg)]">Try a platform agent — no install needed.</span> Choose
        one under Platform agents in a room&apos;s agent picker.
      </span>
    </p>
  );
}
