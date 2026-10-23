import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  CaretDownIcon,
  CircuitryIcon,
  CloudIcon,
  ShieldCheckIcon,
  WarningCircleIcon,
  } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { useDismissOnOutside } from "@/ui/useDismissOnOutside";
import { hostsLabel, samePlatformAgent, type PlatformChoice } from "@/lib/platformAgents";
import { agentDisplayLabel, defaultDisplayName } from "@/lib/displayNames";
import { useDisplayNames } from "@/lib/useDisplayName";
import type { LocalAgent } from "@/lib/useLocalAgent";
import { Button } from "@/ui/Button";
import { ConfirmDialog } from "@/ui/Dialog";
import { Input } from "@/ui/Input";
import type { RoomAgent } from "@/views/composer/types";
import { agentRows, rowIsActive } from "@/views/composer/agentRows";

// AgentPicker is the dropdown that selects which agent a message drives: one
// of my own (local targets / my advertised agents) or another member's agent
// to borrow. Rows are keyed by (owner, core, provider), so two members' agents
// with the same harness stay distinct and each is selectable. Platform agents
// (published by a super admin) are their own group; one not yet in the room
// is added after a confirmation, then driven like a borrowed agent.
export function AgentPicker({
  agent,
  roomAgents,
  recipient,
  subject,
  onPick,
  platform = [],
  onAddPlatform,
  openNonce = 0,
}: {
  agent: LocalAgent;
  roomAgents: RoomAgent[];
  recipient: RoomAgent | null;
  subject: string | null;
  onPick: (r: RoomAgent | null) => void;
  platform?: readonly PlatformChoice[];
  // Resolves true once the agent is in the room.
  onAddPlatform?: (choice: PlatformChoice) => Promise<boolean>;
  // A new value opens the list (a send that has no agent asks for one).
  openNonce?: number;
}) {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (openNonce > 0) setOpen(true);
  }, [openNonce]);
  const [confirmAdd, setConfirmAdd] = useState<PlatformChoice | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  useDismissOnOutside(open, ref, () => setOpen(false));

  // ONE list, grouped by core — identical presentation on every surface.
  const groups = useMemo(
    () => agentRows(agent.targets, roomAgents, subject, platform),
    [agent.targets, roomAgents, subject, platform],
  );
  const current = agent.targets.find((t) => t.id === agent.target);
  // The trigger stays short so the toolbar fits a phone: the provider, plus
  // the owner's initial when the recipient is another member's agent. The
  // full core-qualified handle is in the tooltip and the dropdown rows.
  const provider = recipient?.provider ?? current?.provider;
  // A platform agent is served by whichever host routing picks: the trigger
  // shows a cloud, not one host's initial, and warns while every host is
  // offline.
  const platformPick = recipient?.platform
    ? platform.find((c) => samePlatformAgent({ core: c.agent.core ?? "", provider: c.agent.provider }, { core: recipient.core ?? "", provider: recipient.provider }))
    : undefined;
  const platformOffline = !!platformPick && !platformPick.online;
  const owner = recipient && !recipient.self && !recipient.platform ? recipient.ownerId : "";
  const nameOf = useDisplayNames([owner, ...roomAgents.map((a) => a.ownerId), ...platform.flatMap((c) => c.hosts.map((h) => h.hostUserId))]);
  const fullLabel = recipient
    ? recipient.platform
      ? `${recipient.provider} (platform agent${platformOffline ? ", offline" : ""})`
      : agentDisplayLabel(recipient, nameOf)
    : current
      ? current.core
        ? `${current.core}/${current.provider}`
        : `${current.provider}`
      : agent.detected === false
        ? "No local agent"
        : "Select agent";


  return (
    <div className="relative min-w-[4.5rem] shrink" ref={ref} data-tour="agent-picker">
      <button
        onClick={() => setOpen((o) => !o)}
        className={cn(
          "tactile flex h-10 max-w-full min-w-0 items-center gap-1.5 rounded-[var(--radius)] px-2 text-sm",
          "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
        )}
        title={`Agent: ${fullLabel} — select the agent this message drives`}
        aria-label={`Agent: ${fullLabel}`}
        aria-haspopup="true"
        aria-expanded={open}
      >
        {platformOffline || (!recipient && !current) ? (
          <WarningCircleIcon size={15} className="text-[var(--color-warning)]" />
        ) : recipient?.platform ? (
          <CloudIcon size={15} weight="fill" className="text-[var(--color-accent)]" />
        ) : (
          <CircuitryIcon size={15} className="hidden text-[var(--color-accent)] @md:block" />
        )}
        <span className="min-w-0 truncate @md:max-w-[10rem]">{provider ?? fullLabel}</span>
        {owner && (
          <span
            aria-hidden
            className="grid size-4 shrink-0 place-items-center rounded-full bg-[var(--color-accent-soft)] text-[0.6rem] font-semibold uppercase text-[var(--color-accent)]"
          >
            {nameOf(owner)[0]}
          </span>
        )}
        {(owner || recipient?.platform) && recipient?.sandboxed && <SandboxBadge />}
        <CaretDownIcon size={12} className="shrink-0" />
      </button>

      {open && (
        <div className="animate-in absolute bottom-full left-0 z-20 mb-2 w-72 max-w-[calc(100vw-2rem)] overflow-hidden rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] shadow-[var(--shadow-pop)]">
          <div className="border-b border-[var(--color-border)] px-3 py-2 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Agent
          </div>
          <div className="max-h-64 overflow-y-auto py-1">
            {groups.map((g) => (
              <div key={g.group} role="group" aria-label={g.group}>
                <div className="px-3 py-1 text-[0.65rem] font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
                  {g.group}
                </div>
                {g.rows.map((row) => {
                  const active = rowIsActive(row, recipient, agent.target);
                  return (
                    <button
                      key={row.key}
                      type="button"
                      aria-pressed={active}
                      onClick={() => {
                        if (row.localTargetId) {
                          agent.setTarget(row.localTargetId);
                          onPick(null);
                        } else if (row.platform && !row.platform.inRoom) {
                          setConfirmAdd(row.platform);
                        } else if (row.agent) {
                          onPick(row.agent);
                        }
                        setOpen(false);
                      }}
                      className={cn(
                        "flex w-full items-center gap-2 px-3 py-2 text-left text-sm",
                        active
                          ? "bg-[var(--color-surface-2)] text-[var(--color-fg)]"
                          : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
                      )}
                    >
                      {row.platform && <OnlineDot online={row.platform.online} />}
                      <span className="min-w-0 flex-1">
                        <span className="font-medium text-[var(--color-fg)]">{row.provider}</span>
                        {row.platform ? (
                          <span className="block truncate text-xs text-[var(--color-fg-subtle)]" title={`Hosted by ${hostsLabel(row.platform.hosts, nameOf)}`}>
                            {row.platform.online ? `hosted by ${hostsLabel(row.platform.hosts, nameOf)}` : "offline"}
                          </span>
                        ) : (
                          <span className="ml-1.5 text-xs text-[var(--color-fg-subtle)]">{row.self ? "yours" : nameOf(row.ownerId)}</span>
                        )}
                      </span>
                      {!row.self && row.agent?.sandboxed && <SandboxBadge />}
                      {row.platform ? (
                        !row.platform.online ? (
                          <span className="rounded-full border border-[var(--color-border-strong)] px-1.5 text-[0.6rem] font-medium text-[var(--color-fg-subtle)]">
                            Offline
                          </span>
                        ) : !row.platform.inRoom && (
                          <span className="rounded-full border border-[var(--color-border-strong)] px-1.5 text-[0.6rem] font-medium text-[var(--color-fg-muted)]">
                            Add
                          </span>
                        )
                      ) : !row.self && (
                        <span className="rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-1.5 text-[0.6rem] font-medium text-[var(--color-accent)]">
                          Borrow
                        </span>
                      )}
                      {active && <span className="size-1.5 rounded-full bg-[var(--color-accent)]" />}
                    </button>
                  );
                })}
              </div>
            ))}
            {groups.length === 0 && (
              <div className="px-3 py-3 text-sm text-[var(--color-fg-muted)]">
                No agents available yet.
              </div>
            )}
          </div>
          {/* Manual core address: dev-discovery gateways only (production
              gateways reject caller-supplied addresses). */}
          {agent.devDiscovery && (
          <details className="border-t border-[var(--color-border)]">
            <summary className="cursor-pointer px-3 py-2 text-xs text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]">
              Connection settings
            </summary>
            <div className="flex flex-col gap-2 px-3 pb-3">
              <Input
                value={agent.url}
                placeholder="http://127.0.0.1:4545"
                onChange={(e) => agent.setUrl(e.target.value)}
              />
              <Input
                type="password"
                value={agent.token}
                placeholder="access token"
                onChange={(e) => agent.setToken(e.target.value)}
              />
              <Button variant="secondary" size="sm" onClick={() => agent.save()}>
                Save & re-check
              </Button>
            </div>
          </details>
          )}
          <button
            onClick={() => agent.reload()}
            className="w-full border-t border-[var(--color-border)] px-3 py-2 text-left text-xs text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
          >
            Re-check for local agents
          </button>
        </div>
      )}
      {/* Portalled: the chat column is a CSS container, which would confine a
          fixed overlay to the column. */}
      {createPortal(
        <ConfirmDialog
          open={confirmAdd !== null}
          onClose={() => setConfirmAdd(null)}
          onConfirm={() => {
            const choice = confirmAdd;
            if (!choice || !onAddPlatform) return;
            void onAddPlatform(choice).then((added) => {
              if (added) onPick(choice.agent);
            });
          }}
          title="Add a platform agent?"
          description={confirmAdd ? platformAddQuestion(confirmAdd, nameOf) : undefined}
          confirmLabel="Add to room"
        />,
        document.body,
      )}
    </div>
  );
}

