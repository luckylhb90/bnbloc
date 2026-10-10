import { Avatar } from "@/views/room/Message";
import { ActorName } from "@/views/UserName";

export function AgentWorking({ label }: { readonly label: string }) {
  return (
    <div className="mt-3 flex gap-3 animate-in">
      <Avatar label="agent" agent />
      <div className="flex items-center gap-2 py-1.5">
        {label !== "agent" && (
          <ActorName actor={label} className="tabular text-xs text-[var(--color-fg-subtle)]" />
        )}
        <span className="flex items-center gap-1.5">
          <span className="size-1.5 animate-bounce rounded-full bg-[var(--color-fg-subtle)] [animation-delay:-0.3s]" />
          <span className="size-1.5 animate-bounce rounded-full bg-[var(--color-fg-subtle)] [animation-delay:-0.15s]" />
          <span className="size-1.5 animate-bounce rounded-full bg-[var(--color-fg-subtle)]" />
        </span>
      </div>
    </div>
  );
}
