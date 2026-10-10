import { useState } from "react";
import { cn } from "@/lib/cn";
import { formatAgentHandle, maskEmail, maskEmailsInText } from "@/lib/displayNames";
import { Input } from "@/ui/Input";
import { usePrivacyMode } from "@/lib/privacyMode";
import { useDisplayName, useDisplayNames } from "@/lib/useDisplayName";

// The single rendering of a person (PRD 2026-10-10 §1): the display name, never
// the id — not even as a hover title, so a screenshot or a demo recording
// never shows an address. Mentions/recipients/URLs keep using the canonical
// id — this is presentation only.
export function UserName({
  id,
  className,
  you,
}: {
  readonly id: string;
  readonly className?: string;
  // Append " (you)" — callers pass sameUser(id, subject).
  readonly you?: boolean;
}) {
  const name = useDisplayName(id);
  return (
    <span className={cn("min-w-0 truncate", className)} title={name}>
      {name}
      {you ? " (you)" : ""}
    </span>
  );
}

// useAgentHandle is "<owner display name> · <provider>" for string contexts
// (aria labels, <option>, toasts built in render).
export function useAgentHandle(ownerId: string, provider: string): string {
  return formatAgentHandle(useDisplayName(ownerId), provider);
}

export function AgentHandle({
  owner,
  provider,
  className,
}: {
  readonly owner: string;
  readonly provider: string;
  readonly className?: string;
}) {
  const handle = useAgentHandle(owner, provider);
  return <span className={cn("min-w-0 truncate", className)}>{handle}</span>;
}

// Email renders an address only where the address itself is the point (org
// member administration, invites). In privacy mode it shows the person's
// display name (or, for an address that is not a user yet, its default).
export function Email({ address, className }: { readonly address: string; readonly className?: string }) {
  const privacy = usePrivacyMode();
  const name = useDisplayName(privacy ? address : null);
  return <span className={cn("min-w-0 truncate", className)}>{privacy ? name || maskEmail(address) : address}</span>;
}

// UserWithEmail is the member-administration rendering: the display name with
// the address as secondary text (the address is the point there). Privacy
// mode drops the secondary address.
export function UserWithEmail({
  id,
  you,
  className,
}: {
  readonly id: string;
  readonly you?: boolean;
  readonly className?: string;
}) {
  const name = useDisplayName(id);
  const privacy = usePrivacyMode();
  return (
    <span className={cn("inline-flex min-w-0 flex-col", className)}>
      <span className="truncate">
        {name}
        {you && <span className="ml-2 text-xs text-[var(--color-fg-subtle)]">(you)</span>}
      </span>
      {!privacy && name !== id && <span className="truncate text-xs text-[var(--color-fg-subtle)]">{id}</span>}
    </span>
  );
}

// ActorName renders a canonical actor without decoration: an agent
// ("<ownerId>/<provider>") as its handle, anyone else as their name.
export function ActorName({ actor, className }: { readonly actor: string; readonly className?: string }) {
  const slash = actor.lastIndexOf("/");
  if (slash > 0 && slash < actor.length - 1) {
    const owner = actor.slice(0, slash);
    return <AgentHandle owner={owner.slice(owner.lastIndexOf("/") + 1)} provider={actor.slice(slash + 1)} className={className} />;
  }
  return <UserName id={actor} className={className} />;
}

// EmailInput is an input that holds an address (invite, member id). The
// address stays visible while the member types it; in privacy mode it is
// masked whenever the field is not focused (PRD 2026-10-10 §2).
export function EmailInput({ value, onFocus, onBlur, ...props }: React.ComponentProps<typeof Input> & { readonly value: string }) {
  const privacy = usePrivacyMode();
  const [focused, setFocused] = useState(false);
  const masked = privacy && !focused;
  return (
    <Input
      {...props}
      value={masked ? maskEmailsInText(value) : value}
      readOnly={masked || props.readOnly}
      onFocus={(e) => {
        setFocused(true);
        onFocus?.(e);
      }}
      onBlur={(e) => {
        setFocused(false);
        onBlur?.(e);
      }}
    />
  );
}

// UserNames renders a comma-separated list of people by display name.
export function UserNames({ ids, className }: { readonly ids: readonly string[]; readonly className?: string }) {
  const nameOf = useDisplayNames(ids);
  return <span className={className}>{ids.map(nameOf).join(", ")}</span>;
}
