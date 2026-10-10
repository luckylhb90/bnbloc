import { useEffect, useMemo, useState } from "react";
import { Select } from "@/ui/Select";
import { HandshakeIcon, ShieldCheckIcon, PlusIcon } from "@phosphor-icons/react";
import { api, ApiError } from "@/lib/api";
import { borrowKey, borrowTarget } from "@/lib/roomAgents";
import { Button } from "@/ui/Button";
import { Input, Field } from "@/ui/Input";
import { Dialog } from "@/ui/Dialog";
import { Badge } from "@/ui/Card";
import { useToast } from "@/ui/Toast";
import { QuotaRequestsPanel } from "@/views/team/QuotaRequestsPanel";
import { UserNames } from "@/views/UserName";

// Authorization TTL options offered to the owner (label -> ms).
const TTL_OPTIONS: { label: string; ms: number }[] = [
  { label: "1 hour", ms: 60 * 60 * 1000 },
  { label: "8 hours", ms: 8 * 60 * 60 * 1000 },
  { label: "24 hours", ms: 24 * 60 * 60 * 1000 },
  { label: "7 days", ms: 7 * 24 * 60 * 60 * 1000 },
];

// One borrowable agent: a provider under a specific core. core is undefined for
// a coreless entry (a provider the owner named by hand).
interface Agent {
  core?: string;
  provider: string;
}

// dedupeAgents preserves first-seen order, keyed by (core, provider).
function dedupeAgents(xs: Agent[]): Agent[] {
  const seen = new Set<string>();
  const out: Agent[] = [];
  for (const x of xs) {
    const k = borrowKey(x);
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(x);
  }
  return out;
}

// groupByCore buckets agents by core in first-seen order; coreless agents fall
// under "" (rendered as a flat, unlabeled group).
function groupByCore(xs: Agent[]): [string, Agent[]][] {
  const order: string[] = [];
  const byCore = new Map<string, Agent[]>();
  for (const a of xs) {
    const c = a.core || "";
    if (!byCore.has(c)) {
      byCore.set(c, []);
      order.push(c);
    }
    byCore.get(c)!.push(a);
  }
  return order.map((c) => [c, byCore.get(c)!]);
}

