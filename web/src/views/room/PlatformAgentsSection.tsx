import { useState } from "react";
import { CloudIcon, ShieldCheckIcon } from "@phosphor-icons/react";
import { sameUser } from "@/lib/format";
import { hostsLabel, platformAgentOnline, type RoomPlatformAgent } from "@/lib/platformAgents";
import { Badge } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { useDisplayNames } from "@/lib/useDisplayName";

// PlatformAgentsSection is the Members panel's "Platform agents" list: the
// agents added to this room from the platform, with their host and a Remove
// for any member who may chat, the room owner, or one of its hosts. Hosts are
// listed here only, never among the people.
export function PlatformAgentsSection({
  agents,
  myRole,
  subject,
  onRemove,
}: {
  readonly agents: readonly RoomPlatformAgent[];
  readonly myRole: string | undefined;
  readonly subject: string | null;
  readonly onRemove: (agent: RoomPlatformAgent) => Promise<void>;
}) {
  const [removing, setRemoving] = useState<string | null>(null);
  const nameOf = useDisplayNames(agents.flatMap((a) => [...a.hosts.map((h) => h.hostUserId), a.addedBy]));
  if (agents.length === 0) return null;
  const mayChat = !!myRole && myRole !== "visitor";
  return (
    <div className="mt-6">
      <div className="mb-2 flex items-center gap-2">
        <CloudIcon size={16} className="text-[var(--color-fg-muted)]" />
        <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">Platform agents</h2>
        <Badge tone="neutral">{agents.length}</Badge>
      </div>
      <div className="flex flex-col gap-1">
        {agents.map((a) => {
          const key = `${a.core}/${a.provider}`;
          const online = platformAgentOnline(a);
          const canRemove = mayChat || a.hosts.some((h) => sameUser(h.hostUserId, subject));
          return (
            <div key={key} className="flex items-center gap-3 rounded-[var(--radius)] px-2 py-2 hover:bg-[var(--color-surface-2)]">
              <div className="relative">
                <div className="grid size-8 place-items-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
                  <CloudIcon size={16} weight="fill" />
                </div>
                <span
                  className={
                    "absolute -bottom-0.5 -right-0.5 size-2.5 rounded-full border-2 border-[var(--color-surface)] " +
                    (online ? "bg-[var(--color-accent)]" : "bg-[var(--color-fg-subtle)]")
                  }
                  title={online ? "Online" : "Offline: no host's desktop is connected"}
                />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5 truncate text-sm">
                  {a.provider}
                  <span title="Runs in its host's sandbox" aria-label="Sandboxed" className="text-[var(--color-accent)]">
                    <ShieldCheckIcon size={13} weight="fill" />
                  </span>
                </div>
                <div className="truncate text-xs text-[var(--color-fg-subtle)]">
                  hosted by {hostsLabel(a.hosts, nameOf)}
                  {a.addedBy ? ` · added by ${nameOf(a.addedBy)}` : ""}
                </div>
              </div>
              {canRemove && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={removing === key}
                  aria-label={`Remove ${a.provider} from this room`}
                  onClick={() => {
                    setRemoving(key);
                    void onRemove(a).finally(() => setRemoving(null));
                  }}
                >
                  {removing === key ? "Removing…" : "Remove"}
                </Button>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
