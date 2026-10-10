import { SquaresFourIcon } from "@phosphor-icons/react";
import { FileCard } from "./FileCard";

// The file cards under a chat message: the deliverables that message's agent
// turn produced, as a grid of up to MAX_CARDS, with "View all deliverables
// (N)" opening the workbench's deliverables list filtered to the message.

const MAX_CARDS = 6;

export function MessageDeliverables({
  roomId,
  seq,
  artifactIds,
  onOpen,
  onViewAll,
}: {
  readonly roomId: string;
  readonly seq: number;
  readonly artifactIds: readonly string[];
  readonly onOpen: (artifactId: string) => void;
  readonly onViewAll: (seq: number) => void;
}) {
  if (artifactIds.length === 0) return null;
  return (
    <div className="mt-1.5 flex flex-col gap-1.5" data-message-deliverables data-tour="deliverable">
      <div className="grid max-w-xl grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-2">
        {artifactIds.slice(0, MAX_CARDS).map((id) => (
          <FileCard key={id} roomId={roomId} artifactId={id} onOpen={onOpen} />
        ))}
      </div>
      {artifactIds.length > MAX_CARDS && (
        <button
          type="button"
          onClick={() => onViewAll(seq)}
          className="tactile inline-flex w-fit items-center gap-1.5 rounded-[var(--radius)] px-2 py-1 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)]"
        >
          <SquaresFourIcon size={14} aria-hidden />
          View all deliverables ({artifactIds.length})
        </button>
      )}
    </div>
  );
}