// BorrowAuthDialog is the owner's per-room control for borrowing of their own
// agents. Authorization is scoped to THIS room (roomId), so authorizing here
// does not expose the agent in other rooms. Borrowing another member's agent
// requires the owner to authorize it here first (pre-authorization); a borrower
// can never self-authorize. It does not depend on local-agent discovery, so an
// owner signed in from the browser (no local core) can authorize too.
export function BorrowAuthDialog({
  roomId,
  roomName,
  subject,
  knownAgents,
  onClose,
  onQuotaChanged,
}: {
  roomId: string;
  roomName: string;
  subject: string | null;
  knownAgents: Agent[];
  onClose: () => void;
  // Fires after the owner decides a quota request here (refreshes the badge).
  onQuotaChanged?: () => void;
}) {
  const toast = useToast();
  const [ttlMs, setTtlMs] = useState(TTL_OPTIONS[2].ms);
  const [busy, setBusy] = useState("");
  // borrowKey -> expiry ms (live authorization); absent = not authorized.
  const [authorized, setAuthorized] = useState<Record<string, number>>({});
  const [loaded, setLoaded] = useState(false);
  // Agents the owner added by hand this session (beyond the known set).
  const [added, setAdded] = useState<Agent[]>([]);
  const [newProvider, setNewProvider] = useState("");
  // borrowKey -> borrow usage (runs + recent borrowers), for owner accountability.
  const [usage, setUsage] = useState<
    Record<string, { runsUsed: number; runQuota?: number; recentBorrowers: string[] }>
  >({});

  // The agents to show: my agents advertised in this room (data, never a
  // baked-in provider list) plus whatever the owner added by name. Live
  // authorizations are always among these, since they were authorized here.
  const rows = dedupeAgents([...knownAgents, ...added]);

  // Load current authorization state for the candidate agents, so the panel
  // reflects existing (unexpired) authorizations across sessions. Keyed on
  // the agents' identities, not the array instance: a caller re-rendering
  // with an equal list must not refetch (and reset) the panel.
  const probeKey = useMemo(() => dedupeAgents(knownAgents).map(borrowKey).join(","), [knownAgents]);
  useEffect(() => {
    let alive = true;
    void (async () => {
      const owner = subject || "";
      const probe = dedupeAgents(knownAgents);
      const entries = await Promise.all(
        probe.map(async (a): Promise<[string, number] | null> => {
          if (!owner) return null;
          try {
            const res = await api.borrowAuthorization(roomId, borrowTarget(a), owner);
            return res.authorized && res.expiresAtMs ? [borrowKey(a), res.expiresAtMs] : null;
          } catch {
            return null;
          }
        }),
      );
      if (!alive) return;
      const next: Record<string, number> = {};
      for (const e of entries) if (e) next[e[0]] = e[1];
      setAuthorized(next);
      setLoaded(true);
      // Fetch owner accountability (usage + recent borrowers) for the same set,
      // keyed by the core-qualified target so per-core usage stays distinct.
      try {
        const u = await api.borrowUsage(roomId);
        if (!alive) return;
        const um: Record<string, { runsUsed: number; runQuota?: number; recentBorrowers: string[] }> = {};
        for (const a of u.authorizations) {
          um[a.provider] = {
            runsUsed: a.runsUsed,
            runQuota: a.runQuota,
            recentBorrowers: a.recentBorrowers || [],
          };
        }
        setUsage(um);
      } catch {
        /* usage is presentational; ignore failures */
      }
    })();
    return () => {
      alive = false;
    };
    // knownAgents is read through probeKey (its identity), by design.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [roomId, subject, probeKey]);

  const authorize = async (a: Agent) => {
    const key = borrowKey(a);
    setBusy(key);
    try {
      // No model restriction from the client: an empty range means "the
      // owner's agent decides"; the owner's org policy still applies server-side.
      const res = await api.authorizeBorrow(roomId, borrowTarget(a), [], ttlMs);
      setAuthorized((cur) => ({ ...cur, [key]: res.expiresAtMs }));
      toast.success(`Authorized borrowing of your ${key} in ${roomName}`);
    } catch (e) {
      toast.error(
        e instanceof ApiError ? `Authorize failed: ${e.message}` : "Authorize failed",
      );
    } finally {
      setBusy("");
    }
  };

  const revoke = async (a: Agent) => {
    const key = borrowKey(a);
    setBusy(key);
    try {
      await api.deauthorizeBorrow(roomId, borrowTarget(a));
      setAuthorized((cur) => {
        const next = { ...cur };
        delete next[key];
        return next;
      });
      toast.success(`Revoked borrowing of your ${key} in ${roomName}`);
    } catch (e) {
      toast.error(
        e instanceof ApiError ? `Revoke failed: ${e.message}` : "Revoke failed",
      );
    } finally {
      setBusy("");
    }
  };

  // A hand-added provider is coreless (the owner names a harness); it authorizes
  // the default-core agent, matching the coreless fallback in borrowTarget.
  const addProvider = () => {
    const p = newProvider.trim();
    if (!p) return;
    setAdded((cur) => dedupeAgents([...cur, { provider: p }]));
    setNewProvider("");
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="Authorize borrowing of your agents"
      description={`Let other members of “${roomName}” use your agents here. This applies to this room only; your provider credential never leaves your machine — only the model and turn ride to your agent.`}
      footer={
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Access duration" hint="How long each authorization lasts before it must be renewed">
          <Select
            value={ttlMs}
            onChange={(e) => setTtlMs(Number(e.target.value))}
            className="h-10 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)]"
          >
            {TTL_OPTIONS.map((o) => (
              <option key={o.ms} value={o.ms}>
                {o.label}
              </option>
            ))}
          </Select>
        </Field>

        <div className="flex items-center gap-1.5 text-[var(--color-accent)]">
          <ShieldCheckIcon size={15} weight="fill" />
          <span className="text-xs font-medium">
            Credential never leaves your machine
          </span>
        </div>

        <div className="flex flex-col gap-3">
          {rows.length === 0 && (
            <p className="text-sm text-[var(--color-fg-muted)]">
              None of your agents is advertised in this room yet. Agents appear here once your bridge
              (the bnbloc desktop app) is running and connected; you can also authorize one by name below.
            </p>
          )}
          {groupByCore(rows).map(([coreName, agents]) => (
            <div key={coreName || "_"} className="flex flex-col gap-2">
              {/* Core subheader — only when a core is known, so coreless
                  (browser/manual) entries render as a flat list as before. */}
              {coreName && (
                <div className="px-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
                  {coreName}
                </div>
              )}
              {agents.map((a) => {
                const key = borrowKey(a);
                const on = authorized[key] !== undefined;
                const u = usage[key];
                const used = u?.runsUsed ?? 0;
                return (
                  <div
                    key={key}
                    className="flex flex-col gap-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2"
                  >
                    <div className="flex items-center justify-between gap-3">
                      <div className="flex items-center gap-2">
                        <HandshakeIcon size={16} className="text-[var(--color-accent)]" />
                        <span className="text-sm text-[var(--color-fg)]">{a.provider}</span>
                        {on && <Badge tone="accent">Borrowable</Badge>}
                      </div>
                      {on ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={busy === key}
                          onClick={() => revoke(a)}
                        >
                          {busy === key ? "…" : "Revoke"}
                        </Button>
                      ) : (
                        <Button
                          variant="primary"
                          size="sm"
                          disabled={busy === key || !loaded}
                          onClick={() => authorize(a)}
                        >
                          {busy === key ? "…" : "Authorize"}
                        </Button>
                      )}
                    </div>
                    {on && used > 0 && (
                      <div className="pl-6 text-xs text-[var(--color-fg-subtle)]">
                        Borrowed {used}
                        {u?.runQuota != null ? `/${u.runQuota}` : ""} time
                        {used === 1 ? "" : "s"}
                        {u && u.recentBorrowers.length > 0 && (
                          <>
                            {" "}· by <UserNames ids={u.recentBorrowers} />
                          </>
                        )}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          ))}
        </div>

        <div className="flex flex-col gap-2">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
            Quota requests
          </span>
          <QuotaRequestsPanel
            role="owner"
            roomId={roomId}
            pendingOnly
            emptyText="No pending requests for more runs in this room."
            onChanged={onQuotaChanged}
          />
        </div>

        <Field label="Add a provider" hint="Authorize another of your agents by its provider name">
          <div className="flex gap-2">
            <Input
              value={newProvider}
              placeholder="provider name"
              onChange={(e) => setNewProvider(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addProvider();
                }
              }}
            />
            <Button variant="outline" onClick={addProvider} disabled={!newProvider.trim()}>
              <PlusIcon size={15} /> Add
            </Button>
          </div>
        </Field>
      </div>
    </Dialog>
  );
}
