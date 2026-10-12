import { BrainIcon } from "@phosphor-icons/react";
import { expertBadge, type ExpertMarker } from "@/lib/contentComponents";

// ExpertTag marks a message that asked its agent to wear an expert:
// "expert Reviewer v1.2.0".
export function ExpertTag({ expert, className }: { readonly expert: ExpertMarker; readonly className?: string }) {
  const label = expertBadge(expert);
  return (
    <span
      data-expert
      title={`The agent answered as the expert ${label}`}
      className={"inline-flex min-w-0 items-center gap-1 text-[0.68rem] text-[var(--color-accent)] " + (className ?? "")}
    >
      <BrainIcon size={12} weight="fill" className="shrink-0" />
      <span className="truncate">expert {label}</span>
    </span>
  );
}
