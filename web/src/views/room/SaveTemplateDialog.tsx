import { useEffect, useMemo, useState } from "react";
import { ApiError } from "@/lib/api";
import { starterCandidates, type TemplateAgent, type TemplateVisibility } from "@/lib/roomTemplates";
import { agentHandle } from "@/lib/roomAgents";
import { roomTemplatesApi } from "@/lib/roomTemplatesApi";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import type { RoomAgent } from "@/views/composer/types";
import { TEMPLATE_VISIBILITY } from "@/views/library/TemplateDialogs";
import type { FeedItem } from "@/views/room/types";
import { errorText } from "@/lib/errors";

const agentKey = (a: TemplateAgent) => agentHandle(a);

// SaveTemplateDialog saves the room as a template (owner/admin). The server
// captures the description, channels, and — when chosen — members and
// schedules; the member picks starter prompts from recent messages and
// suggested agents from the room's roster (hints only, never authorized).
export function SaveTemplateDialog({
  open,
  roomId,
  roomName,
  feed,
  roomAgents,
  onClose,
}: {
  readonly open: boolean;
  readonly roomId: string;
  readonly roomName: string;
  readonly feed: readonly FeedItem[];
  readonly roomAgents: readonly RoomAgent[];
  readonly onClose: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState<TemplateVisibility>("private");
  const [prompts, setPrompts] = useState<string[]>([]);
  const [agents, setAgents] = useState<string[]>([]);
  const [includeMembers, setIncludeMembers] = useState(true);
  const [includeSchedules, setIncludeSchedules] = useState(true);
  const [busy, setBusy] = useState(false);

  const candidates = useMemo(() => (open ? starterCandidates(feed) : []), [open, feed]);
  const agentOptions = useMemo(() => {
    const seen = new Set<string>();
    const out: TemplateAgent[] = [];
    for (const a of roomAgents) {
      const t: TemplateAgent = { ownerId: a.ownerId, provider: a.provider, ...(a.core ? { core: a.core } : {}) };
      const k = agentKey(t);
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(t);
    }
    return out;
  }, [roomAgents]);

  useEffect(() => {
    if (!open) return;
    setName(roomName);
    setDescription("");
    setVisibility("private");
    setPrompts([]);
    setAgents([]);
    setIncludeMembers(true);
    setIncludeSchedules(true);
  }, [open, roomName]);

  const toggle = (list: string[], v: string, on: boolean) => (on ? [...list, v] : list.filter((x) => x !== v));

  const submit = async () => {
    if (busy || !name.trim()) return;
    setBusy(true);
    try {
      await roomTemplatesApi.fromRoom(roomId, {
        name: name.trim(),
        description: description.trim(),
        visibility,
        // Keep the feed's chronological order for the chosen prompts.
        starterPrompts: [...candidates].reverse().filter((p) => prompts.includes(p)),
        suggestedAgents: agentOptions.filter((a) => agents.includes(agentKey(a))),
        includeMembers,
        includeSchedules,
      });
      toast.success("Saved as template — find it in Library › Templates");
      onClose();
    } catch (e) {
      toast.error(e instanceof ApiError && e.status === 403 ? "Only the room owner or an admin can save it as a template" : errorText(e, "Could not save the template"));
    } finally {
      setBusy(false);
    }
  };

  const check = "flex items-start gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm hover:bg-[var(--color-surface-2)]";
  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title="Save room as template"
      description="Captures the description and channels. Suggested agents are hints: nobody's agent is authorized by a template."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !name.trim()}>
            {busy ? "Saving…" : "Save template"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Name">
          <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Description" hint="Optional — when to use this template">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field label="Visibility">
          <Segment options={TEMPLATE_VISIBILITY} value={visibility} onChange={setVisibility} />
        </Field>
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Include</span>
          <label className={check}>
            <input type="checkbox" checked={includeMembers} onChange={(e) => setIncludeMembers(e.target.checked)} />
            Members (added to new rooms as members)
          </label>
          <label className={check}>
            <input type="checkbox" checked={includeSchedules} onChange={(e) => setIncludeSchedules(e.target.checked)} />
            Schedules (created paused in new rooms)
          </label>
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
            Starter prompts · recent messages
          </span>
          {candidates.length === 0 ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">No recent messages in this channel to pick from.</p>
          ) : (
            <div className="flex max-h-56 flex-col overflow-y-auto">
              {candidates.map((p) => (
                <label key={p} className={check}>
                  <input type="checkbox" className="mt-1" checked={prompts.includes(p)} onChange={(e) => setPrompts((l) => toggle(l, p, e.target.checked))} />
                  <span className="line-clamp-2 min-w-0 break-words">{p}</span>
                </label>
              ))}
            </div>
          )}
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Suggested agents</span>
          {agentOptions.length === 0 ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">No agents are reachable in this room right now.</p>
          ) : (
            agentOptions.map((a) => {
              const k = agentKey(a);
              return (
                <label key={k} className={check}>
                  <input type="checkbox" className="mt-1" checked={agents.includes(k)} onChange={(e) => setAgents((l) => toggle(l, k, e.target.checked))} />
                  <span className="font-mono text-xs leading-6">{k}</span>
                </label>
              );
            })
          )}
        </div>
      </div>
    </Dialog>
  );
}