function platformAddQuestion(c: PlatformChoice, nameOf: (id: string) => string = defaultDisplayName): string {
  const hosts = c.hosts.map((h) => nameOf(h.hostUserId)).join(", ");
  return `Add ${c.agent.provider} (hosted by ${hosts}) to this room? It will read this room's conversation to answer, and runs in the host's sandbox.`;
}

// OnlineDot is a platform agent's availability: its host's desktop (bridge,
// core and sandbox) is online.
function OnlineDot({ online }: { online: boolean }) {
  return (
    <span
      title={online ? "Online" : "Offline: its host's desktop is not connected"}
      aria-label={online ? "Online" : "Offline"}
      className={cn(
        "size-2 shrink-0 rounded-full",
        online ? "bg-[var(--color-accent)]" : "bg-[var(--color-fg-subtle)]",
      )}
    />
  );
}

// SandboxBadge marks an agent whose borrowed drives run in its owner's agent
// sandbox (shell commands in a VM, the agent confined to the room workspace).
function SandboxBadge() {
  return (
    <span
      title="Sandboxed: when borrowed, this agent runs its commands in its owner's agent sandbox"
      aria-label="Sandboxed"
      className="grid shrink-0 place-items-center text-[var(--color-accent)]"
    >
      <ShieldCheckIcon size={13} weight="fill" />
    </span>
  );
}
